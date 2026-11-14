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
bash scripts/edge/b2-record-holders-die.sh     # env: N=8 BASE_PORT=9525 SEED=1
```

## The catalogue

Status: ✅ script written and passing · ⬜ planned (prediction only, not
yet tested).

| # | Setup | Prediction | Status |
|---|---|---|---|
| **A. How nodes join** | | | |
| A1 | Star: 15 nodes join via node0, then node0 is killed | Lookups stay correct; a new node bootstrapping via the dead hub fails after one RPC timeout | ⬜ |
| A2 | Thundering herd: 20 nodes join via node0 at the same moment | Lookups still correct (tests concurrent bucket updates) | ⬜ |
| A3 | Two islands joined by one bridge node; the bridge is killed | Cross-island lookups work, then never heal (no bucket refresh) | ⬜ |
| A4 | Tiny buckets (k = 1, 2) at N = 20 | Some lookups miss the true closest nodes | ⬜ |
| **B. Churn** | | | |
| B1 | Kill half the nodes, then repeat the same lookups | +1 RPC timeout per round with a dead contact, and the repeat is just as slow (dead contacts are never removed) | ⬜ |
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
until and unless they do respond" (Sec. 2.3).

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
