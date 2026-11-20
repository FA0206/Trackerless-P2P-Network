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
	data, _, err := pc.exchange(op, key, index)
	return data, err
}

// fetchPiece fetches piece i and the Merkle proof that came with it. The
// proof is only parsed here, not trusted: VerifyPiece checks it.
func (pc *providerConn) fetchPiece(key kademlia.ID, i int) ([]byte, []Hash, error) {
	data, header, err := pc.exchange(opGetPiece, key, i)
	if err != nil {
		return nil, nil, err
	}
	if len(header.Proof) > maxProofLen {
		return nil, nil, fmt.Errorf("proof for piece %d has %d hashes, at most %d possible", i, len(header.Proof), maxProofLen)
	}
	proof := make([]Hash, len(header.Proof))
	for j, s := range header.Proof {
		if proof[j], err = parseHash(s); err != nil {
			return nil, nil, fmt.Errorf("proof for piece %d: %w", i, err)
		}
	}
	return data, proof, nil
}

// exchange sends one request and reads the response header and data frame.
func (pc *providerConn) exchange(op string, key kademlia.ID, index int) ([]byte, responseHeader, error) {
	var header responseHeader
	pc.conn.SetDeadline(time.Now().Add(pc.ioTimeout))
	body, _ := json.Marshal(request{Op: op, Key: key, Index: index})
	if err := framing.Write(pc.conn, body); err != nil {
		return nil, header, err
	}
	headerBytes, err := framing.Read(pc.conn, maxHeaderFrame)
	if err != nil {
		return nil, header, fmt.Errorf("read response header: %w", err)
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, header, fmt.Errorf("decode response header: %w", err)
	}
	if !header.OK {
		return nil, header, errors.New("provider refused: " + header.Error)
	}
	data, err := framing.Read(pc.conn, maxDataFrame)
	return data, header, err
}
