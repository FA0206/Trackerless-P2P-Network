package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The node's stream: a comment first, `id:` + `data:` per event, a blank
// line after each, heartbeats as comments (docs/NODE-API.md).
func TestParseSSEReadsOnlyData(t *testing.T) {
	stream := ": events from node 2b37…, level DEBUG and above\n\n" +
		"id: 1\ndata: {\"seq\":1,\"msg\":\"rpc received\"}\n\n" +
		": heartbeat\n\n" +
		"id: 3\ndata: {\"seq\":3,\"msg\":\"value stored\"}\n\n"
	var got []string
	parseSSE(strings.NewReader(stream), func(d string) { got = append(got, d) })
	if len(got) != 2 || !strings.Contains(got[0], "rpc received") || !strings.Contains(got[1], "value stored") {
		t.Fatalf("got %q", got)
	}
}

func TestXorLess(t *testing.T) {
	base := "0000000000000000000000000000000000000000"
	near := "0000000000000000000000000000000000000001"
	far := "8000000000000000000000000000000000000000"
	if !xorLess(base, near, far) || xorLess(base, far, near) || xorLess(base, near, near) {
		t.Fatal("xorLess orders by XOR distance from base")
	}
}

func newTestRecorder(t *testing.T) *recorder {
	t.Helper()
	rc, err := newRecorder(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rc.close)
	return rc
}

func piece(node string, index, of int) nodeEvent {
	return nodeEvent{Node: node, Msg: "piece received", Attrs: map[string]any{"index": float64(index), "of": float64(of)}}
}

// The kill rule (scripts/demo-seeder-death.sh): kill once the leechers
// together hold every piece while none holds them all.
func TestSpreadIsTheKillRule(t *testing.T) {
	rc := newTestRecorder(t)
	rc.add(piece("a", 0, 4))
	rc.add(piece("a", 1, 4))
	rc.add(piece("b", 2, 4))
	if union, of, done, _ := rc.spread([]string{"a", "b"}); union != 3 || of != 4 || done {
		t.Fatalf("union=%d of=%d done=%v; want 3 4 false", union, of, done)
	}
	rc.add(piece("b", 3, 4))
	union, of, done, each := rc.spread([]string{"a", "b"})
	if union != 4 || of != 4 || done || each["a"] != 2 || each["b"] != 2 {
		t.Fatalf("the kill moment: union=%d of=%d done=%v each=%v", union, of, done, each)
	}
	rc.add(piece("a", 2, 4))
	rc.add(piece("a", 3, 4))
	if _, _, done, _ := rc.spread([]string{"a", "b"}); !done {
		t.Fatal("a holds every piece: a kill now would be VOID")
	}
}

func TestRecorderCountsDroppedAndWritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	rc, err := newRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	rc.add(nodeEvent{Node: "viz0", Msg: "rpc sent", Dropped: 0})
	rc.add(nodeEvent{Node: "viz1", Msg: "rpc sent", Dropped: 2})
	if total, dropped, _ := rc.totals(); total != 2 || dropped != 2 || rc.count("rpc sent") != 2 {
		t.Fatalf("total=%d dropped=%d", total, dropped)
	}
	rc.close()
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var first nodeEvent
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first.Node != "viz0" {
		t.Fatalf("events.jsonl: %q", data)
	}
}

// In watch mode the page gets the node list from the file, and /viz/next
// refuses: vizrun only serves the page.
func TestWatchModeConfigAndNoScenes(t *testing.T) {
	dir := t.TempDir()
	watch := filepath.Join(dir, "nodes.json")
	os.WriteFile(watch, []byte(`{"nodes":[{"name":"laptop-a","api":"192.168.1.20:8700"}]}`), 0o644)
	r, err := newRunner(config{watchFile: watch, outDir: dir, webDir: dir, seedPrefix: "viz", n: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer r.rec.close()
	srv := httptest.NewServer(r.routes())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/viz/config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Nodes []pageNode `json:"nodes"`
		Watch bool       `json:"watch"`
	}
	json.NewDecoder(res.Body).Decode(&cfg)
	res.Body.Close()
	if !cfg.Watch || len(cfg.Nodes) != 1 || cfg.Nodes[0].API != "192.168.1.20:8700" {
		t.Fatalf("config = %+v", cfg)
	}
	res, err = http.Post(srv.URL+"/viz/next", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("/viz/next in watch mode: %d, want 409", res.StatusCode)
	}
}

func TestTooFewNodesRefused(t *testing.T) {
	if _, err := newRunner(config{n: 5, outDir: t.TempDir()}); err == nil {
		t.Fatal("the scenes' cast needs 18 nodes; --n 5 must be refused")
	}
}
