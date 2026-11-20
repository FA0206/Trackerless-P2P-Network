// Package transfer is the file-sharing layer on top of the Kademlia DHT:
// it splits files into verifiable pieces, announces "I have file X" in the
// DHT, and moves the actual bytes between peers over TCP. The DHT only
// ever carries small provider records; see docs/07-file-transfer-over-tcp.md.
package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"kademlia-demo/internal/kademlia"
)

const (
	// DefaultPieceSize is how much of a file one piece holds. A 1 MiB demo
	// file becomes 16 pieces -- enough to see pieces come from different
	// peers, small enough to read every log line.
	DefaultPieceSize = 64 * 1024
	maxPieceSize     = 4 << 20
	maxPieces        = 1 << 16
)

// Manifest describes a shared file: its size, how it's cut into pieces,
// and the SHA-256 of every piece. Its hash is the file's content key
// (ContentKey), so whoever knows the key can check that a manifest is
// genuine, and then check every piece against the manifest.
//
// This is BitTorrent v1's approach, a flat list of piece hashes. A Merkle
// tree would replace PieceHashes with one root hash plus a per-piece proof;
// see docs/FAQ.md for why that isn't needed at this project's scale. The
// tree itself is in merkle.go (docs/09-merkle-trees.md), not used here yet.
type Manifest struct {
	Name        string   `json:"name"` // informational only; never used as a path
	Size        int64    `json:"size"`
	PieceSize   int      `json:"piece_size"`
	PieceHashes []string `json:"piece_hashes"` // hex SHA-256, one per piece
}

// BuildManifest reads the file at path and hashes it piece by piece.
func BuildManifest(path string, pieceSize int) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Manifest{}, err
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("transfer: %s is not a regular file", path)
	}

	m := Manifest{Name: filepath.Base(path), Size: info.Size(), PieceSize: pieceSize}
	buf := make([]byte, pieceSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			sum := sha256.Sum256(buf[:n])
			m.PieceHashes = append(m.PieceHashes, hex.EncodeToString(sum[:]))
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("transfer: %s changed while being read? %w", path, err)
	}
	return m, nil
}

// NumPieces is ceil(Size / PieceSize).
func (m Manifest) NumPieces() int {
	return int((m.Size + int64(m.PieceSize) - 1) / int64(m.PieceSize))
}

// PieceOffset is where piece i starts in the file.
func (m Manifest) PieceOffset(i int) int64 {
	return int64(i) * int64(m.PieceSize)
}

// PieceLength is PieceSize for every piece but the last, which holds the
// remainder.
func (m Manifest) PieceLength(i int) int {
	return int(min(int64(m.PieceSize), m.Size-m.PieceOffset(i)))
}

// Validate checks that a manifest is internally consistent. A manifest
// from another peer has passed the content-key check, but that only proves
// the publisher wrote it, not that it's sane.
func (m Manifest) Validate() error {
	if m.PieceSize <= 0 || m.PieceSize > maxPieceSize {
		return fmt.Errorf("transfer: piece size %d out of range (1..%d)", m.PieceSize, maxPieceSize)
	}
	if m.Size < 0 {
		return fmt.Errorf("transfer: negative size %d", m.Size)
	}
	if m.Size > int64(maxPieces)*int64(m.PieceSize) {
		return fmt.Errorf("transfer: file needs more than %d pieces", maxPieces)
	}
	if len(m.PieceHashes) != m.NumPieces() {
		return fmt.Errorf("transfer: %d piece hashes for %d pieces", len(m.PieceHashes), m.NumPieces())
	}
	for i, h := range m.PieceHashes {
		if b, err := hex.DecodeString(h); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("transfer: piece %d hash %q is not a SHA-256 hex digest", i, h)
		}
	}
	return nil
}

// VerifyPiece checks that data really is piece i of this file.
func (m Manifest) VerifyPiece(i int, data []byte) error {
	if i < 0 || i >= m.NumPieces() {
		return fmt.Errorf("transfer: piece index %d out of range", i)
	}
	if len(data) != m.PieceLength(i) {
		return fmt.Errorf("transfer: piece %d is %d bytes, expected %d", i, len(data), m.PieceLength(i))
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != m.PieceHashes[i] {
		return fmt.Errorf("transfer: piece %d failed its hash check", i)
	}
	return nil
}

// EncodeManifest returns the manifest's canonical bytes: exactly these
// bytes are hashed into the content key and sent over TCP. Go's JSON
// encoder writes struct fields in declaration order, so the same Manifest
// always encodes identically.
func EncodeManifest(m Manifest) ([]byte, error) {
	return json.Marshal(m)
}

// ContentKey is the DHT key a file is published under: SHA-256 of its
// manifest bytes, truncated to 160 bits to fit Kademlia's key space (the
// same trick BitTorrent v2 uses). Plain SHA-1 would fit without
// truncating, but SHA-1 has practical collision attacks, and this key is
// the root of the whole integrity chain.
func ContentKey(manifestBytes []byte) kademlia.ID {
	sum := sha256.Sum256(manifestBytes)
	var key kademlia.ID
	copy(key[:], sum[:kademlia.IDBytes])
	return key
}

// DecodeManifest parses manifest bytes received from a peer, after
// checking that they hash to the content key we asked for. This check is
// what makes it safe to fetch the manifest from a stranger.
func DecodeManifest(key kademlia.ID, data []byte) (Manifest, error) {
	if ContentKey(data) != key {
		return Manifest{}, errors.New("transfer: manifest does not hash to the requested content key")
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("transfer: decode manifest: %w", err)
	}
	return m, m.Validate()
}
