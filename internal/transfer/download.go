package transfer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"time"

	"kademlia-demo/internal/kademlia"
)

// Swarm-download timers (docs/08-swarming.md).
// SIMPLIFICATION: fixed values, not tuned for real networks.
const (
	bitfieldMaxAge  = 500 * time.Millisecond // re-ask an idle provider what it has after this
	rediscoverEvery = 1 * time.Second        // re-run FIND_VALUE to find new providers
	loopTick        = 100 * time.Millisecond // re-check timers at least this often
	announceTimeout = 30 * time.Second
)

// stallTimeout: give up after this long with no way to progress. A var
// only so tests can shorten it.
var stallTimeout = 15 * time.Second

// DownloadResult reports what a download did, for logs and the demos.
type DownloadResult struct {
	Key             kademlia.ID
	Name            string
	Size            int64
	Pieces          int
	ProvidersFound  int            // every provider learned of, incl. later rediscoveries
	PiecesFrom      map[string]int // provider TCP address -> pieces fetched from it
	FailedProviders []string       // providers abandoned during this download
	MaxInFlight     int            // most piece requests that were outstanding at once
	Lookup          *kademlia.LookupResult
	Duration        time.Duration
}

// Download fetches the file with the given content key into outPath,
// which must not exist yet, from every provider the DHT knows of, in
// parallel (docs/08-swarming.md):
//
//  1. FIND_VALUE(key) -> provider records.
//  2. Fetch the manifest from any provider; check it hashes to key.
//  3. Create outPath (O_EXCL) and add it to the library as a PARTIAL file:
//     from now on this peer serves the pieces it has.
//  4. Event loop: ask providers for their bitfields, fetch pieces
//     rarest-first from idle providers (1 request per provider, at most
//     MaxInFlight in total), verify, write, publish each piece; announce
//     after the first piece; rediscover providers every second.
//  5. Fail if no live provider has a missing piece for stallTimeout.
func (p *Peer) Download(ctx context.Context, key kademlia.ID, outPath string) (*DownloadResult, error) {
	if _, _, ok := p.lib.manifestFor(key); ok {
		return nil, fmt.Errorf("transfer: %s is already in the library", key)
	}
	start := time.Now()
	lookup, err := p.node.FindValue(ctx, key)
	if err != nil {
		return nil, err
	}
	records := p.decodeProviders(lookup.Values)
	if len(records) == 0 {
		return nil, fmt.Errorf("transfer: no providers found for %s", key)
	}
	p.logger.Info("providers found", "key", key.String(), "count", len(records))

	ctx, cancel := context.WithCancel(ctx)
	d := &swarmDownload{p: p, ctx: ctx, key: key, providers: map[kademlia.ID]*providerState{},
		inflight: map[int]kademlia.ID{}, events: make(chan event),
		rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	defer func() { cancel(); d.release() }()
	for _, r := range records {
		d.addProvider(r)
	}

	manifestBytes, err := d.fetchManifest()
	if err != nil {
		return nil, err
	}
	m := d.manifest
	d.result = &DownloadResult{Key: key, Name: m.Name, Size: m.Size, Pieces: m.NumPieces(),
		PiecesFrom: map[string]int{}, Lookup: lookup}
	if err := d.openFile(outPath, manifestBytes); err != nil {
		return nil, err
	}
	if err := d.run(); err != nil {
		d.abort(outPath)
		d.finishResult(start)
		return d.result, err
	}
	if err := d.file.Close(); err != nil {
		d.abort(outPath)
		return d.result, err
	}
	d.finishResult(start)
	p.logger.Info("download complete", "key", key.String(), "name", m.Name, "bytes", m.Size,
		"pieces", m.NumPieces(), "providers", d.result.ProvidersFound,
		"failed_providers", len(d.result.FailedProviders), "max_inflight", d.result.MaxInFlight,
		"duration", d.result.Duration.Round(time.Millisecond))
	return d.result, nil
}

// providerState is the download's view of one provider. Owned by the
// event-loop goroutine; never touched by workers.
type providerState struct {
	rec    ProviderRecord
	conn   *providerConn // nil until dialed; lent to the worker while busy
	have   Bitfield      // nil until its first GET_BITFIELD
	haveAt time.Time
	busy   bool // a request (piece or bitfield) is outstanding
	dead   bool
}

type eventKind int

const (
	evPiece eventKind = iota
	evBitfield
	evDiscovered
	evAnnounced
)

// event is the one message a worker sends back when it's done.
type event struct {
	kind     eventKind
	provider kademlia.ID
	conn     *providerConn // returned to the loop (nil if the dial failed)
	piece    int
	data     []byte
	have     Bitfield
	records  []ProviderRecord
	err      error
}

// swarmDownload is one download's state. Invariant 6 (CLAUDE.md): only the
// event-loop goroutine (Download -> run) reads or writes it; workers get
// the few values they need as arguments and report back on `events`.
type swarmDownload struct {
	p         *Peer
	ctx       context.Context
	key       kademlia.ID
	manifest  Manifest
	file      *os.File
	providers map[kademlia.ID]*providerState
	order     []kademlia.ID // providers in the order we learned of them
	failed    []string

	have     Bitfield            // pieces verified and written
	haveN    int                 // count of set bits in `have`
	inflight map[int]kademlia.ID // piece -> provider it was requested from
	events   chan event
	busyBF   int // bitfield requests outstanding
	pending  int // workers that haven't reported back yet

	discovering   bool
	lastDiscovery time.Time
	announcing    bool
	announced     bool
	announceAfter time.Time // earliest retry after a failed announce

	rng    *rand.Rand
	result *DownloadResult
}

func (d *swarmDownload) addProvider(r ProviderRecord) bool {
	if _, known := d.providers[r.NodeID]; known {
		return false // includes dead ones: they stay dead (SIMPLIFICATION, docs/08)
	}
	d.providers[r.NodeID] = &providerState{rec: r}
	d.order = append(d.order, r.NodeID)
	return true
}

// SIMPLIFICATION: one failure and a provider is dropped for the rest of
// this download -- no retries.
func (d *swarmDownload) markDead(ps *providerState, err error) {
	if ps.dead {
		return
	}
	ps.dead = true
	if ps.conn != nil {
		ps.conn.close()
		ps.conn = nil
	}
	d.failed = append(d.failed, ps.rec.Addr())
	d.p.logger.Warn("provider failed, trying the next one", "provider", ps.rec.Addr(), "error", err)
}

// fetchManifest asks providers one at a time, before any concurrency
// starts, until one returns a manifest that hashes to the key. A partial
// provider can answer: it has the manifest from the start of its download.
func (d *swarmDownload) fetchManifest() ([]byte, error) {
	for _, id := range d.order {
		ps := d.providers[id]
		conn, err := dialProvider(d.ctx, ps.rec, d.p.ioTimeout)
		if err == nil {
			d.p.logger.Info("tcp connected to provider", "provider", ps.rec.Addr())
			ps.conn = conn
			var data []byte
			if data, err = conn.fetch(opGetManifest, d.key, 0); err == nil {
				if d.manifest, err = DecodeManifest(d.key, data); err == nil {
					return data, nil
				}
			}
		}
		d.markDead(ps, err)
	}
	return nil, errors.New("transfer: fetch manifest: every provider failed")
}

// openFile creates the output file (never overwriting anything) and
// registers it as a partial library entry.
func (d *swarmDownload) openFile(outPath string, manifestBytes []byte) error {
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := d.p.lib.addPartial(d.key, d.manifest, manifestBytes, outPath); err != nil {
		f.Close()
		os.Remove(outPath) // we just created it
		return err
	}
	d.file = f
	d.have = NewBitfield(d.manifest.NumPieces())
	return nil
}

// abort stops serving the partial file, then deletes it (no resume yet).
func (d *swarmDownload) abort(outPath string) {
	d.p.lib.remove(d.key)
	d.file.Close()
	os.Remove(outPath)
}

func (d *swarmDownload) finishResult(start time.Time) {
	d.result.ProvidersFound = len(d.providers)
	d.result.FailedProviders = d.failed
	d.result.Duration = time.Since(start)
}

// release closes idle connections and drains workers still running, so
// none of them blocks forever on `events`.
func (d *swarmDownload) release() {
	for _, ps := range d.providers {
		if ps.conn != nil {
			ps.conn.close()
		}
	}
	if n := d.pending; n > 0 {
		events := d.events
		go func() {
			for i := 0; i < n; i++ {
				if ev := <-events; ev.conn != nil {
					ev.conn.close()
				}
			}
		}()
	}
}

// run is the event loop. It returns nil once every piece is written and
// this peer has announced itself.
func (d *swarmDownload) run() error {
	n := d.manifest.NumPieces()
	ticker := time.NewTicker(loopTick)
	defer ticker.Stop()
	var stalledSince time.Time
	for d.haveN < n {
		d.schedule()
		if !d.canProgress() {
			if stalledSince.IsZero() {
				stalledSince = time.Now()
				d.lastDiscovery = time.Time{} // look for providers right away
				d.p.logger.Warn("download stalled: no live provider has the missing pieces; looking for more",
					"have", d.haveN, "of", n)
			} else if time.Since(stalledSince) > stallTimeout {
				return fmt.Errorf("transfer: stalled for %s with %d of %d pieces: no live provider has the rest",
					stallTimeout, d.haveN, n)
			}
		} else {
			stalledSince = time.Time{}
		}
		if err := d.wait(ticker.C); err != nil {
			return err
		}
	}
	// Make sure a finished download is announced before returning, so the
	// next downloader can find us.
	for d.announcing {
		if err := d.wait(nil); err != nil {
			return err
		}
	}
	if !d.announced {
		d.announceNow()
	}
	return nil
}

// wait handles one event (or a tick).
func (d *swarmDownload) wait(tick <-chan time.Time) error {
	select {
	case ev := <-d.events:
		d.pending--
		return d.handle(ev)
	case <-tick:
		return nil
	case <-d.ctx.Done():
		return d.ctx.Err()
	}
}

// schedule starts every request that's allowed right now.
func (d *swarmDownload) schedule() {
	now := time.Now()
	for len(d.inflight) < d.p.maxInFlight {
		holders, ids := d.holderViews()
		piece, h, ok := pickPiece(d.needed(), d.manifest.NumPieces(), holders, d.rng)
		if !ok {
			break
		}
		d.startPiece(d.providers[ids[h]], piece)
	}
	n := d.manifest.NumPieces()
	for _, id := range d.order {
		ps := d.providers[id]
		if ps.dead || ps.busy {
			continue
		}
		if ps.have == nil || (ps.have.Count(n) < n && now.Sub(ps.haveAt) >= bitfieldMaxAge) {
			d.startBitfield(ps)
		}
	}
	if !d.discovering && now.Sub(d.lastDiscovery) >= rediscoverEvery {
		d.startDiscovery()
	}
	if !d.announced && !d.announcing && d.haveN > 0 && now.After(d.announceAfter) {
		d.startAnnounce()
	}
}

// canProgress reports whether a missing piece can still arrive from a
// provider we know: one is in flight, or some live provider has (or, its
// bitfield not fetched yet, might have) a piece we still need. Bitfield
// polling alone doesn't count -- it would otherwise reset the stall timer
// every 500 ms forever.
func (d *swarmDownload) canProgress() bool {
	if len(d.inflight) > 0 {
		return true
	}
	need := d.needed()
	n := d.manifest.NumPieces()
	for _, ps := range d.providers {
		if ps.dead {
			continue
		}
		if ps.have == nil {
			return true
		}
		for i := 0; i < n; i++ {
			if need.Has(i) && ps.have.Has(i) {
				return true
			}
		}
	}
	return false
}

// needed: pieces we don't have and aren't already fetching.
func (d *swarmDownload) needed() Bitfield {
	n := d.manifest.NumPieces()
	need := NewBitfield(n)
	for i := 0; i < n; i++ {
		if _, busy := d.inflight[i]; !d.have.Has(i) && !busy {
			need.Set(i)
		}
	}
	return need
}

// holderViews lists live providers for the picker, with parallel IDs.
func (d *swarmDownload) holderViews() ([]holderView, []kademlia.ID) {
	var views []holderView
	var ids []kademlia.ID
	for _, id := range d.order {
		ps := d.providers[id]
		if ps.dead {
			continue
		}
		views = append(views, holderView{have: ps.have, idle: !ps.busy})
		ids = append(ids, id)
	}
	return views, ids
}

// lend hands the provider's connection to a worker; the worker returns it
// in its event (dialing first if there was none).
func (d *swarmDownload) lend(ps *providerState) *providerConn {
	ps.busy = true
	conn := ps.conn
	ps.conn = nil
	d.pending++
	return conn
}

func (d *swarmDownload) startPiece(ps *providerState, piece int) {
	d.inflight[piece] = ps.rec.NodeID
	if len(d.inflight) > d.result.MaxInFlight {
		d.result.MaxInFlight = len(d.inflight)
	}
	d.p.logger.Info("piece requested", "index", piece, "from", ps.rec.Addr(), "inflight", len(d.inflight))
	conn, rec, key, m, ctx := d.lend(ps), ps.rec, d.key, d.manifest, d.ctx
	timeout, logger, events := d.p.ioTimeout, d.p.logger, d.events
	go func() {
		ev := event{kind: evPiece, provider: rec.NodeID, piece: piece}
		conn, ev.err = ensureConn(ctx, conn, rec, timeout, logger)
		if ev.err == nil {
			if ev.data, ev.err = conn.fetch(opGetPiece, key, piece); ev.err == nil {
				ev.err = m.VerifyPiece(piece, ev.data)
			}
		}
		ev.conn = conn
		events <- ev
	}()
}

func (d *swarmDownload) startBitfield(ps *providerState) {
	d.busyBF++
	conn, rec, key, n, ctx := d.lend(ps), ps.rec, d.key, d.manifest.NumPieces(), d.ctx
	timeout, logger, events := d.p.ioTimeout, d.p.logger, d.events
	go func() {
		ev := event{kind: evBitfield, provider: rec.NodeID}
		conn, ev.err = ensureConn(ctx, conn, rec, timeout, logger)
		if ev.err == nil {
			var data []byte
			if data, ev.err = conn.fetch(opGetBitfield, key, 0); ev.err == nil {
				ev.have, ev.err = decodeBitfield(data, n)
			}
		}
		ev.conn = conn
		events <- ev
	}()
}

func (d *swarmDownload) startDiscovery() {
	d.discovering = true
	d.pending++
	p, key, ctx, events := d.p, d.key, d.ctx, d.events
	go func() {
		ev := event{kind: evDiscovered}
		var lookup *kademlia.LookupResult
		if lookup, ev.err = p.node.FindValue(ctx, key); ev.err == nil {
			ev.records = p.decodeProviders(lookup.Values)
		}
		events <- ev
	}()
}

// startAnnounce runs on its own timeout, not the download's context: the
// download may finish (and cancel its context) while the announce is
// still storing our record.
func (d *swarmDownload) startAnnounce() {
	d.announcing = true
	d.pending++
	p, key, events := d.p, d.key, d.events
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
		defer cancel()
		events <- event{kind: evAnnounced, err: p.announce(ctx, key)}
	}()
}

func (d *swarmDownload) announceNow() {
	ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
	defer cancel()
	if err := d.p.announce(ctx, d.key); err != nil {
		d.p.logger.Warn("downloaded but could not announce", "key", d.key.String(), "error", err)
		return
	}
	d.announced = true
}

// ensureConn dials rec unless the worker was lent an open connection.
func ensureConn(ctx context.Context, conn *providerConn, rec ProviderRecord, timeout time.Duration, logger *slog.Logger) (*providerConn, error) {
	if conn != nil {
		return conn, nil
	}
	conn, err := dialProvider(ctx, rec, timeout)
	if err == nil {
		logger.Info("tcp connected to provider", "provider", rec.Addr())
	}
	return conn, err
}

// handle applies a worker's result to the download state.
func (d *swarmDownload) handle(ev event) error {
	switch ev.kind {
	case evPiece:
		ps := d.providers[ev.provider]
		ps.busy, ps.conn = false, ev.conn
		delete(d.inflight, ev.piece)
		if ev.err != nil {
			d.markDead(ps, ev.err)
			return nil
		}
		if _, err := d.file.WriteAt(ev.data, d.manifest.PieceOffset(ev.piece)); err != nil {
			return err
		}
		d.have.Set(ev.piece) // write, then publish:
		d.haveN++
		d.p.lib.markHave(d.key, ev.piece) // only now can our server hand it out
		d.result.PiecesFrom[ps.rec.Addr()]++
		d.p.logger.Info("piece received", "index", ev.piece, "of", d.manifest.NumPieces(),
			"from", ps.rec.Addr(), "bytes", len(ev.data), "have", d.haveN, "inflight", len(d.inflight))
	case evBitfield:
		ps := d.providers[ev.provider]
		ps.busy, ps.conn = false, ev.conn
		d.busyBF--
		if ev.err != nil {
			d.markDead(ps, ev.err)
			return nil
		}
		ps.have, ps.haveAt = ev.have, time.Now()
	case evDiscovered:
		d.discovering, d.lastDiscovery = false, time.Now()
		added := 0
		for _, r := range ev.records {
			if d.addProvider(r) {
				added++
			}
		}
		if added > 0 {
			d.p.logger.Info("new providers found", "key", d.key.String(), "added", added, "known", len(d.providers))
		}
	case evAnnounced:
		d.announcing = false
		if ev.err != nil {
			d.announceAfter = time.Now().Add(rediscoverEvery)
			d.p.logger.Warn("announce failed; will retry", "key", d.key.String(), "error", ev.err)
		} else {
			d.announced = true
		}
	}
	return nil
}
