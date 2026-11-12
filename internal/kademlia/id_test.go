package kademlia

import (
	"math/big"
	"testing"
)

func bigFromID(id ID) *big.Int {
	return new(big.Int).SetBytes(id[:])
}

func idFromByte(last byte) ID {
	var id ID
	id[IDBytes-1] = last
	return id
}

func TestXorIdentity(t *testing.T) {
	a := RandomID()
	if d := Xor(a, a); !d.IsZero() {
		t.Errorf("Xor(a, a) = %s, want all-zero", d)
	}
}

func TestXorSymmetric(t *testing.T) {
	a, b := RandomID(), RandomID()
	if Xor(a, b) != Xor(b, a) {
		t.Errorf("Xor not symmetric: Xor(a,b)=%s Xor(b,a)=%s", Xor(a, b), Xor(b, a))
	}
}

func TestXorTriangleInequality(t *testing.T) {
	// Paper Sec. 2.1: d(x,y) + d(y,z) >= d(x,z).
	for i := 0; i < 1000; i++ {
		x, y, z := RandomID(), RandomID(), RandomID()
		dxy := bigFromID(Xor(x, y))
		dyz := bigFromID(Xor(y, z))
		dxz := bigFromID(Xor(x, z))
		sum := new(big.Int).Add(dxy, dyz)
		if sum.Cmp(dxz) < 0 {
			t.Fatalf("triangle inequality violated: d(x,y)=%v d(y,z)=%v d(x,z)=%v", dxy, dyz, dxz)
		}
	}
}

func TestLessOrdersAsUnsignedInteger(t *testing.T) {
	small := idFromByte(1)
	big := idFromByte(2)
	if !Less(small, big) {
		t.Errorf("expected %s < %s", small, big)
	}
	if Less(big, small) {
		t.Errorf("expected %s not< %s", big, small)
	}
	if Less(small, small) {
		t.Errorf("Less(x, x) should be false")
	}
}

func TestBucketIndexRange(t *testing.T) {
	self := ID{}           // all-zero
	other := idFromByte(1) // differs only in the lowest bit
	idx := BucketIndex(self, other)
	if idx != 0 {
		t.Errorf("expected bucket index 0 for a 1-bit difference, got %d", idx)
	}

	var farthest ID
	farthest[0] = 0x80 // highest bit of the whole ID set -> bucket 159
	idx = BucketIndex(self, farthest)
	if idx != IDBits-1 {
		t.Errorf("expected bucket index %d for the top bit, got %d", IDBits-1, idx)
	}
}

func TestBucketIndexPanicsOnSelf(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected BucketIndex(self, self) to panic")
		}
	}()
	id := RandomID()
	BucketIndex(id, id)
}

func TestIDHexRoundTrip(t *testing.T) {
	id := RandomID()
	s := id.String()
	parsed, err := IDFromHex(s)
	if err != nil {
		t.Fatalf("IDFromHex: %v", err)
	}
	if parsed != id {
		t.Errorf("round trip mismatch: got %s want %s", parsed, id)
	}
}

func TestHashIDDeterministic(t *testing.T) {
	a := HashID([]byte("node0"))
	b := HashID([]byte("node0"))
	if a != b {
		t.Errorf("HashID not deterministic: %s != %s", a, b)
	}
	c := HashID([]byte("node1"))
	if a == c {
		t.Errorf("HashID collided for different inputs (astronomically unlikely, check impl)")
	}
}
