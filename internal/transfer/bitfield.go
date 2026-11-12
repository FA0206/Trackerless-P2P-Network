package transfer

import "fmt"

// Bitfield records which pieces a peer has: bit i is piece i, with the
// high bit of byte 0 for piece 0 -- the same layout as BitTorrent's
// `bitfield` message (BEP 3), so the bytes go on the wire unchanged in a
// GET_BITFIELD reply. Pure data, no I/O. See docs/08-swarming.md.
type Bitfield []byte

// NewBitfield returns an all-zero bitfield for n pieces.
func NewBitfield(n int) Bitfield {
	return make(Bitfield, (n+7)/8)
}

// FullBitfield returns a bitfield with all n pieces set.
func FullBitfield(n int) Bitfield {
	b := NewBitfield(n)
	for i := 0; i < n; i++ {
		b.Set(i)
	}
	return b
}

// Has reports whether piece i is set.
func (b Bitfield) Has(i int) bool {
	return b[i/8]&(0x80>>(i%8)) != 0
}

// Set marks piece i.
func (b Bitfield) Set(i int) {
	b[i/8] |= 0x80 >> (i % 8)
}

// Count is the number of pieces set.
func (b Bitfield) Count(n int) int {
	c := 0
	for i := 0; i < n; i++ {
		if b.Has(i) {
			c++
		}
	}
	return c
}

// Clone returns an independent copy.
func (b Bitfield) Clone() Bitfield {
	return append(Bitfield(nil), b...)
}

// decodeBitfield validates a bitfield received from a peer for a file of
// n pieces: it must be exactly ceil(n/8) bytes, and the spare bits after
// piece n-1 must be zero (a peer can't claim pieces that don't exist).
func decodeBitfield(data []byte, n int) (Bitfield, error) {
	if len(data) != (n+7)/8 {
		return nil, fmt.Errorf("transfer: bitfield is %d bytes, want %d for %d pieces", len(data), (n+7)/8, n)
	}
	b := Bitfield(data)
	for i := n; i < len(b)*8; i++ {
		if b.Has(i) {
			return nil, fmt.Errorf("transfer: bitfield claims piece %d of a %d-piece file", i, n)
		}
	}
	return b, nil
}
