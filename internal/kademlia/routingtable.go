package kademlia

import "sort"

// RoutingTable is a node's view of the network: 160 fixed k-buckets, one
// per possible XOR-distance "order of magnitude" from this node's own ID.
//
// SIMPLIFICATION: the paper (Sec. 2.4) describes the routing table as a
// binary tree of buckets that starts as a single bucket and *splits* as it
// fills, so that buckets near the node's own ID are finer-grained than
// buckets far away. We skip that: we always allocate exactly 160 buckets,
// one per possible bucket index (0..159), indexed directly by
// BucketIndex(self, other). This is simpler to implement and reason about,
// and for a demo-scale network (a handful to a few hundred nodes) it costs
// nothing in practice -- 160 mostly-empty bucket structs is negligible
// memory. The cost we're accepting: in a network with many millions of
// nodes, the tree version lets a node keep *more than k* contacts in the
// range nearest its own ID (by splitting that region further), which this
// fixed version cannot do. See docs/02-routing-table-and-kbuckets.md.
type RoutingTable struct {
	self    ID
	k       int
	buckets [IDBits]*Bucket
}

// NewRoutingTable creates a routing table for a node with the given ID,
// with each of the 160 k-buckets able to hold up to k contacts.
func NewRoutingTable(self ID, k int) *RoutingTable {
	rt := &RoutingTable{self: self, k: k}
	for i := range rt.buckets {
		rt.buckets[i] = NewBucket(k)
	}
	return rt
}

// setStaleCheck tells every bucket how to ask whether a contact is stale
// (Node passes its staleTracker's isStale). Called once, before the node
// handles any message. Without it, no contact is ever stale.
func (rt *RoutingTable) setStaleCheck(isStale func(ID) bool) {
	for _, b := range rt.buckets {
		b.mu.Lock()
		b.isStale = isStale
		b.mu.Unlock()
	}
}

// bucketFor returns the k-bucket that `other` belongs in.
func (rt *RoutingTable) bucketFor(other ID) *Bucket {
	return rt.buckets[BucketIndex(rt.self, other)]
}

// Update records that a contact was just seen, exactly like Bucket.Update,
// after silently ignoring attempts to add the node's own ID (a node never
// has a routing entry for itself).
func (rt *RoutingTable) Update(c Contact) (outcome UpdateOutcome, staleCandidate Contact) {
	if c.ID == rt.self {
		return Seen, Contact{}
	}
	return rt.bucketFor(c.ID).Update(c)
}

// KeepAlive and ReplaceStale forward to the bucket a contact belongs in;
// see Bucket's doc comment for the ping-then-decide eviction protocol.
func (rt *RoutingTable) KeepAlive(stale Contact) {
	rt.bucketFor(stale.ID).KeepAlive(stale)
}

func (rt *RoutingTable) ReplaceStale(stale, replacement Contact) bool {
	return rt.bucketFor(stale.ID).ReplaceStale(stale, replacement)
}

// Closest returns up to `count` contacts closest to `target` by XOR
// distance, sorted nearest-first.
//
// SIMPLIFICATION: the paper's bucket-splitting routing table lets you find
// candidates by scanning outward from the single bucket nearest the
// target. Since our table is a flat array of (at most) 160*k contacts --
// a few thousand at most for any k used in practice -- we just collect
// every contact we know about and sort the whole thing. That's O(n log n)
// instead of roughly O(k), which is fine at this scale and much simpler to
// get right; see docs/02-routing-table-and-kbuckets.md.
//
// Stale contacts (stale.go) are left out: they failed several RPCs in a
// row, so neither our lookups nor our FIND_NODE replies should send anyone
// to them. ClosestIncludingStale is the fallback when nothing fresh is
// left.
func (rt *RoutingTable) Closest(target ID, count int) []Contact {
	return rt.closest(target, count, false)
}

// ClosestIncludingStale is Closest with stale contacts included. A lookup
// uses it only when there's no fresh contact at all, which is what
// happens if this node's own network went down for a while: every contact
// failed. The paper keeps the table rather than voiding it (Sec. 4.1).
func (rt *RoutingTable) ClosestIncludingStale(target ID, count int) []Contact {
	return rt.closest(target, count, true)
}

func (rt *RoutingTable) closest(target ID, count int, includeStale bool) []Contact {
	var all []Contact
	for _, b := range rt.buckets {
		if includeStale {
			all = append(all, b.Contacts()...)
		} else {
			all = append(all, b.Fresh()...)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return Less(Xor(all[i].ID, target), Xor(all[j].ID, target))
	})
	if len(all) > count {
		all = all[:count]
	}
	return all
}

// Snapshot returns the contents of every non-empty bucket, keyed by bucket
// index, each oldest-first. It's for inspection/debugging only.
func (rt *RoutingTable) Snapshot() map[int][]Contact {
	out := make(map[int][]Contact)
	for i, b := range rt.buckets {
		if contacts := b.Contacts(); len(contacts) > 0 {
			out[i] = contacts
		}
	}
	return out
}

// Size returns the total number of contacts known across all buckets.
func (rt *RoutingTable) Size() int {
	n := 0
	for _, b := range rt.buckets {
		n += b.Len()
	}
	return n
}
