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

### The non-simplified way: visit buckets in XOR order

The paper only hints at it (Sec. 2.3, on FIND_NODE replies): "These
triples can come from a single k-bucket, or they may come from multiple
k-buckets if the closest k-bucket is not full." The trick is that the
**buckets themselves can be ranked by distance to the target**, without
looking at the contacts inside them.

Let `s` be our ID, `t` the target, and `i = BucketIndex(s, t)`, the
highest bit where `s` and `t` differ. Take a contact `c` that lives in
bucket `j`. Its distance to the target, `c ⊕ t`, works out like this:

| Bucket of `c` | Why | Distance `c ⊕ t` lies in |
|---|---|---|
| `j = i` (the target's own bucket) | `c` and `t` both differ from `s` at bit `i`, so they agree there and above | `[0, 2^i)` — the closest group |
| `j < i` (any lower bucket) | `c` agrees with `s` at bit `i`, `t` doesn't | `[2^i, 2^(i+1))` — all lower buckets share one range, so they must be sorted *together* |
| `j > i` | `c` differs from `s` at bit `j`, `t` doesn't | `[2^j, 2^(j+1))` — each higher bucket is its own range, farther as `j` grows |

So the exact visiting order is:

1. bucket `i`;
2. then all buckets below `i`, merged and sorted;
3. then buckets `i+1, i+2, …` in increasing order.

Stop as soon as you have `count` contacts. Only the group you stop in
needs sorting. (If `t = s`, as in a self-lookup, every contact in bucket
`j` is at distance `[2^j, 2^(j+1))`, so you simply walk buckets
`0, 1, 2, …`.)

Note that "expand outward, `i±1`, `i±2`, …" — the obvious guess — is
**not** this order. A contact in bucket `i−5` is closer to the target
than any contact in bucket `i+1`.

libtorrent's `routing_table::find_node` walks its buckets this way:
first from the target's bucket toward the buckets nearer itself, then
back out toward the farthest. (Checked in libtorrent's
`src/kademlia/routing_table.cpp`, branch `RC_2_0`. libtorrent numbers
buckets by shared-prefix length, the reverse of our `BucketIndex`, so
its "toward higher indexes" is our "toward lower".)

**Cost.** Let `C` be the total number of contacts in the table:

| | This project's `Closest` | Bucket-ordered walk |
|---|---|---|
| Contacts examined | all `C` | only those in the buckets actually taken (≈ `count`, plus one partially used group) |
| Sorting | `O(C log C)` | `O(m log m)`, where `m` is the size of the groups taken (≈ `k`) |
| Bucket headers scanned | 160 | up to 160 (it's a flat array either way) |

A node in a network of `N` nodes holds roughly `k · log₂(N/k)`
contacts, since only the top ~`log₂ N` buckets are non-empty (see the
real tables in DRY-RUN part 1). Even for a million nodes that's a few
hundred contacts, so the sort costs microseconds. The walk matters when
a node answers thousands of `FIND_NODE`s per second, not for
correctness.

### Bucket splitting (paper Sec. 2.4) and what it changes

The paper's table is a binary tree. It starts as a single bucket. A full
bucket **splits only if its range contains our own ID**; a full bucket
that doesn't simply drops newcomers (after the ping check). Each split
peels off one "far half". The end result has about `log₂(N/k)` leaves,
which is the same shape as the non-empty part of our 160-slot array. So
for the basic rule, the flat array and the tree hold essentially the
same contacts, and the walk above works on either.

The splitting tree can do two things the flat array can't:

- **The relaxed rule (Sec. 2.4, Fig. 5).** Also split buckets *not*
  containing our ID, until we know every contact in the smallest subtree
  around us with at least `k` nodes. In an unbalanced tree this lets a
  node hold more than `k` contacts in one "distance band". Our array is
  capped at `k` per index.
- **Accelerated lookups (Sec. 4.2).** Split far buckets too, up to
  `b − 1` extra levels. That gives about `2^b · log_{2^b} N` buckets, and
  cuts lookups to about `log_{2^b} N` hops. The paper's implementation
  used `b = 5`.

Neither changes how `Closest` should walk the buckets. Both change *how
many* contacts are worth walking. Implementing the tree means replacing
`RoutingTable`'s array with leaves keyed by prefix, and making `Update`
split. `Bucket` itself (the update rule) is unchanged.

## Stale contacts: a node that stops answering

The update rule above only ever reacts to *incoming* messages. A contact
that simply dies sends nothing, so it would stay in its bucket forever,
and every lookup would keep paying an RPC timeout to it (edge case B1,
[EDGE-CASES.md](EDGE-CASES.md#b1--half-the-network-dies-at-once)).

The paper's answer (Sec. 4.1): "When a contact fails to respond to 5
RPCs in a row, it is considered stale. If a k-bucket is not full or its
replacement cache is empty, Kademlia merely flags stale contacts rather
than remove them." This project does that:

- **Counting** (`stale.go`, `staleTracker`): every RPC that times out
  counts one failure against the node it went to, in the routing table
  or not. After `--stale-after` failures in a row (default 5) it's
  **stale**. Any message from it clears the count. It has answered, so
  it's back.
- **Flagged, not removed:** a stale contact keeps its bucket slot.
  `Closest` leaves it out, so our lookups don't start from it and our
  `FIND_NODE` replies don't hand it out. The console's `table` and
  `/api/table` still show it, marked stale. Lookups also skip stale
  nodes that *other* nodes mention ([05](05-iterative-parallel-lookup.md)).
- **Replaced without a ping:** when a new contact arrives for a full
  bucket that holds a stale contact, `Bucket.Update` replaces the oldest
  stale one on the spot (`ReplacedStale`). The failures already showed
  it doesn't answer. Otherwise the usual ping-then-evict rule applies.
- **Not voided:** if this node's own network goes down, every contact
  fails and goes stale. A lookup with nothing fresh falls back to the
  stale contacts (`ClosestIncludingStale`), and the first reply clears
  the flag. That's the paper's reason for flagging rather than removing.

`bucket.go` and `stale.go` do no network I/O. `Node` reports failures
from `sendAndWait` and successes from `touchContact`, and the bucket only
reads the tracker (`isStale`).

**SIMPLIFICATION:** the paper also keeps a **replacement cache** per
bucket (contacts that arrived while it was full, ready to step in) and
backs off exponentially before retrying an unresponsive contact. We have
neither. A stale contact is replaced only when a new contact for its
bucket next messages us, and lookups simply skip it until it speaks.
Neither blocks the end goal. A replacement cache would refill a bucket
faster after a mass death; bucket refresh (planned) does that too.

The threshold is a trade-off. 5 tolerates a few lost UDP packets, the
paper's concern on the Internet, but it takes several lookups to learn
of a death. On a loss-free LAN `--stale-after 1` learns at once. B1
measures both ([FIXES.md](FIXES.md#3-stale-contacts-b1-learning)).

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| One bucket: capacity, ordering, the update rule | `Bucket`, `Update`, `KeepAlive`, `ReplaceStale`, `Fresh` | `bucket.go` |
| 160 buckets together, indexed by `BucketIndex` | `RoutingTable`, `bucketFor` | `routingtable.go` |
| Finding the k closest known contacts to a target | `RoutingTable.Closest` (fresh only), `ClosestIncludingStale` | `routingtable.go` |
| Applying the update rule + async ping-then-evict | `Node.touchContact`, `Node.resolveEviction` | `node.go` |
| Failed RPCs in a row, stale nodes | `staleTracker`; `Node.noteFailure`, `Node.IsStale` | `stale.go`, `node.go` |

Tests: `internal/kademlia/bucket_test.go` covers every branch of the
update rule in isolation (insert until full, re-seeing moves to tail,
`KeepAlive` discards the new contact, `ReplaceStale` evicts, and —
explicitly — that a live node is never dropped just because new contacts
keep arriving). `routingtable_test.go` covers that a node never stores
itself, and that `Closest` returns contacts in the right order.
`stale_test.go` covers stale contacts: the threshold, the reset on a
message, `Fresh`/`Closest` leaving them out, replacement without a ping,
repeated lookups after deaths reaching 0 failed RPCs, and the fallback
when every contact is stale.

## What's next

[03-message-framing.md](03-message-framing.md) covers how two nodes
actually exchange these updates over the wire — the PING/FIND_NODE
messages and how they're framed over UDP.
