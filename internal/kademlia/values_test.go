package kademlia

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"testing"
	"time"
)

// smallKNetwork starts `size` nodes with bucket size / replication k, all
// joined via node 0. A k much smaller than the network makes STORE pick a
// real subset of nodes, so FIND_VALUE has to actually search.
func smallKNetwork(t *testing.T, size, k int) []*Node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodes := make([]*Node, size)
	for i := range nodes {
		logger := slog.New(slog.DiscardHandler)
		id := HashID([]byte{'v', byte(i)})
		if os.Getenv("KADEMLIA_TEST_LOGS") != "" {
			logger = NewLogger(id, slog.LevelDebug)
		}
		n, err := NewNode(Config{ID: id, K: k, RPCTimeout: 500 * time.Millisecond, Logger: logger})
		if err != nil {
			t.Fatalf("NewNode: %v", err)
		}
		n.Start()
		t.Cleanup(func() { n.Close() })
		nodes[i] = n
		if i > 0 {
			if _, err := n.Bootstrap(ctx, nodes[0].Self().UDPAddr()); err != nil {
				t.Fatalf("node %d bootstrap: %v", i, err)
			}
		}
	}
	return nodes
}

// trueClosest computes, by brute force over every node, which k node IDs
// are closest to key -- the ground truth STORE should match.
func trueClosest(nodes []*Node, key ID, k int) map[ID]bool {
	ids := make([]ID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	sort.Slice(ids, func(i, j int) bool { return Less(Xor(ids[i], key), Xor(ids[j], key)) })
	out := make(map[ID]bool)
	for _, id := range ids[:k] {
		out[id] = true
	}
	return out
}

func TestStoreLandsOnKClosestAndFindValueFindsIt(t *testing.T) {
	const size, k = 8, 3
	nodes := smallKNetwork(t, size, k)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := HashID([]byte("greeting"))
	if _, err := nodes[5].Store(ctx, key, []byte("hello")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	want := trueClosest(nodes, key, k)
	for _, n := range nodes {
		has := len(n.store.get(key)) > 0
		if has != want[n.ID] {
			t.Errorf("node %s: holds value=%v, but is among the %d closest=%v", shortID(n.ID), has, k, want[n.ID])
		}
	}

	searchedRemotely := false
	for _, n := range nodes {
		res, err := n.FindValue(ctx, key)
		if err != nil {
			t.Fatalf("FindValue from %s: %v", shortID(n.ID), err)
		}
		if len(res.Values) != 1 || string(res.Values[0]) != "hello" {
			t.Fatalf("FindValue from %s: got %q", shortID(n.ID), res.Values)
		}
		if !res.FromLocalStore {
			searchedRemotely = true
			if res.Rounds == 0 {
				t.Errorf("remote FindValue from %s reported 0 rounds", shortID(n.ID))
			}
		}
	}
	if !searchedRemotely {
		t.Error("every node held the value locally; the test isn't exercising the FIND_VALUE lookup")
	}
}

func TestSameKeyCollectsValuesFromDifferentPublishers(t *testing.T) {
	nodes := smallKNetwork(t, 6, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := HashID([]byte("file"))
	for _, publisher := range []int{1, 4} {
		if _, err := nodes[publisher].Store(ctx, key, []byte{byte('a' + publisher)}); err != nil {
			t.Fatalf("Store from node %d: %v", publisher, err)
		}
	}
	res, err := nodes[2].FindValue(ctx, key)
	if err != nil {
		t.Fatalf("FindValue: %v", err)
	}
	if len(res.Values) != 2 {
		t.Fatalf("expected both publishers' values, got %q", res.Values)
	}
}

func TestFindValueForMissingKey(t *testing.T) {
	nodes := smallKNetwork(t, 5, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := nodes[3].FindValue(ctx, HashID([]byte("nobody stored this")))
	if err != nil {
		t.Fatalf("FindValue: %v", err)
	}
	if len(res.Values) != 0 {
		t.Fatalf("expected no values, got %q", res.Values)
	}
	if len(res.Closest) == 0 {
		t.Error("a failed FIND_VALUE should still report the closest nodes it found")
	}
}

func TestStoreRejectsOversizeValue(t *testing.T) {
	nodes := smallKNetwork(t, 2, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := nodes[0].Store(ctx, RandomID(), make([]byte, maxValueSize+1)); err == nil {
		t.Fatal("expected an error for a value over maxValueSize")
	}
}
