package kademlia

import (
	"context"
	"testing"
	"time"
)

func TestStaleAfterNFailuresInARow(t *testing.T) {
	s := newStaleTracker(3)
	id := idFromByte(7)
	for i := 1; i <= 2; i++ {
		if s.failed(id) || s.isStale(id) {
			t.Fatalf("stale after %d failures, want 3", i)
		}
	}
	if !s.failed(id) || !s.isStale(id) {
		t.Fatal("not stale after the 3rd failure in a row")
	}
	if s.failed(id) {
		t.Error("becameStale reported twice; it should only report the moment it happens")
	}
}

func TestHearingFromANodeClearsItsFailures(t *testing.T) {
	s := newStaleTracker(2)
	id := idFromByte(7)
	s.failed(id)
	if s.heardFrom(id) {
		t.Error("heardFrom said it was stale after 1 of 2 failures")
	}
	s.failed(id) // the count restarted: 1 of 2 again
	if s.isStale(id) {
		t.Fatal("failures weren't consecutive (a message came in between), yet it's stale")
	}
	s.failed(id)
	if !s.heardFrom(id) {
		t.Error("heardFrom didn't report that a stale node spoke again")
	}
	if s.isStale(id) {
		t.Error("still stale after it spoke")
	}
}

func TestStaleTrackerIgnoresZeroIDAndCanBeOff(t *testing.T) {
	s := newStaleTracker(1)
	if s.failed(ID{}) || s.isStale(ID{}) {
		t.Error("the zero ID (Bootstrap's unidentified seed) must never be tracked")
	}
	off := newStaleTracker(-1)
	id := idFromByte(7)
	for i := 0; i < 10; i++ {
		off.failed(id)
	}
	if off.isStale(id) {
		t.Error("staleAfter < 0 should turn tracking off")
	}
	var none *staleTracker
	if none.failed(id) || none.isStale(id) || none.heardFrom(id) {
		t.Error("a nil tracker should never report stale")
	}
}

func TestStaleTrackerIsBounded(t *testing.T) {
	s := newStaleTracker(1)
	for i := 0; i < maxTracked+10; i++ {
		s.failed(HashID([]byte{byte(i), byte(i >> 8), 's'}))
	}
	if len(s.failures) != maxTracked {
		t.Errorf("tracking %d IDs, want at most %d", len(s.failures), maxTracked)
	}
}

func TestBucketSkipsAndReplacesStaleContacts(t *testing.T) {
	stale := map[ID]bool{}
	b := NewBucket(2)
	b.isStale = func(id ID) bool { return stale[id] }
	b.Update(contactN(1))
	b.Update(contactN(2))

	stale[contactN(1).ID] = true
	if fresh := b.Fresh(); len(fresh) != 1 || fresh[0].ID != contactN(2).ID {
		t.Fatalf("Fresh() = %v, want only contact 2", fresh)
	}
	if b.Len() != 2 {
		t.Fatal("a stale contact must keep its slot until replaced")
	}

	// Full bucket, one stale contact: replaced on the spot, no ping.
	outcome, removed := b.Update(contactN(3))
	if outcome != ReplacedStale || removed.ID != contactN(1).ID {
		t.Fatalf("got %v removing %s, want ReplacedStale removing contact 1", outcome, removed)
	}
	got := b.Contacts()
	if len(got) != 2 || got[0].ID != contactN(2).ID || got[1].ID != contactN(3).ID {
		t.Errorf("bucket = %v, want [2 3]", got)
	}

	// No stale contact: the usual ping-before-evict rule (Sec. 2.2).
	if outcome, _ := b.Update(contactN(4)); outcome != FullNeedsPing {
		t.Errorf("got %v, want FullNeedsPing when nothing is stale", outcome)
	}
}

func TestClosestLeavesOutStaleContacts(t *testing.T) {
	rt := NewRoutingTable(idFromByte(0), 20)
	stale := map[ID]bool{contactN(1).ID: true}
	rt.setStaleCheck(func(id ID) bool { return stale[id] })
	for i := byte(1); i <= 3; i++ {
		rt.Update(contactN(i))
	}
	got := rt.Closest(idFromByte(0), 3)
	if len(got) != 2 || got[0].ID != contactN(2).ID {
		t.Errorf("Closest = %v, want [2 3] (1 is stale)", got)
	}
	if all := rt.ClosestIncludingStale(idFromByte(0), 3); len(all) != 3 || all[0].ID != contactN(1).ID {
		t.Errorf("ClosestIncludingStale = %v, want [1 2 3]", all)
	}
}

// After half the network dies, repeating the same lookups must stop
// paying for dead nodes: they become stale, including the ones the
// querier only hears about from other nodes' replies.
func TestRepeatedLookupsStopAskingDeadNodes(t *testing.T) {
	const size, k = 16, 4
	nodes := smallKNetworkWith(t, size, k, 1) // stale after 1 failure: quick to learn
	for i, n := range nodes {
		if i%2 == 1 && i != 15 {
			n.Close()
		}
	}
	querier := nodes[15]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	lookupAll := func() (failed int) {
		for i := 0; i < 4; i++ {
			res, err := querier.Lookup(ctx, HashID([]byte{'s', byte(i)}))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Closest) != k {
				t.Errorf("got %d contacts, want %d", len(res.Closest), k)
			}
			failed += res.RPCsFailed
		}
		return failed
	}
	first := lookupAll()
	if first == 0 {
		t.Fatal("the first pass after the deaths met no dead node; the test isn't testing anything")
	}
	var again int
	for pass := 0; pass < 3; pass++ { // each pass can reveal dead nodes the previous one never met
		if again = lookupAll(); again == 0 {
			break
		}
	}
	if again != 0 {
		t.Errorf("still %d failed RPCs after repeating the lookups (first pass: %d)", again, first)
	}
}

// If every contact is stale (this node's own network was down), a lookup
// still uses them rather than giving up: the paper keeps the table rather
// than voiding it (Sec. 4.1). The first reply clears the flag.
func TestLookupFallsBackToStaleContacts(t *testing.T) {
	nodes := smallKNetworkWith(t, 2, 3, 1)
	a, b := nodes[0], nodes[1]
	a.stale.failed(b.ID) // pretend a's link was down when it last asked b
	if !a.IsStale(b.ID) {
		t.Fatal("setup: b should be stale for a")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := a.Lookup(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Closest) != 1 || res.Closest[0].ID != b.ID {
		t.Fatalf("lookup with only stale contacts returned %v, want b", res.Closest)
	}
	if a.IsStale(b.ID) {
		t.Error("b answered, so it shouldn't be stale any more")
	}
}
