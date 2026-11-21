// Package kademlia implements a Kademlia DHT node: node IDs, the XOR
// distance metric, the k-bucket routing table, UDP message framing, and the
// iterative lookup algorithm described in Maymounkov & Mazieres,
// "Kademlia: A Peer-to-peer Information System Based on the XOR Metric".
package kademlia

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"
)

// IDBits is the width of the Kademlia ID space. The paper (Sec. 2.1) uses
// 160-bit identifiers because that's the output size of SHA-1, which is
// what it suggests for deriving node IDs and keys.
const IDBits = 160

// IDBytes is IDBits expressed in bytes, since Go has no native 160-bit
// integer type and we store IDs as a fixed-size byte array instead.
const IDBytes = IDBits / 8

// ID is a 160-bit Kademlia node ID or key. Byte 0 holds the most
// significant 8 bits, matching how we'd write the ID by hand as hex.
type ID [IDBytes]byte

// RandomID generates a cryptographically random 160-bit ID. Real nodes use
// this (or SHA-1 of some identity) to pick their node ID; see paper Sec. 2.1.
func RandomID() ID {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		// crypto/rand failing means the OS entropy source is broken; there's
		// nothing sensible for the caller to do with a half-random ID.
		panic(fmt.Sprintf("kademlia: crypto/rand failed: %v", err))
	}
	return id
}

// HashID derives a 160-bit ID from arbitrary bytes via SHA-1, e.g. to turn a
// human-readable seed like "node0" into a deterministic ID for demos/tests,
// or (per the paper) to turn a larger blob into a lookup key.
func HashID(data []byte) ID {
	return ID(sha1.Sum(data))
}

// IDFromHex decodes a 40-character hex string into an ID.
func IDFromHex(s string) (ID, error) {
	var id ID
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, fmt.Errorf("kademlia: invalid id hex %q: %w", s, err)
	}
	if len(b) != IDBytes {
		return id, fmt.Errorf("kademlia: id must be %d bytes, got %d", IDBytes, len(b))
	}
	copy(id[:], b)
	return id, nil
}

// String renders the ID as lowercase hex, e.g. for logging.
func (id ID) String() string {
	return hex.EncodeToString(id[:])
}

// MarshalJSON encodes the ID as a hex string so it's readable on the wire
// and in logs instead of a base64 blob or array of numbers.
func (id ID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

// UnmarshalJSON is the inverse of MarshalJSON.
func (id *ID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := IDFromHex(s)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Xor returns the bitwise XOR of two IDs, which the paper (Sec. 2.1) defines
// as the distance metric: d(x, y) = x XOR y, interpreted as an integer.
func Xor(a, b ID) ID {
	var out ID
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// IsZero reports whether the ID is all-zero. XOR distance is zero exactly
// when the two IDs are equal.
func (id ID) IsZero() bool {
	return id == ID{}
}

// Less reports whether a is numerically less than b, treating both as
// 160-bit big-endian unsigned integers. This is what lets us sort IDs (or
// XOR distances) to find "closest" nodes.
func Less(a, b ID) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// bitLen returns the number of bits needed to represent the ID as an
// unsigned integer (i.e. the position of its highest set bit, plus one).
// bitLen(0) == 0.
func (id ID) bitLen() int {
	for i := 0; i < IDBytes; i++ {
		if id[i] != 0 {
			return (IDBytes-i-1)*8 + bits.Len8(id[i])
		}
	}
	return 0
}

// BucketIndex returns which of a node's 160 k-buckets a contact with ID
// `other` belongs in, given the local node's own ID `self`.
//
// Per paper Sec. 2.2: bucket i holds contacts at XOR distance in
// [2^i, 2^(i+1)). That range is exactly "distance's highest set bit is at
// position i", i.e. bitLen(distance)-1. Buckets are numbered 0..159, where
// bucket 159 is the "farthest half of the keyspace" bucket and bucket 0
// would hold a contact that differs from self in only the lowest bit.
//
// BucketIndex panics if self == other: a node never has a bucket entry for
// itself, and callers (routing table, lookup) must filter self out first.
func BucketIndex(self, other ID) int {
	d := Xor(self, other)
	bl := d.bitLen()
	if bl == 0 {
		panic("kademlia: BucketIndex called with self == other")
	}
	return bl - 1
}
