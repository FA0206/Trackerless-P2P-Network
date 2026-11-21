package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// A Merkle tree over a file's pieces: one 32-byte root fingerprints the
// whole file, and any single piece can be checked against that root with a
// short proof (log2 of the piece count, rounded up, sibling hashes)
// instead of the full list of piece hashes. BitTorrent v2 does the same.
// See docs/11-merkle-trees.md.
//
// Pure functions, no I/O (like bitfield.go and picker.go). The manifest
// carries the root (manifest.go), every piece travels with its proof
// (protocol.go), and the downloader checks it (VerifyPiece).
//
// The tree shape and hashing follow RFC 6962 (Certificate Transparency),
// Sec. 2.1, and the proof check follows RFC 9162, Sec. 2.1.3.2:
//
//   - leaf:  H(0x00 || piece)          inner node: H(0x01 || left || right)
//
//     The different prefixes stop anyone passing an inner node off as a
//     leaf (a "second preimage" attack on naive Merkle trees).
//   - n leaves, n not a power of two: the left subtree takes the largest
//     power of two smaller than n, the right subtree takes the rest. No
//     leaf is ever duplicated (Bitcoin's duplicate-the-last-leaf trick lets
//     two different leaf lists share a root, CVE-2012-2459).
//   - no leaves (an empty file): the root is H("") (SHA-256 of nothing).

// Hash is a SHA-256 digest: a leaf, an inner node, or the root.
type Hash [sha256.Size]byte

// String is the hash in hex, the form it takes in JSON (manifest, proofs).
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// parseHash decodes a hex hash, as received in a manifest or a proof.
func parseHash(s string) (Hash, error) {
	var h Hash
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(h) {
		return h, fmt.Errorf("transfer: %q is not a SHA-256 hex digest", s)
	}
	copy(h[:], b)
	return h, nil
}

// LeafHash hashes one piece's bytes as a leaf: H(0x00 || piece).
func LeafHash(piece []byte) Hash {
	h := sha256.New()
	h.Write([]byte{0x00})
	h.Write(piece)
	var out Hash
	h.Sum(out[:0])
	return out
}

// nodeHash combines two children into their parent: H(0x01 || left || right).
func nodeHash(left, right Hash) Hash {
	h := sha256.New()
	h.Write([]byte{0x01})
	h.Write(left[:])
	h.Write(right[:])
	var out Hash
	h.Sum(out[:0])
	return out
}

// MerkleTree holds a file's leaf hashes and every inner hash, computed
// once when it's built (O(n) hashing). After that it's read-only, so the
// TCP server's goroutines can ask it for proofs concurrently.
type MerkleTree struct {
	leaves []Hash
	memo   map[[2]int]Hash // subtree [start, end) -> its hash; complete after NewMerkleTree
	root   Hash
}

// NewMerkleTree builds a tree over the given leaf hashes (one per piece, in
// order, from LeafHash). It keeps its own copy of the slice, and computes
// every inner hash now: Proof only ever asks for subtrees of this one
// shape, so later calls just read the memo.
func NewMerkleTree(leaves []Hash) *MerkleTree {
	t := &MerkleTree{leaves: append([]Hash(nil), leaves...), memo: make(map[[2]int]Hash)}
	if len(t.leaves) == 0 {
		t.root = sha256.Sum256(nil)
	} else {
		t.root = t.subtree(0, len(t.leaves))
	}
	return t
}

// Len is the number of leaves (pieces).
func (t *MerkleTree) Len() int { return len(t.leaves) }

// Root is the tree's root hash. For an empty tree it's H("").
func (t *MerkleTree) Root() Hash { return t.root }

// subtree is the hash of leaves[start:end] (end > start), RFC 6962's MTH.
func (t *MerkleTree) subtree(start, end int) Hash {
	if end-start == 1 {
		return t.leaves[start]
	}
	if h, ok := t.memo[[2]int{start, end}]; ok {
		return h
	}
	k := splitPoint(end - start)
	h := nodeHash(t.subtree(start, start+k), t.subtree(start+k, end))
	t.memo[[2]int{start, end}] = h
	return h
}

// splitPoint is the largest power of two smaller than n (n >= 2): how many
// leaves go into the left subtree.
func splitPoint(n int) int {
	k := 1
	for k*2 < n {
		k *= 2
	}
	return k
}

// ErrBadLeafIndex is returned by Proof for an index outside the tree.
var ErrBadLeafIndex = errors.New("transfer: merkle leaf index out of range")

// Proof returns the sibling hashes on the path from leaf i up to the root,
// nearest first (RFC 6962's audit path). With the leaf's hash and the root,
// it's all a verifier needs: see VerifyMerkleProof.
func (t *MerkleTree) Proof(i int) ([]Hash, error) {
	if i < 0 || i >= len(t.leaves) {
		return nil, ErrBadLeafIndex
	}
	var proof []Hash
	start, end := 0, len(t.leaves)
	// Walk down from the root; each step records the subtree the leaf is
	// NOT in. Collected top-down, so reverse at the end (nearest first).
	for end-start > 1 {
		k := splitPoint(end - start)
		if i < start+k {
			proof = append(proof, t.subtree(start+k, end))
			end = start + k
		} else {
			proof = append(proof, t.subtree(start, start+k))
			start += k
		}
	}
	for a, b := 0, len(proof)-1; a < b; a, b = a+1, b-1 {
		proof[a], proof[b] = proof[b], proof[a]
	}
	return proof, nil
}

// VerifyMerkleProof reports whether leaf (a LeafHash) is leaf i of a tree
// of n leaves with the given root, using proof (from Proof). It
// recomputes the root from the leaf upwards and compares. This is RFC
// 9162's inclusion-proof check, which also rejects a proof of the wrong
// length or for the wrong position, so a peer can't reuse a genuine
// proof for a different piece.
//
// root and n must come from trusted data (the manifest, which the content
// key vouches for), never from the peer sending the piece: the check
// proves "leaf i under this root, in a tree of n leaves".
func VerifyMerkleProof(root Hash, i, n int, leaf Hash, proof []Hash) bool {
	if i < 0 || i >= n {
		return false
	}
	fn, sn := i, n-1 // the leaf's index, and the last index, at the current level
	r := leaf
	for _, p := range proof {
		if sn == 0 {
			return false // proof longer than the path to the root
		}
		if fn&1 == 1 || fn == sn {
			// We're a right child (or the last node, carried up alone):
			// the sibling goes on the left.
			r = nodeHash(p, r)
			if fn&1 == 0 {
				// The last node was carried up without a partner: climb
				// until it becomes a right child, matching the uneven split.
				for fn&1 == 0 && fn != 0 {
					fn >>= 1
					sn >>= 1
				}
			}
		} else {
			r = nodeHash(r, p) // we're a left child: the sibling goes on the right
		}
		fn >>= 1
		sn >>= 1
	}
	return sn == 0 && r == root
}
