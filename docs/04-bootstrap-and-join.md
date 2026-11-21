# 4. Bootstrap and join: how a new node enters the network

Prerequisite: [03-message-framing.md](03-message-framing.md). This doc
leans on [05](05-iterative-parallel-lookup.md) (the lookup procedure) —
the two are tightly coupled, since joining mostly *is* a lookup.

## What a new node needs before it can join

There is no server, no registration, and no "join" message in Kademlia.
Here is the complete list of what a new node must have:

| Requirement | Where it comes from | In this code |
|---|---|---|
| **Its own 160-bit node ID** | Picked by the node itself — random, or SHA-1 of something. Nobody assigns or approves it. | `--id-seed` (SHA-1 of a string) or `RandomID()`; `resolveID` in `cmd/node/main.go` |
| **A UDP socket others can reach** | Bound locally on some port | `--port`; `NewTransport` in `transport.go` |
| **The IP address and UDP port of any one node already in the network** (the *bootstrap node*) | Out of band: typed on the command line, a config file, a teammate telling you, a well-known hostname (BitTorrent's DHT uses `router.bittorrent.com:6881`) | `--bootstrap 127.0.0.1:9000` |
| The same wire protocol | Same code / compatible version | `messages.go`, `transport.go` |

And just as important, what it does **not** need:

- **Not the bootstrap node's ID.** Only `IP:port`. The new node learns the
  ID by asking (step 1 below).
- **Not a list of nodes, nor the network's size.** One working address is
  enough; the rest is discovered.
- **Not a special bootstrap node.** Any node already in the network works,
  and that node doesn't even know it's being used for bootstrapping — it
  just answers `PING` and `FIND_NODE` like it would for anyone.
- **Nothing from the rest of the network in advance.** Existing nodes do
  nothing special to accept a newcomer.

The one address you need is the only piece of "outside knowledge" in the
whole system. It's needed once, at join time. After that the node relies
only on its routing table, so the bootstrap node can leave the network
without affecting it.

The very **first** node is the exception: it starts with no
`--bootstrap` and an empty routing table. It's "the network" until
someone joins through it.

## The join procedure, step by step

The paper's recipe (Sec. 2.3, "To join the network..."):

> To join the network, a node `u` must have a contact to an already
> participating node `w`. `u` inserts `w` into the appropriate k-bucket.
> `u` then performs a node lookup for its own node ID. Finally, `u`
> refreshes all k-buckets further away than its closest neighbor.

This project implements it in `Node.Bootstrap`
([`node.go`](../internal/kademlia/node.go)) in three steps. The first two follow; step 3 has its own section below. Here is real
log output from `scripts/demo.sh`, node4 (`3fb55a72…`, port 9004)
joining via node3's address (port 9003). Timestamps are stripped; the
full trace for every node is in [DRY-RUN.md](DRY-RUN.md).

### Step 0 — start up

```
msg="node started" id=3fb55a726b74dfbe73ae5d6de38b36166212c007 addr=0.0.0.0:9004
msg=bootstrapping via=127.0.0.1:9003
```

The routing table is empty, and nobody in the network knows node4 exists.

### Step 1 — PING the address to find out who is there

```
msg="rpc sent"     type=PING to=0000000000000000000000000000000000000000@127.0.0.1:9003
msg="rpc received" type=PONG from=94fab99d6ca8abab7d217af7e3c5532697bf1a7a@127.0.0.1:9003
msg="bootstrap node identified" id=94fab99d6ca8abab7d217af7e3c5532697bf1a7a
```

The all-zero `to=` ID is honest: node4 doesn't yet know who's at 9003.
The PONG's envelope carries the responder's real `SenderID`. Like every
incoming message, it goes through `handleMessage → touchContact`, which
files node3 in node4's routing table **under its real ID**. That's the
"`u` inserts `w` into the appropriate k-bucket" step of the paper, done
with an actual confirmed ID. Node3, in turn, has recorded node4 from the
PING it received.

If nobody answers, `Bootstrap` returns an error ("bootstrap node … did
not answer PING") instead of carrying on with an empty table. If the
address turns out to be the node itself, that's an error too.

### Step 2 — look up your own ID

```
msg="rpc sent"     type=FIND_NODE to=94fab99d…@127.0.0.1:9003        ← round 1: the only node we know
msg="rpc received" type=FIND_NODE_RESP from=94fab99d…@127.0.0.1:9003 ← node3 replies with nodes 0, 1, 2
msg="rpc sent"     type=FIND_NODE to=db612776…@127.0.0.1:9002        ← round 2: alpha=3 in parallel
msg="rpc sent"     type=FIND_NODE to=2b370ab7…@127.0.0.1:9000
msg="rpc sent"     type=FIND_NODE to=10f71961…@127.0.0.1:9001
msg="rpc received" type=FIND_NODE_RESP from=db612776…   (×3 replies)
msg="lookup converged" rounds=2 rpcs_sent=4 rpcs_failed=0 closest_found=4
msg="bootstrap complete" routing_table_size=4
```

The self-lookup has two effects:

1. **It fills node4's routing table.** Every reply arrives as a message
   from a real node, and each one is recorded via `touchContact`. That
   happens to every responder, not just the ones that end up in
   `Closest`.
2. **It announces node4 to the network.** Every `FIND_NODE` node4 sends
   carries `SenderID = node4`, and every receiver records the sender
   ([02](02-routing-table-and-kbuckets.md)'s update rule). So everyone
   node4 queried now has node4 in their routing table, without any
   separate "announce" message.

Why look up your *own* ID rather than a random one? The lookup walks
toward the target. When the target is you, the nodes you meet are your
nearest neighbors — exactly the nodes that most need to know about you.
Those are the ones that would answer for keys near your ID. Along the way
you also meet nodes in farther distance bands, which seed your far
buckets.

### Before and after

| | Before | After |
|---|---|---|
| node4's routing table | empty | node0, node1, node2, node3 |
| Who has node4 in their table | nobody | node3 (from the PING), then node0–3 (from the FIND_NODEs) |

In a 5-node network with `k = 20`, "everyone" is reachable in two rounds.
In a large network, a joining node meets roughly `log₂(n)` nodes — not
everyone. The rest learn about it gradually, as their own lookups pass
through nodes that already know it.

## A bug the dry run caught

The first version of `Bootstrap` skipped step 1. It passed the bootstrap
address straight into the lookup as a candidate with a placeholder
all-zero ID. That never corrupted the routing table, but the logs showed
two real side effects on every join:

- node2 sent `FIND_NODE` to node1 **twice**: once as `0000…@:9001`, and
  again as `10f71961…@:9001` after another reply mentioned node1's real ID.
- the result reported `closest_found=3` when only 2 other nodes existed,
  because the placeholder counted as a "node".

PINGing first removes the placeholder entirely. The lookup only ever
sees real IDs. `TestBootstrapLearnsRealSeedID` in
`internal/kademlia/integration_test.go` guards against regressions.

## Step 3 of the join: refresh the far buckets

The paper's full join has one more step (Sec. 2.3): "`u` refreshes all
k-buckets further away than its closest neighbor." A self-lookup mostly
finds nodes near you. To fill a far bucket, you need to look up a
random ID inside that bucket's range (`RandomIDInBucket`, `refresh.go`).
So after the self-lookup, `Bootstrap` finds its closest neighbor's
bucket and looks up one random ID in every bucket above it. In a
20-node network that's about 4 lookups, and in `demo.sh` 2 or 3
([DRY-RUN.md](DRY-RUN.md), part 2c):

```
[node1] msg="buckets refreshed" reason=join buckets=2 rpcs_sent=2 rpcs_failed=0 routing_table_size=1 learned=0
```

It's best effort: a failed refresh lookup doesn't fail the join.

The first version skipped this step ("at a few dozen nodes the
self-lookup reaches essentially everyone"). Edge case A4 showed that's
wrong for a small k. With k = 1, nodes joined knowing almost nobody, and
9–14 of 20 lookups were correct. With the join refresh it's 14–20 of 20
([EDGE-CASES.md](EDGE-CASES.md#a4--tiny-buckets)). `--join-refresh=false`
turns it off, only so experiments like A4 can compare.

Every refresh lookup also makes the nodes it asks learn about the
newcomer (they record the sender), so the join announces it to the far
side of the network too. After joining, a node keeps refreshing buckets
no lookup has touched for `--refresh-interval` (default 1 h): see
[02](02-routing-table-and-kbuckets.md#bucket-refresh-keeping-quiet-buckets-alive).

## Join scenarios: what if…?

Every case below has a test that shows the behavior. "Rule 1" is docs/09's
"one node per device, or all on one machine". The first two cases and the
duplicate-ID twin case can also be run on real laptops, step by step, with
real expected output: open
[`scripts/multi-device/join-tests.html`](../scripts/multi-device/join-tests.html)
in a browser.

| What if… | What happens | Shown by |
|---|---|---|
| **no `--bootstrap` is given?** | The node is the first node of a network of one. That's not an error: its lookups ask and find nobody, values it stores stay on it alone, and the next node joins through it (and then finds those values). | `TestFirstNodeWithoutBootstrap` |
| **two nodes both start without `--bootstrap`?** | Two separate networks, forever. With no tracker and no broadcast, nothing introduces them. One join, in either direction, merges them. | `TestNodesWithoutBootstrapStaySeparate`; edge case A3 for a bridge between two built-up networks |
| **I join through a node other than the first?** | Same result. Any member can introduce you: the PING tells you who it is, and the self-lookup does the rest. | `TestEndToEndBootstrapAndLookup` (a chain), `TestBootstrapNodeCanLeaveAfterJoin` |
| **the bootstrap node leaves after I've joined?** | Nothing breaks. It was needed for the first contact only; you already know the others. | `TestBootstrapNodeCanLeaveAfterJoin` |
| **nobody is at that address?** (wrong IP or port, node not started, firewall) | The PING gets no answer within `--rpc-timeout`, and the node exits: `bootstrap failed: … did not answer PING`. | `TestBootstrapFailures` |
| **the address is my own?** | Fails at once: the PONG carries this node's own ID. | `TestBootstrapFailures` |
| **the node there has my ID?** (the same `--id-seed`) | Fails the same way, since a node can't tell itself from a twin: `the node at … has the same ID as this node`. | `TestBootstrapThroughNodeWithOurOwnID` |
| **my twin joins through someone else?** | It succeeds. The protocol doesn't detect duplicate IDs: that node keeps one contact per ID, the twin that spoke last, and everything for that ID goes to it alone. Never reuse a seed. | `TestDuplicateIDNewestSenderWins` |
| **some other program answers at that address?** | Its replies don't parse, so they're dropped (`dropping malformed packet`), and the join fails as if nobody were there. | `TestBootstrapToSomethingElse` |
| **the bootstrap node answers the PING, then dies?** | The join still reports success, but the self-lookup reached nobody, so the node knows only a dead contact. That's today's behavior; a stricter join would call it a failure. | `TestBootstrapNodeGoesSilentAfterPing` |
| **`--bootstrap` is malformed?** (no port, IPv6, a bad port) | The node refuses to start: `parsing -bootstrap "…"`. | `TestParseBootstrap` (`cmd/node`) |
| **`--bootstrap` is a host name?** (`laptop-a.local:9000`) | Allowed, unlike `--advertise-ip`: it's resolved once, on this machine. | `TestParseBootstrap` |
| **many nodes join through one node at the same moment?** | All join, the lookups come out right, and the race detector finds nothing. | `scripts/edge/a2-thundering-herd.sh` |
| **on several machines, I join through `127.0.0.1`?** | It breaks rule 1, and remote nodes get handed a `127.0.0.1` address they can't use ([09](09-multi-device.md), F2). | — (ruled out by rule 1) |

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| Join: PING the address, then self-lookup, then refresh | `Node.Bootstrap` | `node.go` |
| Refreshing buckets (at join and periodically) | `refreshBuckets`, `RandomIDInBucket`, `refreshLoop` | `refresh.go` |
| The lookup itself | `Node.Lookup` | `lookup.go` |
| Recording senders (how both sides learn about each other) | `Node.handleMessage`, `Node.touchContact` | `node.go` |
| CLI flag `--bootstrap host:port` | `run` | `cmd/node/main.go` |

Tests in `internal/kademlia/integration_test.go`:
`TestEndToEndBootstrapAndLookup` (a chain of five joins, then a lookup),
`TestBootstrapLearnsRealSeedID` (no placeholder leaks; one RPC per real
node), and `TestBootstrapFailures` (unreachable address, own address).
`refresh_test.go`: `TestJoinRefreshLooksUpEveryFartherBucket` (every
bucket above the closest neighbor gets a lookup, and none with
`SkipJoinRefresh`) and `TestRandomIDInBucketLandsInThatBucket`.
The other join scenarios are in `internal/kademlia/bootstrap_test.go`,
and parsing `--bootstrap` is `parseBootstrap` in `cmd/node/main.go`
(`TestParseBootstrap`); the table above maps each scenario to its test.

## What's next

[05-iterative-parallel-lookup.md](05-iterative-parallel-lookup.md): the
lookup algorithm that step 2 runs.
