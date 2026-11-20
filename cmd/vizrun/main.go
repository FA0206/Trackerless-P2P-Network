// Command vizrun runs the visualizer demo (docs/11-visualizer.md): it
// builds and starts a network of real nodes, serves the page in viz/,
// and runs one scene each time the page asks (the presenter presses →).
//
// It talks to the nodes ONLY over their HTTP API (--api, --api-control)
// and by signals (SIGKILL for "the seeder dies", SIGTERM to clean up).
// It imports nothing from internal/: to it, a node is a process with an
// HTTP address, exactly as it is to the page or to curl.
//
//	go run ./cmd/vizrun                     # start 20 nodes, serve http://127.0.0.1:8790/
//	go run ./cmd/vizrun --auto              # run every scene once, no page; print checks
//	go run ./cmd/vizrun --watch nodes.json  # start nothing; just serve the page for these nodes
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// config holds the flags. Plain flags, no config files (CLAUDE.md).
type config struct {
	n           int
	k           int
	basePort    int
	apiBase     int
	httpAddr    string
	serveDelay  time.Duration
	maxInflight int
	seedPrefix  string
	joinSeed    int64
	webDir      string
	outDir      string
	watchFile   string
	auto        bool
	nodeBin     string
}

func parseFlags() config {
	var c config
	flag.IntVar(&c.n, "n", 20, "number of nodes in the network (viz0..viz<n-1>)")
	flag.IntVar(&c.k, "k", 4, "bucket size / replication k for every node (small, so 'the k closest' is a real subset)")
	flag.IntVar(&c.basePort, "base-port", 9700, "UDP and TCP port of viz0; node i uses base-port+i")
	flag.IntVar(&c.apiBase, "api-base", 8700, "HTTP API port of viz0; node i uses api-base+i")
	flag.StringVar(&c.httpAddr, "http", "127.0.0.1:8790", "where to serve the page")
	flag.DurationVar(&c.serveDelay, "serve-delay", time.Second, "--debug-serve-delay for every node: slows piece answers (never DHT traffic) so swarming is visible")
	flag.IntVar(&c.maxInflight, "max-inflight", 1, "--max-inflight for every node")
	flag.StringVar(&c.seedPrefix, "seed-prefix", "viz", "node i runs with --id-seed <prefix><i>")
	flag.Int64Var(&c.joinSeed, "join-seed", 1, "seed for choosing each newcomer's bootstrap node (same seed, same joins)")
	flag.StringVar(&c.webDir, "web", "viz", "directory holding the page (served from disk)")
	flag.StringVar(&c.outDir, "out", "", "where to keep node logs, events.jsonl and files (default: a new temp dir)")
	flag.StringVar(&c.watchFile, "watch", "", `JSON file {"nodes":[{"name":"…","api":"host:port"}]}: start no nodes, run no scenes, only serve the page for these nodes`)
	flag.BoolVar(&c.auto, "auto", false, "run every scene back to back without a page, print the checks, and exit (scripts/demo-viz.sh)")
	flag.StringVar(&c.nodeBin, "node-bin", "", "use this node binary instead of building ./cmd/node")
	flag.Parse()
	return c
}

func main() {
	cfg := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r, err := newRunner(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vizrun:", err)
		os.Exit(1)
	}
	err = r.run(ctx)
	r.cleanup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vizrun:", err)
		os.Exit(1)
	}
}
