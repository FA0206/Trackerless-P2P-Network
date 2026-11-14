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
bash scripts/edge/run-all.sh                   # every case, one verdict line each (~2 min)

bash scripts/edge/a2-thundering-herd.sh        # env: N=20 K=4 QUERIERS=4 TARGETS=3 BASE_PORT=9425 SEED=1 RACE=1
bash scripts/edge/a3-bridge-between-islands.sh # env: BASE_PORT=9450 (nothing random, no SEED)
bash scripts/edge/a4-tiny-buckets.sh           # env: N=20 QUERIERS=5 TARGETS=4 BASE_PORT=9475 SEED=1
bash scripts/edge/b1-half-the-network-dies.sh  # env: N=16 K=4 KILL=8 TARGETS=4 MAX_PASSES=10 BASE_PORT=9500 SEED=1 NODE_FLAGS=
bash scripts/edge/b2-record-holders-die.sh     # env: N=8 BASE_PORT=9525 SEED=1 REPUBLISH=15s
bash scripts/edge/b3-late-joiners-hide-a-value.sh  # env: N=8 LATE=4 BASE_PORT=9550 SEED=1 REPUBLISH=20s
```

`run-all.sh` on the current code (with the lookup fix, stale contacts
and republishing; B1, B2 and B3's predictions changed with them, see
their sections and [FIXES.md](FIXES.md)):

```
a2-thundering-herd                  7s  PASS: 19 nodes joined through one node at once; lookups were correct and no data race was reported.
a3-bridge-between-islands          22s  PASS: an unused bridge's death split the network for good; a bridge that had carried one lookup each way could die harmlessly.
a4-tiny-buckets                    14s  PASS: with nothing dead, k=4 lookups were all correct and k=1 lookups were not.
b1-half-the-network-dies           59s  PASS: after half the network died, lookups paid a timeout per dead node, but no answer came back short, and by pass 7 the querier had stopped asking dead nodes.
b2-record-holders-die              41s  PASS: with every record holder dead the file could not be found, though 2 providers were online, until the seeder's next republish brought it back.
b3-late-joiners-hide-a-value       42s  PASS: newcomers closer to the key missed a value whose holders were alive, until the putter's next republish moved it to them.
```

B2 and B3 take ~40 s because they wait for real republishes (15 s and
20 s intervals).

The cases were written before swarming ([08](08-swarming.md)) was merged
in, and re-run after it with the same verdicts. Swarming changes only
`internal/transfer`, not the DHT, and a download with no provider records
still fails at once with the same message, so B2's file case is unchanged.

## Results at a glance

| Case | Holds up or breaks? | Headline number | Root cause | Fix |
|---|---|---|---|---|
| A2 thundering herd | **holds up** | 19/19 joins, 12/12 lookups correct, 0 data races in 20 race-detector processes | — | — |
| A3 bridge between islands | **breaks if quiet** | quiet: 0 of 5 found across, forever; used: 5 of 5 | no bucket refresh | bucket refresh (Sec. 2.3) |
| A4 tiny buckets | **breaks at k = 1** | k=1: 9–14 of 20 correct; k=4: 20 of 20 | thin routing tables: no post-join refresh | post-join bucket refresh (Sec. 2.3) |
| B1 half the network dies | **slow at first, recovers; was wrong** | was: every answer short, and repeats no better. Now: none short; 2–8 s per lookup right after the kill, back to ms after 1 repeat (`--stale-after 1`) or 3–6 (default 5); SEED=4 still misses a live node | dead contacts in other nodes' tables (own shortlists and learning fixed) | **done:** drop failed candidates (Sec. 2.3), stale contacts (Sec. 4.1). Planned: bucket refresh |
| B2 record holders die | **breaks until the next republish** | file unfindable with 2 providers online until the seeder's next republish (13–15 s later with a 15 s interval), which brings it back by itself | records written only at share time (fixed: republish) | **done:** lookup fix, republish (Sec. 2.5). Not yet: expiry |
| B3 late joiners | **breaks until the next republish** | 13 of 16 newcomer `get`s miss; holders alive; the next republish moves the value to them | values move to closer nodes only at a republish | **done:** republish (Sec. 2.5). Not yet: hand-over on join |

**The main finding** (B1 and B2 both show it): a lookup only
ever started from the k closest contacts in the routing table, a contact
that times out is never removed, and failed candidates kept their slots
in the lookup's k-closest list. So once nodes died, lookups returned
**short answers** (live nodes missing), and in the worst case **nothing
at all**, even though live nodes were known. **The lookup half is fixed
on `feature/edge-case-fixes`**: no answer is short any more, and
re-sharing a lost file works. **The learning half is fixed too:** a node
that fails 5 RPCs in a row (`--stale-after`) is skipped, so repeated
lookups get back to milliseconds. Still open: a node that never sends an
RPC to its dead contacts never learns they're dead. It keeps handing
them out in replies, where they can hide a live node (B1, SEED=4). That
needs bucket refresh.

**The second finding** (A3, A4 and B3): nothing maintains the network in
the background. Contacts spread only as a side effect of traffic, and
values stay where they were first stored. A quiet island cut off from
the rest never reconnects (A3). With k = 1, joins reach so few nodes
that lookups stop at nodes with empty buckets (A4). Nodes that join near
a key can't see a value sitting on healthy holders (B3). The paper's
bucket refresh and hourly republish (Sec. 2.3, 2.5) were what was
missing. **Republish is built now** (B2 and B3 recover at the next
republish); bucket refresh is next.

**What holds up:** concurrency. Twenty processes joining through one
node at the same instant, all built with the race detector, gave correct
lookups and no data races (A2).

## Fix priority

Ranked by how much each problem blocks the end goal (README: downloads
survive the seeder leaving, demo across real devices, evaluation graphs),
and by how much else depends on fixing it first.

| Rank | Case | Problem | Why this rank | Fix | Where |
|---|---|---|---|---|---|
| 1 | **B1** | Lookups around dead nodes are wrong (short answers) and slow, and don't improve. **Short answers and not improving: fixed.** Slow right after a death, and dead contacts in other nodes' replies: open | The root cause underneath B2. Every churn experiment runs into it, including the seeder-kill and time-to-recovery graph, and the answers are *wrong*, not just slow. A small change in one function | Drop failed candidates from the k-closest list so the lookup continues to live nodes; remove a contact after repeated RPC failures. For the latency graph: keep α RPCs in flight instead of synchronous rounds | `lookup.go`, `node.go`, `bucket.go` |
| 2 | **B2** | A file becomes unfindable when its record holders die, even with providers online. **Fixed: the next republish brings it back** | Directly the end goal ("downloads keep working when nodes leave") and the recovery graph. Needs fix 1 first, or the republish lookup stalls on the same dead contacts | Republish values hourly; expire them after 24 h (Sec. 2.5) | `store.go`, `values.go` |
| 3 | **B3** | Newcomers near a key can't see its value. **Fixed one interval late by republishing**; hand-over on join: open | Same family as B2, but nothing is lost: the holders are alive. Hourly republishing from fix 2 resolves it over time | Fix 2, plus: when a node meets a newcomer closer to a key it holds, it STOREs the value there (Sec. 2.5) | `node.go` (`touchContact`) |
| 4 | **A3** | A quiet group cut off by one node's death never reconnects | Matters for the multi-device demo (two LANs started separately) and for long-idle networks. Busy networks connect themselves through traffic, so it rarely bites today | Bucket refresh: a lookup for a random ID in every bucket not used for a while (Sec. 2.3) | `node.go` |
| 5 | **A4** | With k = 1, lookups go wrong with nobody dead | Only matters for k ≤ 2. Every demo uses k ≥ 3, where all lookups were correct. Shares its code with fix 4 | After joining, refresh every bucket farther away than the closest neighbor (Sec. 2.3) | `node.go` (`Bootstrap`) |
| — | **A2** | — | Holds up; nothing to fix | — | — |

**Three code changes cover all five.** In order: **(a)** lookup failure
handling (fixes B1, unblocks B2); **(b)** republish and hand-over
(B2, B3); **(c)** bucket refresh, periodic and after joining (A3, A4).

**Progress on `feature/edge-case-fixes`:**
- **(a), part 1, done:** failed candidates give up their places in a
  lookup, and lookups are seeded from the whole routing table. B1's
  "short" prediction and B2's step 4 flipped, and both scripts were
  updated with them.
- **(a), part 2, done:** a node that fails 5 RPCs in a row is stale,
  and lookups and replies skip it (paper Sec. 4.1). B1's "nothing was
  learned" flipped. B1 now repeats its lookups until the querier stops
  asking dead nodes, which measures time to recovery.
- **(b), republish, done:** publishers and holders store their values
  again every `--republish-interval` (paper Sec. 2.5). B2's and B3's
  step 4 (a manual re-share or re-`put`) became "wait for the next
  republish". Hand-over on join (B3 at once, not one interval later) and
  expiry are not built.
- **next:** (c) bucket refresh. Each step's investigation, change and
  evidence is in [FIXES.md](FIXES.md).

Each fix will flip some of these scripts to FAIL. Then update that script's
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
| B1 | Kill half the nodes, then repeat the same lookups until none asks a dead node | +1 RPC timeout per round with a dead contact at first; the querier **learns** (stale contacts) and gets back to milliseconds. No answer is **short** (they were before the lookup fix) | [section](#b1--half-the-network-dies-at-once) |
| B2 | Every node holding a file's provider record dies; the seeder stays up | The file can't be found until the seeder's **next republish** brings it back by itself | [section](#b2--the-provider-record-holders-all-die) |
| B3 | Late joiners with IDs closer to a key than its holders | Newcomers' `get`s miss it, though the holders are alive, until the putter's next republish moves it to them | [section](#b3--late-joiners-closer-to-a-key) |

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
**querier**, looks up 4 random targets, pass after pass:

```
pass 1    all 16 alive                         baseline
          ── 8 random nodes killed at once (SIGKILL) ──
pass 2    8 alive                              right after the kill
pass 3..  8 alive                              the same lookups again, until a
                                               pass has no failed RPC (at most
                                               MAX_PASSES=10 passes after the kill)
```

The passes after the kill are a small **time-to-recovery** measurement:
how long until the survivors stop paying for the dead.
`NODE_FLAGS="--stale-after 1"` runs every node with a different stale
threshold. (The first versions of this script ran exactly 3 passes.)

The querier, the victims and the targets all come from `SEED`. Each
answer is compared with the brute-force 4 closest **live** nodes. Each
lookup is timed on the monotonic clock (`now_ms` in `edge.sh`).

### Prediction

| Pass | Predicted | Why |
|---|---|---|
| 1 | correct, 0 failed RPCs, milliseconds | nothing's dead |
| 2 | failed RPCs; ≥ 2 s for every lookup that meets a dead contact | rounds are synchronous: a round waits for its slowest RPC, and a dead one takes the full `--rpc-timeout` (2 s) ([05](05-iterative-parallel-lookup.md)) |
| 3.. | **the querier learns**: failed RPCs fall pass by pass, and some pass has none (back to milliseconds) | a node that fails `--stale-after` (5) RPCs in a row is stale, and lookups skip it even when others mention it ([02](02-routing-table-and-kbuckets.md#stale-contacts-a-node-that-stops-answering)). Before that fix, pass 3 asked the same dead nodes again (see below) |
| 2.. | **no answer is short**: every lookup that finishes returns k live nodes | a failed candidate gives up its place to the next-closest one (the lookup fix, [05](05-iterative-parallel-lookup.md)). Before it, every answer here came back short (see below) |
| 2.. | *reported only:* an answer can miss a live node, and a lookup can run out of the console's 10 s | dead contacts crowd live ones out of other nodes' replies; many dead contacts mean many 2 s rounds (see below) |

### Real output (`SEED=1`)

With both fixes (the lookup fix and stale contacts):

```
  querier: node10;  to be killed: node5 node7 node9 node11 node12 node13 node14 node15

=== Pass 1: node10 looks up 4 targets, everyone alive ===
  pass 1  6da4c727…  rounds=2  rpcs=4   failed=0      7ms  ok
  pass 1  d4183492…  rounds=3  rpcs=7   failed=0     13ms  ok
  pass 1  64416b78…  rounds=2  rpcs=4   failed=0      8ms  ok
  pass 1  e66a18d1…  rounds=2  rpcs=4   failed=0     11ms  ok
  pass 1  total: 0 failed RPCs, 39ms for 4 lookups, 0 wrong answer(s), 0 of them short, 0 ran out of time

=== Killing 8 of 16 nodes at once (SIGKILL) ===
  killed: node5 node7 node9 node11 node12 node13 node14 node15
  alive:  node0 node1 node2 node3 node4 node6 node8 node10
  node10's routing table: 10 contacts in 4 non-empty buckets, 5 of them now dead

=== Pass 2: the same lookups, right after the kill ===
  pass 2  6da4c727…  rounds=4  rpcs=8   failed=4    4.18s  ok
  pass 2  d4183492…  rounds=6  rpcs=13  failed=7    8.05s  ok
  pass 2  64416b78…  rounds=4  rpcs=8   failed=4    4.07s  ok
  pass 2  e66a18d1…  lookup failed: lookup failed: context deadline exceeded
  pass 2  total: 15 failed RPCs, 26.41s for 4 lookups, 0 wrong answer(s), 0 of them short, 1 ran out of time

=== Pass 3: the same lookups again ===
  pass 3  6da4c727…  rounds=4  rpcs=8   failed=4    4.10s  ok
  pass 3  d4183492…  rounds=6  rpcs=9   failed=4    8.13s  ok
  pass 3  64416b78…  rounds=2  rpcs=4   failed=0      9ms  ok
  pass 3  e66a18d1…  rounds=3  rpcs=7   failed=2    4.05s  ok
  pass 3  total: 10 failed RPCs, 16.30s for 4 lookups, 0 wrong answer(s), 0 of them short, 0 ran out of time

=== Pass 4: the same lookups again ===
  pass 4  6da4c727…  rounds=2  rpcs=4   failed=0      7ms  ok
  pass 4  d4183492…  rounds=3  rpcs=5   failed=1    2.10s  ok
  pass 4  64416b78…  rounds=2  rpcs=4   failed=0    113ms  ok
  pass 4  e66a18d1…  rounds=3  rpcs=5   failed=1    2.11s  ok
  pass 4  total: 2 failed RPCs, 4.33s for 4 lookups, 0 wrong answer(s), 0 of them short, 0 ran out of time

=== Pass 5: the same lookups again ===
  pass 5  6da4c727…  rounds=2  rpcs=4   failed=0      9ms  ok
  pass 5  d4183492…  rounds=3  rpcs=5   failed=1    2.10s  ok
  pass 5  64416b78…  rounds=2  rpcs=4   failed=0      9ms  ok
  pass 5  e66a18d1…  rounds=3  rpcs=5   failed=1    2.10s  ok
  pass 5  total: 2 failed RPCs, 4.22s for 4 lookups, 0 wrong answer(s), 0 of them short, 0 ran out of time

=== Pass 6: the same lookups again ===
  pass 6  6da4c727…  rounds=2  rpcs=4   failed=0      7ms  ok
  pass 6  d4183492…  rounds=3  rpcs=5   failed=1    2.17s  ok
  pass 6  64416b78…  rounds=2  rpcs=4   failed=0    117ms  ok
  pass 6  e66a18d1…  rounds=3  rpcs=5   failed=1    2.09s  ok
  pass 6  total: 2 failed RPCs, 4.39s for 4 lookups, 0 wrong answer(s), 0 of them short, 0 ran out of time

=== Pass 7: the same lookups again ===
  pass 7  6da4c727…  rounds=2  rpcs=4   failed=0      8ms  ok
  pass 7  d4183492…  rounds=2  rpcs=4   failed=0      7ms  ok
  pass 7  64416b78…  rounds=2  rpcs=4   failed=0      7ms  ok
  pass 7  e66a18d1…  rounds=2  rpcs=4   failed=0     10ms  ok
  pass 7  total: 0 failed RPCs, 32ms for 4 lookups, 0 wrong answer(s), 0 of them short, 0 ran out of time

=== Recovery: failed RPCs and time, pass by pass after the kill ===
  pass 2: 15 failed, 26.41s
  pass 3: 10 failed, 16.30s
  pass 4: 2 failed, 4.33s
  pass 5: 2 failed, 4.22s
  pass 6: 2 failed, 4.39s
  pass 7: 0 failed, 32ms

=== Verdict ===
  as predicted: pass 1 had 0 failed RPCs and 0 wrong answers (39ms in total)
  as predicted: pass 2: 4 of 4 lookups hit dead nodes, 15 failed RPCs, 26.41s in total (at least one 2000ms timeout each)
  as predicted: the querier learned: by pass 7 no lookup asked a dead node (32ms for 4 lookups, 55.71s of lookups after the kill)
  as predicted: no answer was short: every lookup that finished returned 4 live nodes
  reported, not asserted: 0 answer(s) missed a live node no survivor mentioned; 1 of 24 lookups after the kill ran out of the console's time

PASS: after half the network died, lookups paid a timeout per dead node, but no answer came back short, and by pass 7 the querier had stopped asking dead nodes.
```

The querier's log shows the learning: one `contact marked stale` line
for each of the 8 dead nodes. Only 5 of them were in its routing table
(`5 of them now dead` above). It knew the other 3 only from other nodes'
replies, which is why failures are counted for every node, not only
table contacts ([FIXES.md](FIXES.md#3-stale-contacts-b1-learning)).

**Seeds 1–4, both stale thresholds** (each run stops at the first pass
with no failed RPC):

| SEED | `--stale-after` | failed RPCs, pass 2 → … | clean at | lookup time after the kill | missed a live node |
|---|---|---|---|---|---|
| 1 | 5 (default) | 15 → 10 → 2 → 2 → 2 → 0 | pass 7 (sometimes 8) | 55.71 s | 0 |
| 2 | 5 | 14 → 14 → 6 → 0 | pass 5 | 43.03 s | 0 |
| 3 | 5 | 15 → 12 → 6 → 0 | pass 5 | 44.97 s | 0 |
| 4 | 5 | 17 → 9 → 1 → 1 → 1 → 0 | pass 7 | 41.87 s | 18 of 24 |
| 1 | 1 | 8 → 0 | pass 3 | 10.54 s | 0 |
| 2 | 1 | 7 → 0 | pass 3 | 10.48 s | 0 |
| 3 | 1 | 7 → 0 | pass 3 | 8.21 s | 0 |
| 4 | 1 | 6 → 0 | pass 3 | 8.26 s | 6 of 8 |

With the paper's threshold of 5, a dead node that only one lookup meets
fails once per pass, so it takes 5 passes from the first meeting. With 1,
every dead node is skipped after its first timeout. Even pass 2 is
cheaper (6–8 failed RPCs instead of 14–17), because the first lookups'
failures spare the later ones. SEED=4's wrong answers don't go away with
either threshold: see "Why SEED=4 misses a live node" below.

### After the lookup fix, before stale contacts

**Seeds 1–4 with only the lookup fix** (exactly 3 passes, 64 lookups after the kill):

| SEED | pass 2 | pass 3 | short | missed a live node | ran out of time |
|---|---|---|---|---|---|
| 1 | 26.45 s, 15 failed | 28.31 s, 8 failed | 0 | 0 | 3 of 8 |
| 2 | 16.43 s, 14 failed | 16.28 s, 14 failed | 0 | 0 | 0 |
| 3 | 20.76 s, 15 failed | 22.11 s, 15 failed | 0 | 0 | 0 |
| 4 | 22.50 s, 17 failed | 22.59 s, 17 failed | 0 | 6 of 8 | 0 |

So the lookup fix made the answers **right** but not **fast**. Before it,
lookups after the kill gave up quickly with short answers. Now they keep
going past every dead contact they meet, at 2 s per round, and take
2–8 s. Three of SEED=1's lookups needed more than the console's 10 s.

### Before the lookup fix (`SEED=1`)

The output this case was built on, with the original `lookup.go`:

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

**Seeds 1–4 (64 lookups after the kill) all gave the same picture:**

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

### Why the answers were short: one lookup, round by round

On the original code, one lookup from `SEED=2` (querier node5, target `62433a6b…`), pass 2,
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

**The fix** does exactly that (`iterativeFind`, `lookup.go`;
[05](05-iterative-parallel-lookup.md)). Candidates are no longer
trimmed. The *working set* is the k closest candidates that haven't
failed, and a failed one gives up its place to the next-closest. The
lookup is also seeded with the whole routing table rather than its k
closest contacts, so it has somewhere to go when those are all dead.
`TestLookupAfterHalfTheNetworkDies` (`lookup_test.go`) is this case in
Go: on the original code it fails with answers of 0–3 nodes, and now it
passes.

### Why SEED=4 misses a live node

With the fix, three of SEED=4's lookups return 4 live nodes, but not
the right 4: `got [2 1 12 11] want [2 1 7 12]`. Nobody ever tells the
querier about node7. A survivor's `table` after the run shows why. node1
and node2 know node7, but their contacts closest to target `f0bbace9…`
are dead. node2's nearest are node5, node8, node14 and node15, all
killed. A `FIND_NODE` reply carries only the k = 4 closest contacts, so
node2's reply is 4 dead nodes, and node7 is never mentioned.

The lookup did its job: it returned the closest live nodes it could hear
of. What's wrong is the network's knowledge. Nodes keep handing out
contacts that are dead.

**Stale contacts don't fix this, and the runs confirm it** (18 of 24
answers still miss node7 at threshold 5, 6 of 8 at threshold 1). The
querier learns which nodes are dead, but node2 doesn't. node2 never
sends an RPC to its dead contacts. It only answers the querier's
requests, so it never counts a failure. What would fix it is traffic
from node2 itself: **bucket refresh** (Sec. 2.3), a lookup for a random
ID in each bucket, which makes node2 test its own contacts.

### What it shows, and what would fix it

After churn the network is **slow** at first: about 2 s per round that
meets a dead contact, against ~10 ms before. It **recovers now**: once
a dead node fails `--stale-after` RPCs in a row, the querier skips it,
and lookups are back to milliseconds (pass 3 with threshold 1, pass 5–8
with 5). It used to be **wrong**, with every answer short. Now no answer
is short, but one can still miss a live node that dead contacts crowd
out of other nodes' replies.

| Problem | Fix | Status | Where | Paper |
|---|---|---|---|---|
| short answers | failed candidates give up their places; seed from the whole table | **done** | `lookup.go` | Sec. 2.3 |
| no learning: the same dead nodes asked on every repeat | stale after N failed RPCs in a row; lookups and replies skip stale nodes | **done** | `stale.go`, `node.go`, `bucket.go`, `lookup.go` | Sec. 4.1 |
| dead contacts in *other* nodes' replies hide a live node (SEED=4) | bucket refresh, so every node tests its own contacts | planned | `node.go` | Sec. 2.3 |
| 2 s per round right after a death, so lookups can run out of time | keep α RPCs in flight instead of synchronous rounds | later | `lookup.go` | Sec. 2.3 |

How each fix was found and checked: [FIXES.md](FIXES.md).

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

The seeder republishes what it published every `REPUBLISH` (default
15 s; [06](06-store-and-find-value.md#republishing-keeping-values-on-the-k-closest-live-nodes)).
Every other node keeps the paper's 1 h, so only the seeder's republish
plays a part.

| Step | What happens | Prediction |
|---|---|---|
| 1 | node2 downloads (control) | works, and node2 becomes a provider |
| 2 | right after one of node1's republishes: kill the 3 holders; ping node1 and node2 | both providers answer |
| 3 | node4 downloads, before node1's next republish | **fails: "no providers found"**, with 2 providers online |
| 4 | nobody does anything until node1's next republish; then node5 downloads | **works**: the republish puts the record on the 3 closest live nodes |

Killing the holders right after a republish leaves step 3 a whole
interval, so it sees the network with no republish in between.

### Real output (`SEED=1`)

```
=== node1 shares a file; its record goes to the 3 nodes closest to the key ===
  content key:            93accc525bce78b07f7868ba330d890537216c3d
  expected holders:       node0 node6 node3  (brute force: the 3 IDs closest to the key)
  holders (from logs):    node0 node3 node6  (nodes that logged "value stored")

=== Step 1 (control): node2 downloads while the holders are alive ===
  node2: fetched "shared-1-1.bin" (262144 bytes, 4 pieces) in 4ms
  node2's copy is identical; node2 is now a provider too

=== Step 2: right after one of node1's republishes, kill the 3 record holders (SIGKILL) ===
  node1 republished its record (every 15s); killing the holders now
  killed node0
  killed node6
  killed node3
  providers still online, pinged from node4:
    PONG from 127.0.0.1:9526 in 719µs
    PONG from 127.0.0.1:9527 in 416µs

=== Step 3: node4 tries to download (predicted: no record found) ===
  node4: fetch failed: transfer: no providers found for 93accc525bce78b07f7868ba330d890537216c3d
  its FIND_VALUE: values_found=0 rounds=3 rpcs_sent=6 rpcs_failed=3
  as predicted: the file is online on 2 nodes, but nobody can find them

=== Step 4: nobody does anything; node1's next republish (predicted: brings it back) ===
  node1 republished 12.86s after step 3 ended
  its republish lookup: rounds=2 rpcs_sent=6 rpcs_failed=3 closest_found=3
  its republish:        values=1 stored_at=2 stored_locally=true failed=0
  as predicted: the republish skipped the dead holders and the record went to the 3 closest live nodes
  node5: fetched "shared-1-1.bin" (262144 bytes, 4 pieces) in 6.076s
  as predicted: node5 found the new record and its copy is identical

PASS: with every record holder dead the file could not be found, though 2 providers were online, until the seeder's next republish brought it back.
```

The record came back with nobody doing anything. node5's download took
6 s, not milliseconds: node5's own lookups still ask the dead holders
first, at 2 s per round. They aren't stale yet for node5, which hasn't
sent them 5 RPCs.

`SEED=2` and `SEED=3` (both of which needed a renamed file) give the same
verdict. Their republishes came 13.01 s and 14.73 s after step 3 ended.

### How step 4 changed with each fix

| Code | Step 4 | Result |
|---|---|---|
| original | node1 shares the file again by hand | **fails**: its lookup stops at the dead holders, and the record stays on node1 alone |
| + lookup fix ([FIXES.md](FIXES.md#2-lookups-skip-dead-candidates-b1-b2)) | node1 shares the file again by hand | works: the lookup skips the dead holders |
| + republishing ([FIXES.md](FIXES.md#4-republishing-b2-b3)) | nobody does anything | works: node1's next republish does the re-share |

### Step 3: why the file can't be found

A downloader finds providers only through `FIND_VALUE(key)`, which asks
the nodes closest to the key. Those are exactly the 3 dead holders. The
record was last written by node1's republish just before the kill, and
nothing copies it anywhere else until the next one. Holders republish
too, but all of them died together. Between republishes, a key whose
holders all die is unfindable. The shorter the interval, the shorter
that gap; the paper's 1 h makes it long, but it's rare for all k
holders to die within an hour (Sec. 2.2: k "is chosen such that any
given k nodes are very unlikely to fail within an hour of each other").

### Step 4: re-sharing didn't help before the lookup fix

Step 4 has flipped twice. The first version of this script predicted
that re-sharing would work, and on the original code it didn't. The fix
for B1 then made it work. Here is what the original code did.

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
showed this across a whole network: every lookup after the kill came
back short.

**The fix** (`lookup.go`, [05](05-iterative-parallel-lookup.md)) deals
with point 1 and with the trimming: a lookup is seeded with the whole
routing table, and a failed candidate gives up its place to the
next-closest one. node1's re-announce lookup now reads `rounds=2 rpcs_sent=6
rpcs_failed=3 closest_found=3`: the 3 dead holders fail, and the next 3
contacts, all alive, take their places. Point 2 (dead contacts stay in
the table) is still true, which is why node5's download takes 6 s.

### What it shows, and what would fix it

A file is online on two nodes but invisible as soon as the few nodes
near its key die together. Churn there is enough to lose a file, even if
the seeder never leaves. **Now it comes back by itself** at the
publisher's next republish, with no manual re-share.

| Fix | Status | Where | Paper |
|---|---|---|---|
| Drop failed candidates from a lookup's shortlist, so live nodes farther out move up | **done** | `lookup.go` (`iterativeFind`) | Sec. 2.3 |
| Seed a lookup from the whole routing table | **done** | `lookup.go` | Sec. 2.3 |
| Stale after repeated RPC failures, so lookups stop asking dead nodes | **done** (each node learns from its own RPCs, so node5's first download still pays) | `stale.go`, `node.go` | Sec. 4.1 |
| Republish (publisher and holders). That makes step 4 automatic | **done** | `republish.go`, `values.go`, `store.go` | Sec. 2.5 |
| Expire values 24 h after publication, so dead providers' records go away | not yet | `store.go`, STORE message | Sec. 2.3 |
| Bucket refresh, so a node learns live replacements for the dead | planned | `node.go` | Sec. 2.3, 2.5 |

**Republish needed the lookup fix first.** The 3 holders died at the
same moment, so none of them could republish. The publisher's own
republish goes through `Store`, the same lookup as step 4, and before
the fix it stopped at the same dead contacts. With the lookup carrying
on past them, republish can now keep records alive as holders die.

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
| 4 | nobody does anything until node1's next republish; the other newcomers `get` it | the value moves to the 2 closest (newcomers); all find it |

node1 (the putter) republishes what it published every `REPUBLISH`
(default 20 s; [06](06-store-and-find-value.md#republishing-keeping-values-on-the-k-closest-live-nodes)),
and every other node keeps the paper's 1 h. The newcomers join right
after one of node1's republishes, so steps 2 and 3 see none.

**Why step 3 should fail.** A `get` is a FIND_VALUE lookup that starts
from the k closest contacts in the querier's own routing table
(`iterativeFind`, `lookup.go`). A newcomer's closest contacts are other
newcomers, since they're closest to the key. They don't have the value,
and they answer with the contacts *they* know closest to the key: more
newcomers. With k = 2, the lookup can run out of unqueried candidates
before it ever reaches a holder. Nothing moves the value toward the
newcomers until the next republish ([06](06-store-and-find-value.md#republishing-keeping-values-on-the-k-closest-live-nodes)),
whose fresh lookup finds them. That is the paper's mechanism too
(Sec. 2.5). The paper also hands a newly joined node the values it's now
closest to, right when it joins (Sec. 2.5). That isn't built, so until
the next republish, newcomers miss the value.

### Real output (`SEED=1`)

```
=== node1 puts a value under SHA-1("b3-1-3") ===
  key:                 0ead8775570a34b61555f59a29dfc0571fe6a88d
  expected holders:    node7 node3  (brute force: the 2 closest IDs)
  holders (from logs): node3 node7
  as predicted: the value landed exactly on the 2 closest nodes

=== Step 1 (control): every other node gets it ===
    node0  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node1  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node2  found 1 value(s) via network: rounds=2 rpcs_sent=4 rpcs_failed=0
    node4  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node5  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node6  found 1 value(s) via network: rounds=2 rpcs_sent=4 rpcs_failed=0
  as predicted: 6 of 6 found it

=== Step 2: right after one of node1's republishes, 4 nodes join, each closer to the key than both holders ===
    node8 joined: id 0eb1a3ae…  distance to key 001c24db…  (seed "edgeb3-late-1-50")
    node9 joined: id 0f11c5bf…  distance to key 01bc42ca…  (seed "edgeb3-late-1-97")
    node10 joined: id 0cc6589c…  distance to key 026bdfe9…  (seed "edgeb3-late-1-207")
    node11 joined: id 0f7c0769…  distance to key 01d1801c…  (seed "edgeb3-late-1-209")
    the bar: holder node7 is at distance 02bbf8c4…  (210 seeds tried)
    the 2 closest nodes to the key are now: node8 node9

=== Step 3: everyone gets it again (the holders are still alive) ===
    node0  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node1  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node2  found 1 value(s) via network: rounds=2 rpcs_sent=4 rpcs_failed=0
    node4  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node5  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node6  found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node8  MISS  0 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node9  MISS  0 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node10 found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node11 MISS  0 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
  original nodes: 6 of 6 found it (reported, not asserted)
  as predicted: 3 of 4 newcomers missed it, though both holders are alive

=== Step 4: nobody does anything; node1's next republish (predicted: moves it to the newcomers) ===
  node1 republished 16.23s after step 3 ended: values=1 stored_at=2 stored_locally=false failed=0
  now stored on: node8 node9 (of the 2 closest: node8 node9 )
  as predicted: the republish reached the 2 closest nodes, which are newcomers
    node10 found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
    node11 found 1 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
  as predicted: 2 of 2 other newcomers find it now

PASS: newcomers closer to the key missed a value whose holders were alive, until the putter's next republish moved it to them.
```

node1 republished 16 s after step 3 ended (its interval is 20 s, and
steps 2 and 3 took about 4 s), with nobody doing anything. Before
republishing existed, step 4 was a manual re-`put` by node1, with the
same result.

**Seeds 1–4:**

| SEED | newcomers that missed (step 3) | original nodes that found it (step 3) | newcomers that found it after step 4 |
|---|---|---|---|
| 1 | 3 of 4 | 6 of 6 | 2 of 2 |
| 2 | 3 of 4 | 6 of 6 | 2 of 2 |
| 3 | 3 of 4 | 6 of 6 | 2 of 2 |
| 4 | 4 of 4 | 6 of 6 | 2 of 2 |

Seeds 1–3 were re-run with republishing and gave the same counts; the
SEED=4 row is from the manual re-`put` version.

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
be blind to it while it sits on two healthy holders. This was the
"values don't move to closer nodes" half of the old "Values never expire
or get republished" simplification. B2 was the other half, "values
vanish when their holders die".

**Republishing fixes it, one interval late.** Unlike B2, nobody is dead
here, so the republish's lookup finds the true 2 closest nodes, and the
value moves to them. Until then, newcomers miss it. Holders republish
too (the Go test `TestHolderRepublishReachesACloserNewcomer` covers
that, with the publisher gone). This script only shortens the putter's
interval, so step 4 is the publisher's pass.

| Fix | Status | Where | Paper |
|---|---|---|---|
| Republish (publisher and holders), so values drift to whoever is closest now | **done** | `republish.go` | Sec. 2.5 |
| When a node learns of a newcomer closer to a key it holds, it STOREs the value there at once, not one interval later | not yet | `node.go` (`touchContact`) | Sec. 2.5 |
| Caching: the searcher stores a found value at the closest node on its path that lacked it | not planned | `values.go` (`FindValue`) | Sec. 2.3 |

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
