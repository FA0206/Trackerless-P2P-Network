# 09 — Merkle trees: one hash for the whole file, a short proof per piece

> **Status:** step 1 of 4 is built: the tree, proofs and their
> verification, as pure functions with tests (`internal/transfer/merkle.go`).
> Transfers don't use it yet: the manifest still carries the flat list of
> piece hashes ([07](07-file-transfer-over-tcp.md)). Steps 2–4 (using it
> in transfers, a tamper demo, the diagram) come next; see the end.

## The problem it solves

Every shared file has a **manifest** ([07](07-file-transfer-over-tcp.md)):
its name, size, piece size, and the SHA-256 of **every** piece. The
content key is the hash of the manifest, so a downloader that trusts the
key can trust the manifest, and then check each piece against its entry.

That works, but the list grows with the file:

| File | Pieces (64 KiB each) | Flat hash list in the manifest |
|---|---|---|
| 1 MB | 16 | ~1 KB |
| 100 MB | 1,600 | ~100 KB |
| 1 GB | 16,384 | ~1 MB |

And a downloader has to fetch the **whole** list before it can check
**any** piece.

A **Merkle tree** fingerprints the whole file with **one 32-byte hash**,
the root, and still lets you check any single piece on its own. You need
the piece plus a short **proof**: a handful of hashes, about log₂ of the
piece count (4 for 16 pieces, 14 for 16,384). BitTorrent v2 works this
way, and so does Certificate Transparency, the public log of HTTPS
certificates.

## How the tree is built

Hash each piece (the **leaves**), then hash pairs of hashes together,
level by level, until one hash is left (the **root**). Here are 5 pieces,
with their real hashes, first 8 hex digits:

```
                          ROOT = 049a8eca
                         /               \
               H0123 = ed41edcf            \
              /                 \            \
      H01 = 22e764e3       H23 = 2dce2581     \
       /        \           /        \         \
  L0=f2ee2f60 L1=c69413ac L2=87060d6c L3=2357cf99 L4=65530b00
  "piece 0"   "piece 1"   "piece 2"   "piece 3"   "piece 4"
```

Three details, all from **RFC 6962** (Sec. 2.1), the standard behind
Certificate Transparency:

1. **Leaves and inner nodes are hashed differently:**
   `leaf = SHA-256(0x00 ‖ piece)` and `node = SHA-256(0x01 ‖ left ‖ right)`.
   Without the 0x00 / 0x01 prefix, someone could present an inner node's
   two children as if they were one piece, and it would hash to the same
   value (a "second preimage" attack).
2. **An uneven count is split, not padded.** With 5 pieces, the left side
   takes the largest power of two smaller than 5 (that's 4), and the
   right side takes the rest (piece 4 alone), which is why `L4` joins
   only at the very top. Bitcoin instead copies the last leaf to make
   pairs. That lets two different lists of leaves produce the same root
   (CVE-2012-2459), so we don't.
3. **An empty file** (0 pieces) has root SHA-256 of nothing.

## How a proof works

To show that a piece is genuine, a peer sends the piece **and its
proof**: the sibling hash at each level on the way up, nearest first.
For piece 2 that's `[L3, H01, L4]`. The receiver already trusts the root,
because it came in the manifest. It rebuilds the path:

```
step 1   H(0x00 ‖ piece 2)              = L2 = 87060d6c
step 2   H(0x01 ‖ L2 ‖ L3)              = 2dce2581   (= H23)
step 3   H(0x01 ‖ H01 ‖ that)           = ed41edcf   (= H0123)
step 4   H(0x01 ‖ that ‖ L4)            = 049a8eca   = ROOT ✓
```

Change **one byte** of piece 2 and step 1 gives a different hash, and so
does every step after it. A tampered piece 2 ends at `bdf9d8f4`, not
`049a8eca`, and is rejected. The proof can't be faked either: building
a different path to the same root would mean breaking SHA-256.

Which side each sibling goes on (left or right) follows from the
piece's **index** and the **number of pieces**. That's why the check
needs both, and why both must come from the trusted manifest, never from
the peer sending the piece. (For pieces on the left edge, a tree of n and
of n + 1 pieces can share the same path shape; a verifier that takes n
from the manifest isn't affected. RFC 9162 makes the same assumption.)

## The code

`internal/transfer/merkle.go` has no networking, like `bitfield.go` and
`picker.go`:

| Function | What it does |
|---|---|
| `LeafHash(piece)` | `SHA-256(0x00 ‖ piece)` |
| `NewMerkleTree(leaves)` | a tree over the leaf hashes, in piece order |
| `(*MerkleTree).Root()` | the root (`SHA-256("")` for 0 pieces) |
| `(*MerkleTree).Proof(i)` | the sibling hashes for piece i, nearest first |
| `VerifyMerkleProof(root, i, n, leaf, proof)` | recomputes the root from the leaf and the proof; true only if it matches |

`MerkleTree` remembers every subtree hash it computes, so a seeder
handing out proofs for all n pieces does O(n) hashing in total, not O(n)
per proof. `VerifyMerkleProof` is RFC 9162's inclusion-proof algorithm
(Sec. 2.1.3.2). It also rejects a proof that's too long, too short, or
for a different index.

## How we know it's right

`internal/transfer/merkle_test.go`:

- **Standard test vectors:** the roots of the RFC 6962 test data (8
  leaves, roots for 1 to 8 of them) match. A separate Python script,
  written independently, computes the same 8 roots.
- **Every proof verifies,** for every piece of every tree from 1 to 70
  pieces, and no proof is longer than the tree is deep.
- **Tampering is caught:** a forged piece, a genuine piece at the wrong
  index, any proof hash with one bit flipped, a truncated proof, a proof
  with an extra hash, and the wrong root all fail.
- **One changed piece changes the root.**
- **Leaves and inner nodes can't be confused** (the 0x00 / 0x01 prefixes).
- **The worked example above:** the root `049a8eca…` and the proof
  `[L3, H01, L4]` for piece 2 come straight from the code.

## What's next

1. ~~The tree, proofs, verification, tests~~ (this doc).
2. **Use it in transfers.**
   - The manifest carries the root instead of the list, so it stays
     small whatever the file size.
   - Every piece reply carries its proof, and the downloader checks it.
   - Partial seeders (who serve pieces before they have the whole file,
     [08](08-swarming.md)) keep the proof that came with each piece, so
     they can pass it on.
3. **A tamper demo:** a node that serves a corrupted piece, caught by the
   proof check, with the download finishing from other peers. Plus a
   manifest-size comparison, flat list vs root.
4. **A diagram of a real file's tree,** with one piece's proof path
   highlighted.
