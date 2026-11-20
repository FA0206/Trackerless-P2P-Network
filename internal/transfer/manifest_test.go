package transfer

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeRandomFile creates a file of the given size with random contents.
func writeRandomFile(t *testing.T, dir string, size int) string {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	path := filepath.Join(dir, "input.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManifestPieceLayout(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 2*1000+300)
	m, _, err := BuildManifest(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if m.NumPieces() != 3 {
		t.Fatalf("expected 3 pieces, got %d", m.NumPieces())
	}
	if m.PieceLength(0) != 1000 || m.PieceLength(2) != 300 {
		t.Errorf("piece lengths: first=%d last=%d, want 1000 and 300", m.PieceLength(0), m.PieceLength(2))
	}
	if m.PieceOffset(2) != 2000 {
		t.Errorf("PieceOffset(2) = %d, want 2000", m.PieceOffset(2))
	}
}

func TestEmptyFileHasNoPieces(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 0)
	m, _, err := BuildManifest(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if m.NumPieces() != 0 {
		t.Fatalf("expected 0 pieces, got %d", m.NumPieces())
	}
}

func TestVerifyPieceCatchesCorruption(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 2500)
	m, tree, _ := BuildManifest(path, 1000)
	data, _ := os.ReadFile(path)
	proof1, _ := tree.Proof(1)
	proof0, _ := tree.Proof(0)
	proof2, _ := tree.Proof(2)

	if err := m.VerifyPiece(1, data[1000:2000], proof1); err != nil {
		t.Fatalf("genuine piece rejected: %v", err)
	}
	bad := append([]byte(nil), data[1000:2000]...)
	bad[10] ^= 1
	if err := m.VerifyPiece(1, bad, proof1); err == nil {
		t.Error("a piece with one flipped bit passed verification")
	}
	if err := m.VerifyPiece(1, data[0:1000], proof0); err == nil {
		t.Error("piece 0's bytes, with piece 0's genuine proof, passed verification as piece 1")
	}
	if err := m.VerifyPiece(2, data[2000:2400], proof2); err == nil {
		t.Error("a truncated last piece passed verification")
	}
	if err := m.VerifyPiece(1, data[1000:2000], nil); err == nil {
		t.Error("a genuine piece without its proof passed verification")
	}
}

// The point of the Merkle root: the manifest doesn't grow with the file.
// (With the old flat list, it grew by 67 bytes per piece.)
func TestManifestSizeDoesNotGrowWithTheFile(t *testing.T) {
	dir := t.TempDir()
	sizes := map[int]int{}
	for _, pieces := range []int{1, 10, 1000} {
		path := filepath.Join(dir, fmt.Sprintf("f%d.bin", pieces))
		os.WriteFile(path, make([]byte, pieces*100), 0o644)
		m, _, err := BuildManifest(path, 100)
		if err != nil {
			t.Fatal(err)
		}
		m.Name = "same" // the name's length doesn't count here
		b, _ := EncodeManifest(m)
		sizes[pieces] = len(b)
	}
	if d := sizes[1000] - sizes[1]; d > 3 { // only the size and count digits differ
		t.Errorf("manifest sizes %v: 1000 pieces cost %d more bytes than 1", sizes, d)
	}
}

func TestBuildManifestRootMatchesTheTree(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 2500)
	m, tree, _ := BuildManifest(path, 1000)
	if tree.Len() != 3 || m.MerkleRoot != tree.Root().String() {
		t.Errorf("manifest root %s, tree root %s over %d pieces", m.MerkleRoot, tree.Root(), tree.Len())
	}
	m.MerkleRoot = "not hex"
	if m.Validate() == nil {
		t.Error("a manifest with a malformed root passed validation")
	}
}

func TestDecodeManifestChecksContentKey(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 1500)
	m, _, _ := BuildManifest(path, 1000)
	encoded, _ := EncodeManifest(m)
	key := ContentKey(encoded)

	if _, err := DecodeManifest(key, encoded); err != nil {
		t.Fatalf("genuine manifest rejected: %v", err)
	}
	m.Size++ // a peer lying about the file
	tampered, _ := EncodeManifest(m)
	if _, err := DecodeManifest(key, tampered); err == nil {
		t.Error("tampered manifest accepted under the original key")
	}
}

func TestContentKeyIsDeterministic(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 3000)
	m1, _, _ := BuildManifest(path, 1000)
	m2, _, _ := BuildManifest(path, 1000)
	b1, _ := EncodeManifest(m1)
	b2, _ := EncodeManifest(m2)
	if ContentKey(b1) != ContentKey(b2) {
		t.Error("the same file produced two different content keys")
	}
}
