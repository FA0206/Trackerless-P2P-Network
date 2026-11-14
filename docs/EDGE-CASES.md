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
bash scripts/edge/a3-bridge-between-islands.sh # env: BASE_PORT=9450 (nothing random, no SEED)
bash scripts/edge/b1-half-the-network-dies.sh  # env: N=16 K=4 KILL=8 TARGETS=4 BASE_PORT=9500 SEED=1
bash scripts/edge/b2-record-holders-die.sh     # env: N=8 BASE_PORT=9525 SEED=1
```

**The main finding so far** (B1 and B2 both show it): a lookup only
ever starts from the k closest contacts in the routing table, a contact
that times out is never removed, and failed candidates keep their slots
in the lookup's k-closest list. So once nodes die, lookups return
**short answers** (live nodes missing), and in the worst case **nothing
at all**, even though live nodes are known. Nothing improves on its own.

## The catalogue

Status: ✅ script written and passing · ⬜ planned (prediction only, not
yet tested).

| # | Setup | Prediction | Status |
|---|---|---|---|
| **A. How nodes join** | | | |
| A1 | Star: 15 nodes join via node0, then node0 is killed | Lookups stay correct; a new node bootstrapping via the dead hub fails after one RPC timeout | ⬜ |
| A2 | Thundering herd: 20 nodes join via node0 at the same moment | Lookups still correct (tests concurrent bucket updates) | ⬜ |
| A3 | Two islands joined by one bridge node; the bridge is killed | If nobody used the bridge: split for good. If one lookup crossed it: no harm | ✅ [below](#a3--two-islands-and-one-bridge) |
| A4 | Tiny buckets (k = 1, 2) at N = 20 | Some lookups miss the true closest nodes | ⬜ |
| **B. Churn** | | | |
| B1 | Kill half the nodes, then repeat the same lookups | +1 RPC timeout per round with a dead contact, the repeat is just as slow, and answers come back **short** | ✅ [below](#b1--half-the-network-dies-at-once) |
| B2 | Every node holding a file's provider record dies; the seeder stays up | The file can't be found, and **re-sharing doesn't fix it** | ✅ [below](#b2--the-provider-record-holders-all-die) |
| B3 | Late joiners with IDs closer to a key than its holders | `get` finds nothing: the value never moves to them | ⬜ |
| B4 | Seeder restarts with the same ID on a new port and re-shares | Every download wastes a connection on the stale record | ⬜ |
| **C. Limits and identity** | | | |
| C1 | Hot file: 18 downloaders | Only 16 provider records survive; the original seeder's goes first | ⬜ |
| C2 | Two nodes with the same ID on different ports | Others' contact flips between the two addresses | ⬜ |

**Ports.** Each case has its own block so they can run side by side with
the demos: A1 9400, A2 9425, A3 9450, A4 9475, B1 9500, B2 9525, B3 9550,
B4 9575, C1 9600, C2 9625 (override with `BASE_PORT`).

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
At that point, flip the prediction, the way `demo-seeder-death.sh` flips
on the partial-seeding branch.

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
