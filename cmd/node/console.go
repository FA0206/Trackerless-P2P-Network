package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"kademlia-demo/internal/kademlia"
	"kademlia-demo/internal/transfer"
)

// consoleEnv runs typed commands against this process's node and peer, so
// you can drive a running node by hand from its terminal (see
// docs/DEBUGGING.md). The console is a convenience; nothing in the
// protocol depends on it.
type consoleEnv struct {
	node            *kademlia.Node
	peer            *transfer.Peer
	opTimeout       time.Duration // one lookup / store
	transferTimeout time.Duration // a whole download
	out             io.Writer
}

// run reads one command per line until `in` hits EOF; the node itself
// keeps running afterwards.
func (c *consoleEnv) run(ctx context.Context, in io.Reader) {
	fmt.Fprintln(c.out, "console ready -- type 'help' for commands")
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		cmd, args := fields[0], fields[1:]
		switch cmd {
		case "help":
			c.help()
		case "id":
			fmt.Fprintf(c.out, "id=%s udp=%s tcp_port=%d\n", c.node.ID, c.node.Addr(), c.peer.TCPPort())
		case "table":
			c.table()
		case "lookup":
			c.lookup(ctx, args)
		case "ping":
			c.ping(ctx, args)
		case "put":
			c.put(ctx, args)
		case "get":
			c.get(ctx, args)
		case "share":
			c.share(ctx, args)
		case "fetch":
			c.fetch(ctx, args)
		case "files":
			c.files()
		default:
			fmt.Fprintf(c.out, "unknown command %q -- type 'help'\n", cmd)
		}
	}
}

func (c *consoleEnv) help() {
	fmt.Fprintln(c.out, `DHT:
  id                         this node's ID, UDP address, and TCP port
  table                      every non-empty k-bucket (index, contacts oldest->newest)
  lookup <hex-id|self>       iterative FIND_NODE lookup; prints the k closest nodes
  ping <host:port>           PING an address and report whether it answered
  put <name> <text...>       STORE text under key SHA-1(name) on the k closest nodes
  get <name>                 FIND_VALUE for key SHA-1(name)
Files:
  share <path>               publish a local file; prints its content key and magnet link
  fetch <key|magnet> <out>   download a shared file by content key or magnet link
                             (out must not exist; the link's name is ignored).
                             After a fetch gives up, the same fetch resumes it
  files                      files this node can serve, each with its magnet link`)
}

func (c *consoleEnv) table() {
	snapshot := c.node.RoutingTableSnapshot()
	if len(snapshot) == 0 {
		fmt.Fprintln(c.out, "routing table is empty")
		return
	}
	indexes := make([]int, 0, len(snapshot))
	for i := range snapshot {
		indexes = append(indexes, i)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(indexes)))
	total := 0
	for _, i := range indexes {
		fmt.Fprintf(c.out, "bucket %3d (distance in [2^%d, 2^%d)):\n", i, i, i+1)
		for _, contact := range snapshot[i] {
			if c.node.IsStale(contact.ID) {
				fmt.Fprintf(c.out, "    %s  (stale: lookups skip it)\n", contact)
			} else {
				fmt.Fprintf(c.out, "    %s\n", contact)
			}
			total++
		}
	}
	fmt.Fprintf(c.out, "%d contacts in %d non-empty buckets; holding values for %d keys\n",
		total, len(indexes), c.node.StoredKeyCount())
}

func (c *consoleEnv) lookup(ctx context.Context, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(c.out, "usage: lookup <40-char-hex-id|self>")
		return
	}
	target := c.node.ID
	if args[0] != "self" {
		var err error
		if target, err = kademlia.IDFromHex(args[0]); err != nil {
			fmt.Fprintln(c.out, err)
			return
		}
	}
	lctx, cancel := context.WithTimeout(ctx, c.opTimeout)
	defer cancel()
	result, err := c.node.Lookup(lctx, target)
	if err != nil {
		fmt.Fprintln(c.out, "lookup failed:", err)
		return
	}
	fmt.Fprintf(c.out, "lookup %s: rounds=%d rpcs_sent=%d rpcs_failed=%d\n",
		target, result.Rounds, result.RPCsSent, result.RPCsFailed)
	for i, contact := range result.Closest {
		fmt.Fprintf(c.out, "  %2d. %s  distance=%s\n", i+1, contact, kademlia.Xor(contact.ID, target))
	}
}

func (c *consoleEnv) ping(ctx context.Context, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(c.out, "usage: ping <host:port>")
		return
	}
	target, err := parseContact(args[0])
	if err != nil {
		fmt.Fprintln(c.out, err)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, c.opTimeout)
	defer cancel()
	start := time.Now()
	if c.node.Ping(pctx, target) {
		fmt.Fprintf(c.out, "PONG from %s in %s\n", args[0], time.Since(start).Round(time.Microsecond))
	} else {
		fmt.Fprintf(c.out, "no reply from %s\n", args[0])
	}
}

func (c *consoleEnv) put(ctx context.Context, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(c.out, "usage: put <name> <text...>")
		return
	}
	key := kademlia.HashID([]byte(args[0]))
	sctx, cancel := context.WithTimeout(ctx, c.opTimeout)
	defer cancel()
	res, err := c.node.Store(sctx, key, []byte(strings.Join(args[1:], " ")))
	if err != nil {
		fmt.Fprintln(c.out, "put failed:", err)
		return
	}
	fmt.Fprintf(c.out, "put %s (key %s): stored on %d other node(s), locally=%v, failed=%d\n",
		args[0], key, len(res.StoredAt), res.StoredLocally, res.Failed)
	for _, contact := range res.StoredAt {
		fmt.Fprintf(c.out, "    %s\n", contact)
	}
}

func (c *consoleEnv) get(ctx context.Context, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(c.out, "usage: get <name>")
		return
	}
	key := kademlia.HashID([]byte(args[0]))
	gctx, cancel := context.WithTimeout(ctx, c.opTimeout)
	defer cancel()
	res, err := c.node.FindValue(gctx, key)
	if err != nil {
		fmt.Fprintln(c.out, "get failed:", err)
		return
	}
	how := fmt.Sprintf("via network: rounds=%d rpcs_sent=%d rpcs_failed=%d", res.Rounds, res.RPCsSent, res.RPCsFailed)
	if res.FromLocalStore {
		how = "from this node's own store"
	}
	fmt.Fprintf(c.out, "get %s: %d value(s) %s\n", args[0], len(res.Values), how)
	for _, v := range res.Values {
		fmt.Fprintf(c.out, "    %q\n", v)
	}
}

func (c *consoleEnv) share(ctx context.Context, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(c.out, "usage: share <path>")
		return
	}
	sctx, cancel := context.WithTimeout(ctx, c.opTimeout)
	defer cancel()
	key, err := c.peer.Share(sctx, args[0])
	if err != nil {
		fmt.Fprintln(c.out, "share failed:", err)
		return
	}
	// The "content key:" line must stay as it is: the demo scripts grep it
	// and take its third field (docs/ARCHITECTURE.md, "If you change X").
	fmt.Fprintf(c.out, "shared %s\n  content key: %s\n  magnet link: %s\n  others can run: fetch %s <out-path>\n",
		args[0], key, c.magnetFor(key), key)
}

// magnetFor returns the magnet link for a file in this node's library,
// named by its manifest (docs/12-magnet-links.md).
func (c *consoleEnv) magnetFor(key kademlia.ID) string {
	for _, f := range c.peer.Library().List() {
		if f.Key == key {
			return f.Magnet()
		}
	}
	return transfer.MagnetLink(key, "") // not reached after a successful Share
}

func (c *consoleEnv) fetch(ctx context.Context, args []string) {
	if len(args) != 2 {
		fmt.Fprintln(c.out, "usage: fetch <40-char-hex-key|magnet-link> <out-path>")
		return
	}
	// Only the key matters: a magnet link's dn (name) is never trusted
	// and never used as a path, so out-path stays required.
	key, err := transfer.ParseKeyOrMagnet(args[0])
	if err != nil {
		fmt.Fprintln(c.out, err)
		return
	}
	fctx, cancel := context.WithTimeout(ctx, c.transferTimeout)
	defer cancel()
	res, err := c.peer.Download(fctx, key, args[1])
	if err != nil {
		fmt.Fprintln(c.out, "fetch failed:", err)
		return
	}
	fmt.Fprintf(c.out, "fetched %q (%d bytes, %d pieces) into %s in %s\n",
		res.Name, res.Size, res.Pieces, args[1], res.Duration.Round(time.Millisecond))
	for addr, n := range res.PiecesFrom {
		fmt.Fprintf(c.out, "    %d piece(s) from %s\n", n, addr)
	}
	if res.AlreadyHad > 0 {
		fmt.Fprintf(c.out, "    %d piece(s) were already on disk (resumed)\n", res.AlreadyHad)
	}
	for _, addr := range res.FailedProviders {
		fmt.Fprintf(c.out, "    provider %s failed and was skipped\n", addr)
	}
}

func (c *consoleEnv) files() {
	list := c.peer.Library().List()
	if len(list) == 0 {
		fmt.Fprintln(c.out, "no files shared or downloaded yet")
		return
	}
	for _, f := range list {
		fmt.Fprintf(c.out, "%s  %s  %d bytes, has %d/%d pieces  (%s)\n", f.Key, f.Name, f.Size, f.Have, f.Pieces, f.Path)
		fmt.Fprintf(c.out, "    %s\n", f.Magnet())
	}
}
