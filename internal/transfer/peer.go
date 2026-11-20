package transfer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"kademlia-demo/internal/kademlia"
)

// Defaults for Config.
const (
	DefaultIOTimeout   = 10 * time.Second // every TCP dial, request, and response
	DefaultMaxInFlight = 4                // piece requests in flight per download
)

// Config holds a Peer's settings. Zero values get the defaults.
type Config struct {
	TCPPort     int           // file server port; 0 lets the OS pick
	IOTimeout   time.Duration // default DefaultIOTimeout
	MaxInFlight int           // default DefaultMaxInFlight; per provider it's always 1
}

// Peer is a DHT node plus the ability to share and download files: a
// library of files it can serve (complete or partial), and a TCP server
// serving them.
type Peer struct {
	node        *kademlia.Node
	lib         *Library
	srv         *server
	logger      *slog.Logger
	ioTimeout   time.Duration
	maxInFlight int
}

// NewPeer starts a TCP file server next to an already-running DHT node.
func NewPeer(node *kademlia.Node, cfg Config) (*Peer, error) {
	if cfg.IOTimeout == 0 {
		cfg.IOTimeout = DefaultIOTimeout
	}
	if cfg.MaxInFlight == 0 {
		cfg.MaxInFlight = DefaultMaxInFlight
	}
	lib := newLibrary()
	srv, err := listen(cfg.TCPPort, lib, node.Logger(), cfg.IOTimeout)
	if err != nil {
		return nil, err
	}
	go srv.serve()
	return &Peer{node: node, lib: lib, srv: srv, logger: node.Logger(),
		ioTimeout: cfg.IOTimeout, maxInFlight: cfg.MaxInFlight}, nil
}

// Close stops the TCP server and closes its open connections. It does
// not close the DHT node.
func (p *Peer) Close() error {
	return p.srv.close()
}

// SetDebugServeDelay is a DEBUG ONLY knob (CLI: --debug-serve-delay): the
// file server waits d before answering each GET_PIECE, so demos can kill a
// provider while downloads are still running. 0 turns it off.
func (p *Peer) SetDebugServeDelay(d time.Duration) {
	p.srv.debugServeDelay.Store(int64(d))
}

// TCPPort is the port the file server listens on.
func (p *Peer) TCPPort() int {
	return p.srv.port()
}

// Library lists the files this peer can serve.
func (p *Peer) Library() *Library {
	return p.lib
}

// Share makes a local file downloadable: cut it into pieces, add it to the
// library, and announce a provider record under its content key.
func (p *Peer) Share(ctx context.Context, path string) (kademlia.ID, error) {
	m, tree, err := BuildManifest(path, DefaultPieceSize)
	if err != nil {
		return kademlia.ID{}, err
	}
	manifestBytes, err := EncodeManifest(m)
	if err != nil {
		return kademlia.ID{}, err
	}
	key := ContentKey(manifestBytes)
	p.lib.addComplete(key, m, manifestBytes, path, tree)
	if err := p.announce(ctx, key); err != nil {
		return key, err
	}
	p.logger.Info("share complete", "key", key.String(), "name", m.Name, "size", m.Size, "pieces", m.NumPieces(),
		"merkle_root", m.MerkleRoot, "manifest_bytes", len(manifestBytes))
	return key, nil
}

// announce publishes "I have this file, reach me at IP:TCP-port" in the DHT.
func (p *Peer) announce(ctx context.Context, key kademlia.ID) error {
	self := p.node.Self()
	record := ProviderRecord{NodeID: self.ID, IP: self.IP, Port: p.TCPPort()}
	res, err := p.node.Store(ctx, key, record.encode())
	if err != nil {
		return fmt.Errorf("transfer: announce %s: %w", key, err)
	}
	p.logger.Info("provider announced", "key", key.String(), "tcp_addr", record.Addr(),
		"replicas", len(res.StoredAt)+boolToInt(res.StoredLocally))
	return nil
}

// decodeProviders turns DHT values into provider records, dropping
// malformed values, duplicates, and ourselves.
func (p *Peer) decodeProviders(values [][]byte) []ProviderRecord {
	var out []ProviderRecord
	seen := map[kademlia.ID]bool{}
	for _, v := range values {
		rec, err := decodeProviderRecord(v)
		if err != nil {
			p.logger.Warn("ignoring bad provider record", "error", err)
			continue
		}
		if rec.NodeID == p.node.ID || seen[rec.NodeID] {
			continue
		}
		seen[rec.NodeID] = true
		out = append(out, rec)
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
