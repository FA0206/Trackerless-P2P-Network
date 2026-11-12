// Command node runs a single Kademlia participant as a standalone process,
// for manual testing and for scripts/demo.sh. See the top-level README for
// a full walkthrough.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kademlia-demo/internal/kademlia"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	port := flag.Int("port", 9000, "UDP port to listen on")
	idSeed := flag.String("id-seed", "", "deterministic node ID = SHA-1(id-seed); if empty, a random ID is used")
	bootstrap := flag.String("bootstrap", "", "host:port of an existing node to join through (optional)")
	lookupTarget := flag.String("lookup", "", "after joining, perform one extra lookup for this ID (40-char hex), log the result, and keep running")
	lookupAfter := flag.Duration("lookup-after", 1*time.Second, "how long to wait after bootstrap before running -lookup, to give the rest of the demo network time to join")
	verbose := flag.Bool("verbose", false, "log at debug level (every bucket update, every RPC timeout)")
	quiet := flag.Bool("quiet", false, "log warnings/errors only (handy when typing console commands)")
	console := flag.Bool("console", true, "read debug commands (help, table, lookup, ping) from stdin")
	k := flag.Int("k", kademlia.DefaultK, "bucket size / replication parameter k")
	alpha := flag.Int("alpha", kademlia.DefaultAlpha, "lookup concurrency parameter alpha")
	rpcTimeout := flag.Duration("rpc-timeout", kademlia.DefaultRPCTimeout, "per-RPC timeout (raise this, e.g. to 5m, when pausing at breakpoints)")
	flag.Parse()

	id, err := resolveID(*idSeed)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	switch {
	case *verbose:
		level = slog.LevelDebug
	case *quiet:
		level = slog.LevelWarn
	}
	logger := kademlia.NewLogger(id, level)

	// A whole lookup is several sequential RPC rounds, so its deadline must
	// scale with the per-RPC timeout -- otherwise raising -rpc-timeout for a
	// debugging session would still leave bootstrap dying after a fixed 10s.
	opTimeout := max(10*time.Second, 5**rpcTimeout)

	n, err := kademlia.NewNode(id, *port, *k, *alpha, *rpcTimeout, logger)
	if err != nil {
		return err
	}
	n.Start()
	defer n.Close()

	logger.Info("node started", "id", id.String(), "addr", n.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *bootstrap != "" {
		seedAddr, err := net.ResolveUDPAddr("udp4", *bootstrap)
		if err != nil {
			return fmt.Errorf("parsing -bootstrap: %w", err)
		}
		logger.Info("bootstrapping", "via", seedAddr.String())
		bctx, cancel := context.WithTimeout(ctx, opTimeout)
		result, err := n.Bootstrap(bctx, seedAddr)
		cancel()
		if err != nil {
			return fmt.Errorf("bootstrap failed: %w", err)
		}
		logger.Info("bootstrap complete", "routing_table_size", n.RoutingTableSize(),
			"self_lookup_rounds", result.Rounds, "self_lookup_rpcs", result.RPCsSent)
	}

	if *lookupTarget != "" {
		target, err := kademlia.IDFromHex(*lookupTarget)
		if err != nil {
			return fmt.Errorf("parsing -lookup: %w", err)
		}
		go func() {
			select {
			case <-time.After(*lookupAfter):
			case <-ctx.Done():
				return
			}
			lctx, cancel := context.WithTimeout(ctx, opTimeout)
			defer cancel()
			result, err := n.Lookup(lctx, target)
			if err != nil {
				logger.Error("triggered lookup failed", "target", target.String(), "error", err)
				return
			}
			ids := make([]string, len(result.Closest))
			for i, c := range result.Closest {
				ids[i] = c.ID.String()
			}
			logger.Info("TRIGGERED LOOKUP RESULT",
				"target", target.String(),
				"rounds", result.Rounds,
				"rpcs_sent", result.RPCsSent,
				"rpcs_failed", result.RPCsFailed,
				"closest_count", len(result.Closest),
				"closest_ids", strings.Join(ids, ","))
		}()
	}

	if *console {
		go runConsole(ctx, n, opTimeout, os.Stdin, os.Stdout)
	}

	<-ctx.Done()
	logger.Info("shutting down")
	return nil
}

// resolveID turns -id-seed into a deterministic ID, or generates a random
// one if no seed was given.
func resolveID(seed string) (kademlia.ID, error) {
	if seed == "" {
		return kademlia.RandomID(), nil
	}
	return kademlia.HashID([]byte(seed)), nil
}

// parseContact turns "host:port" into a Contact with an all-zero ID, for
// the console's `ping` command: we only know the address, and the PONG
// carries the real ID (which handleMessage records in the routing table).
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
