package transfer

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"kademlia-demo/internal/kademlia"
)

// Library is the set of complete files a peer can serve: files it shared
// itself and files it finished downloading.
//
// SIMPLIFICATION: only complete files are served. A peer becomes a
// provider once it has every piece, not while downloading, so every
// provider holds the whole file. The end goal (pieces spread so no single
// node has the whole file) needs per-piece availability on top of this.
type Library struct {
	mu    sync.RWMutex
	files map[kademlia.ID]libraryEntry
}

type libraryEntry struct {
	manifest      Manifest
	manifestBytes []byte
	path          string
}

// SharedFile describes one library entry, for listing.
type SharedFile struct {
	Key    kademlia.ID
	Name   string
	Size   int64
	Pieces int
	Path   string
}

func newLibrary() *Library {
	return &Library{files: make(map[kademlia.ID]libraryEntry)}
}

func (l *Library) add(key kademlia.ID, m Manifest, manifestBytes []byte, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[key] = libraryEntry{manifest: m, manifestBytes: manifestBytes, path: path}
}

func (l *Library) lookup(key kademlia.ID) (libraryEntry, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.files[key]
	return e, ok
}

// readPiece reads piece i of a library file from disk.
func (l *Library) readPiece(key kademlia.ID, i int) ([]byte, error) {
	e, ok := l.lookup(key)
	if !ok {
		return nil, fmt.Errorf("unknown content key %s", key)
	}
	if i < 0 || i >= e.manifest.NumPieces() {
		return nil, fmt.Errorf("piece index %d out of range (0..%d)", i, e.manifest.NumPieces()-1)
	}
	f, err := os.Open(e.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, e.manifest.PieceLength(i))
	if _, err := f.ReadAt(buf, e.manifest.PieceOffset(i)); err != nil {
		return nil, fmt.Errorf("read piece %d: %w", i, err)
	}
	return buf, nil
}

// List returns every file in the library, sorted by name.
func (l *Library) List() []SharedFile {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]SharedFile, 0, len(l.files))
	for key, e := range l.files {
		out = append(out, SharedFile{Key: key, Name: e.manifest.Name, Size: e.manifest.Size, Pieces: e.manifest.NumPieces(), Path: e.path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
