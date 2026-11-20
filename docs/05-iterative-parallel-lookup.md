# 5. Iterative, parallel (α-concurrent) lookup

Prerequisite: everything before this one. This is where it all comes
together: distance ([01](01-ids-and-xor-distance.md)), the routing table
as a starting point ([02](02-routing-table-and-kbuckets.md)),
`FIND_NODE` as the wire operation ([03](03-message-framing.md)), and
bootstrap as "a lookup for your own ID"
([04](04-bootstrap-and-join.md)).

## What it is

The paper calls this "the most important procedure a Kademlia participant
must perform" (Sec. 2.3): given a target ID, find the `k` nodes in the
network closest to it. Every higher-level operation (join, and — out of
scope here but worth knowing — `STORE`/`FIND_VALUE`) is built on top of
this one procedure.

Code: `Node.Lookup` / the unexported `Node.lookup` in
[`internal/kademlia/lookup.go`](../internal/kademlia/lookup.go).

## The algorithm, step by step

Starting state: a **candidate list**, seeded from the `k` closest contacts
this node already knows of (from its own routing table —
`RoutingTable.Closest`). For a node that's just joining, that's the one
bootstrap node it PINGed first (see [04](04-bootstrap-and-join.md)).
Every candidate starts unqueried.

Then, repeatedly:

1. From the candidate list, take up to **α ("alpha")** candidates that are
   closest to the target *and haven't been queried yet*. α is a
   concurrency parameter (paper Sec. 2.3; this project defaults it to 3,
   same as the paper's example) — it controls how many `FIND_NODE` RPCs
   are in flight at once during one round, independent of `k`.
2. Send `FIND_NODE` to all of them **in parallel**, mark each as queried
   immediately (so a slow reply doesn't cause it to be re-queried next
   round), and wait for the round to finish.
3. For every contact any of those replies returned, add it to the
   candidate list if it's new. A candidate that **didn't answer** (RPC
   timeout: a dead node) is removed from the list for the rest of this
   lookup, and isn't re-added if a later reply names it again.
4. Re-sort the candidate list by distance to the target. Only its first
   `k` entries, the **k closest live candidates**, are ever queried; the
   rest are spares.
5. If every one of those `k` closest candidates has been queried and has
   answered, stop. Otherwise, go back to step 1.

The list starts with **every contact in this node's routing table**,
nearest first. With nobody dead, only the nearest `k` are ever looked at,
so the lookup asks exactly the same nodes it would with a list of `k`.
The spares matter when nodes don't answer: each dead candidate drops out,
and the next-closest live one moves up into the `k` closest and gets
asked.

The stopping condition is the paper's (Sec. 2.3): "the lookup terminates
when the initiator has queried and gotten responses from the k closest
nodes it has seen", where nodes that fail to respond "are removed from
consideration until and unless they do respond". So a dead node along
the path costs one RPC timeout, never a wrong answer: the result is the
`k` closest nodes that answered, and has fewer than `k` only when fewer
than `k` live nodes could be reached at all. (The paper goes further,
with *no* timeout delay at all; see the synchronous-rounds simplification
below.)

**Before this was fixed**, a failed candidate kept its slot among the `k`
closest, and the list only ever started from the `k` nearest contacts. So
a lookup next to one dead node returned `k-1` nodes, and one whose `k`
nearest contacts were all dead returned none, while live nodes were known
([EDGE-CASES.md](EDGE-CASES.md), B1 and B2). Worse, `Store` then believed
it was itself among the closest (`selfAmongClosest`: "fewer than `k`
found") and kept a copy even when it was far from the key ([06](06-store-and-find-value.md)).
`TestLookupSkipsADeadNodeNearTheKey` and
`TestLookupFindsLiveNodesWhenAllNearestAreDead` (`deadnodes_test.go`) pin
the fix: both fail on the old code with 2 and 0 contacts.

Why this converges in `O(log n)` rounds rather than needing some fixed
iteration count: each round that finds anything closer than what's
already known effectively halves the remaining distance to the target (in
the binary-tree picture from [01](01-ids-and-xor-distance.md), you're
descending one more level of the tree each time) — see the paper's proof
sketch, Sec. 3.

## What α actually buys you (vs. just asking one node at a time)

If α were 1, this degenerates into something like Chord's lookup: ask the
single best-known node, wait for its reply, ask the new best-known node,
repeat. The paper is explicit about why α > 1 is better (Sec. 2.3,
abstract): with several candidates equally good distance-wise, Kademlia
can fire off requests to more than one of them *at once*. In the paper's
version, the lookup keeps going as replies arrive ("this recursion can
begin before all α of the previous RPCs have returned"), so a dead
contact never holds up the others.

## SIMPLIFICATION we made here: rounds are synchronous

This implementation has a **round barrier**. Each round sends up to α
RPCs in parallel, then waits for *all* of them — replies or timeouts —
before choosing the next batch (the `for i := 0; i < len(batch); i++`
loop in `lookup.go`). Consequences:

- α still helps: the round costs the slowest of its RPCs, not their sum.
- A dead contact costs a full `rpcTimeout` for its round, even if the
  other two replied in a millisecond.

This was chosen because "collect a round, then merge" keeps all candidate
bookkeeping in one goroutine and is easy to reason about and log. It's
the first thing to change before measuring lookup latency or recovery
time under churn. A barrier-free version would start a replacement RPC
whenever any reply arrives, keeping up to α in flight at all times.

## Round and RPC accounting (what the demo reports)

`LookupResult` (`lookup.go`) tracks exactly what `scripts/demo.sh` prints:

```go
type LookupResult struct {
    Target     ID
    Closest    []Contact // up to k contacts, nearest-to-target first
    Rounds     int       // number of parallel alpha-wide batches issued
    RPCsSent   int
    RPCsFailed int
}
```

`Rounds` increments once per pass through the loop above (step 1–5);
`RPCsSent`/`RPCsFailed` count individual `FIND_NODE` calls, which is why
`RPCsSent` is typically a small multiple of `Rounds` (up to α per round,
fewer in the last round once candidates run out).

## SIMPLIFICATION we made here: always query exactly the k closest once each

The paper describes one extra early-exit heuristic (Sec. 2.3): "If a round
of `FIND_NODE`s fails to return a node any closer than the closest already
seen, the initiator resends the `FIND_NODE` to all of the k closest nodes
it has not already queried" — implying that *if* a round **does** improve
on the closest-seen distance, the lookup can keep narrowing with fresh
candidates rather than immediately committing to exhausting the full `k`
closest list.

This implementation doesn't special-case that: it always queries every one
of the (up to) `k` closest candidates exactly once, whether or not a given
round improved on the best distance seen. The final answer is identical
either way (the stopping condition — all `k` closest queried — is the
same), and this is simpler to implement and reason about than tracking
"did this round improve on the best distance" as a separate signal. The
cost accepted: in a lookup that converges very quickly (the target is
already well known), this version may issue a handful more `FIND_NODE`
RPCs than the paper's version would, querying some of the `k` closest
candidates "for completeness" that a stricter implementation might skip.
At this project's scale that's a negligible difference, not a correctness
one.

## Concurrency details worth knowing if you extend this

Each round's α parallel RPCs are genuinely concurrent Go goroutines
(`go func(c *lookupCandidate) { ... }`, in the loop body in `lookup.go`),
each with its own per-RPC timeout (`context.WithTimeout(ctx, n.rpcTimeout)`
derived from the lookup's own context), reporting back on a shared
buffered channel that the main loop reads exactly `len(batch)` times
before moving on. There's deliberately no shared mutable state touched
inside those goroutines beyond sending on that channel — the candidate
list itself (`candidates` map, `order` slice) is only ever read or written
back in the single goroutine running the main loop, after all of a
round's outcomes have been collected. That's what avoids needing a mutex
around the lookup's own bookkeeping, separate from the mutex already
inside `RoutingTable`/`Bucket` for the routing-table side of things.

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| The lookup algorithm itself | `Node.Lookup`, `Node.iterativeFind` | `lookup.go` |
| Dropping a node that didn't answer; spares from the routing table | the `failed` set and `closestK` in `iterativeFind` | `lookup.go` |
| Per-RPC send/receive this builds on | `Node.FindNode`, `Node.sendAndWait` | `node.go` |
| Result type / what the demo reports | `LookupResult` | `lookup.go` |

Tested end-to-end in `internal/kademlia/integration_test.go`
(`TestEndToEndBootstrapAndLookup`): five real nodes over real loopback UDP,
checking that a lookup from one of them finds exactly the other four, and
that `Rounds`/`RPCsSent` come back non-zero. The same scenario, run as five
separate OS processes instead of in-process goroutines, is
`scripts/demo.sh` — see the top-level README for how to run it and read
its output.

## What's next

[06-store-and-find-value.md](06-store-and-find-value.md): the same lookup,
used to store and retrieve values. Or, to see docs 01–05 in motion first:

- [DRY-RUN.md](DRY-RUN.md) part 1 — all five pieces traced function by
  function through a real run of the demo.
- [ARCHITECTURE.md](ARCHITECTURE.md) — how the files depend on each other.
- The top-level [README.md](../README.md) — project status against the
  end goal, and which simplifications must be undone before it.
