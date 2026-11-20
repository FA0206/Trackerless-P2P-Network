// Package transfer is the file-sharing layer on top of the Kademlia DHT:
// it splits files into verifiable pieces, announces "I have file X" in the
// DHT, and moves the actual bytes between peers over TCP. The DHT only
// ever carries small provider records; see docs/07-file-transfer-over-tcp.md.
package transfer

import (
	"crypto/sha256"
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
// and the root of the Merkle tree over its pieces (merkle.go,
// docs/09-merkle-trees.md). Its hash is the file's content key
// (ContentKey), so whoever knows the key can check that a manifest is
// genuine, and then check every piece, with its proof, against the root.
//
// The manifest stays a few hundred bytes however big the file is. The
// first version carried a flat list of every piece's SHA-256 instead
// (BitTorrent v1's approach), which grows with the file: ~1 MB for 1 GB.
type Manifest struct {
	Name       string `json:"name"` // informational only; never used as a path
	Size       int64  `json:"size"`
	PieceSize  int    `json:"piece_size"`
	MerkleRoot string `json:"merkle_root"` // hex SHA-256, the root of the tree over the pieces
}

// BuildManifest reads the file at path, hashes it piece by piece into a
// Merkle tree, and returns the manifest and the tree. The sharer keeps the
// tree: it's what makes the proof sent with each piece.
func BuildManifest(path string, pieceSize int) (Manifest, *MerkleTree, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Manifest{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, nil, fmt.Errorf("transfer: %s is not a regular file", path)
	}

	m := Manifest{Name: filepath.Base(path), Size: info.Size(), PieceSize: pieceSize}
	var leaves []Hash
	buf := make([]byte, pieceSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			leaves = append(leaves, LeafHash(buf[:n]))
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return Manifest{}, nil, err
		}
	}
	tree := NewMerkleTree(leaves)
	m.MerkleRoot = tree.Root().String()
	if err := m.Validate(); err != nil {
		return Manifest{}, nil, err
	}
	if tree.Len() != m.NumPieces() {
		return Manifest{}, nil, fmt.Errorf("transfer: %s changed while being read? %d pieces hashed, %d expected",
			path, tree.Len(), m.NumPieces())
	}
	return m, tree, nil
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
	if _, err := parseHash(m.MerkleRoot); err != nil {
		return fmt.Errorf("transfer: bad merkle_root: %w", err)
	}
	return nil
}

// root is the Merkle root as a Hash. Only call it on a validated manifest.
func (m Manifest) root() Hash {
	h, _ := parseHash(m.MerkleRoot)
	return h
}

// VerifyPiece checks that data really is piece i of this file, using the
// proof that came with it: the piece's leaf hash and the proof must lead
// to the manifest's Merkle root (VerifyMerkleProof). The index and the
// piece count come from the manifest, never from the sender.
func (m Manifest) VerifyPiece(i int, data []byte, proof []Hash) error {
	if i < 0 || i >= m.NumPieces() {
		return fmt.Errorf("transfer: piece index %d out of range", i)
	}
	if len(data) != m.PieceLength(i) {
		return fmt.Errorf("transfer: piece %d is %d bytes, expected %d", i, len(data), m.PieceLength(i))
	}
	if !VerifyMerkleProof(m.root(), i, m.NumPieces(), LeafHash(data), proof) {
		return fmt.Errorf("transfer: piece %d failed its Merkle proof check", i)
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
