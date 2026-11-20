package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// runner owns everything one demo run needs: the node processes, the
// event recorder, the status stream to the page, and the scene list.
type runner struct {
	cfg    config
	out    string // node logs, events.jsonl, files
	bin    string // the node binary
	commit string
	rnd    *rand.Rand

	rec    *recorder
	status *hub
	scenes []scene

	mu       sync.Mutex
	nodes    []*procNode // every node started, in start order (dead ones too)
	next     int         // index of the scene /viz/next runs
	running  bool        // a scene is running
	pageAcks chan string // names the page reports as subscribed
	results  []string    // check lines printed at the end
	failed   bool        // a check failed

	// state handed from scene to scene
	file1   sharedFile
	fetches map[string]chan fetchResult // node -> its running fetch
}

// sharedFile is a file a scene shared: where it is and its magnet link.
type sharedFile struct {
	path, key, magnet, name string
	pieces                  int
	seeder                  string
}

func newRunner(cfg config) (*runner, error) {
	if cfg.watchFile == "" && cfg.n < 18 {
		return nil, fmt.Errorf("--n %d: the scenes' cast needs at least 18 nodes", cfg.n)
	}
	out := cfg.outDir
	if out == "" {
		var err error
		if out, err = os.MkdirTemp("", "vizrun-"); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(out, "files"), 0o755); err != nil {
		return nil, err
	}
	rec, err := newRecorder(filepath.Join(out, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	r := &runner{
		cfg:      cfg,
		out:      out,
		commit:   gitCommit(),
		rnd:      rand.New(rand.NewSource(cfg.joinSeed)),
		rec:      rec,
		status:   newHub(),
		pageAcks: make(chan string, 64),
		fetches:  map[string]chan fetchResult{},
	}
	r.scenes = allScenes()
	return r, nil
}

// run starts the network (unless --watch), serves the page, and then
// either waits for the page to ask for scenes, or (--auto) runs them all.
func (r *runner) run(ctx context.Context) error {
	fmt.Printf("vizrun: output in %s\n", r.out)
	if r.cfg.watchFile == "" {
		if err := r.buildNode(); err != nil {
			return err
		}
		// viz0 is the first node: alone, no bootstrap. Scene 1 adds the rest.
		if _, err := r.startNode(ctx, 0, nil); err != nil {
			return err
		}
	}

	srv := &http.Server{Addr: r.cfg.httpAddr, Handler: r.routes()}
	ln, err := net.Listen("tcp", r.cfg.httpAddr)
	if err != nil {
		return fmt.Errorf("serve the page: %w", err)
	}
	go srv.Serve(ln)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	if r.cfg.auto {
		return r.runAll(ctx)
	}
	fmt.Printf("vizrun: open http://%s/  (→ or Space runs the next scene; Ctrl-C stops everything)\n", r.cfg.httpAddr)
	<-ctx.Done()
	return nil
}

// runAll is --auto: every scene in order, then the checks.
func (r *runner) runAll(ctx context.Context) error {
	for range r.scenes {
		if err := r.runNext(ctx); err != nil {
			return err
		}
	}
	r.printChecks()
	if r.failed {
		return errors.New("FAIL: a check failed (see above)")
	}
	fmt.Println("vizrun: PASS")
	return nil
}

// runNext runs the next scene and reports it on the status stream.
func (r *runner) runNext(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errBusy
	}
	if r.next >= len(r.scenes) {
		r.mu.Unlock()
		return errNoMoreScenes
	}
	sc := r.scenes[r.next]
	index := r.next
	r.next++
	r.running = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	fmt.Printf("vizrun: scene %d: %s\n", index+1, sc.title)
	r.emit("scene started", map[string]any{"scene": sc.id, "index": index, "title": sc.title})
	err := sc.run(ctx, r)
	if err != nil {
		r.check(false, "scene %s: %v", sc.id, err)
	}
	r.emit("scene done", map[string]any{"scene": sc.id, "index": index, "error": errString(err)})
	return nil
}

var (
	errBusy         = errors.New("a scene is still running")
	errNoMoreScenes = errors.New("no more scenes")
)

// emit sends one runner fact to the page's status stream (and prints it
// in --auto mode, where there is no page).
func (r *runner) emit(kind string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = kind
	fields["time"] = time.Now().Format(time.RFC3339Nano)
	data, _ := json.Marshal(fields)
	r.status.publish(data)
}

// check records one verification result, printed at the end of --auto.
func (r *runner) check(ok bool, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		r.results = append(r.results, "ok    "+line)
	} else {
		r.results = append(r.results, "FAIL  "+line)
		r.failed = true
	}
	fmt.Printf("vizrun: check: %s %s\n", map[bool]string{true: "ok", false: "FAIL"}[ok], line)
}

func (r *runner) printChecks() {
	total, dropped, _ := r.rec.totals()
	r.check(dropped == 0, "events recorded=%d dropped=%d (dropped must be 0)", total, dropped)
	fmt.Println("vizrun: checks:")
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.results {
		fmt.Println("  " + l)
	}
}

// cleanup SIGTERMs every node still running and waits for it to exit
// (SIGKILL after 3 s), so no port stays bound after vizrun ends.
func (r *runner) cleanup() {
	r.mu.Lock()
	nodes := append([]*procNode(nil), r.nodes...)
	r.mu.Unlock()
	for _, n := range nodes {
		n.stop()
	}
	r.rec.close()
	fmt.Printf("vizrun: stopped %d node(s); logs and events.jsonl kept in %s\n", len(nodes), r.out)
}

// gitCommit is the short commit the run was made from, "+dirty" if the
// tree has changes; shown in the outro. Empty if git isn't available.
func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	c := strings.TrimSpace(string(out))
	if st, err := exec.Command("git", "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(st))) > 0 {
		c += "+dirty"
	}
	return c
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
