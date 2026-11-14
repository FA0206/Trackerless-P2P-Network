# Edge cases: what the current code struggles with

The demos in `scripts/` show the network working. This document is the
opposite: specific node setups chosen to push the code until something
gives, so we know (and can show) where it breaks before building on it.
These scenarios also feed the visual demo later.

Each case is a script in `scripts/edge/`. Every script:

- **writes down a prediction first** (in its header and here), based on
  reading the code, and **PASSes when the observed behavior matches it**,
  whether the prediction is "this holds up" or "this breaks". It's the
  same convention as `scripts/demo-seeder-death.sh`. A FAIL means the code
  behaved differently from what we thought, which is worth understanding
  either way.
- **is reproducible.** Node IDs are fixed (`--id-seed`), and anything
  random comes from `SEED` (printed on the first line). Rerun with
  `SEED=<n>` to get the same run.
- **checks itself against brute force** where it can. The script knows
  every node ID, so it computes "the k closest nodes to X" itself (the
  same way `demo-big-network.sh` does) and compares it with what the DHT
  did.
- **changes no Go code.** It drives ordinary `cmd/node` processes through
  their console, using `scripts/lib/nodes.sh` plus `scripts/lib/edge.sh`.

```sh
bash scripts/edge/run-all.sh                   # every case, one verdict line each (~1.5 min)

bash scripts/edge/a2-thundering-herd.sh        # env: N=20 K=4 QUERIERS=4 TARGETS=3 BASE_PORT=9425 SEED=1 RACE=1
bash scripts/edge/a3-bridge-between-islands.sh # env: BASE_PORT=9450 (nothing random, no SEED)
bash scripts/edge/a4-tiny-buckets.sh           # env: N=20 QUERIERS=5 TARGETS=4 BASE_PORT=9475 SEED=1
bash scripts/edge/b1-half-the-network-dies.sh  # env: N=16 K=4 KILL=8 TARGETS=4 BASE_PORT=9500 SEED=1
bash scripts/edge/b2-record-holders-die.sh     # env: N=8 BASE_PORT=9525 SEED=1
bash scripts/edge/b3-late-joiners-hide-a-value.sh  # env: N=8 LATE=4 BASE_PORT=9550 SEED=1
```

`run-all.sh` on the current code:

```
a2-thundering-herd                  6s  PASS: 19 nodes joined through one node at once; lookups were correct and no data race was reported.
a3-bridge-between-islands          22s  PASS: an unused bridge's death split the network for good; a bridge that had carried one lookup each way could die harmlessly.
a4-tiny-buckets                    14s  PASS: with nothing dead, k=4 lookups were all correct and k=1 lookups were not.
b1-half-the-network-dies           25s  PASS: after half the network died, lookups paid an RPC timeout per dead contact, paid it again on the repeat, and returned short answers.
b2-record-holders-die              11s  PASS: with every record holder dead the file could not be found, though 2 providers were online, and re-sharing did not bring it back.
b3-late-joiners-hide-a-value        9s  PASS: newcomers closer to the key missed a value whose holders were alive; re-putting moved it to them.
```

The cases were written before swarming ([08](08-swarming.md)) was merged
in, and re-run after it with the same verdicts. Swarming changes only
`internal/transfer`, not the DHT, and a download with no provider records
still fails at once with the same message, so B2's file case is unchanged.

## Results at a glance

| Case | Holds up or breaks? | Headline number | Root cause | Fix (core code, not made here) |
|---|---|---|---|---|
| A2 thundering herd | **holds up** | 19/19 joins, 12/12 lookups correct, 0 data races in 20 race-detector processes | — | — |
| A3 bridge between islands | **breaks if quiet** | quiet: 0 of 5 found across, forever; used: 5 of 5 | no bucket refresh | bucket refresh (Sec. 2.3) |
| A4 tiny buckets | **breaks at k = 1** | k=1: 9–14 of 20 correct; k=4: 20 of 20 | thin routing tables: no post-join refresh | post-join bucket refresh (Sec. 2.3) |
| B1 half the network dies | **breaks** | 133 ms → 10.35 s for 4 lookups; every answer short; repeat no better | dead contacts kept in tables and lookup shortlists | drop failed candidates; evict on repeated failure (Sec. 2.3, 4.1) |
| B2 record holders die | **breaks** | file unfindable with 2 providers online; re-share doesn't help | no republish + B1's lookup problem | lookup fix first, then republish (Sec. 2.5) |
| B3 late joiners | **breaks** | 13 of 16 newcomer `get`s miss; holders alive | values never move to closer nodes | republish / hand over on join (Sec. 2.5) |

**The main finding** (B1 and B2 both show it): a lookup only
ever starts from the k closest contacts in the routing table, a contact
that times out is never removed, and failed candidates keep their slots
in the lookup's k-closest list. So once nodes die, lookups return
**short answers** (live nodes missing), and in the worst case **nothing
at all**, even though live nodes are known. Nothing improves on its own.

**The second finding** (A3, A4 and B3): nothing maintains the network in
the background. Contacts spread only as a side effect of traffic, and
values stay where they were first stored. A quiet island cut off from
the rest never reconnects (A3). With k = 1, joins reach so few nodes
that lookups stop at nodes with empty buckets (A4). Nodes that join near
a key can't see a value sitting on healthy holders (B3). The paper's
bucket refresh and hourly republish (Sec. 2.3, 2.5) are what's missing.

**What holds up:** concurrency. Twenty processes joining through one
node at the same instant, all built with the race detector, gave correct
lookups and no data races (A2).

## Fix priority

Ranked by how much each problem blocks the end goal (README: downloads
survive the seeder leaving, demo across real devices, evaluation graphs),
and by how much else depends on fixing it first.

| Rank | Case | Problem | Why this rank | Fix | Where |
|---|---|---|---|---|---|
| 1 | **B1** | Lookups around dead nodes are wrong (short answers) and slow, and don't improve | The root cause underneath B2. Every churn experiment runs into it, including the seeder-kill and time-to-recovery graph, and the answers are *wrong*, not just slow. A small change in one function | Drop failed candidates from the k-closest list so the lookup continues to live nodes; remove a contact after repeated RPC failures. For the latency graph: keep α RPCs in flight instead of synchronous rounds | `lookup.go`, `node.go`, `bucket.go` |
| 2 | **B2** | A file becomes unfindable when its record holders die, even with providers online | Directly the end goal ("downloads keep working when nodes leave") and the recovery graph. Needs fix 1 first, or the republish lookup stalls on the same dead contacts | Republish values hourly; expire them after 24 h (Sec. 2.5) | `store.go`, `values.go` |
| 3 | **B3** | Newcomers near a key can't see its value | Same family as B2, but nothing is lost: the holders are alive. Hourly republishing from fix 2 resolves it over time | Fix 2, plus: when a node meets a newcomer closer to a key it holds, it STOREs the value there (Sec. 2.5) | `node.go` (`touchContact`) |
| 4 | **A3** | A quiet group cut off by one node's death never reconnects | Matters for the multi-device demo (two LANs started separately) and for long-idle networks. Busy networks connect themselves through traffic, so it rarely bites today | Bucket refresh: a lookup for a random ID in every bucket not used for a while (Sec. 2.3) | `node.go` |
| 5 | **A4** | With k = 1, lookups go wrong with nobody dead | Only matters for k ≤ 2. Every demo uses k ≥ 3, where all lookups were correct. Shares its code with fix 4 | After joining, refresh every bucket farther away than the closest neighbor (Sec. 2.3) | `node.go` (`Bootstrap`) |
| — | **A2** | — | Holds up; nothing to fix | — | — |

**Three code changes cover all five.** In order: **(a)** lookup failure
handling (fixes B1, unblocks B2); **(b)** republish and hand-over
(B2, B3); **(c)** bucket refresh, periodic and after joining (A3, A4).
Each will flip some of these scripts to FAIL. Then update that script's
prediction and its section here in the same change, as
`demo-seeder-death.sh` did when swarming (parallel downloads and partial
seeding, [08](08-swarming.md)) landed.

## The catalogue

Cases are grouped as **A** (how nodes join) and **B** (churn: nodes
dying and arriving). Every case has a script that passes on the current
code.

| # | Setup | Prediction | Details |
|---|---|---|---|
| **A. How nodes join** | | | |
| A2 | Thundering herd: 20 nodes join via node0 at the same moment | Holds up: all join, lookups correct, no data races | [section](#a2--a-thundering-herd-of-joins) |
| A3 | Two islands joined by one bridge node; the bridge is killed | If nobody used the bridge: split for good. If one lookup crossed it: no harm | [section](#a3--two-islands-and-one-bridge) |
| A4 | Tiny buckets (k = 1, 2, 4) on the same 20 nodes | k = 1 returns wrong nodes with nothing dead; k = 4 doesn't | [section](#a4--tiny-buckets) |
| **B. Churn** | | | |
| B1 | Kill half the nodes, then repeat the same lookups | +1 RPC timeout per round with a dead contact, the repeat is just as slow, and answers come back **short** | [section](#b1--half-the-network-dies-at-once) |
| B2 | Every node holding a file's provider record dies; the seeder stays up | The file can't be found, and **re-sharing doesn't fix it** | [section](#b2--the-provider-record-holders-all-die) |
| B3 | Late joiners with IDs closer to a key than its holders | Newcomers' `get`s miss it, though the holders are alive; a re-`put` moves it to them | [section](#b3--late-joiners-closer-to-a-key) |

**Ports.** Each case has its own block so they can run side by side with
the demos: A2 9425, A3 9450, A4 9475, B1 9500, B2 9525, B3 9550 (override
with `BASE_PORT`). A4 builds its three networks one after another on the
same 20 ports.

---

## A2 — a thundering herd of joins

**Script:** `scripts/edge/a2-thundering-herd.sh`

### Setup

node0 starts alone. Then 19 nodes are launched **at once**, all with
`--bootstrap node0`, without waiting for each other
(`start_node_nowait` in `edge.sh`). `k = 4`, so buckets fill and the
eviction check (ping the oldest contact) runs while joins overlap.

Every node binary is built with **Go's race detector**: the script sets
`GOFLAGS=-race` before `nodes_init` builds it (`RACE=0` turns it off).
A data race in any of the 20 processes would print `WARNING: DATA RACE`
into that node's log. `go test -race` checks the same code in tests;
this checks it in 20 real processes talking over real sockets at once.

Then 4 random nodes each look up 3 random IDs, checked against the
brute-force 4 closest.

### Prediction

It holds up: every join completes, every lookup is correct, no race.
Each received packet is handled on its own goroutine, and the shared
state (buckets, value store, `pending`) is behind locks
([ARCHITECTURE.md](ARCHITECTURE.md), "Goroutines and shared state").

### Real output (`SEED=1`)

```
=== 19 nodes join through node0 at the same moment ===
  19 of 19 joined, all within 553ms
  routing table size right after joining: min 2, median 5, max 6

=== Lookups checked against a brute-force answer ===
  node7  -> cc281173…: rounds=2 rpcs_sent=6 rpcs_failed=0  ok
  node7  -> 290e2d0b…: rounds=2 rpcs_sent=4 rpcs_failed=0  ok
  …                                                        (12 lookups, all ok)
  node3  -> 1470ed9b…: rounds=2 rpcs_sent=5 rpcs_failed=0  ok

=== Data races reported by any of the 20 processes ===
  none

PASS: 19 nodes joined through one node at once; lookups were correct and no data race was reported.
```

Seeds 2 and 3: the same verdict. Joins took 269 ms and 209 ms, and the
smallest routing table right after joining held 1 and 2 contacts.

### What it shows

The concurrency design holds. One detail is worth noticing: a node that
joins while everyone else is joining can finish its join knowing only
**1 or 2 contacts**, because node0 hadn't heard of anyone else yet when
it answered. Lookups were still correct a moment later, because the
traffic that follows fills tables in: every node that contacts you
becomes your contact. With k = 4 that is enough. With k = 1 it isn't
([A4](#a4--tiny-buckets)).

---

## A3 — two islands and one bridge

**Script:** `scripts/edge/a3-bridge-between-islands.sh`

### Setup

Two networks run side by side, built the same way:

```
   island A                                    island B
   a0 ◄── a1 a2 a3 a4 join via a0              b0 ◄── b1 b2 b3 b4 join via b0
    ▲                                           ▲      (B is never told about A)
    │ X joins via a0                            │
    └────────────── X (the bridge) ── PING ─────┘
```

X bootstraps into island A, so its self-lookup reaches every A node, and
each of them records X. Then X PINGs b0, so X and b0 know each other.
That's the only link: b1–b4 have never heard of X or of A.

- **quiet** network: nothing more happens before X is killed.
- **used** network: before X is killed, b3 looks up a3's ID and a3 looks
  up b3's ID, once each, through the bridge.

After X is killed (SIGKILL), a3 and b3 each look up the other, twice.

k is the default 20, bigger than the 11-node network, so a correct
lookup returns **every** live node. "Found 5 of 5 on the other island"
is the brute-force answer. No randomness is involved: two runs give the
same counts.

### Prediction

- **quiet:** `table` shows that the only cross-island knowledge is the 4
  A nodes knowing X and b0 knowing X. After X dies, lookups find **0**
  nodes on the other island, pay a 2 s timeout on the dead X, and the
  repeat is the same. Nothing reconnects the islands: there's no bucket
  refresh ([04](04-bootstrap-and-join.md)), and no node ever tries
  another bootstrap.
- **used:** the islands **survive** losing X. b3's lookup through the
  bridge sent FIND_NODE to every A node directly, and each recorded b3
  (`handleMessage` → `touchContact`, [02](02-routing-table-and-kbuckets.md)).
  After that, the islands no longer depend on X.

### Real output

```
=== quiet: who knows whom before the bridge dies (from `table`) ===
    a1 knows 1 node(s) outside its island
    a2 knows 1 node(s) outside its island
    a3 knows 1 node(s) outside its island
    a4 knows 1 node(s) outside its island
    b1 knows 0 node(s) outside its island
    b2 knows 0 node(s) outside its island
    b3 knows 0 node(s) outside its island
    b4 knows 0 node(s) outside its island
    b0 knows 1 node(s) outside its island
  as predicted: each A node knows only X across the gap (4 in total from a1-a4, b1-b4); among B only b0 knows X

=== used: one lookup across the bridge from each side, before it dies ===
    b3 looks up a3: rounds=4 rpcs_sent=10 rpcs_failed=0   124ms  found 5 of 5 on the other island, 10 nodes in all
    a3 looks up b3: rounds=4 rpcs_sent=10 rpcs_failed=0   129ms  found 5 of 5 on the other island, 10 nodes in all
  as predicted: the bridge works: both lookups found all 5 nodes on the other island

=== Killing the bridge X in both networks (SIGKILL) ===

=== quiet: lookups across the gap, twice ===
    b3 looks up a3: rounds=2 rpcs_sent=5 rpcs_failed=1   2.07s  found 0 of 5 on the other island, 4 nodes in all
    a3 looks up b3: rounds=2 rpcs_sent=5 rpcs_failed=1   2.05s  found 0 of 5 on the other island, 4 nodes in all
    b3 looks up a3: rounds=2 rpcs_sent=5 rpcs_failed=1   2.06s  found 0 of 5 on the other island, 4 nodes in all
    a3 looks up b3: rounds=2 rpcs_sent=5 rpcs_failed=1   2.07s  found 0 of 5 on the other island, 4 nodes in all
  as predicted: 4 lookups found 0 nodes across the gap: the islands are cut apart, and stay that way

=== used: the same lookups ===
    b3 looks up a3: rounds=4 rpcs_sent=10 rpcs_failed=1   2.06s  found 5 of 5 on the other island, 9 nodes in all
    a3 looks up b3: rounds=4 rpcs_sent=10 rpcs_failed=1   2.07s  found 5 of 5 on the other island, 9 nodes in all
    b3 looks up a3: rounds=4 rpcs_sent=10 rpcs_failed=1   2.07s  found 5 of 5 on the other island, 9 nodes in all
    a3 looks up b3: rounds=4 rpcs_sent=10 rpcs_failed=1   2.07s  found 5 of 5 on the other island, 9 nodes in all
  as predicted: 4 lookups found 20 of 20 possible across the gap: one lookup through the bridge had already connected the islands

PASS: an unused bridge's death split the network for good; a bridge that had carried one lookup each way could die harmlessly.
```

How to read the counts:

- **quiet, after the kill: "4 nodes in all".** That's the querier's own
  island minus itself. Its lookup asks its island-mates, b0 (or an A
  node) hands back X, X times out (`rpcs_failed=1`), and there's nothing
  else to try.
- **used, before the kill: "10 nodes in all"** = everyone except the
  querier, X included. After the kill, **9** = every live node except the
  querier. That's the correct answer.
- **used, after the kill: still 2 s each.** The islands are fine, but
  every node still has dead X in its table and asks it on every lookup
  (B1's "nothing is learned", again).

### What it shows

- **Knowledge spreads only through messages, and lookups spread it
  fast.** A single lookup across the gap was enough to join the two
  islands for good. That's the "every message updates the sender's
  bucket" rule ([02](02-routing-table-and-kbuckets.md)) doing what the
  paper intends.
- **Without traffic, nothing spreads.** A quiet island stays one node
  deep, and a single death cuts it off with no way back. In the paper,
  bucket refresh would eventually send a lookup into every distant bucket
  (Sec. 2.3), and X's island-mates would learn b0 that way, *before* X
  died. Here there is no refresh (README simplifications table, "No
  bucket refresh").
- **A real network is mostly "used".** Real nodes join through one
  well-known node and then do lookups all the time, so a true one-node
  bridge is rare. It does happen with separately started groups that
  meet late, for example two LANs in the multi-device demo.

Fix (core code, not made here): bucket refresh, i.e. a periodic lookup
for a random ID in each bucket not used recently (`node.go`;
paper Sec. 2.3). It also fills the quiet island's tables before the
bridge dies.

---

## A4 — tiny buckets

**Script:** `scripts/edge/a4-tiny-buckets.sh`

### Setup

The **same** 20-node network is built three times, with k = 1, 2 and 4:
the same node IDs, the same random joins (each node bootstraps through a
random running node, re-seeded from `SEED` each time), and the same
lookups (5 queriers × 4 random targets). Only k differs. Nobody dies.
Every answer is compared with the brute-force k closest. The three
networks run one after another on the same ports.

### Prediction

- **k = 4:** every lookup correct (`demo-big-network.sh` runs this
  setting).
- **k = 1:** some lookups return the wrong node. Each bucket holds one
  contact, each FIND_NODE reply carries one contact, and the lookup stops
  once its single best candidate has been asked.
- **k = 2:** in between; reported, not asserted.

### Real output (`SEED=1`)

```
=== k=1: 20 nodes on ports 9475-9494, random joins (SEED=1) ===
  k=1: 11 of 20 lookups correct, 1.8 rounds on average
    node7 -> 729dbc18…  rounds=1  got [8] want [19]
    node13 -> 25cdfbf6…  rounds=3  got [12] want [18]
    node13 -> 1753323d…  rounds=1  got [5] want [17]
    …                                                   (9 wrong in all)

=== k=2: 20 nodes on ports 9475-9494, random joins (SEED=1) ===
  k=2: 11 of 20 lookups correct, 1.6 rounds on average
    node13 -> 1753323d…  rounds=1  got [5 12] want [17 7]
    …                                                   (9 wrong in all)

=== k=4: 20 nodes on ports 9475-9494, random joins (SEED=1) ===
  k=4: 20 of 20 lookups correct, 2.1 rounds on average

PASS: with nothing dead, k=4 lookups were all correct and k=1 lookups were not.
```

| SEED | k = 1 | k = 2 | k = 4 |
|---|---|---|---|
| 1 | 11 of 20 | 11 of 20 | 20 of 20 |
| 2 | 9 of 20 | 20 of 20 | 20 of 20 |
| 3 | 14 of 20 | 20 of 20 | 20 of 20 |

So k = 1 gets about half its lookups wrong, more than "some". k = 2
depends on the network: perfect for two seeds, and as bad as k = 1 for
the other.

### Why: the routing tables are nearly empty

Take `node13 -> 1753323d… got [5] want [17]` in the k = 1 network.
node13 asked node5, node5 had nothing closer to offer, and the lookup
stopped. Counting the senders in node5's log, and computing bucket
indexes from the IDs (a one-off analysis, not part of the script):

```
node5 heard from: [0, 4, 12, 13]                  ← 4 of the 19 other nodes, ever
target 1753323d lies in node5's bucket 158
  node7, node11, node15, node17, node18 live in that bucket of node5,
  and every one of them is closer to the target than node5 is
```

XOR guarantees this: any node in that bucket of node5 is closer to the
target than node5 itself ([01](01-ids-and-xor-distance.md)). So if node5
had **any** contact in bucket 158, the lookup would have moved on toward
node17. It has none: in its whole life, node5 received messages from
only 4 nodes. The problem isn't that buckets hold just 1 contact. It's
that most of node5's buckets hold **zero**.

That comes from how a node joins. Its self-lookup is its only active
step, and with k = 1 every reply carries a single contact. A join
reaches a handful of nodes, and only those learn about the newcomer.
The paper adds a step for exactly this: after its self-lookup, a joining
node **refreshes every bucket farther away than its closest neighbor**,
doing one lookup into each (Sec. 2.3). That step is skipped here (README
simplifications table, "No bucket refresh", and
[04](04-bootstrap-and-join.md)). At k = 4 and above, replies carry
enough contacts that it hasn't mattered so far. At k = 1 it does.

### What it shows

Lookup correctness depends on routing-table *coverage*: a contact in
every bucket where nodes exist. Small k removes the redundancy that
hides missing coverage. The demos use k = 3 (the 5-node transfer and
seeder-death demos, where every node is within a lookup or two anyway),
4 or 20. Treat the post-join refresh as the fix if experiments ever need
a small k on a bigger network. Before the evaluation graphs, it's worth adding
anyway: it makes the lookup-latency results less dependent on join
order.

---

## B1 — half the network dies at once

**Script:** `scripts/edge/b1-half-the-network-dies.sh`

### Setup

16 nodes, `k = 4`, each joining through a randomly chosen running node
(as in `demo-big-network.sh`), so lookups take 2–3 rounds. One node, the
**querier**, looks up 4 random targets three times:

```
pass 1   all 16 alive                         baseline
         ── 8 random nodes killed at once (SIGKILL) ──
pass 2   8 alive                              right after the kill
pass 3   8 alive                              the same lookups again
```

The querier, the victims and the targets all come from `SEED`. Each
answer is compared with the brute-force 4 closest **live** nodes. Each
lookup is timed on the monotonic clock (`now_ms` in `edge.sh`).

### Prediction

| Pass | Predicted | Why |
|---|---|---|
| 1 | correct, 0 failed RPCs, milliseconds | nothing's dead |
| 2 | failed RPCs; ≥ 2 s for every lookup that meets a dead contact | rounds are synchronous: a round waits for its slowest RPC, and a dead one takes the full `--rpc-timeout` (2 s) ([05](05-iterative-parallel-lookup.md)) |
| 3 | the **same** dead contacts asked again; just as slow | a timeout never removes a contact from the routing table ([02](02-routing-table-and-kbuckets.md)), so nothing is learned |
| 2, 3 | answers **short**: right nodes, right order, but live ones missing | failed candidates keep their k-closest slots (see below). **Learned from the first runs**, not predicted up front |

### Real output (`SEED=1`)

```
  querier: node10;  to be killed: node5 node7 node9 node11 node12 node13 node14 node15

=== Pass 1: node10 looks up 4 targets, everyone alive ===
  pass 1  6da4c727…  rounds=2  rpcs=4   failed=0    111ms  ok
  pass 1  d4183492…  rounds=3  rpcs=7   failed=0      7ms  ok
  pass 1  64416b78…  rounds=2  rpcs=4   failed=0      7ms  ok
  pass 1  e66a18d1…  rounds=2  rpcs=4   failed=0      8ms  ok
  pass 1  total: 0 failed RPCs, 133ms for 4 lookups, 0 wrong answer(s), 0 of them short

=== Killing 8 of 16 nodes at once (SIGKILL) ===
  alive:  node0 node1 node2 node3 node4 node6 node8 node10
  node10's routing table: 10 contacts in 4 non-empty buckets, 5 of them now dead

=== Pass 2: the same lookups, right after the kill ===
  pass 2  6da4c727…  rounds=2  rpcs=4   failed=3    2.06s  SHORT: got [0] want [0 3 6 8]
  pass 2  d4183492…  rounds=2  rpcs=4   failed=2    2.10s  SHORT: got [2 8] want [2 8 1 4]
  pass 2  64416b78…  rounds=2  rpcs=4   failed=3    2.11s  SHORT: got [0] want [0 3 6 8]
  pass 2  e66a18d1…  rounds=2  rpcs=4   failed=2    4.06s  SHORT: got [8 2] want [8 2 4 1]
  pass 2  total: 10 failed RPCs, 10.35s for 4 lookups, 4 wrong answer(s), 4 of them short

=== Pass 3: the same lookups once more ===
  pass 3  6da4c727…  rounds=2  rpcs=4   failed=3    2.08s  SHORT: got [0] want [0 3 6 8]
  pass 3  d4183492…  rounds=2  rpcs=4   failed=2    2.08s  SHORT: got [2 8] want [2 8 1 4]
  pass 3  64416b78…  rounds=2  rpcs=4   failed=3    2.08s  SHORT: got [0] want [0 3 6 8]
  pass 3  e66a18d1…  rounds=2  rpcs=4   failed=2    4.17s  SHORT: got [8 2] want [8 2 4 1]
  pass 3  total: 10 failed RPCs, 10.43s for 4 lookups, 4 wrong answer(s), 4 of them short

PASS: after half the network died, lookups paid an RPC timeout per dead contact, paid it again on the repeat, and returned short answers.
```

`got [0] want [0 3 6 8]` means: the querier got back node0 only; the
true 4 closest live nodes are node0, node3, node6, node8. In node index
terms, `got` is always the start of `want`.

**Seeds 1–4 (64 lookups after the kill) all give the same picture:**

| SEED | pass 1 | pass 2 | pass 3 | short answers (pass 2 / 3) |
|---|---|---|---|---|
| 1 | 133 ms | 10.35 s, 10 failed | 10.43 s, 10 failed | 4/4 · 4/4 |
| 2 | 137 ms | 12.16 s, 9 failed | 10.64 s, 9 failed | 4/4 · 4/4 |
| 3 | 150 ms | 12.44 s, 8 failed | 12.45 s, 8 failed | 4/4 · 4/4 |
| 4 | 27 ms | 10.30 s, 11 failed | 14.53 s, 11 failed | 4/4 · 4/4 |

Seed 4 includes `SHORT: got [] want [2 1 7 12]`: a lookup that found
**no node at all** while 8 were alive. The failed-RPC counts repeat
exactly from pass 2 to pass 3. The time can differ: it depends on whether
a lookup's dead contacts land in one round (one 2 s wait) or two.

### Why the answers are short: one lookup, round by round

One lookup from `SEED=2` (querier node5, target `62433a6b…`), pass 2,
from node5's log (timestamps kept, IDs shortened):

```
01.166  rpc sent      FIND_NODE to=4e07e18a…:9511        round 1: the 3 closest known
01.166  rpc sent      FIND_NODE to=798cecdb…:9509
01.166  rpc sent      FIND_NODE to=21efa2ee…:9500
01.167  rpc received  FIND_NODE_RESP ×3                   all alive, answered in 1 ms
01.167  rpc sent      FIND_NODE to=4d9ff77f…:9510        round 2: the not-yet-asked
01.167  rpc sent      FIND_NODE to=69352a7f…:9512          candidates among the k closest
03.169  lookup converged rounds=2 rpcs_sent=5 rpcs_failed=2 closest_found=2
```

`:9510` and `:9512` are dead. Round 2 waits the full 2 s for them, and
then the lookup **stops**. In `iterativeFind` (`lookup.go`) the
candidate list is trimmed to the k = 4 closest after every round, and
the two dead nodes are among those 4. All 4 are now "queried", so the
loop ends. The result leaves out failed candidates, so 2 nodes come back.
The live nodes that should fill those 2 slots were trimmed away earlier,
or never added, because the dead ones were closer.

The paper's rule is the opposite: nodes that fail to respond "are
removed from consideration until and unless they do respond" (Sec. 2.3).
The lookup should keep going until it has heard back from k live nodes.

### What it shows, and what would fix it

After churn, the network is **slow**: about 2 s per round that meets a
dead contact, against ~10 ms before. It **doesn't recover**: pass 3 asks
the same dead nodes. And it's **wrong**: every lookup after the kill
returned fewer than the k closest live nodes. Since `Store` and
`FindValue` are built on the same lookup, B2's lost file is the same
problem one level up.

Fixes, none made here (they change core code):

| Problem | Fix | Where | Paper |
|---|---|---|---|
| short answers | drop failed candidates from the k-closest list, so the lookup keeps going until k live nodes answered | `lookup.go` | Sec. 2.3 |
| 2 s per round | keep α RPCs in flight instead of synchronous rounds | `lookup.go` | Sec. 2.3 |
| no learning | remove a contact after repeated RPC failures | `node.go`, `bucket.go` | Sec. 4.1 |

The first fix also turns this script's "short" assertion into a FAIL.
At that point, flip the prediction, the way `demo-seeder-death.sh` was
flipped when swarming landed.

---

## B2 — the provider-record holders all die

**Script:** `scripts/edge/b2-record-holders-die.sh`

### Setup

8 nodes, `k = 3`, all joining via node0. node1 (the seeder) shares a
4-piece file. Its provider record is `STORE`d on the 3 nodes whose IDs are
XOR-closest to the content key ([06](06-store-and-find-value.md)). Those
3 nodes **hold the record but not the file**.

```
            content key 93accc52…
                    │  record stored on the 3 closest IDs
         ┌──────────┼──────────┐
       node0      node3      node6        ← record holders (killed in step 2)

       node1 (seeder, has the file)       node2 (downloads in step 1, has the file)
       node4, node5, node7                ← leechers in steps 3 and 4
```

The content key depends on the file, not on who shares it, so the
seeder can't be picked in advance to avoid being a holder. If it is one
(killing the holders would kill the seeder too), the script shares a
renamed copy, which has a different key. That's the `sharing a renamed
file instead` line that appears with some seeds.

### Steps and prediction

| Step | What happens | Prediction |
|---|---|---|
| 1 | node2 downloads (control) | works, and node2 becomes a provider |
| 2 | kill the 3 holders; ping node1 and node2 | both providers answer |
| 3 | node4 downloads | **fails: "no providers found"**, with 2 providers online |
| 4 | node1 shares the file again, then node5 downloads | **still fails** (see below) |

### Real output (`SEED=1`)

```
=== node1 shares a file; its record goes to the 3 nodes closest to the key ===
  content key:            93accc525bce78b07f7868ba330d890537216c3d
  expected holders:       node0 node6 node3  (brute force: the 3 IDs closest to the key)
  holders (from logs):    node0 node3 node6  (nodes that logged "value stored")

=== Step 1 (control): node2 downloads while the holders are alive ===
  node2: fetched "shared-1-1.bin" (262144 bytes, 4 pieces) in 10ms
  node2's copy is identical; node2 is now a provider too

=== Step 2: kill the 3 record holders (SIGKILL) ===
  killed node0
  killed node6
  killed node3
  providers still online, pinged from node4:
    PONG from 127.0.0.1:9526 in 1.272ms
    PONG from 127.0.0.1:9527 in 873µs

=== Step 3: node4 tries to download (predicted: no record found) ===
  node4: fetch failed: transfer: no providers found for 93accc525bce78b07f7868ba330d890537216c3d
  its FIND_VALUE: values_found=0 rounds=2 rpcs_sent=4 rpcs_failed=3
  as predicted: the file is online on 2 nodes, but nobody can find them

=== Step 4: node1 shares the file again (predicted: this doesn't help) ===
  node1's re-announce lookup: rounds=1 rpcs_sent=3 rpcs_failed=3 closest_found=0
  node1's store:              stored_at=0 stored_locally=true failed=0
  as predicted: every contact it started from was a dead holder, so the record stayed on node1 alone
  node5: fetch failed: transfer: no providers found for 93accc525bce78b07f7868ba330d890537216c3d
  as predicted: node5's lookup also stops at the dead holders and never reaches node1

PASS: with every record holder dead the file could not be found, though 2 providers were online, and re-sharing did not bring it back.
```

`SEED=2` and `SEED=3` (both of which needed a renamed file) give the same
verdict with different keys.

### Step 3: why the file can't be found

A downloader finds providers only through `FIND_VALUE(key)`, which asks
the nodes closest to the key. Those are exactly the 3 dead holders. The
record was written once, at share time, and nothing ever copies it
anywhere else: values are never republished (`store.go`,
`SIMPLIFICATION`; README simplifications table, "Values never expire or
get republished"). In the paper, every holder republishes hourly
(Sec. 2.5), which keeps the record on the k closest *live* nodes as
holders die.

### Step 4: why re-sharing doesn't help (the prediction we got wrong)

The first version of this script predicted that re-sharing would work:
`Share` calls `Store`, which runs a fresh lookup and should find the 3
closest live nodes. The run said otherwise, and node1's log shows why:

```
msg="lookup converged" target=93accc52… rounds=1 rpcs_sent=3 rpcs_failed=3 closest_found=0
msg="store complete"   key=93accc52…    stored_at=0 stored_locally=true failed=0
```

The lookup asked 3 nodes, all 3 failed, and it stopped with no answer.
Two pieces of code combine to cause this:

1. **A lookup starts from the k closest contacts in the routing table,
   and only those** (`iterativeFind`, `lookup.go`:
   `n.rt.Closest(target, n.k)`). node1 stored the record on node0, node3
   and node6 earlier, so they're in its routing table, and they're its 3
   closest contacts to this key. All 3 are dead, and nothing else is ever
   added to the candidate list, so the lookup ends after one round.
2. **Dead contacts never leave a routing table because of a timeout.**
   A contact is only removed by the eviction check, which runs when a
   *new* contact arrives for a *full* bucket ([02](02-routing-table-and-kbuckets.md)).
   A failed lookup RPC just marks the candidate `failed` for that one
   lookup.

With nobody found, `Store` concludes that node1 itself is among the k
closest (`selfAmongClosest`: fewer than k others were found) and keeps
the record locally. node5's `FIND_VALUE` has the same problem: its
shortlist fills up with the dead holders and never gets as far as node1.

A related detail makes this worse in bigger networks: when a lookup
trims its candidate list to the k closest (`lookup.go`, after each
round), **failed candidates still take up slots**. A live node that is
slightly farther away gets trimmed even though a dead one is kept. The
paper says nodes that fail to respond "are removed from consideration
until and unless they do respond" (Sec. 2.3). [B1](#b1--half-the-network-dies-at-once)
shows this across a whole network: every lookup after the kill came
back short.

### What it shows, and what would fix it

The file is online on two nodes but invisible, and the obvious manual
fix doesn't work. Churn among the few nodes near a key is enough to lose
a file, even if the seeder never leaves.

These fixes are **not made here**: this step only measures. Each one is
a change to core code, for a later, separate step:

| Fix | Where | Paper |
|---|---|---|
| Republish values hourly; expire after 24 h | `store.go`, `values.go` | Sec. 2.5 |
| Drop failed candidates from a lookup's shortlist, so live nodes farther out move up | `lookup.go` (`iterativeFind`) | Sec. 2.3 |
| Seed a lookup from more of the routing table, or refill when every candidate fails | `lookup.go` | Sec. 2.3 |
| Remove a contact after repeated RPC failures, not only on a full-bucket ping | `node.go`, `bucket.go` | Sec. 4.1 (stale after 5 failures) |
| Bucket refresh, so a node learns live replacements for the dead | `node.go` | Sec. 2.3, 2.5 |

**Republish alone wouldn't save this run.** The 3 holders died at the
same moment, so none of them could republish. The publisher's own
republish goes through `Store`, the same lookup as step 4, and stops at
the same dead contacts. What's missing is the lookup carrying on past
them: node1's routing table *does* know live nodes (node2, node4, …),
but the lookup only ever starts from its 3 closest contacts. So the
third fix (refill from the routing table when candidates fail) has to
come first, and republish then keeps records alive as holders die one
by one.

---

## B3 — late joiners closer to a key

**Script:** `scripts/edge/b3-late-joiners-hide-a-value.sh`

### Setup

8 nodes, `k = 2`, all joining via node0. node1 `put`s a short text. It's
stored on the 2 nodes closest to the key SHA-1(name)
([06](06-store-and-find-value.md)). Then 4 more nodes join, **each with
an ID closer to the key than either holder**. Nobody dies.

```
  distance to the key ──►  (closer is left)

  before:   ·  ·  ·  ·  ·  [H] [H]  ·  ·  ·  ·  ·     H = holds the value
  after:    n  n  n  n  ·  [H] [H]  ·  ·  ·  ·  ·     n = newcomer, no value
```

How the script controls this:

- **The key is picked in advance.** `put <name>` stores under
  SHA-1(name), which bash computes with `shasum`. The script tries names
  `b3-<SEED>-1`, `-2`, … until node1 isn't one of the 2 closest, so the
  value lands on two *other* nodes, whose logs show it.
- **Newcomer IDs are found by brute force.** The script tries seeds
  `edgeb3-late-<SEED>-0`, `-1`, … and keeps those whose SHA-1 is closer
  to the key than the nearer holder (50–210 tries per run). It starts
  each newcomer with `--id-seed <that seed>`. `start_node` already
  passes `--id-seed edgeb3-<i>`, but Go's `flag` package keeps the
  **last** value of a repeated flag, so the second one wins.
  `nodes.sh` doesn't change.

### Steps and prediction

| Step | What happens | Prediction |
|---|---|---|
| 1 | every other node `get`s it (control) | all find it |
| 2 | 4 newcomers join, all closer to the key | the 2 closest nodes are now newcomers |
| 3 | everyone `get`s it again | **newcomers miss it**, though both holders are alive |
| 4 | node1 `put`s it again; the other newcomers `get` it | the value moves to the 2 closest (newcomers); all find it |

**Why step 3 should fail.** A `get` is a FIND_VALUE lookup that starts
from the k closest contacts in the querier's own routing table
(`iterativeFind`, `lookup.go`). A newcomer's closest contacts are other
newcomers, since they're closest to the key. They don't have the value,
and they answer with the contacts *they* know closest to the key: more
newcomers. With k = 2, the lookup can run out of unqueried candidates
before it ever reaches a holder. Nothing moves the value toward the
newcomers: values are never republished (`store.go`, `SIMPLIFICATION`).
In the paper, holders republish every hour, and that is how a value
reaches nodes that join closer to its key (Sec. 2.5). A newly joined
node is also supposed to be handed the values it is now closest to
(Sec. 2.5); there's no such step here either.

### Real output (`SEED=1`)

```
=== node1 puts a value under SHA-1("b3-1-3") ===
  key:                 0ead8775570a34b61555f59a29dfc0571fe6a88d
  expected holders:    node7 node3  (brute force: the 2 closest IDs)
  holders (from logs): node3 node7
  as predicted: the value landed exactly on the 2 closest nodes

=== Step 1 (control): every other node gets it ===
    node0  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    …                                                   (node1, 2, 4, 5, 6 the same)
  as predicted: 6 of 6 found it

=== Step 2: 4 nodes join, each closer to the key than both holders ===
    node8 joined: id 0eb1a3ae…  distance to key 001c24db…  (seed "edgeb3-late-1-50")
    node9 joined: id 0f11c5bf…  distance to key 01bc42ca…  (seed "edgeb3-late-1-97")
    node10 joined: id 0cc6589c…  distance to key 026bdfe9…  (seed "edgeb3-late-1-207")
    node11 joined: id 0f7c0769…  distance to key 01d1801c…  (seed "edgeb3-late-1-209")
    the bar: holder node7 is at distance 02bbf8c4…  (210 seeds tried)
    the 2 closest nodes to the key are now: node8 node9

=== Step 3: everyone gets it again (the holders are still alive) ===
    node0  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    …                                                   (node1, 2, 4, 5, 6 the same)
    node8  MISS  0 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node9  MISS  0 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node10 found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node11 MISS  0 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
  original nodes: 6 of 6 found it (reported, not asserted)
  as predicted: 3 of 4 newcomers missed it, though both holders are alive

=== Step 4: node1 puts the same value again (a manual republish) ===
  stored on:  node8 node9
  as predicted: the re-put reached the 2 closest nodes, which are newcomers
    node10 found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node11 found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
  as predicted: 2 of 2 other newcomers find it now

PASS: newcomers closer to the key missed a value whose holders were alive; re-putting moved it to them.
```

**Seeds 1–4:**

| SEED | newcomers that missed (step 3) | original nodes that found it (step 3) | newcomers that found it after the re-put |
|---|---|---|---|
| 1 | 3 of 4 | 6 of 6 | 2 of 2 |
| 2 | 3 of 4 | 6 of 6 | 2 of 2 |
| 3 | 3 of 4 | 6 of 6 | 2 of 2 |
| 4 | 4 of 4 | 6 of 6 | 2 of 2 |

### The prediction we got wrong: node10 found it

The first version asserted that **every** newcomer misses. In `SEED=1`,
node10 found the value. Its log shows why:

```
msg="rpc sent" type=FIND_VALUE to=0eb1a3ae…:9558      node8, a newcomer
msg="rpc sent" type=FIND_VALUE to=0c167fb1…:9557      node7, a HOLDER
msg="find_value finished" values_found=1 rounds=1 rpcs_sent=2 rpcs_failed=0
```

node10's 2 closest contacts to the key were node8 and **node7**, not
node8 and node9. During its join, node10 got FIND_NODE replies from
node0, node3, node4, node7 and node8, but **never heard of node9**.
Every reply carries at most k = 2 contacts, and node9 was never among
them. So for node10, a holder was still one of its 2 closest known
nodes. node8, which did know node9 and node11, asked only those two and
missed.

So the rule is narrower: **a node misses the value when its k closest
known contacts are all nodes without it.** Whether that's true depends
on the partial routing tables a small k produces. The check is now "at
least one newcomer misses". The original nodes all found it, in every
seed. Their tables were filled before the newcomers arrived, so a holder
is still among their closest contacts. Once they learn about the
newcomers, they would start missing it too.

### What it shows, and what would fix it

A value is only as findable as its placement is up to date. Nodes that
join near a key, which are exactly the nodes a lookup converges on, can
be blind to it while it sits on two healthy holders. This is the "values
don't move to closer nodes" half of the README's "Values never expire or
get republished" simplification. B2 is the other half, "values vanish
when their holders die".

Step 4 shows the fix works when the lookup is healthy. Unlike B2, nobody
is dead here, so the re-put's lookup finds the true 2 closest nodes, and
the value moves to them.

| Fix (not made here) | Where | Paper |
|---|---|---|
| Holders republish every hour, so values drift to whoever is closest now | `store.go`, `values.go` | Sec. 2.5 |
| When a node learns of a newcomer closer to a key it holds, it STOREs the value there | `node.go` (`touchContact`) | Sec. 2.5 |
| Caching: the searcher stores a found value at the closest node on its path that lacked it | `values.go` (`FindValue`) | Sec. 2.3 |

### A flaky FAIL that was the script's fault

B3 once failed in a `run-all.sh` run, and then passed 15 times in a row.
A loop of 30 runs (half with the CPU kept busy) caught 5 failures, all
at step 4's "the re-put reached the 2 closest nodes", with `stored on:`
showing only one of node8 and node9. The putter's log showed the DHT had
done the right thing:

```
put b3-1-3 (key 0ead8775570a34b61555f59a29dfc0571fe6a88d): stored on 2 other node(s), locally=false, failed=0
time=2026-11-14T22:31:00.169+05:30 level=INFO msg="rpc sent" node=cfee051d type=PING to=0c167fb10ee3802321b9cfb31f440a07e8bbb032@127.0.0.1:9557 rpc_id=aad5bd35
    0eb1a3aebfcbdc55a6cc85d006d59b5fe446ffe8@127.0.0.1:9558
    0f11c5bf112711b5369b5a057fa96a9496b4cbe7@127.0.0.1:9559
```

The console prints the header and each contact as separate writes, and
here a log line from another goroutine (a bucket-eviction PING) landed
between them. The script took "the 2 lines after `put`", so it got the
log line and node8, and missed node9. It could also read the block
before the contact lines were written. The fix: `put_contacts` skips
`time=` lines (as `lookup_answer` in `edge.sh` already did), and the
script waits until it has as many contacts as the header announces.
After it, 30 of 30 runs passed. `demo-parallel.sh`'s provider count had
the same weakness and skips log lines now too.

**Lesson for every script here:** console output and log lines share
one file, and they can interleave. Parse console blocks by their own
line shape, and skip `time=` lines.
