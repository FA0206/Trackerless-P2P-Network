package nodeapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kademlia-demo/internal/kademlia"
	"kademlia-demo/internal/transfer"
)

// testNode is a real DHT node + file peer on loopback, with its API served
// by an httptest server.
type testNode struct {
	node   *kademlia.Node
	peer   *transfer.Peer
	api    *httptest.Server
	events *Events
}

// startNodes starts n nodes (k=3), each joined via the first, and an API
// server for each. Each node's logger is wired like cmd/node's with --api:
// a TeeHandler (here around a handler that prints nothing) feeding the
// node's event hub.
func startNodes(t *testing.T, n int) []testNode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodes := make([]testNode, n)
	for i := range nodes {
		id := kademlia.HashID([]byte{'a', 'p', 'i', byte(i)})
		events := NewEvents()
		node, err := kademlia.NewNode(kademlia.Config{ID: id, K: 3, RPCTimeout: 500 * time.Millisecond,
			Logger: slog.New(TeeHandler(slog.DiscardHandler, events))})
		if err != nil {
			t.Fatal(err)
		}
		node.Start()
		t.Cleanup(func() { node.Close() })
		peer, err := transfer.NewPeer(node, transfer.Config{IOTimeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { peer.Close() })
		api := httptest.NewServer(New(Config{Node: node, Peer: peer, K: 3, Alpha: 3,
			RPCTimeout: 500 * time.Millisecond, Events: events, Control: true}).Handler())
		t.Cleanup(api.Close)
		nodes[i] = testNode{node, peer, api, events}
		if i > 0 {
			if _, err := node.Bootstrap(ctx, nodes[0].node.Self().UDPAddr()); err != nil {
				t.Fatalf("bootstrap %d: %v", i, err)
			}
		}
	}
	return nodes
}

// get fetches path from tn's API and decodes the JSON body into out.
func get(t *testing.T, tn testNode, path string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(tn.api.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
	return resp
}

func TestNodeEndpointDescribesTheNode(t *testing.T) {
	nodes := startNodes(t, 2)
	var info NodeInfo
	resp := get(t, nodes[1], "/api/node", &info)
	self := nodes[1].node.Self()
	if info.ID != self.ID || info.IP != self.IP || info.UDPPort != self.Port {
		t.Errorf("identity: got %s %s:%d, want %s %s:%d", info.ID, info.IP, info.UDPPort, self.ID, self.IP, self.Port)
	}
	if info.TCPPort != nodes[1].peer.TCPPort() || info.K != 3 || info.Alpha != 3 || info.RPCTimeoutMS != 500 {
		t.Errorf("settings: got %+v", info)
	}
	if info.RoutingTableSize != 1 {
		t.Errorf("after joining a 2-node network it should know 1 contact, got %d", info.RoutingTableSize)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header: a visualizer page on another origin couldn't read it")
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
}

func TestTableEndpointMatchesTheRoutingTable(t *testing.T) {
	nodes := startNodes(t, 4)
	var table Table
	get(t, nodes[0], "/api/table", &table)
	if table.ID != nodes[0].node.ID || table.K != 3 {
		t.Fatalf("header: got id=%s k=%d", table.ID, table.K)
	}
	if table.Size != nodes[0].node.RoutingTableSize() || table.Size != 3 {
		t.Fatalf("size: API says %d, node says %d, want 3", table.Size, nodes[0].node.RoutingTableSize())
	}
	seen := map[kademlia.ID]bool{}
	prev := kademlia.IDBits
	for _, b := range table.Buckets {
		if b.Index >= prev {
			t.Errorf("buckets should be farthest first, got %d after %d", b.Index, prev)
		}
		prev = b.Index
		for _, c := range b.Contacts {
			if got := kademlia.BucketIndex(table.ID, c.ID); got != b.Index {
				t.Errorf("contact %s listed in bucket %d, belongs in %d", c.ID, b.Index, got)
			}
			if c.Distance != kademlia.Xor(table.ID, c.ID) {
				t.Errorf("contact %s: wrong distance", c.ID)
			}
			seen[c.ID] = true
		}
	}
	for _, other := range nodes[1:] {
		if !seen[other.node.ID] {
			t.Errorf("node %s missing from the table", other.node.ID)
		}
	}
}

func TestStoreEndpointShowsHeldValues(t *testing.T) {
	nodes := startNodes(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := kademlia.HashID([]byte("greeting"))
	res, err := nodes[0].node.Store(ctx, key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	holders := map[kademlia.ID]bool{}
	for _, c := range res.StoredAt {
		holders[c.ID] = true
	}
	if res.StoredLocally {
		holders[nodes[0].node.ID] = true
	}
	for _, tn := range nodes {
		var store Store
		get(t, tn, "/api/store", &store)
		if !holders[tn.node.ID] {
			if len(store.Keys) != 0 {
				t.Errorf("node %s holds nothing, but the API lists %d keys", tn.node.ID, len(store.Keys))
			}
			continue
		}
		if len(store.Keys) != 1 || store.Keys[0].Key != key || len(store.Keys[0].Values) != 1 {
			t.Fatalf("holder %s: got %+v", tn.node.ID, store)
		}
		v := store.Keys[0].Values[0]
		if v.Text == nil || *v.Text != "hello" || v.Base64 != "aGVsbG8=" {
			t.Errorf("holder %s: value %+v, want text \"hello\" and its base64", tn.node.ID, v)
		}
	}
}

func TestStoreEndpointGivesBinaryValuesAsBase64Only(t *testing.T) {
	nodes := startNodes(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A lone node stores locally: it is the closest node it knows.
	if _, err := nodes[0].node.Store(ctx, kademlia.HashID([]byte("bin")), []byte{0xff, 0xfe}); err != nil {
		t.Fatal(err)
	}
	var store Store
	get(t, nodes[0], "/api/store", &store)
	if len(store.Keys) != 1 {
		t.Fatalf("got %+v", store)
	}
	v := store.Keys[0].Values[0]
	if v.Text != nil || v.Base64 != "//4=" {
		t.Errorf("invalid UTF-8 should have no text, got %+v", v)
	}
}

func TestFilesEndpointListsSharedFiles(t *testing.T) {
	nodes := startNodes(t, 2)
	var empty Files
	get(t, nodes[0], "/api/files", &empty)
	if empty.Files == nil || len(empty.Files) != 0 {
		t.Fatalf("no files yet: want an empty list, got %#v", empty.Files)
	}

	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, make([]byte, transfer.DefaultPieceSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key, err := nodes[0].peer.Share(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var files Files
	get(t, nodes[0], "/api/files", &files)
	want := File{Key: key, Name: "notes.txt", Size: int64(transfer.DefaultPieceSize + 1), Pieces: 2, Have: 2, Path: path}
	if len(files.Files) != 1 || files.Files[0] != want {
		t.Errorf("got %+v, want [%+v]", files.Files, want)
	}
}

// With partial seeding, a download in progress is already in the library
// (and served). The API must show it as partial, not as a complete file.
func TestFilesEndpointShowsDownloadProgress(t *testing.T) {
	nodes := startNodes(t, 2)
	seeder, leecher := nodes[0], nodes[1]
	dir := t.TempDir()
	src := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(src, make([]byte, 3*transfer.DefaultPieceSize), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key, err := seeder.peer.Share(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	// One provider means one request at a time, so with this delay the
	// three pieces land about 300 ms apart.
	seeder.peer.SetDebugServeDelay(300 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := leecher.peer.Download(ctx, key, filepath.Join(dir, "copy.bin"))
		done <- err
	}()

	sawPartial := false
	for !sawPartial && ctx.Err() == nil {
		var files Files
		get(t, leecher, "/api/files", &files)
		if len(files.Files) == 1 && files.Files[0].Pieces == 3 &&
			files.Files[0].Have > 0 && files.Files[0].Have < 3 {
			sawPartial = true
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !sawPartial {
		t.Error("never saw the download with 0 < have < pieces")
	}
	var files Files
	get(t, leecher, "/api/files", &files)
	if len(files.Files) != 1 || files.Files[0].Have != 3 {
		t.Errorf("after the download: %+v, want have = 3", files.Files)
	}
}

func TestOnlyGETIsAllowed(t *testing.T) {
	nodes := startNodes(t, 1)
	for _, path := range []string{"/api/node", "/api/table", "/api/store", "/api/files"} {
		resp, err := http.Post(nodes[0].api.URL+path, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: status %d, want 405", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(nodes[0].api.URL + "/api/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: status %d, want 404", resp.StatusCode)
	}
}
