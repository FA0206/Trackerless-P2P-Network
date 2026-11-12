package kademlia

import "testing"

func TestRoutingTableIgnoresSelf(t *testing.T) {
	self := idFromByte(0)
	rt := NewRoutingTable(self, 2)
	outcome, _ := rt.Update(Contact{ID: self, IP: "127.0.0.1", Port: 1})
	if outcome != Seen {
		t.Fatalf("expected Seen (no-op) for self, got %v", outcome)
	}
	if rt.Size() != 0 {
		t.Fatalf("routing table should never store self, got size %d", rt.Size())
	}
}

func TestRoutingTableClosestOrdering(t *testing.T) {
	self := idFromByte(0)
	rt := NewRoutingTable(self, 20)

	for i := byte(1); i <= 10; i++ {
		rt.Update(contactN(i))
	}

	target := idFromByte(0)
	closest := rt.Closest(target, 3)
	if len(closest) != 3 {
		t.Fatalf("expected 3 closest contacts, got %d", len(closest))
	}
	// Distances to target=0 are just the contact's own id-byte value, so
	// nearest should be 1, 2, 3 in order.
	want := []byte{1, 2, 3}
	for i, c := range closest {
		if c.ID != idFromByte(want[i]) {
			t.Errorf("closest[%d] = %s, want contact %d", i, c, want[i])
		}
	}
}

func TestRoutingTableClosestCapsAtAvailable(t *testing.T) {
	self := idFromByte(0)
	rt := NewRoutingTable(self, 20)
	rt.Update(contactN(1))
	rt.Update(contactN(2))
	closest := rt.Closest(idFromByte(0), 10)
	if len(closest) != 2 {
		t.Fatalf("expected 2 (all known contacts), got %d", len(closest))
	}
}
