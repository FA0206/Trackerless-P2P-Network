# 09 — Merkle trees: one hash for the whole file, a short proof per piece

> **Status:** all 4 steps are built:
> 1. the tree, proofs and verification (`internal/transfer/merkle.go`);
> 2. transfers using them: the manifest carries the root, and every piece
>    travels with its proof;
> 3. a demo, `scripts/demo-merkle.sh`, where a dishonest provider is
>    caught and the manifest stays the same size from 1 to 2,048 pieces;
> 4. a diagram of a real file's tree (see the end).

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

## How file transfers use it

**Sharing** (`BuildManifest`, `Peer.Share`): the sharer hashes each
piece into a leaf, builds the tree, and puts the root in the manifest.
The manifest is now name, size, piece size and root:

```json
{"name":"original.bin","size":1048576,"piece_size":65536,"merkle_root":"e46caffd69ce0a25…"}
```

That's **138 bytes** for a 1 MiB, 16-piece file, against 1,146 bytes
with the old flat list, and it stays about that size for any file. The
content key is still the hash of the manifest, so it now vouches for the
root. The sharer keeps the tree in its library (`addComplete`), to make
proofs.

**Serving a piece** (`server.go`, `Library.readPiece`): the reply's
header carries the proof as hex hashes, then the piece follows:

```
server → client   frame: {"ok":true,"proof":["…","…","…","…"]}
server → client   frame: <65536 raw bytes of the piece>
```

**Checking it** (`download.go` → `Manifest.VerifyPiece(i, data, proof)`):
the downloader takes the index and the piece count from the manifest
it already verified, never from the sender. A bad piece, a bad proof, or
a good piece sent for the wrong index all fail. The provider is then
dropped, and the piece is fetched from another one. A real run shows
each piece with a 4-hash proof (16 pieces → 4 levels):

```
msg="piece received" index=10 of=16 from=127.0.0.1:9003 bytes=65536 have=1 inflight=1 proof=4
```

**Passing pieces on** (partial seeding, [08](08-swarming.md)). This is the
one subtle part. A downloader serves pieces before it has the whole
file, so it **can't build the tree**: it doesn't have every leaf. So it
keeps the proof that came with each piece (`Library.markHave(key, i,
proof)`) and sends that same proof to whoever asks for that piece. The
proof is genuine (it was just verified), and it doesn't depend on who
sends it. A test checks that a downloader's proofs equal the seeder's,
byte for byte.

**Concurrency:** the server answers requests from several goroutines,
all asking one tree for proofs. `NewMerkleTree` computes every inner
hash up front, so `Proof` only reads (`TestProofsAreSafeConcurrently`,
under `-race`).

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
- **In transfers** (`manifest_test.go`, `transfer_test.go`):
  - a flipped byte, a piece sent with another piece's genuine proof, a
    truncated piece and a missing proof are all rejected;
  - the manifest's size doesn't grow with the number of pieces;
  - a provider serving corrupted pieces *with the genuine proofs* is
    caught and dropped;
  - a downloader serves exactly the seeder's proofs;
  - leechers finish from each other after the seeder dies, so every
    piece they pass on carries a valid stored proof. All existing
    demos and edge cases pass unchanged.

## What's next

1. ~~The tree, proofs, verification, tests.~~
2. ~~Use it in transfers: the root in the manifest, a proof with every
   piece, partial seeders passing proofs on.~~
3. ~~A tamper demo and a manifest-size comparison~~ (below).
4. ~~A diagram of a real file's tree~~ (below).

## The diagram

An interactive diagram (an HTML page, made for slides) draws the real
tree of `merkle-demo.bin`, a reproducible 1 MiB file:
- piece `i` (0–15) is the line `Piece NN of the Merkle tree demo file.\n`
  (NN = the two-digit index), repeated and cut to 65,536 bytes;
- the file's SHA-256 is `688da393…c951223`.

A node shared it and logged:

```
msg="share complete" key=1231940f875eee8614112eb600523231578dd1b0 name=merkle-demo.bin size=1048576 pieces=16 merkle_root=778798a830691588daaac7288b91c9024f763886f41498b94ca61f1fbfa3460e manifest_bytes=141
```

All 31 hashes in the diagram were recomputed independently (Python
`hashlib`) from the file's bytes, and the root matched the node's
exactly. The page highlights piece 10's path to the root and its 4 proof
hashes:

| Proof hash | Level | Sibling | Hash |
|---|---|---|---|
| 1 | leaves | piece 11 | `ccce1c7e…` |
| 2 | 1 | node 4 | `a5dffabe…` |
| 3 | 2 | node 3 | `42be902a…` |
| 4 | 3 | node 0 | `c00a0bf1…` |

It also lists the verification steps, and selecting any piece shows that
piece's proof instead.

## Seeing it work: `scripts/demo-merkle.sh`

```sh
bash scripts/demo-merkle.sh      # env: BASE_PORT=9600; prints PASS
```

**Part 1, a dishonest provider.**
- node1 shares a 1 MiB file (16 pieces).
- node2 downloads a genuine copy and becomes a provider too, but it runs
  with the DEBUG ONLY `--debug-corrupt-pieces`: it flips one byte of
  every piece it serves and sends the **genuine** proof. Only the proof
  check can catch it.
- node3 then downloads with both providers available. It asks both at
  once, so node2 always gets a request.

Real output:

```
  node1 shared original.bin: 16 pieces, key fb71d1ab…
    merkle_root=4c8df61ff76dbc18f291bea949158c5756ace5f01c39bcff1f07221ca1f28d64
    manifest_bytes=138
  node2 downloaded a genuine copy: fetched "original.bin" (1048576 bytes, 16 pieces) in 15ms

  node3 downloads, with node1 and node2 both available:
    fetched "original.bin" (1048576 bytes, 16 pieces) in 11ms
      16 piece(s) from 127.0.0.1:9601
      provider 127.0.0.1:9602 failed and was skipped

  What node2 did, and what node3 saw (logs):
    [node2] level=WARN msg="DEBUG: serving a corrupted piece" index=8
    [node3] level=WARN msg="provider failed, trying the next one" provider=127.0.0.1:9602 error="transfer: piece 8 failed its Merkle proof check"
    [node3] level=INFO msg="piece received" index=13 of=16 from=127.0.0.1:9601 bytes=65536 have=1 inflight=0 proof=4
    [node3] … 16 pieces received in all, every one with its proof checked

  as predicted: node3 rejected node2's piece: it failed its Merkle proof check
  as predicted: node3 accepted 0 pieces from node2
  as predicted: node3's copy is byte-identical to the original
```

Over 5 runs, the corrupted piece node3 caught was 8, 6, 6, 4 and 12. The
downloader picks pieces in random rarest-first order, so the check isn't
tied to one piece.

**Part 2, manifest size.** node1 shares files of 1 to 2,048 pieces. The
"manifest now" column is the real size, from the `share complete` log
line. The "old flat list" column is what the old format (a list of every
piece's SHA-256) would have been, computed exactly from its JSON layout.
The formula reproduces the 1,146 bytes the old code really produced for a
16-piece `original.bin`.

```
  file                      pieces    manifest now   old flat list (computed)
  f1.bin (64 KiB)                1           130 B                      133 B
  f16.bin (1024 KiB)            16           133 B                     1141 B
  f256.bin (16384 KiB)         256           135 B                    17223 B
  f2048.bin (131072 KiB)      2048           137 B                   137289 B
```

From 1 to 2,048 pieces the manifest grew by 7 bytes, all from the longer
file name and size. The flat list would have grown by about 135 KB, and
by about 1 MB for a 1 GB file.

`TestDebugCorruptPiecesIsCaughtByTheProofCheck` (`transfer_test.go`) is
Part 1 as a Go test.
