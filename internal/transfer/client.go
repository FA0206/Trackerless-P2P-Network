package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"kademlia-demo/internal/framing"
	"kademlia-demo/internal/kademlia"
)

// providerConn is a downloader's open TCP connection to one provider.
type providerConn struct {
	provider  ProviderRecord
	conn      net.Conn
	ioTimeout time.Duration
}

// dialProvider opens a TCP connection to a provider. net.Dialer asks the
// kernel to do the three-way handshake (SYN, SYN-ACK, ACK) and returns
// once it completes -- or fails fast with "connection refused" if nothing
// listens on that port (e.g. the provider process was killed).
func dialProvider(ctx context.Context, p ProviderRecord, timeout time.Duration) (*providerConn, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp4", p.Addr())
	if err != nil {
		return nil, err
	}
	return &providerConn{provider: p, conn: conn, ioTimeout: timeout}, nil
}

func (pc *providerConn) close() {
	pc.conn.Close()
}

// fetch performs one request/response exchange (protocol.go) and returns
// the data frame.
func (pc *providerConn) fetch(op string, key kademlia.ID, index int) ([]byte, error) {
	pc.conn.SetDeadline(time.Now().Add(pc.ioTimeout))
	body, _ := json.Marshal(request{Op: op, Key: key, Index: index})
	if err := framing.Write(pc.conn, body); err != nil {
		return nil, err
	}
	headerBytes, err := framing.Read(pc.conn, maxRequestFrame)
	if err != nil {
		return nil, fmt.Errorf("read response header: %w", err)
	}
	var header responseHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("decode response header: %w", err)
	}
	if !header.OK {
		return nil, errors.New("provider refused: " + header.Error)
	}
	return framing.Read(pc.conn, maxDataFrame)
}
