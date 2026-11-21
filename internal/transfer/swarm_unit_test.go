package transfer

import (
	"math/rand"
	"testing"
)

func TestBitfieldLayoutMatchesBitTorrent(t *testing.T) {
	b := NewBitfield(10)
	if len(b) != 2 {
		t.Fatalf("10 pieces need 2 bytes, got %d", len(b))
	}
	b.Set(0)
	b.Set(9)
	if b[0] != 0x80 || b[1] != 0x40 {
		t.Fatalf("piece 0 must be the high bit of byte 0 and piece 9 bit 6 of byte 1, got % x", []byte(b))
	}
	if !b.Has(0) || b.Has(1) || !b.Has(9) || b.Count(10) != 2 {
		t.Error("Has/Count disagree with Set")
	}
	if FullBitfield(10).Count(10) != 10 {
		t.Error("FullBitfield should set every piece")
	}
}

func TestDecodeBitfieldRejectsBadInput(t *testing.T) {
	if _, err := decodeBitfield([]byte{0xFF}, 10); err == nil {
		t.Error("accepted a 1-byte bitfield for 10 pieces")
	}
	if _, err := decodeBitfield([]byte{0xFF, 0xC0}, 10); err != nil {
		t.Errorf("rejected a valid full 10-piece bitfield: %v", err)
	}
	if _, err := decodeBitfield([]byte{0xFF, 0xE0}, 10); err == nil {
		t.Error("accepted a bitfield claiming piece 10 of a 10-piece file")
	}
}

func bits(n int, set ...int) Bitfield {
	b := NewBitfield(n)
	for _, i := range set {
		b.Set(i)
	}
	return b
}

func TestPickPiecePrefersRarest(t *testing.T) {
	need := FullBitfield(4)
	holders := []holderView{
		{have: FullBitfield(4), idle: true}, // seeder: has everything
		{have: bits(4, 0, 1, 2), idle: true},
		{have: bits(4, 0, 1), idle: true},
	}
	// Holder counts: piece 0 -> 3, 1 -> 3, 2 -> 2, 3 -> 1. Piece 3 is rarest.
	for seed := int64(0); seed < 20; seed++ {
		piece, holder, ok := pickPiece(need, 4, holders, rand.New(rand.NewSource(seed)))
		if !ok || piece != 3 || holder != 0 {
			t.Fatalf("seed %d: got piece %d from holder %d (ok=%v), want piece 3 from the seeder", seed, piece, holder, ok)
		}
	}
}

func TestPickPieceSkipsBusyHoldersAndPiecesNotNeeded(t *testing.T) {
	need := bits(4, 1, 2, 3) // piece 0 already done or in flight
	holders := []holderView{
		{have: FullBitfield(4), idle: false}, // busy: can't take a request
		{have: bits(4, 0, 2), idle: true},
		{have: nil, idle: true}, // bitfield unknown yet
	}
	piece, holder, ok := pickPiece(need, 4, holders, rand.New(rand.NewSource(1)))
	if !ok || piece != 2 || holder != 1 {
		t.Fatalf("got piece %d from holder %d (ok=%v), want piece 2 from holder 1", piece, holder, ok)
	}

	holders[1].idle = false
	if _, _, ok := pickPiece(need, 4, holders, rand.New(rand.NewSource(1))); ok {
		t.Error("assigned a piece although no idle holder has any needed piece")
	}
}

// With every piece equally rare (only the seeder has them), different
// downloaders must not all pick the same first piece -- that's what the
// random tie-break is for.
func TestPickPieceSpreadsTies(t *testing.T) {
	need := FullBitfield(16)
	holders := []holderView{{have: FullBitfield(16), idle: true}}
	seen := map[int]bool{}
	for seed := int64(0); seed < 50; seed++ {
		piece, _, _ := pickPiece(need, 16, holders, rand.New(rand.NewSource(seed)))
		seen[piece] = true
	}
	if len(seen) < 8 {
		t.Fatalf("50 downloaders' first picks covered only %d of 16 pieces; ties aren't random", len(seen))
	}
}
