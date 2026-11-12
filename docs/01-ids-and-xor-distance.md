# 1. IDs and the XOR distance metric

Read this first — everything else in the system (routing table, lookup,
bootstrap) is built on top of this one idea.

## What it is

Every node in the network, and every key you might want to look up, gets a
**160-bit identifier**. We represent it as a 20-byte array (`ID`, in
[`internal/kademlia/id.go`](../internal/kademlia/id.go)). 160 bits is the
paper's choice because it's the output size of SHA-1, which is one easy way
to generate an ID (or to turn a piece of data into a lookup key) — see
paper Section 2.1.

To make routing decisions ("which node is closest to this ID?"), Kademlia
needs a notion of **distance** between two IDs. It defines:

```
distance(x, y) = x XOR y        (interpreted as a 160-bit integer)
```

This is implemented in `Xor(a, b ID) ID` in `id.go`.

## Why XOR, specifically

A distance metric for this purpose needs a few properties:

- **`distance(x, x) == 0`**, and `distance(x, y) > 0` whenever `x != y`.
  XOR of a number with itself is always zero, and XOR of two different
  numbers is never zero — trivially true for XOR.
- **Symmetry**: `distance(x, y) == distance(y, x)`. XOR is symmetric by
  construction. This matters more than it might sound: it means that if
  you're close to me, I'm equally close to you. Every lookup request a
  node receives comes from roughly the same distribution of distances as
  the requests it sends — so a node *learns about other nodes by serving
  their queries*, not just by making its own. (The paper contrasts this
  with Chord's clockwise-circle metric, Sec. 1: because Chord's notion of
  "distance" is directional, a Chord node's finger table can't be
  populated just from the traffic it happens to receive.)
- **Triangle inequality**: `distance(x,y) + distance(y,z) >= distance(x,z)`.
  This is what lets "closer" be transitive enough for a greedy routing
  algorithm (repeatedly move to a node that's closer to the target) to
  actually make progress and converge. XOR satisfies it; see the proof
  sketch in paper Sec. 2.1, and `TestXorTriangleInequality` in
  [`id_test.go`](../internal/kademlia/id_test.go), which checks it
  empirically against 1000 random triples.
- **Unidirectionality**: for any point `x` and distance `d > 0`, there is
  *exactly one* point `y` with `distance(x, y) == d` (flip exactly the
  bits that XOR with `x` to produce `d`). This means that two different
  nodes looking up the *same* key will tend to converge on the *same* path
  through the network, which is what makes caching along the lookup path
  effective (paper Sec. 2.1) — out of scope for this pass, but worth
  knowing why the metric was chosen with it in mind.

## Ordering: "closest" and "how many bits in common"

Because we also want to rank multiple nodes by distance to a target (e.g.
"which 20 nodes do I know of that are closest to this key?"), we need to
compare two distances. `Less(a, b ID) bool` in `id.go` treats an `ID` as a
160-bit big-endian unsigned integer and compares magnitude. Combined with
`Xor`, this gives us `Less(Xor(candidate, target), Xor(other, target))` —
"is `candidate` closer to `target` than `other` is?" — which is exactly the
comparison the routing table's `Closest` method and the lookup algorithm
sort by.

There's a second, equivalent way to think about XOR distance that the
paper leans on for intuition (Sec. 2.1, and the binary-tree picture in
Figs. 1–2): **the magnitude of `distance(x, y)` is determined by the
position of the highest bit on which `x` and `y` differ.** If `x` and `y`
share a 150-bit prefix and then differ, their XOR distance's highest set
bit is at position 9 (from the top), regardless of what the remaining
9 bits are. In the binary-tree picture, this is "the height of the
smallest subtree containing both `x` and `y`": more shared prefix bits
means a smaller (deeper, more specific) subtree contains both, means a
smaller distance.

This "position of the highest differing bit" is also exactly how we
decide which **k-bucket** a contact belongs in — see
[02-routing-table-and-kbuckets.md](02-routing-table-and-kbuckets.md).
`BucketIndex(self, other ID) int` in `id.go` computes it directly from the
XOR distance via `bitLen` (position of the most significant set bit), with
no separate "count matching prefix bits" logic needed — they're the same
calculation viewed two ways.

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| 160-bit ID type, hex (de)serialization | `ID`, `String`, `IDFromHex` | `id.go` |
| Deterministic ID from a seed (used by the demo and CLI's `-id-seed`) | `HashID` | `id.go` |
| XOR distance | `Xor` | `id.go` |
| Comparing distances / IDs | `Less` | `id.go` |
| Which k-bucket a contact belongs in | `BucketIndex` | `id.go` |

Tests: `internal/kademlia/id_test.go` covers the metric properties
(identity, symmetry, triangle inequality), the ordering, and bucket-index
edge cases (closest possible bit, farthest possible bit, and that
`BucketIndex(self, self)` panics rather than silently misbehaving, since a
node should never try to bucket itself).

## What's next

Once you're comfortable with "distance is XOR, closeness is a bit-prefix
match, and we can sort IDs by distance to a target," move on to
[02-routing-table-and-kbuckets.md](02-routing-table-and-kbuckets.md), which
is about *where* a node stores what it knows about other nodes, organized
by exactly this notion of distance.
