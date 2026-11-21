package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// The RFC 6962 test data used by Certificate Transparency implementations:
// 8 leaves, and the root of the tree over the first n of them.
var rfc6962Leaves = []string{
	"", "00", "10", "2021", "3031", "40414243",
	"5051525354555657", "606162636465666768696a6b6c6d6e6f",
}

var rfc6962Roots = []string{
	"6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d",
	"fac54203e7cc696cf0dfcb42c92a1d9dbaf70ad9e621f4bd8d98662f00e3c125",
	"aeb6bcfe274b70a14fb067a5e5578264db0fa9b51af5e0ba159158f329e06e77",
	"d37ee418976dd95753c1c73862b9398fa2a2cf9b4ff0fdfe8b30cd95209614b7",
	"4e3bbb1f7b478dcfe71fb631631519a3bca12c9aefca1612bfce4c13a86264d4",
	"76e67dadbcdf1e10e1b74ddc608abd2f98dfb16fbce75277b5232a127f2087ef",
	"ddb89be403809e325750d3d263cd78929c2942b7942a34b77e122c9594a74c8c",
	"5dc9da79a70659a9ad559cb701ded9a2ab9d823aad2f4960cfe370eff4604328",
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMerkleRootMatchesRFC6962Vectors(t *testing.T) {
	var leaves []Hash
	for n, data := range rfc6962Leaves {
		leaves = append(leaves, LeafHash(mustHex(t, data)))
		got := NewMerkleTree(leaves).Root()
		if hex.EncodeToString(got[:]) != rfc6962Roots[n] {
			t.Errorf("root of the first %d leaves: got %x, want %s", n+1, got, rfc6962Roots[n])
		}
	}
}

func TestEmptyTreeRootIsHashOfNothing(t *testing.T) {
	if got := NewMerkleTree(nil).Root(); got != sha256.Sum256(nil) {
		t.Errorf("empty tree root %x, want SHA-256(\"\")", got)
	}
}

// leavesFor returns n distinct leaf hashes, as if from n different pieces.
func leavesFor(n int) []Hash {
	leaves := make([]Hash, n)
	for i := range leaves {
		leaves[i] = LeafHash([]byte(fmt.Sprintf("piece %d", i)))
	}
	return leaves
}

// Every leaf of every tree size verifies, and the proof is as short as the
// tree is deep: at most ceil(log2 n) hashes.
func TestEveryProofVerifies(t *testing.T) {
	for n := 1; n <= 70; n++ {
		leaves := leavesFor(n)
		tree := NewMerkleTree(leaves)
		root := tree.Root()
		depth := 0
		for 1<<depth < n {
			depth++
		}
		for i := 0; i < n; i++ {
			proof, err := tree.Proof(i)
			if err != nil {
				t.Fatalf("n=%d i=%d: %v", n, i, err)
			}
			if len(proof) > depth {
				t.Errorf("n=%d i=%d: proof has %d hashes, the tree is only %d deep", n, i, len(proof), depth)
			}
			if !VerifyMerkleProof(root, i, n, leaves[i], proof) {
				t.Fatalf("n=%d i=%d: a genuine proof didn't verify", n, i)
			}
		}
	}
}

// The point of the tree: change anything and verification fails.
func TestTamperingIsCaught(t *testing.T) {
	for _, n := range []int{2, 3, 5, 8, 13, 16} {
		leaves := leavesFor(n)
		tree := NewMerkleTree(leaves)
		root := tree.Root()
		for i := 0; i < n; i++ {
			proof, _ := tree.Proof(i)
			name := fmt.Sprintf("n=%d i=%d", n, i)

			if VerifyMerkleProof(root, i, n, LeafHash([]byte("a forged piece")), proof) {
				t.Errorf("%s: a forged piece verified", name)
			}
			if VerifyMerkleProof(root, (i+1)%n, n, leaves[i], proof) {
				t.Errorf("%s: a genuine piece verified at the wrong index", name)
			}
			// (Not tested: the wrong tree size. For a leaf on the left edge,
			// sizes n and n+1 give the same path shape, so a genuine proof
			// still lands on the genuine root. That's fine because the
			// verifier never takes n from a peer: root and n both come from
			// the manifest, which the content key vouches for. RFC 9162
			// makes the same assumption.)
			for j := range proof {
				bad := append([]Hash(nil), proof...)
				bad[j][0] ^= 0x01
				if VerifyMerkleProof(root, i, n, leaves[i], bad) {
					t.Errorf("%s: a proof with hash %d flipped verified", name, j)
				}
			}
			if len(proof) > 0 && VerifyMerkleProof(root, i, n, leaves[i], proof[:len(proof)-1]) {
				t.Errorf("%s: a truncated proof verified", name)
			}
			if VerifyMerkleProof(root, i, n, leaves[i], append(append([]Hash(nil), proof...), root)) {
				t.Errorf("%s: a proof with an extra hash verified", name)
			}
			otherRoot := root
			otherRoot[31] ^= 0x01
			if VerifyMerkleProof(otherRoot, i, n, leaves[i], proof) {
				t.Errorf("%s: verified against a different root", name)
			}
		}
	}
}

// Changing one piece of the file changes the root: the root really is a
// fingerprint of every piece.
func TestOnePieceChangesTheRoot(t *testing.T) {
	leaves := leavesFor(16)
	before := NewMerkleTree(leaves).Root()
	leaves[11] = LeafHash([]byte("piece 11, one byte different"))
	if NewMerkleTree(leaves).Root() == before {
		t.Error("changing piece 11 didn't change the root")
	}
}

// A leaf hash and an inner node hash never coincide for the same bytes,
// thanks to the 0x00 / 0x01 prefixes, so an inner node can't be passed off
// as a piece.
func TestLeafAndNodeHashesAreDomainSeparated(t *testing.T) {
	a, b := LeafHash([]byte("a")), LeafHash([]byte("b"))
	inner := nodeHash(a, b)
	asLeaf := LeafHash(append(append([]byte(nil), a[:]...), b[:]...))
	if inner == asLeaf {
		t.Error("an inner node's hash equals the leaf hash of its children's bytes")
	}
}

// The worked example in docs/11-merkle-trees.md: 5 pieces "piece 0" ..
// "piece 4". The doc's hashes were computed separately (Python hashlib);
// this keeps doc and code in agreement.
func TestWorkedExampleFromTheDoc(t *testing.T) {
	leaves := leavesFor(5)
	tree := NewMerkleTree(leaves)
	root := tree.Root()
	if got := hex.EncodeToString(root[:4]); got != "049a8eca" {
		t.Errorf("root starts %s, the doc says 049a8eca", got)
	}
	proof, _ := tree.Proof(2)
	h01 := nodeHash(leaves[0], leaves[1])
	if len(proof) != 3 || proof[0] != leaves[3] || proof[1] != h01 || proof[2] != leaves[4] {
		t.Errorf("proof for piece 2 isn't [L3, H01, L4] as the doc shows")
	}
	for _, c := range []struct {
		h    Hash
		want string
	}{{leaves[3], "2357cf99"}, {h01, "22e764e3"}, {leaves[4], "65530b00"}} {
		if got := hex.EncodeToString(c.h[:4]); got != c.want {
			t.Errorf("hash starts %s, the doc says %s", got, c.want)
		}
	}
}

// The TCP server serves pieces from several goroutines at once, all asking
// one shared file's tree for proofs. NewMerkleTree computes every inner
// hash up front, so Proof only reads. Run with -race.
func TestProofsAreSafeConcurrently(t *testing.T) {
	tree := NewMerkleTree(leavesFor(37))
	root := tree.Root()
	done := make(chan bool)
	for g := 0; g < 8; g++ {
		go func() {
			ok := true
			for i := 0; i < 37; i++ {
				proof, err := tree.Proof(i)
				ok = ok && err == nil && VerifyMerkleProof(root, i, 37, tree.leaves[i], proof)
			}
			done <- ok
		}()
	}
	for g := 0; g < 8; g++ {
		if !<-done {
			t.Error("a proof made concurrently didn't verify")
		}
	}
}

func TestProofRejectsBadIndex(t *testing.T) {
	tree := NewMerkleTree(leavesFor(4))
	for _, i := range []int{-1, 4} {
		if _, err := tree.Proof(i); err != ErrBadLeafIndex {
			t.Errorf("Proof(%d): got %v, want ErrBadLeafIndex", i, err)
		}
	}
	if VerifyMerkleProof(tree.Root(), 0, 0, LeafHash(nil), nil) {
		t.Error("verified against a tree of size 0")
	}
}
