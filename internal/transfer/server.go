package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"kademlia-demo/internal/framing"
)

// server accepts TCP connections from downloaders and answers
// GET_MANIFEST / GET_PIECE requests from the library (protocol.go).
type server struct {
	ln        net.Listener
	lib       *Library
	logger    *slog.Logger
	ioTimeout time.Duration
}

// listen opens the TCP listening socket. port 0 lets the OS pick.
func listen(port int, lib *Library, logger *slog.Logger, ioTimeout time.Duration) (*server, error) {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("transfer: listen tcp :%d: %w", port, err)
	}
	return &server{ln: ln, lib: lib, logger: logger, ioTimeout: ioTimeout}, nil
}

func (s *server) port() int {
	return s.ln.Addr().(*net.TCPAddr).Port
}

// serve accepts connections until close() is called. Each connection is
// handled on its own goroutine. Accept returns once the kernel has
// finished the TCP three-way handshake with the downloader.
func (s *server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger.Error("tcp accept failed", "error", err)
			}
			return
		}
		go s.handleConn(conn)
	}
}

// close stops accepting. Connections already open end on their own when
// the client hangs up or ioTimeout passes with no request.
func (s *server) close() error {
	return s.ln.Close()
}

// handleConn answers requests on one connection, in order, until the
// client closes it or stays idle for ioTimeout.
func (s *server) handleConn(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	s.logger.Info("tcp connection accepted", "from", remote)
	for {
		conn.SetDeadline(time.Now().Add(s.ioTimeout))
		body, err := framing.Read(conn, maxRequestFrame)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger.Debug("tcp connection ended", "from", remote, "error", err)
			}
			return
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			s.writeError(conn, fmt.Sprintf("bad request: %v", err))
			return
		}
		data, err := s.answer(req)
		if err != nil {
			s.logger.Warn("tcp request refused", "from", remote, "op", req.Op, "index", req.Index, "error", err)
			if s.writeError(conn, err.Error()) != nil {
				return
			}
			continue
		}
		if err := s.writeData(conn, data); err != nil {
			s.logger.Debug("tcp write failed", "from", remote, "error", err)
			return
		}
		s.logger.Info("tcp request served", "to", remote, "op", req.Op, "index", req.Index, "bytes", len(data))
	}
}

func (s *server) answer(req request) ([]byte, error) {
	switch req.Op {
	case opGetManifest:
		e, ok := s.lib.lookup(req.Key)
		if !ok {
			return nil, fmt.Errorf("unknown content key %s", req.Key)
		}
		return e.manifestBytes, nil
	case opGetPiece:
		return s.lib.readPiece(req.Key, req.Index)
	default:
		return nil, fmt.Errorf("unknown op %q", req.Op)
	}
}

func (s *server) writeData(conn net.Conn, data []byte) error {
	header, _ := json.Marshal(responseHeader{OK: true})
	if err := framing.Write(conn, header); err != nil {
		return err
	}
	return framing.Write(conn, data)
}

func (s *server) writeError(conn net.Conn, msg string) error {
	header, _ := json.Marshal(responseHeader{Error: msg})
	return framing.Write(conn, header)
}
