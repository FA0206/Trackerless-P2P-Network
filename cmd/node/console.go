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
)

// runConsole reads one command per line from `in` and runs it against the
// node, so you can poke a running node by hand from its terminal (see
// docs/DEBUGGING.md). It's a debugging aid, not part of the protocol.
// Returns when `in` hits EOF; the node itself keeps running.
func runConsole(ctx context.Context, n *kademlia.Node, opTimeout time.Duration, in io.Reader, out io.Writer) {
	fmt.Fprintln(out, "console ready -- type 'help' for commands")
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "help":
			printHelp(out)
		case "id":
			fmt.Fprintf(out, "id=%s addr=%s\n", n.ID, n.Addr())
		case "table":
			printTable(n, out)
		case "lookup":
			consoleLookup(ctx, n, opTimeout, fields[1:], out)
		case "ping":
			consolePing(ctx, n, opTimeout, fields[1:], out)
		default:
			fmt.Fprintf(out, "unknown command %q -- type 'help'\n", fields[0])
		}
	}
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `commands:
  id                    print this node's ID and address
  table                 print every non-empty k-bucket (index, contacts oldest->newest)
  lookup <hex-id|self>  run an iterative lookup and print the k closest nodes found
  ping <host:port>      send a PING to an address and report whether it answered`)
}

func printTable(n *kademlia.Node, out io.Writer) {
	snapshot := n.RoutingTableSnapshot()
	if len(snapshot) == 0 {
		fmt.Fprintln(out, "routing table is empty")
		return
	}
	indexes := make([]int, 0, len(snapshot))
	for i := range snapshot {
		indexes = append(indexes, i)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(indexes)))
	total := 0
	for _, i := range indexes {
		fmt.Fprintf(out, "bucket %3d (distance in [2^%d, 2^%d)):\n", i, i, i+1)
		for _, c := range snapshot[i] {
			fmt.Fprintf(out, "    %s\n", c)
			total++
		}
	}
	fmt.Fprintf(out, "%d contacts in %d non-empty buckets\n", total, len(indexes))
}

func consoleLookup(ctx context.Context, n *kademlia.Node, opTimeout time.Duration, args []string, out io.Writer) {
	if len(args) != 1 {
		fmt.Fprintln(out, "usage: lookup <40-char-hex-id|self>")
		return
	}
	target := n.ID
	if args[0] != "self" {
		var err error
		if target, err = kademlia.IDFromHex(args[0]); err != nil {
			fmt.Fprintln(out, err)
			return
		}
	}
	lctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	result, err := n.Lookup(lctx, target)
	if err != nil {
		fmt.Fprintln(out, "lookup failed:", err)
		return
	}
	fmt.Fprintf(out, "lookup %s: rounds=%d rpcs_sent=%d rpcs_failed=%d\n",
		target, result.Rounds, result.RPCsSent, result.RPCsFailed)
	for i, c := range result.Closest {
		fmt.Fprintf(out, "  %2d. %s  distance=%s\n", i+1, c, kademlia.Xor(c.ID, target))
	}
}

func consolePing(ctx context.Context, n *kademlia.Node, opTimeout time.Duration, args []string, out io.Writer) {
	if len(args) != 1 {
		fmt.Fprintln(out, "usage: ping <host:port>")
		return
	}
	target, err := parseContact(args[0])
	if err != nil {
		fmt.Fprintln(out, err)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	start := time.Now()
	if n.Ping(pctx, target) {
		fmt.Fprintf(out, "PONG from %s in %s\n", args[0], time.Since(start).Round(time.Microsecond))
	} else {
		fmt.Fprintf(out, "no reply from %s\n", args[0])
	}
}
