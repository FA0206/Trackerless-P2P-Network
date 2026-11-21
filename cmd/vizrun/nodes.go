package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// procNode is one node process this runner started.
type procNode struct {
	Name string `json:"name"`
	API  string `json:"api"`  // host:port of its HTTP API
	Port int    `json:"port"` // its UDP (and TCP) port
	ID   string `json:"id"`   // read back from /api/node once it's up

	seed   string
	log    string
	cmd    *exec.Cmd
	exited chan struct{}

	mu   sync.Mutex
	dead bool
}

func (n *procNode) addr() string { return "127.0.0.1:" + strconv.Itoa(n.Port) }

func (n *procNode) isDead() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dead
}

// buildNode compiles ./cmd/node once into the output directory, so every
// node runs the same binary (unless --node-bin names one).
func (r *runner) buildNode() error {
	if r.cfg.nodeBin != "" {
		r.bin = r.cfg.nodeBin
		return nil
	}
	r.bin = filepath.Join(r.out, "node")
	fmt.Println("vizrun: building ./cmd/node ...")
	cmd := exec.Command("go", "build", "-o", r.bin, "./cmd/node")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build node: %w", err)
	}
	return nil
}

// startNode starts node i (seed <prefix><i>), joining through boot unless
// boot is nil, and returns once it has joined, the recorder is subscribed
// to it, and (if a page is watching) the page is subscribed too. Then it
// announces "node up". extra flags go last, so they override the defaults.
func (r *runner) startNode(ctx context.Context, i int, boot *procNode, extra ...string) (*procNode, error) {
	n := &procNode{
		Name:   fmt.Sprintf("%s%d", r.cfg.seedPrefix, i),
		seed:   fmt.Sprintf("%s%d", r.cfg.seedPrefix, i),
		Port:   r.cfg.basePort + i,
		API:    fmt.Sprintf("127.0.0.1:%d", r.cfg.apiBase+i),
		exited: make(chan struct{}),
	}
	n.log = filepath.Join(r.out, n.Name+".log")
	args := []string{
		"--port", strconv.Itoa(n.Port),
		"--id-seed", n.seed,
		"--k", strconv.Itoa(r.cfg.k),
		"--api", n.API, "--api-control",
		"--console=false",
		"--max-inflight", strconv.Itoa(r.cfg.maxInflight),
		"--debug-serve-delay", r.cfg.serveDelay.String(),
	}
	if boot != nil {
		args = append(args, "--bootstrap", boot.addr())
	}
	args = append(args, extra...)

	logFile, err := os.Create(n.log)
	if err != nil {
		return nil, err
	}
	n.cmd = exec.Command(r.bin, args...)
	n.cmd.Stdout, n.cmd.Stderr = logFile, logFile
	if err := n.cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start %s: %w", n.Name, err)
	}
	go func() {
		n.cmd.Wait()
		logFile.Close()
		n.mu.Lock()
		n.dead = true
		n.mu.Unlock()
		close(n.exited)
	}()
	r.mu.Lock()
	r.nodes = append(r.nodes, n)
	r.mu.Unlock()

	if err := r.waitUp(ctx, n, boot != nil); err != nil {
		return nil, err
	}
	// Subscribe before anyone acts on this node: streams replay nothing.
	if err := r.rec.subscribe(ctx, n.Name, n.API); err != nil {
		return nil, fmt.Errorf("%s: subscribe: %w", n.Name, err)
	}
	r.emit("node up", map[string]any{"name": n.Name, "api": n.API, "id": n.ID, "port": n.Port})
	r.waitForPage(n.Name)
	return n, nil
}

// waitUp waits for the node's API to answer and, if it bootstraps, for
// its "bootstrap complete" log line. The API starts before bootstrapping
// (cmd/node), so an answering API alone doesn't mean it has joined.
func (r *runner) waitUp(ctx context.Context, n *procNode, joins bool) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		var info struct {
			ID string `json:"id"`
		}
		if _, err := getJSON(ctx, n.API, "/api/node", &info); err == nil && info.ID != "" {
			n.ID = info.ID
			if !joins || logHas(n.log, "bootstrap complete") {
				return nil
			}
		}
		select {
		case <-n.exited:
			return fmt.Errorf("%s exited while starting; its log:\n%s", n.Name, tail(n.log))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not up after 15s; its log:\n%s", n.Name, tail(n.log))
		}
	}
}

// waitForPage gives a watching page up to 3 s to subscribe to a new node
// (it POSTs /viz/subscribed), so the page doesn't miss what the node does
// next. With no page watching (--auto), it returns at once.
func (r *runner) waitForPage(name string) {
	if r.status.viewers() == 0 {
		return
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case got := <-r.pageAcks:
			if got == name {
				return
			}
		case <-timeout:
			fmt.Printf("vizrun: the page didn't confirm subscribing to %s; going on\n", name)
			return
		}
	}
}

// kill sends sig to the node and waits for it to exit.
func (n *procNode) kill(sig syscall.Signal) {
	if n.isDead() {
		return
	}
	n.cmd.Process.Signal(sig)
	select {
	case <-n.exited:
	case <-time.After(3 * time.Second):
		n.cmd.Process.Kill()
		<-n.exited
	}
}

// stop is the polite end: SIGTERM, then SIGKILL after 3 s.
func (n *procNode) stop() { n.kill(syscall.SIGTERM) }

// node returns the started node with this name, or nil.
func (r *runner) node(name string) *procNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// liveNodes returns the nodes still running, in start order.
func (r *runner) liveNodes() []*procNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	var live []*procNode
	for _, n := range r.nodes {
		if !n.isDead() {
			live = append(live, n)
		}
	}
	return live
}

func logHas(path, text string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte(text))
}

func tail(path string) string {
	data, _ := os.ReadFile(path)
	if len(data) > 1500 {
		data = data[len(data)-1500:]
	}
	return string(data)
}
