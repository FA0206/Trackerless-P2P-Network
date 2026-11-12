// Command node runs a single Kademlia participant as a standalone process,
// for manual testing and for scripts/demo.sh. See the top-level README for
// a full walkthrough.
//
// What this program does, start to finish (the steps are marked in run()):
//
//  1. Read the command-line flags (port, ID seed, who to bootstrap through...).
//  2. Pick this node's 160-bit ID: SHA-1 of -id-seed, or random.
//  3. Open a UDP socket and start a background goroutine that answers
//     other nodes' PING and FIND_NODE requests.
//  4. If -bootstrap was given, join the network through that existing node
//     by looking up our own ID. This fills our routing table and tells the
//     nodes we talk to that we exist.
//  5. If -lookup was given, wait a moment, look up that ID once, and log
//     the closest nodes found.
//  6. Keep running (and keep answering other nodes) until Ctrl+C or
//     SIGTERM, then close the socket and exit.
//
// Example: a two-node network on one machine, in two terminals:
//
//	go run ./cmd/node --port 9000 --id-seed nodeA
//	go run ./cmd/node --port 9001 --id-seed nodeB --bootstrap 127.0.0.1:9000
package main

import (
	"context"
	"flag" // command-line flag parsing (-port, -bootstrap, ...)
	"fmt"
	"log/slog" // structured key=value logging
	"net"
	"os"
	"os/signal" // turns Ctrl+C / SIGTERM into a context cancellation
	"strconv"
	"strings"
	"syscall"
	"time"

	// Our own package: node IDs and XOR distance, the k-bucket routing
	// table, the UDP transport, and the iterative lookup algorithm.
	"kademlia-demo/internal/kademlia"
)

// main is kept tiny on purpose. All the real work happens in run(), which
// returns an error instead of exiting directly. os.Exit skips deferred
// calls, so if run() called it, cleanup like closing the socket would never
// happen. With this setup, run()'s defers always execute, and this is the
// one place that prints the error and sets exit status 1.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// ---- Step 1: command-line flags ----
	//
	// Each flag.X(...) call registers one flag and returns a *pointer* to
	// where its value will live. Those pointers hold the defaults until
	// flag.Parse() reads os.Args and fills in whatever the user passed,
	// which is why the code below reads *port, *bootstrap, and so on.
	// Go's flag package accepts both -port and --port.

	// Each node on the same machine needs its own port.
	port := flag.Int("port", 9000, "UDP port to listen on")
	// A seed makes the ID the same on every run, so logs and demo.sh can
	// predict it. Without one, every run gets a fresh random ID.
	idSeed := flag.String("id-seed", "", "deterministic node ID = SHA-1(id-seed); if empty, a random ID is used")
	// Leave this empty for the very first node: there's nobody to join yet,
	// so that node just waits for others to contact it.
	bootstrap := flag.String("bootstrap", "", "host:port of an existing node to join through (optional)")
	// -lookup and -lookup-after exist for the demo. They make this node
	// search the network once so you can check that it works.
	lookupTarget := flag.String("lookup", "", "after joining, perform one extra lookup for this ID (40-char hex), log the result, and keep running")
	lookupAfter := flag.Duration("lookup-after", 1*time.Second, "how long to wait after bootstrap before running -lookup, to give the rest of the demo network time to join")
	verbose := flag.Bool("verbose", false, "log at debug level (every bucket update, every RPC timeout)")
	// The Kademlia protocol knobs, named as in the paper:
	//   k     (default 20): max contacts per k-bucket, and how many "closest
	//                       nodes" a lookup returns.
	//   alpha (default 3):  how many FIND_NODE requests a lookup sends in
	//                       parallel each round.
	//   rpc-timeout (2s):   how long to wait for any one reply before
	//                       treating that node as unresponsive.
	k := flag.Int("k", kademlia.DefaultK, "bucket size / replication parameter k")
	alpha := flag.Int("alpha", kademlia.DefaultAlpha, "lookup concurrency parameter alpha")
	rpcTimeout := flag.Duration("rpc-timeout", kademlia.DefaultRPCTimeout, "per-RPC timeout")
	flag.Parse()

	// ---- Step 2: choose this node's ID ----
	//
	// Every Kademlia node has a 160-bit ID. The distance between two nodes
	// is the XOR of their IDs, so the ID decides where this node sits in
	// the network and which other nodes end up in its routing table.
	id, err := resolveID(*idSeed)
	if err != nil {
		return err
	}

	// Info level shows the main events: RPCs sent and received, lookups
	// converging. -verbose switches to Debug, which also shows every
	// routing-table update and every RPC timeout. NewLogger writes to
	// stdout and tags each line with node=<first 8 hex chars of our ID>, so
	// you can tell nodes apart when their logs are mixed together.
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := kademlia.NewLogger(id, level)

	// ---- Step 3: create the node and start listening ----
	//
	// NewNode binds the UDP socket on -port (it fails here if the port is
	// already in use) and creates an empty routing table of 160 k-buckets.
	// At this point the node knows nobody.
	n, err := kademlia.NewNode(id, *port, *k, *alpha, *rpcTimeout, logger)
	if err != nil {
		return err
	}
	// Start launches a background goroutine that reads packets from the
	// socket for as long as the node runs, and handles each one
	// (Node.handleMessage):
	//   PING                     -> reply with PONG
	//   FIND_NODE(id)            -> reply with the k contacts we know of
	//                               that are closest to id
	//   PONG / FIND_NODE_RESP    -> hand the reply to whichever of our own
	//                               requests is waiting for it
	// Every packet we receive also adds or refreshes its sender in our
	// routing table, so a node learns about peers just by being contacted.
	//
	// From here on the node is live: other nodes can reach it even while
	// the rest of run() is still bootstrapping.
	n.Start()
	// Close the socket whenever run() returns, for any reason. Closing it
	// also stops the background read loop.
	defer n.Close()

	logger.Info("node started", "id", id.String(), "addr", n.Addr().String())

	// ctx is the "please stop" signal for everything below. It is cancelled
	// when you press Ctrl+C (SIGINT) or the process gets SIGTERM (the `kill`
	// in demo.sh's cleanup sends this). A bootstrap or lookup that is still
	// running at that moment sees the cancellation and gives up. stop()
	// unregisters the handler and restores Go's default signal behaviour.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ---- Step 4 (optional): join an existing network ----
	if *bootstrap != "" {
		// We only know the bootstrap node's address, not its ID, so
		// parseContact puts an all-zero placeholder in the ID. See its doc
		// comment for why that's safe.
		seed, err := parseContact(*bootstrap)
		if err != nil {
			return fmt.Errorf("parsing -bootstrap: %w", err)
		}
		logger.Info("bootstrapping", "via", seed.String())

		// Bootstrapping means looking up our *own* ID, starting from the
		// seed node. Asking "who is closest to me?" does two things:
		//   - the FIND_NODE replies fill *our* routing table with peers,
		//     especially the ones nearest to us;
		//   - every node we query sees us as the sender and adds us to
		//     *its* routing table, so the network learns that we exist.
		//
		// The whole join gets 10 seconds, and each RPC inside it also has
		// its own -rpc-timeout limit. bctx comes from ctx, so Ctrl+C also
		// aborts the join. cancel() runs right after Bootstrap (rather than
		// being deferred) because bctx is no longer needed after that point.
		bctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, err := n.Bootstrap(bctx, seed)
		cancel()
		// Bootstrap only returns an error if the 10s budget runs out or
		// Ctrl+C is pressed. If the seed just doesn't answer, Bootstrap still
		// "succeeds" after one -rpc-timeout but learns nothing, and the log
		// line below shows routing_table_size=0. Check for that if a node
		// seems isolated.
		if err != nil {
			return fmt.Errorf("bootstrap failed: %w", err)
		}
		// routing_table_size: how many peers we know about now.
		// self_lookup_rounds: how many rounds of up to alpha parallel
		//                     FIND_NODE requests the join took.
		// self_lookup_rpcs:   total FIND_NODE requests sent.
		logger.Info("bootstrap complete", "routing_table_size", n.RoutingTableSize(),
			"self_lookup_rounds", result.Rounds, "self_lookup_rpcs", result.RPCsSent)
	}

	// ---- Step 5 (optional): run one lookup for the demo ----
	if *lookupTarget != "" {
		// Parse the target ID now, so a typo shows up as an error at startup
		// instead of a few seconds later.
		target, err := kademlia.IDFromHex(*lookupTarget)
		if err != nil {
			return fmt.Errorf("parsing -lookup: %w", err)
		}
		// Run the lookup in its own goroutine so run() can go straight on
		// to step 6 and wait for Ctrl+C. A failed lookup is logged, but it
		// doesn't stop the node.
		go func() {
			// Wait -lookup-after first (default 1s) so that, in the demo,
			// the other nodes have finished joining before we search. If
			// Ctrl+C arrives during the wait, skip the lookup.
			select {
			case <-time.After(*lookupAfter):
			case <-ctx.Done():
				return
			}
			// Same pattern as bootstrap: a 10s limit on the whole lookup,
			// and lctx comes from ctx so Ctrl+C still cancels it.
			lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			// Lookup is Kademlia's core algorithm. Each round it asks up
			// to alpha of the closest nodes found so far "who do you know
			// near target?", adds their answers to the candidate list, and
			// repeats until the k closest candidates have all been asked.
			result, err := n.Lookup(lctx, target)
			if err != nil {
				logger.Error("triggered lookup failed", "target", target.String(), "error", err)
				return
			}
			// result.Closest is the answer: up to k contacts, nearest to
			// the target first. Join their IDs into one comma-separated
			// string for the log line.
			ids := make([]string, len(result.Closest))
			for i, c := range result.Closest {
				ids[i] = c.ID.String()
			}
			// demo.sh waits for this exact "TRIGGERED LOOKUP RESULT"
			// message, then reads rounds, rpcs_sent, rpcs_failed and
			// closest_ids from it to decide whether the demo passed. Update
			// demo.sh if you change this message or its keys.
			logger.Info("TRIGGERED LOOKUP RESULT",
				"target", target.String(),
				"rounds", result.Rounds,
				"rpcs_sent", result.RPCsSent,
				"rpcs_failed", result.RPCsFailed,
				"closest_count", len(result.Closest),
				"closest_ids", strings.Join(ids, ","))
		}()
	}

	// ---- Step 6: keep serving until told to stop ----
	//
	// Block here until Ctrl+C or SIGTERM cancels ctx. Nothing else runs on
	// this goroutine meanwhile, but the node is not idle: the background
	// read loop from Start() keeps answering PINGs and FIND_NODEs, which is
	// how this node helps other nodes join and run lookups. When run()
	// returns, the deferred stop() and n.Close() run.
	<-ctx.Done()
	logger.Info("shutting down")
	return nil
}

// resolveID turns -id-seed into a deterministic ID, or generates a random
// one if no seed was given.
//
// SHA-1 always maps the same input to the same output, so the same seed
// gives the same ID on every run. That's how demo.sh works out each node's
// ID ahead of time (`printf demo2 | shasum -a 1`).
func resolveID(seed string) (kademlia.ID, error) {
	if seed == "" {
		return kademlia.RandomID(), nil
	}
	return kademlia.HashID([]byte(seed)), nil
}

// parseContact turns the "host:port" from -bootstrap into a Contact.
//
// A Contact is normally (ID, IP, port), but here we only know the address,
// not the remote node's ID, so we leave the ID as all zeros. That
// placeholder is safe because Node.Bootstrap only uses this Contact as the
// starting point of a lookup and never stores it in the routing table. The
// seed's reply carries its real ID, and the normal receive path
// (Node.handleMessage -> touchContact) adds the seed to the routing table
// under that real ID. See Node.Bootstrap and docs/04-bootstrap-and-join.md.
//
// An empty host (e.g. ":9000") becomes 127.0.0.1, because this project
// runs every node on one machine. Use an IP address rather than a hostname:
// Contact.UDPAddr uses net.ParseIP, which does not resolve names.
func parseContact(hostport string) (kademlia.Contact, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return kademlia.Contact{}, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return kademlia.Contact{}, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return kademlia.Contact{ID: kademlia.ID{}, IP: host, Port: port}, nil
}
