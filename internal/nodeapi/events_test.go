package nodeapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kademlia-demo/internal/kademlia"
)

// textHandler mimics kademlia.NewLogger's handler but writes to buf, with
// the time left out so two runs can be compared byte for byte.
func textHandler(buf *bytes.Buffer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(groups) == 0 {
			return slog.Attr{}
		}
		return a
	}})
}

// logSome emits the same mix of records the node does: different levels,
// attrs of different kinds, a With() chain, and an error.
func logSome(l *slog.Logger) {
	l.Debug("bucket updated", "contact", "abc@127.0.0.1:9000")
	l.Info("rpc sent", "type", "PING", "to", "def@127.0.0.1:9001")
	l.Info("piece received", "index", 3, "of", 16, "bytes", 65536)
	l.With("peer", "x").Warn("provider failed, trying the next one", "error", errors.New("connection refused"))
	l.Info("download complete", "duration", (2004 * time.Millisecond).Round(time.Millisecond))
}

// The core promise of the tap: what the node prints doesn't change.
func TestTeeLeavesPrintedOutputUnchanged(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn} {
		var plain, tapped bytes.Buffer
		// Built like cmd/node: NewLogger's handler with the node attr,
		// then (with --api) wrapped.
		logSome(slog.New(textHandler(&plain, level)).With("node", "2b370ab7"))

		events := NewEvents()
		sub, unsubscribe := events.subscribe(slog.LevelDebug) // a viewer wanting everything
		base := slog.New(textHandler(&tapped, level)).With("node", "2b370ab7")
		logSome(slog.New(TeeHandler(base.Handler(), events)))
		unsubscribe()

		if plain.String() != tapped.String() {
			t.Errorf("level %s: printed output changed.\nwithout tap:\n%s\nwith tap:\n%s", level, plain.String(), tapped.String())
		}
		if len(sub.ch) != 5 {
			t.Errorf("level %s: the viewer should get all 5 records regardless of the printed level, got %d", level, len(sub.ch))
		}
	}
}

func TestTeeCostsNothingWithoutSubscribers(t *testing.T) {
	var buf bytes.Buffer
	events := NewEvents()
	h := TeeHandler(textHandler(&buf, slog.LevelInfo), events)
	ctx := context.Background()
	if h.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("no subscribers: DEBUG records should not even be built")
	}
	_, unsubDebug := events.subscribe(slog.LevelDebug)
	if !h.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("a DEBUG subscriber should enable DEBUG records")
	}
	_, unsubInfo := events.subscribe(slog.LevelInfo)
	unsubDebug()
	if h.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("only an INFO subscriber left: DEBUG should be off again")
	}
	unsubInfo()
	if events.minLevel.Load() != noSubscribers {
		t.Fatal("no subscribers left, but some level is still wanted")
	}
}

// A viewer that never reads must not slow down logging, ever.
func TestPublishNeverBlocks(t *testing.T) {
	events := NewEvents()
	sub, unsubscribe := events.subscribe(slog.LevelInfo)
	defer unsubscribe()
	logger := slog.New(TeeHandler(slog.DiscardHandler, events))

	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer+10; i++ {
			logger.Info("rpc sent")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logging blocked on a viewer that doesn't read")
	}
	if len(sub.ch) != subscriberBuffer || sub.dropped.Load() != 10 {
		t.Errorf("want %d buffered and 10 dropped, got %d and %d", subscriberBuffer, len(sub.ch), sub.dropped.Load())
	}
}

func TestEventCarriesAttrsWithNaturalTypes(t *testing.T) {
	events := NewEvents()
	sub, unsubscribe := events.subscribe(slog.LevelDebug)
	defer unsubscribe()
	logger := slog.New(TeeHandler(slog.DiscardHandler, events)).With("peer", "x").WithGroup("g")
	logger.Warn("something", "index", 3, "ok", true, "took", 2*time.Second, "error", errors.New("boom"))

	ev := <-sub.ch
	want := map[string]any{"peer": "x", "g.index": int64(3), "g.ok": true, "g.took": "2s", "g.error": "boom"}
	if ev.Msg != "something" || ev.Level != "WARN" || ev.Seq != 1 {
		t.Errorf("got msg=%q level=%q seq=%d", ev.Msg, ev.Level, ev.Seq)
	}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Errorf("attr %s = %#v, want %#v", k, ev.Attrs[k], v)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, ev.Time); err != nil {
		t.Errorf("time %q: %v", ev.Time, err)
	}
}

// openStream connects to a node's /api/events and returns a function that
// waits for the next SSE event matching pred. The returned cancel closes
// the stream; tests must call it before the httptest server closes.
func openStream(t *testing.T, tn testNode, query string) (next func(pred func(wireEvent) bool) wireEvent, cancel func()) {
	t.Helper()
	// stopRequest, not cancel: `cancel` is the named result, and `:=` in
	// the same scope would reuse it, making the returned closure call itself.
	ctx, stopRequest := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", tn.api.URL+"/api/events"+query, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		stopRequest()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		stopRequest()
		t.Fatalf("status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	// The first thing sent is a comment; once it arrives, we're subscribed.
	select {
	case l := <-lines:
		if !strings.HasPrefix(l, ": events from node") {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no greeting from the stream")
	}
	next = func(pred func(wireEvent) bool) wireEvent {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatal("stream ended")
				}
				data, isData := strings.CutPrefix(l, "data: ")
				if !isData {
					continue
				}
				var ev wireEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatalf("bad event %q: %v", data, err)
				}
				if pred(ev) {
					return ev
				}
			case <-deadline:
				t.Fatal("timed out waiting for an event")
			}
		}
	}
	return next, func() { stopRequest(); resp.Body.Close() }
}

// wireEvent is what a client decodes from one SSE data line.
type wireEvent struct {
	Event
	Dropped uint64 `json:"dropped"`
}

func TestEventStreamShowsRealRPCs(t *testing.T) {
	nodes := startNodes(t, 2)
	next, cancel := openStream(t, nodes[0], "")
	defer cancel()

	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if !nodes[1].node.Ping(ctx, nodes[0].node.Self()) {
		t.Fatal("ping failed")
	}
	ev := next(func(e wireEvent) bool { return e.Msg == "rpc received" && e.Attrs["type"] == "PING" })
	from, _ := ev.Attrs["from"].(string)
	if !strings.HasPrefix(from, nodes[1].node.ID.String()+"@") {
		t.Errorf("PING should come from node1 (%s), event says from=%q", nodes[1].node.ID, from)
	}
	if ev.Level != "INFO" || ev.Dropped != 0 || ev.Seq == 0 {
		t.Errorf("got level=%s dropped=%d seq=%d", ev.Level, ev.Dropped, ev.Seq)
	}
	// The default level is info: DEBUG records (e.g. "bucket updated")
	// must not show up. Store something so more records flow, and check
	// none of them is DEBUG.
	if _, err := nodes[1].node.Store(ctx, kademlia.HashID([]byte("k")), []byte("v")); err != nil {
		t.Fatal(err)
	}
	next(func(e wireEvent) bool {
		if e.Level == "DEBUG" {
			t.Errorf("DEBUG event %q on an info stream", e.Msg)
		}
		return e.Msg == "rpc received" && e.Attrs["type"] == "STORE"
	})
}

func TestEventStreamDebugLevel(t *testing.T) {
	nodes := startNodes(t, 2)
	next, cancel := openStream(t, nodes[0], "?level=debug")
	defer cancel()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	nodes[1].node.Ping(ctx, nodes[0].node.Self())
	// Every received message updates the sender's bucket (DEBUG).
	next(func(e wireEvent) bool { return e.Msg == "bucket updated" && e.Level == "DEBUG" })
}

func TestEventStreamUnsubscribesWhenTheViewerLeaves(t *testing.T) {
	nodes := startNodes(t, 1)
	_, cancel := openStream(t, nodes[0], "")
	if n := nodes[0].events.subscriberCount(); n != 1 {
		t.Fatalf("want 1 subscriber, got %d", n)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for nodes[0].events.subscriberCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the subscription outlived its viewer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if nodes[0].events.wants(slog.LevelError) {
		t.Error("no viewers left, but events are still wanted")
	}
}

func TestEventStreamRejectsABadLevel(t *testing.T) {
	nodes := startNodes(t, 1)
	resp, err := http.Get(nodes[0].api.URL + "/api/events?level=loud")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}

// httptestServer serves api for the rest of the test and returns its URL.
func httptestServer(t *testing.T, api *Server) string {
	t.Helper()
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestNoEventsRouteWithoutAHub(t *testing.T) {
	nodes := startNodes(t, 1)
	api := New(Config{Node: nodes[0].node, Peer: nodes[0].peer}) // Events: nil
	srv := httptestServer(t, api)
	resp, err := http.Get(srv + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}
