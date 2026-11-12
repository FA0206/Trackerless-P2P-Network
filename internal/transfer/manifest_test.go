package transfer

import (
	"crypto/rand"
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
	m, err := BuildManifest(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if m.NumPieces() != 3 || len(m.PieceHashes) != 3 {
		t.Fatalf("expected 3 pieces, got NumPieces=%d hashes=%d", m.NumPieces(), len(m.PieceHashes))
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
	m, err := BuildManifest(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if m.NumPieces() != 0 {
		t.Fatalf("expected 0 pieces, got %d", m.NumPieces())
	}
}

func TestVerifyPieceCatchesCorruption(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 2500)
	m, _ := BuildManifest(path, 1000)
	data, _ := os.ReadFile(path)

	if err := m.VerifyPiece(1, data[1000:2000]); err != nil {
		t.Fatalf("genuine piece rejected: %v", err)
	}
	bad := append([]byte(nil), data[1000:2000]...)
	bad[10] ^= 1
	if err := m.VerifyPiece(1, bad); err == nil {
		t.Error("a piece with one flipped bit passed verification")
	}
	if err := m.VerifyPiece(1, data[0:1000]); err == nil {
		t.Error("piece 0's bytes passed verification as piece 1")
	}
	if err := m.VerifyPiece(2, data[2000:2400]); err == nil {
		t.Error("a truncated last piece passed verification")
	}
}

func TestDecodeManifestChecksContentKey(t *testing.T) {
	path := writeRandomFile(t, t.TempDir(), 1500)
	m, _ := BuildManifest(path, 1000)
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
	m1, _ := BuildManifest(path, 1000)
	m2, _ := BuildManifest(path, 1000)
	b1, _ := EncodeManifest(m1)
	b2, _ := EncodeManifest(m2)
	if ContentKey(b1) != ContentKey(b2) {
		t.Error("the same file produced two different content keys")
	}
}
