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
	"kademlia-demo/internal/nodeapi"
	"kademlia-demo/internal/transfer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	port := flag.Int("port", 9000, "UDP port to listen on")
	tcpPort := flag.Int("tcp-port", 0, "TCP port for serving files; 0 means the same number as -port (TCP and UDP port numbers don't clash)")
	advertiseIP := flag.String("advertise-ip", "127.0.0.1", "IPv4 address other nodes use to download files from this one (it goes into provider records). If not given, it's detected from the route to the -bootstrap node, which can be any node of the network on another machine; without one, it stays 127.0.0.1. So a node started without -bootstrap, like the first one, must be given its IP")
	idSeed := flag.String("id-seed", "", "deterministic node ID = SHA-1(id-seed); if empty, a random ID is used")
	bootstrap := flag.String("bootstrap", "", "host:port of an existing node to join through (optional)")
	lookupTarget := flag.String("lookup", "", "after joining, perform one extra lookup for this ID (40-char hex), log the result, and keep running")
	lookupAfter := flag.Duration("lookup-after", 1*time.Second, "how long to wait after bootstrap before running -lookup, to give the rest of the demo network time to join")
	verbose := flag.Bool("verbose", false, "log at debug level (every bucket update, every RPC timeout)")
	quiet := flag.Bool("quiet", false, "log warnings/errors only (handy when typing console commands)")
	console := flag.Bool("console", true, "read commands (help, table, lookup, put, get, share, fetch, ...) from stdin")
	k := flag.Int("k", kademlia.DefaultK, "bucket size / replication parameter k")
	alpha := flag.Int("alpha", kademlia.DefaultAlpha, "lookup concurrency parameter alpha")
	rpcTimeout := flag.Duration("rpc-timeout", kademlia.DefaultRPCTimeout, "per-RPC timeout (raise this, e.g. to 5m, when pausing at breakpoints)")
	republishInterval := flag.Duration("republish-interval", kademlia.DefaultRepublishInterval, "how often to store again what this node published and what it holds for others (paper: 1h; demos use seconds; negative = off)")
	refreshInterval := flag.Duration("refresh-interval", kademlia.DefaultRefreshInterval, "how often to refresh k-buckets no lookup touched in that time, by looking up a random ID in each (paper: 1h; negative = off; the refresh at join always runs)")
	joinRefresh := flag.Bool("join-refresh", true, "after joining, refresh every k-bucket farther than the closest neighbor (paper Sec. 2.3); false only for experiments")
	staleAfter := flag.Int("stale-after", kademlia.DefaultStaleAfter, "failed RPCs in a row before a contact is stale: kept in the table, but lookups and replies skip it until it's heard from again or its back-off runs out")
	staleBackoff := flag.Duration("stale-backoff", kademlia.DefaultStaleBackoff, "how long a stale contact is skipped before lookups try it again; doubles after each failed retry, up to 1h (negative = never retry)")
	maxInFlight := flag.Int("max-inflight", transfer.DefaultMaxInFlight, "most piece requests a download keeps in flight at once (always at most 1 per provider)")
	debugCorrupt := flag.Bool("debug-corrupt-pieces", false, "DEBUG ONLY: flip one byte of every piece this node serves (sending the genuine Merkle proof), to show downloaders catching a dishonest provider")
	debugServeDelay := flag.Duration("debug-serve-delay", 0, "DEBUG ONLY: wait this long before answering each piece request, so a demo can kill this node mid-transfer (0 = off)")
	debugUploadRate := flag.Int64("debug-upload-rate", 0, "DEBUG ONLY: cap this node's total file upload at this many bytes per second, across all connections, to model a real uplink on localhost (0 = off)")
	apiAddr := flag.String("api", "", "serve this node's state as JSON over HTTP on host:port, e.g. 127.0.0.1:8000 (off if empty)")
	apiControl := flag.Bool("api-control", false, "with -api: also accept POST lookup/ping/put/get/share/fetch, so a page can make this node act (any website open in your browser could too; demo use only)")
	flag.Parse()
	if *apiControl && *apiAddr == "" {
		return fmt.Errorf("-api-control needs -api")
	}

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
	// With --api, every log record is also an event for GET /api/events.
	// The tee passes the printed handler exactly the records it would get
	// anyway, so stdout is unchanged.
	var events *nodeapi.Events
	if *apiAddr != "" {
		events = nodeapi.NewEvents()
		logger = slog.New(nodeapi.TeeHandler(logger.Handler(), events))
	}

	// A whole lookup is several sequential RPC rounds, so its deadline must
	// scale with the per-RPC timeout -- otherwise raising -rpc-timeout for a
	// debugging session would still leave bootstrap dying after a fixed 10s.
	opTimeout := max(10*time.Second, 5**rpcTimeout)

	// Parsed before the node exists: the advertise IP may be detected from
	// the route to the bootstrap node, and it's fixed when the node is built.
	var seedAddr *net.UDPAddr // nil without -bootstrap: this is the first node of a new network
	if *bootstrap != "" {
		if seedAddr, err = parseBootstrap(*bootstrap); err != nil {
			return err
		}
	}
	advertise, detected, err := chooseAdvertiseIP(*advertiseIP, flagWasSet("advertise-ip"), seedAddr, routeSourceIP)
	if err != nil {
		return err
	}
	if detected {
		logger.Info("advertise ip auto-detected", "ip", advertise, "via", seedAddr.String())
	}

	n, err := kademlia.NewNode(kademlia.Config{
		ID:                id,
		Port:              *port,
		AdvertiseIP:       advertise,
		K:                 *k,
		Alpha:             *alpha,
		RPCTimeout:        *rpcTimeout,
		StaleAfter:        *staleAfter,
		StaleBackoff:      *staleBackoff,
		RepublishInterval: *republishInterval,
		RefreshInterval:   *refreshInterval,
		SkipJoinRefresh:   !*joinRefresh,
		Logger:            logger,
	})
	if err != nil {
		return err
	}
	n.Start()
	defer n.Close()

	if *tcpPort == 0 {
		*tcpPort = n.Addr().Port
	}
	peer, err := transfer.NewPeer(n, transfer.Config{TCPPort: *tcpPort, MaxInFlight: *maxInFlight})
	if err != nil {
		return err
	}
	defer peer.Close()
	if *debugServeDelay > 0 {
		peer.SetDebugServeDelay(*debugServeDelay)
		logger.Warn("DEBUG: delaying every piece this node serves", "delay", debugServeDelay.String())
	}
	if *debugCorrupt {
		peer.SetDebugCorruptPieces(true)
		logger.Warn("DEBUG: this node corrupts every piece it serves (genuine proofs, wrong bytes)")
	}
	if *debugUploadRate > 0 {
		peer.SetDebugUploadRate(*debugUploadRate)
		logger.Warn("DEBUG: capping this node's upload", "bytes_per_sec", *debugUploadRate)
	}

	logger.Info("node started", "id", id.String(), "addr", n.Addr().String(), "tcp_port", peer.TCPPort())

	// Started before bootstrapping, so a viewer can watch the join itself.
	if *apiAddr != "" {
		stopAPI, err := startAPI(*apiAddr, nodeapi.Config{Node: n, Peer: peer, K: *k, Alpha: *alpha, RPCTimeout: *rpcTimeout, Events: events,
			Control: *apiControl, OpTimeout: opTimeout, TransferTimeout: 10 * opTimeout}, logger)
		if err != nil {
			return fmt.Errorf("starting -api: %w", err)
		}
		defer stopAPI()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if seedAddr != nil {
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
		c := &consoleEnv{node: n, peer: peer, opTimeout: opTimeout, transferTimeout: 10 * opTimeout, out: os.Stdout}
		go c.run(ctx, os.Stdin)
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

// parseBootstrap turns -bootstrap into the address to join through. A port
// is required. A host name is fine (unlike -advertise-ip): it's resolved
// once, here, e.g. from /etc/hosts or mDNS. IPv6 isn't, since nodes speak
// udp4/tcp4 only. Without a host (":9000") it means this machine.
func parseBootstrap(s string) (*net.UDPAddr, error) {
	addr, err := net.ResolveUDPAddr("udp4", s)
	if err != nil {
		return nil, fmt.Errorf("parsing -bootstrap %q: %w (want ip:port, e.g. 192.168.0.101:9000)", s, err)
	}
	return addr, nil
}

// chooseAdvertiseIP decides which IP this node puts in its provider records,
// the one place a node tells others where it lives (the DHT itself records
// peers under the address their packets come from). value is -advertise-ip
// and set says whether the user typed it; seed is the parsed -bootstrap
// address, nil without one. routeSource is routeSourceIP, passed in so
// tests need no network.
//
//   - Given explicitly: validated and used as is, even 127.0.0.1.
//   - Not given, joining through another machine: the IP this machine uses
//     to reach that machine, and detected is true. Any node of the network
//     will do as the bootstrap node; on one subnet the answer is the same.
//   - Otherwise (no -bootstrap, or it is this machine): the default,
//     127.0.0.1. So a node started without -bootstrap, like the first node
//     of a multi-device network, must be given its IP. Whoever starts the
//     first node knows it anyway: the second node can only join through it.
//
// SIMPLIFICATION: the detected IP is the source of one route, the one to
// the bootstrap node. Right when every node is on one subnet; wrong behind
// NAT, or when other peers reach us over a different interface. Nothing
// discovers how other nodes actually see us.
func chooseAdvertiseIP(value string, set bool, seed *net.UDPAddr,
	routeSource func(*net.UDPAddr) (net.IP, error)) (ip string, detected bool, err error) {
	if set {
		ip, err = parseAdvertiseIP(value)
		return ip, false, err
	}
	// ":9000" and "0.0.0.0:9000" also mean "this machine"; a route to them
	// would "detect" 127.0.0.1.
	if seed == nil || seed.IP == nil || seed.IP.IsUnspecified() || seed.IP.IsLoopback() {
		return value, false, nil
	}
	src, err := routeSource(seed)
	if err != nil || src.To4() == nil || src.IsLoopback() {
		// No usable route: keep the default. Bootstrap then fails loudly
		// if the node really is unreachable.
		return value, false, nil
	}
	return src.To4().String(), true, nil
}

// parseAdvertiseIP accepts only an IPv4 address that another machine can
// dial, and returns it in canonical form. Each rejected kind fails late and
// quietly otherwise: downloaders drop a provider record whose IP doesn't
// parse (a hostname), dial providers over tcp4 only (IPv6), and on Linux
// reach their own machine at 0.0.0.0. 127.0.0.1 is allowed: it's correct
// when every node runs on this machine.
func parseAdvertiseIP(s string) (string, error) {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() {
		return "", fmt.Errorf("-advertise-ip %q: must be an IPv4 address other nodes can reach, e.g. 192.168.0.101 (not a hostname, IPv6, or 0.0.0.0)", s)
	}
	return ip.To4().String(), nil
}

// routeSourceIP asks the OS which local IP it would send from to reach
// remote: the same question as `ip route get <ip>` (its "src" field).
// Dialing UDP sends no packet; it only picks a route and a source address.
func routeSourceIP(remote *net.UDPAddr) (net.IP, error) {
	conn, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP, nil
}

// flagWasSet reports whether the user gave flag name on the command line
// (as opposed to it holding its default value).
func flagWasSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
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
