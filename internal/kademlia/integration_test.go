package kademlia

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// spinUpNode starts a real, listening Node on an OS-assigned loopback
// port, with a quiet logger so test output stays readable, and registers
// its cleanup.
func spinUpNode(t *testing.T, id ID) *Node {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	n, err := NewNode(id, 0, DefaultK, DefaultAlpha, 500*time.Millisecond, logger)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	n.Start()
	t.Cleanup(func() { n.Close() })
	return n
}

// TestEndToEndBootstrapAndLookup exercises the full stack over real UDP
// sockets on localhost: five nodes, a bootstrap chain, and a lookup that
// should find all five.
func TestEndToEndBootstrapAndLookup(t *testing.T) {
	const n = 5
	nodes := make([]*Node, n)
	for i := 0; i < n; i++ {
		nodes[i] = spinUpNode(t, HashID([]byte{byte(i)}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Each node bootstraps against the previous one, like the demo script.
	for i := 1; i < n; i++ {
		if _, err := nodes[i].Bootstrap(ctx, nodes[i-1].Self()); err != nil {
			t.Fatalf("node %d bootstrap: %v", i, err)
		}
	}

	target := RandomID()
	result, err := nodes[0].Lookup(ctx, target)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	if len(result.Closest) != n-1 {
		t.Errorf("expected lookup to find the other %d nodes, found %d: %v", n-1, len(result.Closest), result.Closest)
	}
	if result.Rounds == 0 {
		t.Errorf("expected at least one lookup round")
	}
	if result.RPCsSent == 0 {
		t.Errorf("expected at least one RPC to have been sent")
	}

	found := make(map[ID]bool)
	for _, c := range result.Closest {
		found[c.ID] = true
	}
	for i := 1; i < n; i++ {
		if !found[nodes[i].ID] {
			t.Errorf("lookup result missing node %d (%s)", i, nodes[i].ID)
		}
	}
}

// TestPingRoundTrip checks the simplest possible RPC end to end.
func TestPingRoundTrip(t *testing.T) {
	a := spinUpNode(t, HashID([]byte("a")))
	b := spinUpNode(t, HashID([]byte("b")))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !a.Ping(ctx, b.Self()) {
		t.Fatal("expected ping to b to succeed")
	}

	// Pinging a port nobody listens on should fail, not hang.
	deadContact := Contact{ID: RandomID(), IP: "127.0.0.1", Port: 1}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if a.Ping(ctx2, deadContact) {
		t.Fatal("expected ping to an unreachable contact to fail")
	}
}
