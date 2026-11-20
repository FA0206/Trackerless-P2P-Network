package transfer

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"kademlia-demo/internal/kademlia"
)

// Library is the set of files a peer can serve: files it shared (complete)
// and files it is downloading or has downloaded. A partial entry serves
// only the pieces whose bit is set in `have`, and a bit is set only after
// that piece was verified and written to disk (docs/08-swarming.md,
// "write, then publish"). Shared with the TCP server's goroutines, hence
// the mutex.
type Library struct {
	mu    sync.RWMutex
	files map[kademlia.ID]*libraryEntry
}

// Every piece is served with its Merkle proof (docs/09-merkle-trees.md).
// A file this peer shared has the whole tree and can make any proof. A
// file it's downloading has no tree: it can't build one without every
// piece, so it keeps the proof that came with each piece and passes that
// on (partial seeding, docs/08-swarming.md).
type libraryEntry struct {
	manifest      Manifest
	manifestBytes []byte
	path          string
	have          Bitfield    // guarded by Library.mu
	tree          *MerkleTree // shared files: read-only, makes every proof
	proofs        [][]Hash    // downloads: the proof received with each piece; guarded by Library.mu
}

// SharedFile describes one library entry, for listing.
type SharedFile struct {
	Key    kademlia.ID
	Name   string
	Size   int64
	Pieces int
	Have   int // pieces this peer has; == Pieces once complete
	Path   string
}

func newLibrary() *Library {
	return &Library{files: make(map[kademlia.ID]*libraryEntry)}
}

// addComplete registers a file this peer holds in full (shared by it),
// with the Merkle tree over its pieces (from BuildManifest).
func (l *Library) addComplete(key kademlia.ID, m Manifest, manifestBytes []byte, path string, tree *MerkleTree) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[key] = &libraryEntry{manifest: m, manifestBytes: manifestBytes, path: path,
		have: FullBitfield(m.NumPieces()), tree: tree}
}

// addPartial registers a file being downloaded into path, with no pieces
// yet. It refuses a key the library already has, so one download can
// never clobber another entry.
func (l *Library) addPartial(key kademlia.ID, m Manifest, manifestBytes []byte, path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.files[key]; ok {
		return fmt.Errorf("transfer: %s is already in the library", key)
	}
	l.files[key] = &libraryEntry{manifest: m, manifestBytes: manifestBytes, path: path,
		have: NewBitfield(m.NumPieces()), proofs: make([][]Hash, m.NumPieces())}
	return nil
}

// markHave publishes piece i, with the proof it was verified with: from
// now on it may be served, proof included. Call only after the piece was
// verified and written.
func (l *Library) markHave(key kademlia.ID, i int, proof []Hash) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.files[key]; ok {
		e.have.Set(i)
		if e.proofs != nil {
			e.proofs[i] = proof
		}
	}
}

// remove forgets an entry (a failed download), so nothing more is served
// from its file.
func (l *Library) remove(key kademlia.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.files, key)
}

// manifestFor returns the manifest and its exact bytes, if the key is known.
func (l *Library) manifestFor(key kademlia.ID) (Manifest, []byte, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.files[key]
	if !ok {
		return Manifest{}, nil, false
	}
	return e.manifest, e.manifestBytes, true
}

// bitfield returns a copy of the pieces this peer has for key.
func (l *Library) bitfield(key kademlia.ID) (Bitfield, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.files[key]
	if !ok {
		return nil, false
	}
	return e.have.Clone(), true
}

// readPiece reads piece i from disk, if this peer has it, along with its
// Merkle proof.
func (l *Library) readPiece(key kademlia.ID, i int) ([]byte, []Hash, error) {
	l.mu.RLock()
	e, ok := l.files[key]
	var m Manifest
	var path string
	var proof []Hash
	has := false
	if ok {
		m, path = e.manifest, e.path
		has = i >= 0 && i < m.NumPieces() && e.have.Has(i)
		if has {
			if e.tree != nil {
				proof, _ = e.tree.Proof(i) // read-only after NewMerkleTree; i is in range
			} else {
				proof = e.proofs[i]
			}
		}
	}
	l.mu.RUnlock()
	switch {
	case !ok:
		return nil, nil, fmt.Errorf("unknown content key %s", key)
	case i < 0 || i >= m.NumPieces():
		return nil, nil, fmt.Errorf("piece index %d out of range (0..%d)", i, m.NumPieces()-1)
	case !has:
		return nil, nil, fmt.Errorf("don't have piece %d yet", i)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	buf := make([]byte, m.PieceLength(i))
	if _, err := f.ReadAt(buf, m.PieceOffset(i)); err != nil {
		return nil, nil, fmt.Errorf("read piece %d: %w", i, err)
	}
	return buf, proof, nil
}

// List returns every file in the library, sorted by name.
func (l *Library) List() []SharedFile {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]SharedFile, 0, len(l.files))
	for key, e := range l.files {
		n := e.manifest.NumPieces()
		out = append(out, SharedFile{Key: key, Name: e.manifest.Name, Size: e.manifest.Size,
			Pieces: n, Have: e.have.Count(n), Path: e.path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
