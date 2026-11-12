package transfer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"kademlia-demo/internal/kademlia"
)

// DefaultIOTimeout bounds every TCP dial, request, and response.
const DefaultIOTimeout = 10 * time.Second

// Peer is a DHT node plus the ability to share and download files: a
// library of files it can serve, and a TCP server serving them.
type Peer struct {
	node      *kademlia.Node
	lib       *Library
	srv       *server
	logger    *slog.Logger
	ioTimeout time.Duration
}

// NewPeer starts a TCP file server on tcpPort (0 = OS picks) next to an
// already-running DHT node.
func NewPeer(node *kademlia.Node, tcpPort int, ioTimeout time.Duration) (*Peer, error) {
	lib := newLibrary()
	srv, err := listen(tcpPort, lib, node.Logger(), ioTimeout)
	if err != nil {
		return nil, err
	}
	go srv.serve()
	return &Peer{node: node, lib: lib, srv: srv, logger: node.Logger(), ioTimeout: ioTimeout}, nil
}

// Close stops the TCP server. It does not close the DHT node.
func (p *Peer) Close() error {
	return p.srv.close()
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
	m, err := BuildManifest(path, DefaultPieceSize)
	if err != nil {
		return kademlia.ID{}, err
	}
	manifestBytes, err := EncodeManifest(m)
	if err != nil {
		return kademlia.ID{}, err
	}
	key := ContentKey(manifestBytes)
	p.lib.add(key, m, manifestBytes, path)
	if err := p.announce(ctx, key); err != nil {
		return key, err
	}
	p.logger.Info("share complete", "key", key.String(), "name", m.Name, "size", m.Size, "pieces", m.NumPieces())
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

// DownloadResult reports what a download did, for logs and the demo.
type DownloadResult struct {
	Key             kademlia.ID
	Name            string
	Size            int64
	Pieces          int
	ProvidersFound  int
	PiecesFrom      map[string]int // provider TCP address -> pieces fetched from it
	FailedProviders []string       // providers abandoned during this download
	Lookup          *kademlia.LookupResult
	Duration        time.Duration
}

// Download fetches the file with the given content key into outPath, which
// must not exist yet. Steps (docs/07-file-transfer-over-tcp.md):
//
//  1. FIND_VALUE(key) in the DHT -> provider records.
//  2. Fetch the manifest over TCP from any provider; check it hashes to key.
//  3. Fetch each piece, rotating across providers; check each piece's hash.
//     A provider that fails or sends a bad piece is dropped, and the piece
//     is retried on the next one.
//  4. Add the file to the library and announce ourselves as a provider.
//
// SIMPLIFICATION: pieces are fetched one at a time. Spreading requests
// across providers works, but nothing is downloaded in parallel. That
// matters for the download-time-vs-leechers graph, not for correctness.
func (p *Peer) Download(ctx context.Context, key kademlia.ID, outPath string) (*DownloadResult, error) {
	start := time.Now()
	lookup, err := p.node.FindValue(ctx, key)
	if err != nil {
		return nil, err
	}
	providers := p.decodeProviders(lookup.Values)
	if len(providers) == 0 {
		return nil, fmt.Errorf("transfer: no providers found for %s", key)
	}
	p.logger.Info("providers found", "key", key.String(), "count", len(providers))

	d := &downloader{ctx: ctx, providers: providers, conns: map[kademlia.ID]*providerConn{},
		dead: map[kademlia.ID]bool{}, ioTimeout: p.ioTimeout, logger: p.logger}
	defer d.closeAll()

	m, manifestBytes, err := d.fetchManifest(key)
	if err != nil {
		return nil, err
	}
	result := &DownloadResult{Key: key, Name: m.Name, Size: m.Size, Pieces: m.NumPieces(),
		ProvidersFound: len(providers), PiecesFrom: map[string]int{}, Lookup: lookup}

	if err := d.fetchPiecesInto(key, m, outPath, result); err != nil {
		result.FailedProviders = d.failed
		return result, err
	}
	result.FailedProviders = d.failed
	result.Duration = time.Since(start)

	p.lib.add(key, m, manifestBytes, outPath)
	if err := p.announce(ctx, key); err != nil {
		p.logger.Warn("downloaded but could not announce", "key", key.String(), "error", err)
	}
	p.logger.Info("download complete", "key", key.String(), "name", m.Name, "bytes", m.Size,
		"pieces", m.NumPieces(), "providers", len(providers), "failed_providers", len(d.failed),
		"duration", result.Duration.Round(time.Millisecond))
	return result, nil
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

// downloader holds the per-download state: open connections and which
// providers have been given up on.
type downloader struct {
	ctx       context.Context
	providers []ProviderRecord
	conns     map[kademlia.ID]*providerConn
	dead      map[kademlia.ID]bool
	failed    []string
	ioTimeout time.Duration
	logger    *slog.Logger
}

// candidates lists live providers starting from index `start`, so that
// consecutive pieces start at different providers (round robin).
func (d *downloader) candidates(start int) []ProviderRecord {
	var out []ProviderRecord
	for i := range d.providers {
		p := d.providers[(start+i)%len(d.providers)]
		if !d.dead[p.NodeID] {
			out = append(out, p)
		}
	}
	return out
}

// conn returns an open connection to p, dialing it on first use.
func (d *downloader) conn(p ProviderRecord) (*providerConn, error) {
	if pc, ok := d.conns[p.NodeID]; ok {
		return pc, nil
	}
	pc, err := dialProvider(d.ctx, p, d.ioTimeout)
	if err != nil {
		return nil, err
	}
	d.logger.Info("tcp connected to provider", "provider", p.Addr())
	d.conns[p.NodeID] = pc
	return pc, nil
}

// SIMPLIFICATION: one failure and a provider is dropped for the rest of
// this download -- no retries, no distinguishing a timeout from a bad
// piece.
func (d *downloader) markDead(p ProviderRecord, err error) {
	d.logger.Warn("provider failed, trying the next one", "provider", p.Addr(), "error", err)
	d.dead[p.NodeID] = true
	d.failed = append(d.failed, p.Addr())
	if pc, ok := d.conns[p.NodeID]; ok {
		pc.close()
		delete(d.conns, p.NodeID)
	}
}

func (d *downloader) closeAll() {
	for _, pc := range d.conns {
		pc.close()
	}
}

// fetch gets one item (the manifest, or one piece) from the first live
// provider, starting at `start`, whose answer passes `check`.
func (d *downloader) fetch(start int, op string, key kademlia.ID, index int, check func([]byte) error) ([]byte, ProviderRecord, error) {
	for _, p := range d.candidates(start) {
		if err := d.ctx.Err(); err != nil {
			return nil, p, err
		}
		pc, err := d.conn(p)
		if err == nil {
			var data []byte
			if data, err = pc.fetch(op, key, index); err == nil {
				if err = check(data); err == nil {
					return data, p, nil
				}
			}
		}
		d.markDead(p, err)
	}
	return nil, ProviderRecord{}, errors.New("transfer: every provider failed")
}

func (d *downloader) fetchManifest(key kademlia.ID) (Manifest, []byte, error) {
	var m Manifest
	data, _, err := d.fetch(0, opGetManifest, key, 0, func(b []byte) (err error) {
		m, err = DecodeManifest(key, b)
		return err
	})
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("transfer: fetch manifest: %w", err)
	}
	return m, data, nil
}

// fetchPiecesInto downloads every piece into a new file at outPath. On
// failure the partial file is removed: there's no resuming yet.
func (d *downloader) fetchPiecesInto(key kademlia.ID, m Manifest, outPath string, result *DownloadResult) error {
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(outPath)
		}
	}()

	for i := 0; i < m.NumPieces(); i++ {
		data, from, err := d.fetch(i, opGetPiece, key, i, func(b []byte) error { return m.VerifyPiece(i, b) })
		if err != nil {
			return fmt.Errorf("transfer: piece %d: %w", i, err)
		}
		if _, err := f.WriteAt(data, m.PieceOffset(i)); err != nil {
			return err
		}
		result.PiecesFrom[from.Addr()]++
		d.logger.Info("piece received", "index", i, "of", m.NumPieces(), "from", from.Addr(), "bytes", len(data))
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
