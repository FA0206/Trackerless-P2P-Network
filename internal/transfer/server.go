package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"kademlia-demo/internal/framing"
)

// server accepts TCP connections from downloaders and answers
// GET_MANIFEST / GET_BITFIELD / GET_PIECE requests from the library
// (protocol.go).
type server struct {
	ln        net.Listener
	lib       *Library
	logger    *slog.Logger
	ioTimeout time.Duration

	connsMu sync.Mutex
	conns   map[net.Conn]struct{} // open connections, so close() can end them

	// DEBUG ONLY (--debug-serve-delay): sleep this many nanoseconds before
	// answering each GET_PIECE. Localhost transfers finish in milliseconds,
	// so without it a demo can't reliably kill a seeder mid-download. 0
	// (the default) means no delay. Atomic because it can be set while
	// connections are being served.
	debugServeDelay atomic.Int64
}

// listen opens the TCP listening socket. port 0 lets the OS pick.
func listen(port int, lib *Library, logger *slog.Logger, ioTimeout time.Duration) (*server, error) {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("transfer: listen tcp :%d: %w", port, err)
	}
	return &server{ln: ln, lib: lib, logger: logger, ioTimeout: ioTimeout, conns: map[net.Conn]struct{}{}}, nil
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

// close stops accepting and closes every open connection, so a peer that
// is closed stops serving at once -- like a process that died.
func (s *server) close() error {
	err := s.ln.Close()
	s.connsMu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.connsMu.Unlock()
	return err
}

func (s *server) track(conn net.Conn, open bool) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if open {
		s.conns[conn] = struct{}{}
	} else {
		delete(s.conns, conn)
	}
}

// handleConn answers requests on one connection, in order, until the
// client closes it or stays idle for ioTimeout.
func (s *server) handleConn(conn net.Conn) {
	s.track(conn, true)
	defer s.track(conn, false)
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
		data, proof, err := s.answer(req)
		if err != nil {
			s.logger.Warn("tcp request refused", "from", remote, "op", req.Op, "index", req.Index, "error", err)
			if s.writeError(conn, err.Error()) != nil {
				return
			}
			continue
		}
		if err := s.writeData(conn, data, proof); err != nil {
			s.logger.Debug("tcp write failed", "from", remote, "error", err)
			return
		}
		level := slog.LevelInfo
		if req.Op == opGetBitfield {
			level = slog.LevelDebug // polled every ~500 ms per downloader: too chatty for Info
		}
		s.logger.Log(context.Background(), level, "tcp request served", "to", remote, "op", req.Op, "index", req.Index, "bytes", len(data))
	}
}

// answer returns the data frame for a request, and for GET_PIECE the
// piece's Merkle proof.
func (s *server) answer(req request) ([]byte, []Hash, error) {
	switch req.Op {
	case opGetManifest:
		_, manifestBytes, ok := s.lib.manifestFor(req.Key)
		if !ok {
			return nil, nil, fmt.Errorf("unknown content key %s", req.Key)
		}
		return manifestBytes, nil, nil
	case opGetBitfield:
		have, ok := s.lib.bitfield(req.Key)
		if !ok {
			return nil, nil, fmt.Errorf("unknown content key %s", req.Key)
		}
		return have, nil, nil
	case opGetPiece:
		if d := time.Duration(s.debugServeDelay.Load()); d > 0 {
			time.Sleep(d)
		}
		return s.lib.readPiece(req.Key, req.Index)
	default:
		return nil, nil, fmt.Errorf("unknown op %q", req.Op)
	}
}

func (s *server) writeData(conn net.Conn, data []byte, proof []Hash) error {
	h := responseHeader{OK: true}
	for _, p := range proof {
		h.Proof = append(h.Proof, p.String())
	}
	header, _ := json.Marshal(h)
	if err := framing.Write(conn, header); err != nil {
		return err
	}
	return framing.Write(conn, data)
}

func (s *server) writeError(conn net.Conn, msg string) error {
	header, _ := json.Marshal(responseHeader{Error: msg})
	return framing.Write(conn, header)
}
