package nodeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kademlia-demo/internal/kademlia"
)

// post sends body as JSON to tn's API and decodes the reply into out (if
// not nil). It returns the HTTP status.
func post(t *testing.T, tn testNode, path string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(tn.api.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("POST %s: decode: %v", path, err)
		}
	}
	return resp.StatusCode
}

func ids(cs []ResultContact) []kademlia.ID {
	out := make([]kademlia.ID, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func TestLookupMatchesTheNodesOwnLookup(t *testing.T) {
	nodes := startNodes(t, 5)
	target := kademlia.HashID([]byte("somewhere"))
	var res LookupResponse
	if st := post(t, nodes[4], "/api/lookup", LookupRequest{Target: target.String()}, &res); st != http.StatusOK {
		t.Fatalf("status %d", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	direct, err := nodes[4].node.Lookup(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != target || len(res.Closest) != len(direct.Closest) || len(res.Closest) != 3 {
		t.Fatalf("API: %d contacts for %s; node: %d", len(res.Closest), res.Target, len(direct.Closest))
	}
	for i, c := range res.Closest {
		if c.ID != direct.Closest[i].ID {
			t.Errorf("closest[%d]: API %s, node %s", i, c.ID, direct.Closest[i].ID)
		}
		if c.Distance != kademlia.Xor(c.ID, target) {
			t.Errorf("closest[%d]: distance should be to the target", i)
		}
	}
	if res.Rounds == 0 || res.RPCsSent == 0 || res.RPCsFailed != 0 {
		t.Errorf("counts: %+v", res)
	}

	var self LookupResponse
	post(t, nodes[4], "/api/lookup", LookupRequest{Target: "self"}, &self)
	if self.Target != nodes[4].node.ID {
		t.Errorf(`"self" looked up %s, want the node's own ID`, self.Target)
	}
}

func TestPingReportsWhoAnswered(t *testing.T) {
	nodes := startNodes(t, 2)
	var alive PingResponse
	addr := "127.0.0.1:" + strconv.Itoa(nodes[0].node.Self().Port)
	post(t, nodes[1], "/api/ping", PingRequest{Addr: addr}, &alive)
	if !alive.Alive || alive.ID != nodes[0].node.ID || alive.RTTMS <= 0 {
		t.Errorf("ping %s: got %+v, want alive with node0's ID", addr, alive)
	}

	// A port nobody listens on: a fresh UDP socket's port, released right away.
	dead := "127.0.0.1:" + strconv.Itoa(closedUDPPort(t))
	var gone PingResponse
	post(t, nodes[1], "/api/ping", PingRequest{Addr: dead}, &gone)
	if gone.Alive || !gone.ID.IsZero() {
		t.Errorf("ping %s: got %+v, want not alive", dead, gone)
	}
}

func closedUDPPort(t *testing.T) int {
	t.Helper()
	n, err := kademlia.NewNode(kademlia.Config{ID: kademlia.RandomID()})
	if err != nil {
		t.Fatal(err)
	}
	port := n.Addr().Port
	n.Close()
	return port
}

func TestPutOnOneNodeGetOnAnother(t *testing.T) {
	nodes := startNodes(t, 5)
	var put PutResponse
	if st := post(t, nodes[0], "/api/put", PutRequest{Name: "greeting", Text: "hello over http"}, &put); st != http.StatusOK {
		t.Fatalf("put: status %d", st)
	}
	key := kademlia.HashID([]byte("greeting"))
	if put.Key != key || len(put.StoredAt)+boolToInt(put.StoredLocally) != 3 || put.Failed != 0 {
		t.Fatalf("put: %+v, want key %s on 3 nodes", put, key)
	}
	// The API's answer matches what the holders themselves report.
	for _, c := range put.StoredAt {
		for _, tn := range nodes {
			if tn.node.ID == c.ID && len(tn.node.StoredValues()[key]) != 1 {
				t.Errorf("put says %s holds it, but it doesn't", c.ID)
			}
		}
	}

	var got GetResponse
	post(t, nodes[4], "/api/get", GetRequest{Name: "greeting"}, &got)
	if got.Key != key || len(got.Values) != 1 || got.Values[0].Text == nil || *got.Values[0].Text != "hello over http" {
		t.Fatalf("get: %+v", got)
	}
	if !got.FromLocalStore && len(got.From) == 0 {
		t.Error("get: found the value but says from nobody")
	}

	var missing GetResponse
	post(t, nodes[4], "/api/get", GetRequest{Name: "nothing here"}, &missing)
	if missing.Values == nil || len(missing.Values) != 0 {
		t.Errorf("get of a missing key: want an empty list, got %#v", missing.Values)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestShareOnOneNodeFetchOnAnother(t *testing.T) {
	nodes := startNodes(t, 4)
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	content := bytes.Repeat([]byte("kademlia "), 20000) // 180 KB: 3 pieces
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	var shared File
	if st := post(t, nodes[1], "/api/share", ShareRequest{Path: src}, &shared); st != http.StatusOK {
		t.Fatalf("share: status %d", st)
	}
	if shared.Name != "notes.txt" || shared.Size != int64(len(content)) || shared.Pieces != 3 || shared.Have != 3 || shared.Path != src {
		t.Fatalf("share: %+v", shared)
	}

	out := filepath.Join(dir, "copy.txt")
	var fetched FetchResponse
	if st := post(t, nodes[3], "/api/fetch", FetchRequest{Key: shared.Key.String(), Out: out}, &fetched); st != http.StatusOK {
		t.Fatalf("fetch: status %d", st)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("fetched file differs (err %v)", err)
	}
	seederAddr := "127.0.0.1:" + strconv.Itoa(nodes[1].peer.TCPPort()) // pieces_from is keyed by TCP address
	// One provider: at most one request in flight (docs/08-swarming.md).
	if fetched.Pieces != 3 || fetched.ProvidersFound != 1 || fetched.PiecesFrom[seederAddr] != 3 ||
		fetched.FailedProviders == nil || len(fetched.FailedProviders) != 0 || fetched.MaxInFlight != 1 ||
		fetched.AlreadyHad != 0 {
		t.Errorf("fetch: %+v, want 3 pieces from the seeder at %s, 1 in flight, none already had", fetched, seederAddr)
	}

	// Downloads never overwrite: fetching into an existing path fails and
	// leaves the file alone.
	if err := os.WriteFile(out, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	var e map[string]string
	if st := post(t, nodes[2], "/api/fetch", FetchRequest{Key: shared.Key.String(), Out: out}, &e); st != http.StatusInternalServerError {
		t.Errorf("fetch onto an existing file: status %d, want 500", st)
	}
	if b, _ := os.ReadFile(out); string(b) != "keep me" {
		t.Error("fetch overwrote an existing file")
	}
	if !strings.Contains(e["error"], "fetch failed") {
		t.Errorf("error body: %v", e)
	}
}

// The magnet link /api/share returns is enough for another node to fetch
// the file, and a link whose dn lies still yields the real file: only the
// content key inside it is used (docs/10-magnet-links.md).
func TestFetchAcceptsMagnetLink(t *testing.T) {
	nodes := startNodes(t, 3)
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	content := bytes.Repeat([]byte("magnet "), 20000) // 140 KB: 3 pieces
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	var shared File
	if st := post(t, nodes[0], "/api/share", ShareRequest{Path: src}, &shared); st != http.StatusOK {
		t.Fatalf("share: status %d", st)
	}
	if want := "magnet:?xt=urn:tp2p:" + shared.Key.String() + "&dn=notes.txt"; shared.Magnet != want {
		t.Fatalf("share: magnet %q, want %q", shared.Magnet, want)
	}

	links := []string{
		shared.Magnet,
		strings.Replace(shared.Magnet, "dn=notes.txt", "dn=lies.txt", 1),
	}
	for i, link := range links {
		out := filepath.Join(dir, "copy"+strconv.Itoa(i))
		var fetched FetchResponse
		if st := post(t, nodes[i+1], "/api/fetch", FetchRequest{Key: link, Out: out}, &fetched); st != http.StatusOK {
			t.Fatalf("fetch %q: status %d", link, st)
		}
		if fetched.Key != shared.Key || fetched.Name != "notes.txt" {
			t.Errorf("fetch %q: key %s name %q, want %s and the manifest's name notes.txt", link, fetched.Key, fetched.Name, shared.Key)
		}
		if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, content) {
			t.Errorf("fetch %q: file differs (err %v)", link, err)
		}
	}
}

func TestControlRejectsBadRequests(t *testing.T) {
	nodes := startNodes(t, 1)
	cases := []struct {
		path, contentType, body string
		want                    int
	}{
		{"/api/lookup", "text/plain", `{"target":"self"}`, http.StatusUnsupportedMediaType}, // what an HTML form could send
		{"/api/lookup", "application/json", `{"target":"self","extra":1}`, http.StatusBadRequest},
		{"/api/lookup", "application/json", `{"target":"xyz"}`, http.StatusBadRequest},
		{"/api/lookup", "application/json", `not json`, http.StatusBadRequest},
		{"/api/ping", "application/json", `{"addr":"localhost"}`, http.StatusBadRequest},
		{"/api/ping", "application/json", `{"addr":"example.com:9000"}`, http.StatusBadRequest},
		{"/api/put", "application/json", `{"name":"x"}`, http.StatusBadRequest},
		{"/api/get", "application/json", `{}`, http.StatusBadRequest},
		{"/api/share", "application/json", `{}`, http.StatusBadRequest},
		{"/api/fetch", "application/json", `{"key":"abc","out":"/tmp/x"}`, http.StatusBadRequest},
		{"/api/fetch", "application/json", `{"key":"` + strings.Repeat("a", 40) + `"}`, http.StatusBadRequest},
		{"/api/fetch", "application/json", `{"key":"magnet:?xt=urn:btih:` + strings.Repeat("a", 40) + `","out":"/tmp/x"}`, http.StatusBadRequest},
		{"/api/put", "application/json", `{"name":"big","text":"` + strings.Repeat("x", 300) + `"}`, http.StatusInternalServerError}, // over maxValueSize: the node refuses
	}
	for _, c := range cases {
		resp, err := http.Post(nodes[0].api.URL+c.path, c.contentType, strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		var e map[string]string
		json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if resp.StatusCode != c.want || e["error"] == "" {
			t.Errorf("POST %s %s: status %d (error %q), want %d with an error message", c.path, c.body, resp.StatusCode, e["error"], c.want)
		}
	}
}

func TestControlIsOffUnlessEnabled(t *testing.T) {
	nodes := startNodes(t, 1)
	url := httptestServer(t, New(Config{Node: nodes[0].node, Peer: nodes[0].peer})) // Control: false
	for _, path := range []string{"/api/lookup", "/api/ping", "/api/put", "/api/get", "/api/share", "/api/fetch"} {
		resp, err := http.Post(url+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("control off: POST %s gave %d, want 404", path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodOptions, url+"/api/put", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		t.Error("control off: a CORS preflight should not be approved")
	}
}

func TestControlAnswersCORSPreflight(t *testing.T) {
	nodes := startNodes(t, 1)
	req, _ := http.NewRequest(http.MethodOptions, nodes[0].api.URL+"/api/put", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := resp.Header
	if resp.StatusCode != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") || h.Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Errorf("preflight: %d %v", resp.StatusCode, h)
	}
}

// Several viewers clicking at once: concurrent control requests on one
// node, run under -race.
func TestConcurrentControlRequests(t *testing.T) {
	nodes := startNodes(t, 4)
	post(t, nodes[0], "/api/put", PutRequest{Name: "shared", Text: "v"}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			var res LookupResponse
			if st := post(t, nodes[3], "/api/lookup", LookupRequest{Target: kademlia.HashID([]byte{byte(i)}).String()}, &res); st != http.StatusOK || len(res.Closest) != 3 {
				t.Errorf("lookup %d: status %d, %d contacts", i, st, len(res.Closest))
			}
		}(i)
		go func() {
			defer wg.Done()
			var got GetResponse
			if st := post(t, nodes[3], "/api/get", GetRequest{Name: "shared"}, &got); st != http.StatusOK || len(got.Values) != 1 {
				t.Errorf("get: status %d, %d values", st, len(got.Values))
			}
		}()
	}
	wg.Wait()
}

// A control action shows up on the node's own event stream, like a
// console command does in its log.
func TestControlActionsAppearAsEvents(t *testing.T) {
	nodes := startNodes(t, 3)
	next, cancel := openStream(t, nodes[2], "")
	defer cancel()
	target := kademlia.HashID([]byte("watched"))
	post(t, nodes[2], "/api/lookup", LookupRequest{Target: target.String()}, nil)
	next(func(e wireEvent) bool { return e.Msg == "lookup converged" && e.Attrs["target"] == target.String() })
}
