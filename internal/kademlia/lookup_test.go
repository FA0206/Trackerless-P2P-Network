package kademlia

import (
	"context"
	"testing"
	"time"
)

// TestLookupAfterHalfTheNetworkDies is edge case B1
// as a Go test. After 8 of 16 nodes die, a lookup must still return k
// LIVE nodes. Dead contacts stay in the survivors' routing tables
// (nothing evicts them yet), so the lookup meets them and must skip past
// them: "nodes that fail to respond quickly are removed from
// consideration" (paper Sec. 2.3). Before that fix, a dead candidate kept
// its slot among the k closest and the answer came back short.
//
// It doesn't require the TRUE k closest live nodes: survivors' replies
// carry their k closest contacts, dead ones included, which can crowd out
// a live node nobody then mentions (B1 with SEED=4). Mismatches are
// logged; dropping dead contacts from tables is a separate fix.
func TestLookupAfterHalfTheNetworkDies(t *testing.T) {
	const size, k = 16, 4
	nodes := smallKNetwork(t, size, k)

	var live []*Node
	for i, n := range nodes {
		if i%2 == 1 && i != 15 {
			n.Close() // odd nodes die (node15 survives: it asks)
		} else {
			live = append(live, n)
		}
	}
	querier := nodes[15]

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for i := 0; i < 6; i++ {
		target := HashID([]byte{'b', '1', byte(i)})
		res, err := querier.Lookup(ctx, target)
		if err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
		if len(res.Closest) != k {
			t.Errorf("target %s: got %d contacts, want %d live ones (rpcs_failed=%d)",
				shortID(target), len(res.Closest), k, res.RPCsFailed)
		}
		alive := make(map[ID]bool)
		for _, n := range live {
			alive[n.ID] = true
		}
		for _, c := range res.Closest {
			if !alive[c.ID] || c.ID == querier.ID {
				t.Errorf("target %s: returned %s, which is dead or the querier", shortID(target), shortID(c.ID))
			}
		}
		want := trueClosest(liveExcept(live, querier), target, k)
		for _, c := range res.Closest {
			if !want[c.ID] {
				t.Logf("target %s: %s isn't among the true %d closest live nodes (a live one went unmentioned)",
					shortID(target), shortID(c.ID), k)
			}
		}
	}
}

// liveExcept is nodes without skip: a lookup never returns the querier.
func liveExcept(nodes []*Node, skip *Node) []*Node {
	var out []*Node
	for _, n := range nodes {
		if n != skip {
			out = append(out, n)
		}
	}
	return out
}
