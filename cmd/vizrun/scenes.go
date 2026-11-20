package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// scene is the runner's half of one scene: what to make the nodes do.
// The page's half (what to draw) is the object with the same id in
// viz/scenes.js. To add a scene, add one of each (docs/11-visualizer.md,
// "How to add a scene").
type scene struct {
	id    string
	title string // must match the slide title, and viz/scenes.js
	run   func(ctx context.Context, r *runner) error
}

func allScenes() []scene {
	return []scene{
		{"join", "No tracker, just peers", sceneJoin},
		{"buckets", "Who you know: k-buckets", sceneBuckets},
		{"lookup", "Find anyone in a few hops", sceneLookup},
		{"store", "Storage with no server", sceneStore},
		{"share", "Share a file, get a magnet link", sceneShare},
		{"swarm", "Swarming", sceneSwarm},
		{"kill", "Kill the seeder. The file survives.", sceneKill},
		{"outro", "Live, not faked", sceneOutro},
	}
}

// Who plays which part, by node index (seed viz<i>). Fixed, so every run
// has the same cast. Needs --n of at least 18.
const (
	castLookup  = 7
	castPutter  = 4
	castSeeder1 = 11
)

var castLeechers1 = []int{3, 9, 15}

const (
	file1Pieces = 32 // scene 5-7: big enough that the kill moment lasts a while
	pieceSize   = 64 * 1024
)

// window brackets an action for the page's replay (docs/11-visualizer.md,
// "Replay"): the page buffers every event between "window open" and
// "window closed", then replays them in their real order. The pauses let
// the open reach the page before the action starts, and the action's
// last events arrive before the close.
func (r *runner) window(id, pace string, fields map[string]any, fn func() (map[string]any, error)) error {
	r.emit("window open", map[string]any{"window": id, "pace": pace})
	time.Sleep(150 * time.Millisecond)
	extra, err := fn()
	time.Sleep(300 * time.Millisecond)
	closed := map[string]any{"window": id, "pace": pace}
	for k, v := range fields {
		closed[k] = v
	}
	for k, v := range extra {
		closed[k] = v
	}
	r.emit("window closed", closed)
	return err
}

func (r *runner) nodeAt(i int) *procNode { return r.node(fmt.Sprintf("%s%d", r.cfg.seedPrefix, i)) }

// --- 1. No tracker, just peers ---------------------------------------------

// sceneJoin starts viz1..viz<n-1> one at a time, each bootstrapping
// through a random node already running. The first three joins are shown
// slowly, the rest quickly. Each join is seen from the nodes it contacts:
// their streams are open, the newcomer's isn't yet (it joins in ms).
func sceneJoin(ctx context.Context, r *runner) error {
	for i := 1; i < r.cfg.n; i++ {
		live := r.liveNodes()
		boot := live[r.rnd.Intn(len(live))]
		pace := "fast"
		if i <= 3 {
			pace = "slow"
		}
		err := r.window(fmt.Sprintf("join-%d", i), pace, map[string]any{"boot": boot.Name}, func() (map[string]any, error) {
			n, err := r.startNode(ctx, i, boot)
			if err != nil {
				return nil, err
			}
			return map[string]any{"newcomer": n.Name, "id": n.ID, "api": n.API}, nil
		})
		if err != nil {
			return err
		}
	}
	// Every node must know someone: an empty table means it never joined.
	empty := 0
	for _, n := range r.liveNodes() {
		var info struct {
			Size int `json:"routing_table_size"`
		}
		if _, err := getJSON(ctx, n.API, "/api/node", &info); err != nil || info.Size == 0 {
			empty++
		}
	}
	r.check(empty == 0, "join: %d nodes running, %d with an empty routing table", len(r.liveNodes()), empty)
	return nil
}

// --- 2. Who you know: k-buckets --------------------------------------------

// sceneBuckets makes no node act: the page replays viz0's routing table
// as it grew during scene 1 (its own /api/table snapshots).
func sceneBuckets(ctx context.Context, r *runner) error {
	var table struct {
		Size    int `json:"size"`
		Buckets []struct {
			Index    int   `json:"index"`
			Contacts []any `json:"contacts"`
		} `json:"buckets"`
	}
	if _, err := getJSON(ctx, r.nodeAt(0).API, "/api/table", &table); err != nil {
		return err
	}
	over := 0
	for _, b := range table.Buckets {
		if len(b.Contacts) > r.cfg.k {
			over++
		}
	}
	r.check(over == 0, "buckets: viz0 knows %d nodes in %d buckets, none over k=%d", table.Size, len(table.Buckets), r.cfg.k)
	return nil
}

// --- 3. Find anyone in a few hops ------------------------------------------

// sceneLookup has viz7 look up the node farthest from it (in XOR) that
// isn't in its routing table: a node it has never heard of.
func sceneLookup(ctx context.Context, r *runner) error {
	from := r.nodeAt(castLookup)
	target := r.farthestUnknown(ctx, from)
	events, stopTap := r.rec.tap()
	defer stopTap()

	var res struct {
		Rounds   int `json:"rounds"`
		RPCsSent int `json:"rpcs_sent"`
		Closest  []struct {
			ID string `json:"id"`
		} `json:"closest"`
	}
	err := r.window("lookup", "slow", map[string]any{"from": from.Name, "target": target.Name, "target_id": target.ID}, func() (map[string]any, error) {
		err := postJSON(ctx, from.API, "/api/lookup", map[string]string{"target": target.ID}, &res)
		return map[string]any{"rounds": res.Rounds, "rpcs_sent": res.RPCsSent}, err
	})
	if err != nil {
		return err
	}
	roundEvents, converged := 0, -1
	drain(events, func(ev nodeEvent) {
		if ev.Node != from.Name {
			return
		}
		switch ev.Msg {
		case "lookup round starting":
			roundEvents++
		case "lookup converged":
			converged = attrInt(ev.Attrs, "rounds")
		}
	})
	found := len(res.Closest) > 0 && res.Closest[0].ID == target.ID
	r.check(roundEvents == res.Rounds && converged == res.Rounds,
		"lookup: %s → %s: %d 'lookup round starting' events, 'lookup converged' rounds=%d, response rounds=%d, rpcs_sent=%d",
		from.Name, target.Name, roundEvents, converged, res.Rounds, res.RPCsSent)
	r.check(found, "lookup: the closest node found is %s itself", target.Name)
	return nil
}

// farthestUnknown picks, among the running nodes, the one farthest from
// `from` in XOR distance that `from` doesn't have in its routing table.
func (r *runner) farthestUnknown(ctx context.Context, from *procNode) *procNode {
	var table struct {
		Buckets []struct {
			Contacts []struct {
				ID string `json:"id"`
			} `json:"contacts"`
		} `json:"buckets"`
	}
	getJSON(ctx, from.API, "/api/table", &table)
	known := map[string]bool{}
	for _, b := range table.Buckets {
		for _, c := range b.Contacts {
			known[c.ID] = true
		}
	}
	var best, bestAny *procNode
	for _, n := range r.liveNodes() {
		if n == from {
			continue
		}
		if bestAny == nil || xorLess(from.ID, bestAny.ID, n.ID) {
			bestAny = n
		}
		if !known[n.ID] && (best == nil || xorLess(from.ID, best.ID, n.ID)) {
			best = n
		}
	}
	if best == nil {
		return bestAny // it knows everyone: still pick the farthest
	}
	return best
}

// xorLess reports whether a is closer to base than b (XOR distance).
func xorLess(base, a, b string) bool {
	x, _ := hex.DecodeString(base)
	ya, _ := hex.DecodeString(a)
	yb, _ := hex.DecodeString(b)
	for i := range x {
		da, db := x[i]^ya[i], x[i]^yb[i]
		if da != db {
			return da < db
		}
	}
	return false
}

// drain reads whatever a tap has buffered right now.
func drain(ch <-chan nodeEvent, fn func(nodeEvent)) {
	for {
		select {
		case ev := <-ch:
			fn(ev)
		default:
			return
		}
	}
}

// --- 4. Storage with no server ---------------------------------------------

// sceneStore puts a value from viz4, then gets it from the node farthest
// from its key among those that don't hold it (checked in /api/store),
// so the get really travels.
func sceneStore(ctx context.Context, r *runner) error {
	putter := r.nodeAt(castPutter)
	const name, text = "greeting", "hello from viz4: no server stores this"
	var put struct {
		Key      string `json:"key"`
		StoredAt []any  `json:"stored_at"`
		Locally  bool   `json:"stored_locally"`
	}
	err := r.window("put", "slow", map[string]any{"from": putter.Name, "name": name}, func() (map[string]any, error) {
		err := postJSON(ctx, putter.API, "/api/put", map[string]string{"name": name, "text": text}, &put)
		return map[string]any{"key": put.Key, "stored_at": len(put.StoredAt), "stored_locally": put.Locally}, err
	})
	if err != nil {
		return err
	}

	var getter *procNode
	for _, n := range r.liveNodes() {
		if n == putter || holds(ctx, n, put.Key) {
			continue
		}
		if getter == nil || !xorLess(put.Key, n.ID, getter.ID) {
			getter = n // farthest from the key so far
		}
	}
	if getter == nil {
		return errors.New("every node holds the value; nobody left to get it")
	}
	var get struct {
		Values []struct {
			Text string `json:"text"`
		} `json:"values"`
		Local  bool `json:"from_local_store"`
		Rounds int  `json:"rounds"`
	}
	err = r.window("get", "slow", map[string]any{"from": getter.Name, "name": name}, func() (map[string]any, error) {
		err := postJSON(ctx, getter.API, "/api/get", map[string]string{"name": name}, &get)
		fields := map[string]any{"key": put.Key, "rounds": get.Rounds, "found": len(get.Values) > 0}
		if len(get.Values) > 0 {
			fields["value"] = get.Values[0].Text // values aren't in any event: from the /api/get answer
		}
		return fields, err
	})
	if err != nil {
		return err
	}
	r.check(len(get.Values) > 0 && get.Values[0].Text == text && !get.Local,
		"store: %s put %q on %d node(s) (+self: %v); %s got it back over the network in %d round(s)",
		putter.Name, name, len(put.StoredAt), put.Locally, getter.Name, get.Rounds)
	return nil
}

// holds reports whether the node's /api/store has a value under key.
func holds(ctx context.Context, n *procNode, key string) bool {
	var store struct {
		Keys []struct {
			Key string `json:"key"`
		} `json:"keys"`
	}
	getJSON(ctx, n.API, "/api/store", &store)
	for _, k := range store.Keys {
		if k.Key == key {
			return true
		}
	}
	return false
}

// --- 5. Share a file, get a magnet link ------------------------------------

func sceneShare(ctx context.Context, r *runner) error {
	seeder := r.nodeAt(castSeeder1)
	f, err := r.share(ctx, seeder, "lecture-notes.bin", file1Pieces, true)
	if err != nil {
		return err
	}
	r.file1 = f
	r.check(f.pieces == file1Pieces, "share: %s shared %s (%d pieces), key %s", seeder.Name, f.name, f.pieces, f.key[:8])
	return nil
}

// share writes fresh random bytes (new bytes every run, so every run has
// a new content key) and has `seeder` share them. With inWindow, the
// share's DHT traffic is bracketed for replay.
func (r *runner) share(ctx context.Context, seeder *procNode, name string, pieces int, inWindow bool) (sharedFile, error) {
	path := filepath.Join(r.out, "files", name)
	data := make([]byte, pieces*pieceSize)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return sharedFile{}, err
	}
	return r.shareExisting(ctx, seeder, path, inWindow)
}

func (r *runner) shareExisting(ctx context.Context, seeder *procNode, path string, inWindow bool) (sharedFile, error) {
	var res struct {
		Key    string `json:"key"`
		Name   string `json:"name"`
		Pieces int    `json:"pieces"`
		Magnet string `json:"magnet"`
	}
	do := func() (map[string]any, error) {
		err := postJSON(ctx, seeder.API, "/api/share", map[string]string{"path": path}, &res)
		return map[string]any{"key": res.Key}, err
	}
	var err error
	if inWindow {
		err = r.window("share", "slow", map[string]any{"from": seeder.Name}, do)
	} else {
		_, err = do()
	}
	if err != nil {
		return sharedFile{}, err
	}
	f := sharedFile{path: path, key: res.Key, magnet: res.Magnet, name: res.Name, pieces: res.Pieces, seeder: seeder.Name}
	r.emitMagnet(f)
	return f, nil
}

// emitMagnet sends the link to the page. It is in no event: it comes
// from the /api/share answer.
func (r *runner) emitMagnet(f sharedFile) {
	r.emit("magnet", map[string]any{"node": f.seeder, "magnet": f.magnet, "key": f.key, "name": f.name, "pieces": f.pieces})
}

// --- 6. Swarming ------------------------------------------------------------

// fetchResult is one /api/fetch answer (or its error).
type fetchResult struct {
	node string
	out  string
	err  error
	res  map[string]any
}

// startFetch runs POST /api/fetch in the background (it blocks until the
// download ends) and reports the result on the returned channel.
func (r *runner) startFetch(ctx context.Context, n *procNode, f sharedFile, out string) chan fetchResult {
	r.rec.resetPieces(n.Name)
	ch := make(chan fetchResult, 1)
	r.mu.Lock()
	r.fetches[n.Name] = ch
	r.mu.Unlock()
	r.emit("fetch started", map[string]any{"node": n.Name, "key": f.key, "pieces": f.pieces})
	go func() {
		fctx, cancel := context.WithTimeout(ctx, 110*time.Second) // the node cuts fetch off at 100 s
		defer cancel()
		var res map[string]any
		err := postJSON(fctx, n.API, "/api/fetch", map[string]string{"key": f.magnet, "out": out}, &res)
		ch <- fetchResult{node: n.Name, out: out, err: err, res: res}
	}()
	return ch
}

// waitPieces waits until node has received at least `want` pieces.
func (r *runner) waitPieces(ctx context.Context, n *procNode, want int, fetch chan fetchResult) error {
	deadline := time.After(60 * time.Second)
	for {
		if have, _, _ := r.rec.pieceCount(n.Name); have >= want {
			return nil
		}
		select {
		case res := <-fetch:
			fetch <- res // put it back for whoever waits for the result
			return fmt.Errorf("%s's fetch ended before it had %d pieces: %v", n.Name, want, res.err)
		case <-deadline:
			return fmt.Errorf("%s never reached %d pieces", n.Name, want)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// sceneSwarm starts three leechers, staggered so they are at different
// points (as scripts/demo-seeder-death.sh does): the second starts when
// the first has 4 pieces, the third when the second has 2. The scene
// ends once all three are downloading; the download goes on.
func sceneSwarm(ctx context.Context, r *runner) error {
	if r.file1.magnet == "" {
		return errors.New("no file shared (scene 5 didn't run)")
	}
	waits := []int{4, 2, 1}
	for j, i := range castLeechers1 {
		n := r.nodeAt(i)
		ch := r.startFetch(ctx, n, r.file1, filepath.Join(r.out, "files", n.Name+"-"+r.file1.name))
		if err := r.waitPieces(ctx, n, waits[j], ch); err != nil {
			return err
		}
	}
	return nil
}

// --- 7. Kill the seeder. The file survives. --------------------------------

// sceneKill SIGKILLs the seeder at the first moment the leechers together
// hold every piece while none of them holds them all (the rule of
// scripts/demo-seeder-death.sh). If a leecher already finished, the take
// is VOID: the seeder is still killed (the story goes on), but the page
// and the checks say so. Then it waits for every leecher and compares
// SHA-256 sums.
func sceneKill(ctx context.Context, r *runner) error {
	seeder := r.node(r.file1.seeder)
	names := names(castLeechers1, r)
	reason, void := "", false
	deadline := time.Now().Add(90 * time.Second)
	for reason == "" {
		union, of, anyDone, _ := r.rec.spread(names)
		switch {
		case anyDone:
			reason, void = "VOID: a leecher already had the whole file", true
		case of > 0 && union >= of:
			reason = fmt.Sprintf("the leechers together hold all %d pieces, and none has finished", of)
		case time.Now().After(deadline):
			return errors.New("the kill moment never came")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	union, of, _, each := r.rec.spread(names)
	seeder.kill(syscall.SIGKILL)
	r.emit("killed", map[string]any{"node": seeder.Name, "reason": reason, "void": void, "union": union, "of": of, "each": each})
	r.check(!void, "kill: SIGKILL %s: %s %v", seeder.Name, reason, each)

	return r.finishAndVerify(ctx, names, r.file1)
}

func names(idx []int, r *runner) []string {
	var out []string
	for _, i := range idx {
		out = append(out, r.nodeAt(i).Name)
	}
	return out
}

// finishAndVerify waits for each node's fetch to end, then compares the
// SHA-256 of the original with each downloaded copy. No endpoint gives a
// whole-file hash, so the runner reads both files from disk.
// SIMPLIFICATION: that only works because every node runs on this
// machine; the cross-laptop increment needs another way (each laptop
// hashing its own copy).
func (r *runner) finishAndVerify(ctx context.Context, nodes []string, f sharedFile) error {
	want, err := fileSHA256(f.path)
	if err != nil {
		return err
	}
	for _, name := range nodes {
		r.mu.Lock()
		ch := r.fetches[name]
		r.mu.Unlock()
		var res fetchResult
		select {
		case res = <-ch:
		case <-time.After(110 * time.Second):
			return fmt.Errorf("%s's fetch never ended", name)
		case <-ctx.Done():
			return ctx.Err()
		}
		r.emit("fetch done", map[string]any{"node": name, "ok": res.err == nil, "error": errString(res.err), "result": res.res})
		got, _ := fileSHA256(res.out)
		match := res.err == nil && got == want
		r.emit("sha256", map[string]any{"node": name, "file": filepath.Base(res.out), "want": want, "got": got, "match": match})
		if res.err != nil {
			r.check(false, "fetch: %s: %v", name, res.err)
		}
		r.check(match, "sha256: %s's copy %s the original (%s…)", name, map[bool]string{true: "matches", false: "DOES NOT match"}[match], want[:16])
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- 8. Live, not faked ------------------------------------------------------

func sceneOutro(ctx context.Context, r *runner) error {
	total, dropped, _ := r.rec.totals()
	counts := r.rec.countsCopy()
	keep := map[string]int{}
	for _, m := range []string{"rpc sent", "rpc received", "lookup converged", "value stored", "provider announced", "piece received", "download complete", "download gave up, still serving its pieces", "download resumed"} {
		keep[m] = counts[m]
	}
	r.emit("summary", map[string]any{
		"events": total, "dropped": dropped, "counts": keep,
		"commit": r.commit, "seeds": fmt.Sprintf("%s0..%s%d", r.cfg.seedPrefix, r.cfg.seedPrefix, r.cfg.n),
		"k": r.cfg.k, "alpha": 3, "max_inflight": r.cfg.maxInflight, "serve_delay": r.cfg.serveDelay.String(),
		"nodes": len(r.nodes), "date": time.Now().Format("2006-01-02 15:04"),
	})
	return nil
}
