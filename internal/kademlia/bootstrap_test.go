package kademlia

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"kademlia-demo/internal/framing"
)

// Join scenarios beyond the happy path: no bootstrap address at all, the
// bootstrap node leaving, duplicate IDs, and something at the bootstrap
// address that isn't a working node. The happy paths are in integration_test.go.

func joinCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func hasID(contacts []Contact, id ID) bool {
	for _, c := range contacts {
		if c.ID == id {
			return true
		}
	}
	return false
}

// fakeUDPPeer listens on a loopback port and answers each datagram it
// receives with reply(datagram); a nil reply means stay silent. It stands
// in for something at the bootstrap address that isn't a working node.
func fakeUDPPeer(t *testing.T, reply func(datagram []byte) []byte) *net.UDPAddr {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, maxDatagramSize)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return // closed by the cleanup
			}
			if out := reply(buf[:n]); out != nil {
				conn.WriteToUDP(out, from)
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr)
}

// No bootstrap address: the node is the first node of a network of one.
// That isn't an error. Its lookups ask and find nobody, a value it stores
// stays on it alone, and the next node joins through it and finds both.
func TestFirstNodeWithoutBootstrap(t *testing.T) {
	ctx := joinCtx(t)
	first := spinUpNode(t, HashID([]byte("first")))

	res, err := first.Lookup(ctx, RandomID())
	if err != nil {
		t.Fatalf("lookup while alone: %v", err)
	}
	if len(res.Closest) != 0 || res.RPCsSent != 0 {
		t.Fatalf("alone, a lookup should ask and find nobody; got %d contacts after %d RPCs", len(res.Closest), res.RPCsSent)
	}

	key := HashID([]byte("greeting"))
	stored, err := first.Store(ctx, key, []byte("hello"))
	if err != nil {
		t.Fatalf("store while alone: %v", err)
	}
	if !stored.StoredLocally || len(stored.StoredAt) != 0 {
		t.Fatalf("alone, a value should stay on this node only; got locally=%v, on %d other nodes",
			stored.StoredLocally, len(stored.StoredAt))
	}

	second := spinUpNode(t, HashID([]byte("second")))
	if _, err := second.Bootstrap(ctx, first.Self().UDPAddr()); err != nil {
		t.Fatalf("joining through the first node: %v", err)
	}
	found, err := second.FindValue(ctx, key)
	if err != nil {
		t.Fatalf("find value: %v", err)
	}
	if len(found.Values) != 1 || string(found.Values[0]) != "hello" {
		t.Fatalf("the newcomer should find the value the first node stored alone; got %q", found.Values)
	}
}

// Two nodes started without a bootstrap address are two separate networks.
// With no tracker and no broadcast, nothing ever introduces them. One join,
// in either direction, merges them.
func TestNodesWithoutBootstrapStaySeparate(t *testing.T) {
	ctx := joinCtx(t)
	a := spinUpNode(t, HashID([]byte("island-a")))
	b := spinUpNode(t, HashID([]byte("island-b")))
	key := HashID([]byte("only-on-b"))
	if _, err := b.Store(ctx, key, []byte("b's value")); err != nil {
		t.Fatal(err)
	}

	res, err := a.Lookup(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Closest) != 0 {
		t.Fatalf("a found nodes without ever joining: %v", res.Closest)
	}
	found, err := a.FindValue(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Values) != 0 {
		t.Fatalf("a found b's value without ever joining: %q", found.Values)
	}

	if _, err := a.Bootstrap(ctx, b.Self().UDPAddr()); err != nil {
		t.Fatalf("joining the two networks: %v", err)
	}
	found, err = a.FindValue(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Values) != 1 {
		t.Fatalf("after one join, a should find b's value; got %q", found.Values)
	}
}

// The bootstrap node is needed for the first contact only. After it
// leaves, the nodes that joined through it still find each other, and a
// newcomer can join through any remaining member, not just the first node.
func TestBootstrapNodeCanLeaveAfterJoin(t *testing.T) {
	ctx := joinCtx(t)
	seed := spinUpNode(t, HashID([]byte("seed")))
	b := spinUpNode(t, HashID([]byte("member-b")))
	c := spinUpNode(t, HashID([]byte("member-c")))
	for _, joiner := range []*Node{b, c} {
		if _, err := joiner.Bootstrap(ctx, seed.Self().UDPAddr()); err != nil {
			t.Fatalf("joining through the seed: %v", err)
		}
	}
	seed.Close() // the seed leaves (its cleanup closes it again, harmlessly)

	res, err := b.Lookup(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasID(res.Closest, c.ID) {
		t.Fatalf("after the seed left, b should still find c; got %v", res.Closest)
	}
	if hasID(res.Closest, seed.ID) {
		t.Fatalf("the departed seed was reported as found: %v", res.Closest)
	}

	newcomer := spinUpNode(t, HashID([]byte("newcomer")))
	if _, err := newcomer.Bootstrap(ctx, c.Self().UDPAddr()); err != nil {
		t.Fatalf("joining through a member that isn't the first node: %v", err)
	}
	res, err = newcomer.Lookup(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasID(res.Closest, b.ID) {
		t.Fatalf("the newcomer should find b through c; got %v", res.Closest)
	}
}

// Two nodes with the same ID (the same ID seed). Joining through your own
// twin fails: its PONG carries our own ID, which a node can't tell apart
// from itself.
func TestBootstrapThroughNodeWithOurOwnID(t *testing.T) {
	ctx := joinCtx(t)
	original := spinUpNode(t, HashID([]byte("twin")))
	twin := spinUpNode(t, HashID([]byte("twin")))

	_, err := twin.Bootstrap(ctx, original.Self().UDPAddr())
	if err == nil {
		t.Fatal("joining through a node with our own ID should fail")
	}
	if !strings.Contains(err.Error(), "same ID") {
		t.Errorf("the error should say the IDs are the same: %v", err)
	}
}

// The protocol doesn't detect duplicate IDs. Both twins join fine through a
// third node, which keeps one contact per ID: whichever twin spoke last.
// Everything addressed to that ID then reaches only that twin.
func TestDuplicateIDNewestSenderWins(t *testing.T) {
	ctx := joinCtx(t)
	hub := spinUpNode(t, HashID([]byte("hub")))
	first := spinUpNode(t, HashID([]byte("twin")))
	second := spinUpNode(t, HashID([]byte("twin")))
	for _, joiner := range []*Node{first, second} {
		if _, err := joiner.Bootstrap(ctx, hub.Self().UDPAddr()); err != nil {
			t.Fatalf("a twin joining through the hub: %v", err)
		}
	}

	var entries []Contact
	for _, c := range hub.rt.Closest(first.ID, hub.rt.Size()) {
		if c.ID == first.ID {
			entries = append(entries, c)
		}
	}
	if len(entries) != 1 {
		t.Fatalf("the hub should keep exactly one contact for the shared ID; got %v", entries)
	}
	if entries[0].Port != second.Addr().Port {
		t.Errorf("the shared ID should point at the twin that spoke last (port %d); got %v", second.Addr().Port, entries[0])
	}
}

// Something answers at the bootstrap address, but it isn't a Kademlia node:
// a different program on that port. Its replies don't parse, so they're
// dropped, and the join fails cleanly after one RPC timeout.
func TestBootstrapToSomethingElse(t *testing.T) {
	ctx := joinCtx(t)
	addr := fakeUDPPeer(t, func([]byte) []byte { return []byte("HTTP/1.1 400 Bad Request\r\n\r\n") })
	a := spinUpNode(t, HashID([]byte("a")))

	if _, err := a.Bootstrap(ctx, addr); err == nil {
		t.Fatal("joining through something that isn't a Kademlia node should fail")
	}
	if a.RoutingTableSize() != 0 {
		t.Errorf("nothing should enter the routing table; got %d contacts", a.RoutingTableSize())
	}
}

// The bootstrap node answers the PING, then goes silent before the
// self-lookup (it crashed, or its network dropped). Current behavior: the
// join reports success, because the PING worked, but the self-lookup
// reached nobody, so the node knows only an unresponsive contact. A
// stricter join could treat "the self-lookup reached nobody" as a failure.
func TestBootstrapNodeGoesSilentAfterPing(t *testing.T) {
	ctx := joinCtx(t)
	silentID := HashID([]byte("silent"))
	addr := fakeUDPPeer(t, func(datagram []byte) []byte {
		env, err := decodeEnvelope(datagram)
		if err != nil || env.Type != MsgPing {
			return nil // FIND_NODE and everything else: no answer
		}
		body, err := json.Marshal(Envelope{Type: MsgPong, RPCID: env.RPCID, SenderID: silentID})
		if err != nil {
			return nil
		}
		return framing.Encode(body)
	})
	a := spinUpNode(t, HashID([]byte("a")))

	res, err := a.Bootstrap(ctx, addr)
	if err != nil {
		t.Fatalf("current behavior: the join reports success once the PING worked; got %v", err)
	}
	if len(res.Closest) != 0 || res.RPCsFailed != 1 {
		t.Fatalf("the self-lookup should reach nobody; got %d found, %d failed", len(res.Closest), res.RPCsFailed)
	}
	if a.RoutingTableSize() != 1 {
		t.Errorf("only the silent node should be known; got %d contacts", a.RoutingTableSize())
	}
}
