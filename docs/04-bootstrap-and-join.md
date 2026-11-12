# 4. Bootstrap and join

Prerequisite: [03-message-framing.md](03-message-framing.md). This doc
also leans on ideas from [05](05-iterative-parallel-lookup.md) (the lookup
procedure) — read that one alongside this if anything here is unclear; the
two are tightly coupled, since bootstrap *is* a lookup.

## What it is

A new node doesn't start with an empty routing table and no way to fill
it — it needs **one** existing contact to get going. From that single
contact, it needs to end up with a populated routing table and be
discoverable by the rest of the network. The paper's recipe (Sec. 2.3,
"To join the network..."):

> To join the network, a node `u` must have a contact to an already
> participating node `w`. `u` inserts `w` into the appropriate k-bucket.
> `u` then performs a node lookup for its own node ID.

That's it — the key insight is that **a lookup for your own ID is also,
as a side effect, exactly the process that populates your routing table.**
You don't need a separate "bootstrap protocol" distinct from the lookup
machinery you already need for everything else.

## Why looking yourself up populates your table — and announces you

Walk through what happens when node `u` calls `Lookup(u.ID)`, seeded with
one contact `w`:

1. `u` asks `w` (via `FIND_NODE`) for the contacts closest to `u.ID`.
2. `w` replies with whatever it knows. `u` adds those to its own
   candidate list and keeps asking the closest ones it hasn't asked yet
   (this is the general lookup procedure — see
   [05-iterative-parallel-lookup.md](05-iterative-parallel-lookup.md)).
3. Every one of those `FIND_NODE` requests `u` sends carries `u`'s own ID
   in the envelope (see [03](03-message-framing.md)). So every node `u`
   talks to during this lookup learns about `u` and records it in *their*
   routing table, via the same "every message updates the sender's bucket"
   rule from [02](02-routing-table-and-kbuckets.md). This is how `u`
   becomes discoverable by the rest of the network, without any separate
   "announce myself" message.
4. By the time the lookup converges, `u` has directly exchanged messages
   with (roughly) `log(n)` nodes spread across the distance spectrum from
   itself — which is exactly the shape a useful routing table needs: one
   or more contacts in each relevant distance band. Looking up your *own*
   ID specifically (rather than some arbitrary ID) is what guarantees the
   discovered contacts are well-distributed relative to *you*, since the
   lookup's whole process is "get closer and closer to the target," and
   the target here is you.

In this codebase, this is `Node.Bootstrap` in
[`node.go`](../internal/kademlia/node.go):

```go
func (n *Node) Bootstrap(ctx context.Context, seed Contact) (*LookupResult, error) {
    return n.lookup(ctx, n.ID, []Contact{seed})
}
```

## A subtlety: the seed contact's ID isn't known yet

When you run `node --bootstrap 127.0.0.1:9000`, the CLI only has a
`host:port` — it has no idea what node ID is actually listening there
(that's rather the point of asking). So `parseContact` in
[`cmd/node/main.go`](../cmd/node/main.go) builds a `Contact` with a
**placeholder zero ID** and the real address.

It would be a real bug to insert that placeholder straight into the
routing table (`n.rt.Update(seed)`) before we've confirmed the real ID:
that permanently corrupts one bucket slot with a real, reachable address
filed under the wrong ID. This project avoids that by passing the seed
contact only as a *lookup candidate*, never writing it to the routing
table directly. `Lookup`'s internal implementation (`lookup`, in
[`lookup.go`](../internal/kademlia/lookup.go)) takes an `extraSeeds`
parameter for exactly this: candidates to start the search from that
aren't necessarily already in the routing table.

The real ID gets recorded correctly anyway, with no extra code needed for
it: the very first `FIND_NODE` reply from the seed arrives as a normal
incoming packet, whose envelope carries the seed's *real* `SenderID`. The
ordinary receive path (`Node.handleMessage` → `touchContact`) records that
correctly, independent of whatever placeholder ID the lookup's internal
bookkeeping was using to track that candidate. The placeholder only ever
lives in the lookup's local, throwaway candidate map — it never touches
the persistent routing table. See the comment on `Bootstrap` in `node.go`
for the short version of this.

(One minor, accepted loose end from this: if some *other* contact in the
same lookup also happens to report the seed's real ID back to us before
the seed's own reply arrives, the seed could briefly appear twice in the
lookup's candidate list — once under the placeholder ID, once under its
real one. This doesn't corrupt anything persistent and doesn't come up at
this project's demo scale; it would be worth tidying up if you extend the
lookup logic significantly.)

## SIMPLIFICATION we made here: no extra post-join bucket refresh

The paper's full join procedure has one more step after the self-lookup
(Sec. 2.3): "`u` refreshes all k-buckets further away than its closest
neighbor." The idea is that a self-lookup alone only reaches nodes roughly
*along the path toward you* — it doesn't necessarily populate buckets for
distance ranges that happen to be sparse or empty from your own
perspective but aren't actually empty in the network. The extra refresh
pass (picking a random ID in each such bucket's range and doing a lookup
for it) fills in those gaps.

This project skips that extra pass. For a handful to a few dozen nodes
(this project's target scale), the self-lookup alone reaches essentially
everyone anyway, so the gap this refresh exists to close rarely shows up
in practice here. If you scale this up significantly, this is the next
piece of the paper worth implementing — see the README's "what's next".

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| Join entry point | `Node.Bootstrap` | `node.go` |
| Lookup with extra starting candidates (what makes bootstrap safe before the seed's real ID is known) | `Node.lookup` (unexported, called by both `Lookup` and `Bootstrap`) | `lookup.go` |
| CLI flag wiring (`--bootstrap host:port`) | `run`, `parseContact` | `cmd/node/main.go` |

Tested end-to-end (not as an isolated unit, since its entire job is to
orchestrate the networked lookup) by
`TestEndToEndBootstrapAndLookup` in
`internal/kademlia/integration_test.go`: five real nodes, each bootstrapped
against the previous one in a chain, then a lookup from the first node
confirming it found all the others.

## What's next

[05-iterative-parallel-lookup.md](05-iterative-parallel-lookup.md): the
actual lookup algorithm that both `Bootstrap` and ordinary "find the k
closest nodes to X" calls are built on.
