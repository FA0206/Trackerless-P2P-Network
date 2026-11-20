package transfer

import (
	"path/filepath"
	"strings"
	"testing"

	"kademlia-demo/internal/kademlia"
)

// testManifest is a 4-byte-piece manifest; claimForResume never checks
// hashes, so they're placeholders.
func testManifest(pieces int) Manifest {
	m := Manifest{Name: "f.bin", Size: int64(pieces) * 4, PieceSize: 4}
	for i := 0; i < pieces; i++ {
		m.PieceHashes = append(m.PieceHashes, strings.Repeat("0", 64))
	}
	return m
}

// claimForResume is the gate in front of every Download: it starts a fresh
// download, resumes a stopped one, or refuses.
func TestClaimForResume(t *testing.T) {
	lib := newLibrary()
	key := kademlia.HashID([]byte("file"))
	path := filepath.Join(t.TempDir(), "out.bin")

	if p, err := lib.claimForResume(key, path); p != nil || err != nil {
		t.Fatalf("unknown key: got %v, %v; want a fresh download (nil, nil)", p, err)
	}
	if err := lib.addPartial(key, testManifest(4), []byte("m"), path); err != nil {
		t.Fatal(err)
	}
	lib.markHave(key, 1)
	if _, err := lib.claimForResume(key, path); err == nil || !strings.Contains(err.Error(), "already in the library") {
		t.Errorf("while downloading: got %v, want already in the library", err)
	}

	lib.stopDownloading(key)
	other := filepath.Join(filepath.Dir(path), "other.bin")
	if _, err := lib.claimForResume(key, other); err == nil || !strings.Contains(err.Error(), "stopped partial download at "+path) {
		t.Errorf("another path: got %v, want the stopped-partial error naming %s", err, path)
	}
	p, err := lib.claimForResume(key, path)
	if err != nil || p == nil {
		t.Fatalf("stopped partial at the same path: got %v, %v; want a resume", p, err)
	}
	if !p.have.Has(1) || p.have.Count(4) != 1 {
		t.Errorf("resume starts with %d pieces, want just piece 1", p.have.Count(4))
	}
	p.have.Set(2)
	if have, _ := lib.bitfield(key); have.Has(2) {
		t.Error("claimForResume returned the library's own bitfield, not a copy")
	}
	if _, err := lib.claimForResume(key, path); err == nil {
		t.Error("a second claim succeeded while the first download is running")
	}

	// The partial is deleted by hand and another download takes its path:
	// the stopped entry must not resume into that other file.
	lib.stopDownloading(key)
	other2 := kademlia.HashID([]byte("other"))
	if err := lib.addPartial(other2, testManifest(4), []byte("m"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.claimForResume(key, path); err == nil || !strings.Contains(err.Error(), "now belongs to the library entry for") {
		t.Errorf("path taken by another entry: got %v, want refusal", err)
	}

	done := kademlia.HashID([]byte("done"))
	lib.addComplete(done, testManifest(4), []byte("m"), path)
	if _, err := lib.claimForResume(done, path); err == nil || !strings.Contains(err.Error(), "already in the library") {
		t.Errorf("complete file: got %v, want already in the library", err)
	}
}
