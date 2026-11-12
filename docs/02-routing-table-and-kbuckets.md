# 2. The routing table and k-buckets

Prerequisite: [01-ids-and-xor-distance.md](01-ids-and-xor-distance.md).

## What it is

Every node keeps a **routing table**: its best current knowledge of which
other nodes exist and how to reach them (IP, UDP port, node ID). The
routing table is organized into **160 k-buckets**, one per possible XOR
distance "order of magnitude" from this node's own ID (paper Sec. 2.2).
Bucket `i` holds contacts whose XOR distance from this node falls in
`[2^i, 2^(i+1))` — equivalently, contacts that share exactly `159 - i` bits
of prefix with this node's own ID. Each bucket holds **at most `k`**
contacts (a system-wide replication parameter, 20 in the paper and in this
project's defaults).

Code: [`internal/kademlia/bucket.go`](../internal/kademlia/bucket.go) (one
bucket) and
[`internal/kademlia/routingtable.go`](../internal/kademlia/routingtable.go)
(the 160 of them together).

## Why 160 buckets, and why bucket by distance at all

A node needs to be able to find *any* other node by ID, but it obviously
can't remember every node in a large network. The trick (paper Sec. 2,
Figs. 1–2) is that a node doesn't need to know *everyone* — it needs to
know **at least one** contact in every "distance band" from itself. Given
that guarantee, a lookup can always make progress: from any node, you can
always ask a contact that's closer to the target than you are, and repeat.
Distance bands close to zero (i.e. nodes that are very "far" from you,
sharing almost no ID prefix with you) are numerous, so you only need one
contact there. Bands close to your own ID (nodes that share almost your
entire ID prefix) are rare, so you can afford to remember more of them —
but in practice there usually aren't many to remember anyway.

160 buckets, one per bit position, is simply "one band per unit of
precision this metric can express" — see `BucketIndex` in
[01](01-ids-and-xor-distance.md).

## SIMPLIFICATION we made here: no bucket splitting

The paper's routing table (Sec. 2.4) doesn't actually start with 160
preallocated buckets. It starts with **one** bucket covering the whole ID
space, and that bucket **splits into two** only once it fills up and the
split would separate contacts into a range containing the node's own ID
from one that doesn't (Fig. 4). This means a real Kademlia node's routing
table is usually much smaller than "160 buckets", because most of the
distance range is covered by one or two very coarse, un-split buckets.

**This project does not implement splitting.** `RoutingTable` in
`routingtable.go` allocates a fixed array of exactly 160 `Bucket`s up
front (`NewRoutingTable`), and `BucketIndex(self, other)` indexes directly
into it. This is a deliberate simplification, made for two reasons:

1. **It's much simpler to implement and verify correct.** The splitting
   logic (including the paper's extra wrinkle in Sec. 2.4 for
   pathologically unbalanced trees, where you may need to split buckets
   that *don't* contain your own ID) is one of the fiddlier parts of a
   real Kademlia implementation. Skipping it removes a whole class of
   off-by-one and edge-case bugs from a first pass.
2. **It costs nothing at this project's scale.** 160 mostly-empty `Bucket`
   structs (each just a slice header until something's inserted into it)
   is a trivial amount of memory for a demo of a handful to a few hundred
   nodes. The problem bucket-splitting solves — letting a node remember
   *more than k* contacts in the region nearest its own ID, which matters
   once a network has millions of nodes and that region could otherwise
   only ever hold k contacts total — doesn't come up here.

If you extend this project toward a much larger simulated network, this is
the first simplification you'd want to revisit.

## The update rule (the part that's easy to get subtly wrong)

Whenever a node receives *any* message from another node — a request or a
reply — it must update its routing table for the sender (paper Sec. 2.2).
The exact rule:

1. If the sender is already in the appropriate bucket, move it to the
   **tail** (most-recently-seen end).
2. Else, if the bucket isn't full, insert the sender at the tail.
3. Else (bucket full, sender not already present): **ping the
   least-recently-seen contact** (the head of the list). If it responds,
   move *it* to the tail and discard the new sender's info. If it doesn't
   respond, evict it and insert the new sender at the tail instead.

The key property this gives you: **a bucket never evicts a contact just
because a new one showed up.** Only an actual failed ping evicts anyone.
This is deliberate (paper Sec. 2.2, and Fig. 3's Gnutella uptime data): a
contact that's been online a long time is statistically more likely to
*stay* online than a brand-new, unverified one, so old-but-responsive
contacts are worth preferring over new-and-unverified ones. It also gives
some resistance to a basic flooding attack — you can't evict a node's
routing table by flooding it with new, fake contact announcements, because
new contacts never evict existing ones without a confirmed-dead ping
first.

### Why this is split across two steps in the code

Step 3 needs a network round trip (the ping) before it can decide what to
do, and `Bucket` itself does no networking — it's a pure data structure, which
is what makes it trivial to unit-test (see below). So `Bucket.Update`
doesn't evict anything itself. It returns one of two outcomes:

- `Seen` — done, nothing more to do (covers steps 1 and 2 above).
- `FullNeedsPing` plus the stale candidate contact — the bucket is full of
  *other* contacts, and the caller must ping that candidate and then call
  back in with the result.

The caller (`Node`, in [`node.go`](../internal/kademlia/node.go)) does
exactly that:

```go
outcome, stale := n.rt.Update(sender)
if outcome == FullNeedsPing {
    go n.resolveEviction(stale, sender) // ping, then KeepAlive or ReplaceStale
}
```

`resolveEviction` pings `stale` with a timeout; on success it calls
`Bucket.KeepAlive(stale)` (move to tail, discard the new contact), on
failure it calls `Bucket.ReplaceStale(stale, sender)` (evict `stale`,
insert `sender`). This runs in its own goroutine so that handling one
incoming packet never blocks on a network round trip to some unrelated
third node.

## Finding the k closest contacts to a target

`RoutingTable.Closest(target ID, count int) []Contact` is used in two
places: answering a `FIND_NODE` RPC (reply with the `k` contacts we know of
closest to the requested ID), and seeding our own lookups (start from the
contacts we already know of closest to where we're looking).

**SIMPLIFICATION**, again in the name of using the flat-array routing
table: rather than being clever about which bucket(s) to scan outward from
(which is what you'd want to do with a real, much larger routing table),
`Closest` just gathers every contact from all 160 buckets and sorts the
whole thing by XOR distance to the target. At this project's scale (at
most `160 * k` contacts, a few thousand even at `k = 20`), that's fast
enough, and it's a lot harder to get wrong than bucket-expansion logic.

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| One bucket: capacity, ordering, the update rule | `Bucket`, `Update`, `KeepAlive`, `ReplaceStale` | `bucket.go` |
| 160 buckets together, indexed by `BucketIndex` | `RoutingTable`, `bucketFor` | `routingtable.go` |
| Finding the k closest known contacts to a target | `RoutingTable.Closest` | `routingtable.go` |
| Applying the update rule + async ping-then-evict | `Node.touchContact`, `Node.resolveEviction` | `node.go` |

Tests: `internal/kademlia/bucket_test.go` covers every branch of the
update rule in isolation (insert until full, re-seeing moves to tail,
`KeepAlive` discards the new contact, `ReplaceStale` evicts, and —
explicitly — that a live node is never dropped just because new contacts
keep arriving). `routingtable_test.go` covers that a node never stores
itself, and that `Closest` returns contacts in the right order.

## What's next

[03-message-framing.md](03-message-framing.md) covers how two nodes
actually exchange these updates over the wire — the PING/FIND_NODE
messages and how they're framed over UDP.
