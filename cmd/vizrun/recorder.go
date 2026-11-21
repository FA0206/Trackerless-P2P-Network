package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// nodeEvent is one /api/events event, plus the name
// of the node whose stream it came on: events carry no node ID.
type nodeEvent struct {
	Node    string         `json:"node"`
	Seq     int64          `json:"seq"`
	Time    string         `json:"time"`
	Level   string         `json:"level"`
	Msg     string         `json:"msg"`
	Attrs   map[string]any `json:"attrs"`
	Dropped uint64         `json:"dropped"`
}

// pieceState is what one node's current download has, from its own
// "piece received" events (piece events carry no key, so it's one
// download per node at a time).
type pieceState struct {
	have     map[int]bool
	of       int
	complete bool
	gaveUp   bool
}

// recorder subscribes to every node's event stream at DEBUG level and
// keeps all of it: one line per event in events.jsonl (the ground truth
// for checking what the page shows), counts per message, each stream's
// `dropped`, and per-node piece sets for the kill rule. Scenes can also
// tap the live events.
type recorder struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	f       *os.File
	w       *bufio.Writer
	total   int
	counts  map[string]int
	dropped map[string]uint64
	closed  map[string]bool
	pieces  map[string]*pieceState
	taps    map[int]chan nodeEvent
	nextTap int
}

func newRecorder(path string) (*recorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &recorder{
		ctx: ctx, cancel: cancel,
		f: f, w: bufio.NewWriter(f),
		counts:  map[string]int{},
		dropped: map[string]uint64{},
		closed:  map[string]bool{},
		pieces:  map[string]*pieceState{},
		taps:    map[int]chan nodeEvent{},
	}, nil
}

// subscribe opens the node's /api/events?level=debug and returns once
// the node has registered the subscription (the node subscribes before
// it writes the response header), so nothing the node does afterwards
// is missed. Reading continues in a goroutine until the stream ends.
func (rc *recorder) subscribe(ctx context.Context, name, api string) error {
	req, err := http.NewRequestWithContext(rc.ctx, "GET", "http://"+api+"/api/events?level=debug", nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	go func() {
		defer res.Body.Close()
		parseSSE(res.Body, func(data string) {
			var ev nodeEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return
			}
			ev.Node = name
			rc.add(ev)
		})
		rc.mu.Lock()
		rc.closed[name] = true
		rc.mu.Unlock()
	}()
	return nil
}

// parseSSE calls fn with the data of each Server-Sent Event in r. The
// node writes only `id:` and `data:` lines and `:` comments (heartbeats),
// with a blank line after each event.
func parseSSE(r io.Reader, fn func(data string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				fn(strings.Join(data, "\n"))
				data = data[:0]
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
}

func (rc *recorder) add(ev nodeEvent) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.f == nil {
		return // closed
	}
	line, _ := json.Marshal(ev)
	rc.w.Write(line)
	rc.w.WriteByte('\n')
	rc.total++
	rc.counts[ev.Msg]++
	if ev.Dropped > rc.dropped[ev.Node] {
		rc.dropped[ev.Node] = ev.Dropped
	}
	rc.trackPieces(ev)
	for _, ch := range rc.taps {
		select {
		case ch <- ev:
		default: // a slow tap misses events; the recorder never waits
		}
	}
}

func (rc *recorder) trackPieces(ev nodeEvent) {
	ps := rc.pieces[ev.Node]
	if ps == nil {
		ps = &pieceState{have: map[int]bool{}}
		rc.pieces[ev.Node] = ps
	}
	switch ev.Msg {
	case "piece received":
		ps.have[attrInt(ev.Attrs, "index")] = true
		ps.of = attrInt(ev.Attrs, "of")
	case "download resumed":
		ps.of = attrInt(ev.Attrs, "of")
	case "download complete":
		ps.complete = true
	case "download gave up, still serving its pieces":
		ps.gaveUp = true
	}
}

// resetPieces forgets a node's piece state before it starts a download
// of a new file (scene 8 uses fresh nodes, but this keeps it explicit).
func (rc *recorder) resetPieces(name string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.pieces[name] = &pieceState{have: map[int]bool{}}
}

// pieceCount returns how many pieces a node holds of how many.
func (rc *recorder) pieceCount(name string) (have, of int, complete bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	ps := rc.pieces[name]
	if ps == nil {
		return 0, 0, false
	}
	return len(ps.have), ps.of, ps.complete
}

// spread is the kill rule's snapshot, taken under one lock: the union of
// the nodes' pieces, the file's piece count, whether any of them already
// holds (or finished) the whole file, and each node's count.
func (rc *recorder) spread(names []string) (union, of int, anyDone bool, each map[string]int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	all := map[int]bool{}
	each = map[string]int{}
	for _, n := range names {
		ps := rc.pieces[n]
		if ps == nil {
			continue
		}
		for i := range ps.have {
			all[i] = true
		}
		if ps.of > of {
			of = ps.of
		}
		if ps.complete || (ps.of > 0 && len(ps.have) >= ps.of) {
			anyDone = true
		}
		each[n] = len(ps.have)
	}
	return len(all), of, anyDone, each
}

// tap returns a channel of every event from now on, and a function to
// stop the tap.
func (rc *recorder) tap() (<-chan nodeEvent, func()) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	id := rc.nextTap
	rc.nextTap++
	ch := make(chan nodeEvent, 4096)
	rc.taps[id] = ch
	return ch, func() {
		rc.mu.Lock()
		delete(rc.taps, id)
		rc.mu.Unlock()
	}
}

// count returns how many events with this message were recorded.
func (rc *recorder) count(msg string) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.counts[msg]
}

// totals returns the number of events, the sum of every stream's
// `dropped`, and how many streams have ended.
func (rc *recorder) totals() (total int, dropped uint64, closed int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, d := range rc.dropped {
		dropped += d
	}
	return rc.total, dropped, len(rc.closed)
}

// countsCopy is a snapshot of the per-message counts (for the outro).
func (rc *recorder) countsCopy() map[string]int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	c := make(map[string]int, len(rc.counts))
	for k, v := range rc.counts {
		c[k] = v
	}
	return c
}

func (rc *recorder) close() {
	rc.cancel()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.f != nil {
		rc.w.Flush()
		rc.f.Close()
		rc.f = nil
	}
}

// attrInt reads a numeric attribute; JSON numbers decode as float64.
func attrInt(attrs map[string]any, key string) int {
	switch v := attrs[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
