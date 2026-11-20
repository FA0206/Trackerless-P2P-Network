package kademlia

import (
	"bytes"
	"context"
	"sort"
	"testing"
	"time"
)

// byDistance returns the nodes sorted nearest-to-key first.
func byDistance(nodes []*Node, key ID) []*Node {
	out := append([]*Node(nil), nodes...)
	sort.Slice(out, func(i, j int) bool { return Less(Xor(out[i].ID, key), Xor(out[j].ID, key)) })
	return out
}

// sameIDs reports whether contacts are exactly the nodes in want, in order.
func sameIDs(contacts []Contact, want []*Node) bool {
	if len(contacts) != len(want) {
		return false
	}
	for i := range want {
		if contacts[i].ID != want[i].ID {
			return false
		}
	}
	return true
}

func hasValue(vals [][]byte, v string) bool {
	for _, x := range vals {
		if bytes.Equal(x, []byte(v)) {
			return true
		}
	}
	return false
}

// A dead node among the k closest to a key must not shorten the answer:
// the lookup drops it and asks the next-closest live node (paper Sec. 2.3:
// nodes that fail to respond "are removed from consideration"). Before
// this fix the dead node kept its slot, the lookup ended with k-1 nodes,
// and Store on the node *farthest* from the key concluded it was among
// the closest ("fewer than k found") and kept its own copy.
func TestLookupSkipsADeadNodeNearTheKey(t *testing.T) {
	const size, k = 12, 3
	nodes := smallKNetwork(t, size, k)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	key := HashID([]byte("some file"))
	sorted := byDistance(nodes, key)
	sorted[0].Close() // the node closest to the key dies
	live := sorted[1:]
	far := live[len(live)-1]

	res, err := far.Lookup(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.RPCsFailed == 0 {
		t.Fatal("the lookup never tried the dead node; the test doesn't test anything")
	}
	if !sameIDs(res.Closest, live[:k]) {
		t.Fatalf("lookup returned %d contacts, want the %d closest live nodes", len(res.Closest), k)
	}

	st, err := far.Store(ctx, key, []byte("record from far"))
	if err != nil {
		t.Fatal(err)
	}
	if st.StoredLocally {
		t.Fatal("the node farthest from the key kept its own copy (it saw fewer than k nodes)")
	}
	if len(st.StoredAt) != k {
		t.Fatalf("stored at %d nodes, want k=%d", len(st.StoredAt), k)
	}
}

// Even when every one of a node's own closest contacts is dead, the
// lookup carries on with the rest of its routing table and finds the k
// closest live nodes (EDGE-CASES B2: before, it gave up after one round).
func TestLookupFindsLiveNodesWhenAllNearestAreDead(t *testing.T) {
	const size, k = 12, 3
	nodes := smallKNetwork(t, size, k)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key := HashID([]byte("another file"))
	sorted := byDistance(nodes, key)
	for _, n := range sorted[:k] {
		n.Close() // all k closest to the key die at once
	}
	live := sorted[k:]
	far := live[len(live)-1]

	res, err := far.Lookup(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(res.Closest, live[:k]) {
		t.Fatalf("lookup returned %d contacts (failed RPCs %d), want the %d closest live nodes", len(res.Closest), res.RPCsFailed, k)
	}
}

// FindAllValues asks the network even when this node already holds a
// value for the key, and merges what the k closest live nodes hold.
// FindValue (used by `get`) still answers from the local store.
func TestFindAllValuesLooksPastItsOwnStore(t *testing.T) {
	const size, k = 12, 3
	nodes := smallKNetwork(t, size, k)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	key := HashID([]byte("shared file"))
	sorted := byDistance(nodes, key)
	far := sorted[len(sorted)-1]
	other := sorted[len(sorted)-2]

	far.store.put(key, []byte("stale record")) // e.g. its own record, or a dead seeder's
	if _, err := other.Store(ctx, key, []byte("fresh record")); err != nil {
		t.Fatal(err)
	}

	old, err := far.FindValue(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !old.FromLocalStore || hasValue(old.Values, "fresh record") {
		t.Fatalf("FindValue changed: from local store %v, values %d", old.FromLocalStore, len(old.Values))
	}

	all, err := far.FindAllValues(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !hasValue(all.Values, "stale record") || !hasValue(all.Values, "fresh record") {
		t.Fatalf("FindAllValues returned %d values, want both the local and the network's", len(all.Values))
	}
	if all.RPCsSent == 0 {
		t.Fatal("FindAllValues sent no RPCs")
	}
}

// FindAllValues doesn't stop at the first node that returns values: two
// holders with different values both contribute.
func TestFindAllValuesMergesDifferentHolders(t *testing.T) {
	const size, k = 12, 3
	nodes := smallKNetwork(t, size, k)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	key := HashID([]byte("split list"))
	sorted := byDistance(nodes, key)
	sorted[0].store.put(key, []byte("provider A"))
	sorted[k-1].store.put(key, []byte("provider B"))
	asker := sorted[len(sorted)-1]

	all, err := asker.FindAllValues(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !hasValue(all.Values, "provider A") || !hasValue(all.Values, "provider B") {
		t.Fatalf("got %d values from %d holders, want both providers", len(all.Values), len(all.ValuesFrom))
	}
}
