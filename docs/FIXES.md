# Fix log: how the edge-case problems are being fixed

[EDGE-CASES.md](EDGE-CASES.md) shows *what* breaks. This document records
*how each fix was made*: what we saw, how we found the cause, what
changed, how we know it worked, and what's still open. Newest last. The
work is on branch `feature/edge-case-fixes`.

Every fix follows the same steps:

1. **Reproduce it**, ideally as a Go test that fails on the old code.
2. **Find the cause from evidence**: logs, routing tables, or the paper.
   Not from guessing.
3. **Make the smallest change** that follows the paper, and label any
   shortcut `SIMPLIFICATION:`.
4. **Check everything:** race tests, all demos, all edge cases. A fix is
   expected to flip some edge-case predictions. When it does, update
   that script's prediction and its EDGE-CASES.md section in the same
   commit, with real output.
5. **Write down what's left.** A fix often exposes the next problem.

| # | Fix | Commit | Kind |
|---|---|---|---|
| 1 | [B3's flaky FAIL](#1-b3s-flaky-fail-a-script-bug) | `1ec8628` | test script |
| 2 | [Lookups skip dead candidates](#2-lookups-skip-dead-candidates-b1-b2) | `de5a42d` | `lookup.go` |
| 3 | [Stale contacts](#3-stale-contacts-b1-learning) | `3606ea6` | `stale.go`, `bucket.go`, `routingtable.go`, `node.go`, `lookup.go` |

---

## 1. B3's flaky FAIL: a script bug

**Seen:** after the swarming merge, one `run-all.sh` run reported B3 as
FAIL. The next 15 runs passed, and the failed run's logs were lost (WSL
clears `/tmp` between commands).

**Reproduced:** a loop of 30 runs (half with the CPU kept busy) that
saves the output and node logs of any failure outside `/tmp`. It caught
5 failures, all at the same check: step 4's re-put "reached the 2
closest nodes", with only one of them listed.

**Cause:** the putter's log showed the DHT had done the right thing:

```
put b3-1-3 (key 0ead8775…): stored on 2 other node(s), locally=false, failed=0
time=… level=INFO msg="rpc sent" … type=PING …     ← a log line, in the middle
    0eb1a3aebfcbdc55a6cc85d006d59b5fe446ffe8@127.0.0.1:9558
    0f11c5bf112711b5369b5a057fa96a9496b4cbe7@127.0.0.1:9559
```

Console output and log lines share one file. The console prints the
header and each contact as separate writes, and a log line from another
goroutine (an eviction PING) landed between them. The script took "the
2 lines after `put`", so it got the log line and one contact. It could
also read the block before the contact lines were written.

**Change:** `put_contacts` in the B3 script skips `time=` lines (as
`lookup_answer` in `edge.sh` already did), and the script waits until it
has as many contacts as the header announces. `demo-parallel.sh` counted
providers the same fragile way and was fixed too.

**Verified:** 30 of 30 runs pass (was 25 of 30).

**Lesson:** parse a console block by its own line shape, and skip
interleaved log lines.

---

## 2. Lookups skip dead candidates (B1, B2)

**Seen:** after 8 of 16 nodes die (B1), every lookup came back
**short**: right nodes in the right order, but live ones missing, once
nobody at all. B2: re-sharing a file whose record holders died didn't
bring it back.

**Reproduced in Go:** `TestLookupAfterHalfTheNetworkDies`
(`lookup_test.go`) closes 8 of 16 nodes and checks that every lookup
returns k live nodes. On the old code it failed with answers of 0–3
nodes.

**Cause** (`iterativeFind`, `lookup.go`), confirmed with a round-by-round
trace of one lookup (EDGE-CASES.md, B1):
- it was seeded with only the k closest contacts in the routing table,
  so if those were dead, the rest of the table was never tried;
- after every round it trimmed its candidates to the k closest, *failed
  ones included*. A dead node kept its place, and the live node that
  should have replaced it had been thrown away.

The paper says the opposite: nodes that fail to respond "are removed
from consideration until and unless they do respond" (Sec. 2.3).

**Change:**
- seed with the whole routing table, nearest first;
- never trim; the *working set* is the k closest candidates that
  haven't failed, and only it is queried;
- converge when the working set has all answered, and return it.

With no failures, a lookup queries exactly the same nodes as before
(`demo.sh`'s round and RPC counts are unchanged).

**Verified:** the Go test passes. B1 has no short answers over seeds 1–4.
B2's step 4 now brings the file back (`closest_found=3`, and node5
downloads it). Both predictions flipped, so the scripts and their
EDGE-CASES.md sections were updated.

**What it exposed:**
- *slower lookups:* the lookup now keeps going past every dead node it
  meets, at 2 s per round, so lookups after the kill took 2–8 s, and 3
  of 8 ran out of the console's 10 s (SEED=1);
- *answers that miss a live node:* with SEED=4, 3 lookups returned 4
  live nodes but not the right 4. Survivors' tables after the run showed
  why: node2 knows the missing node7, but its 4 closest contacts to the
  target are dead, and a reply carries only those 4.

Both come from dead contacts never being forgotten, which is fix 3.

---

## 3. Stale contacts (B1: learning)

**Seen:** after fix 2, B1's querier asked the same dead nodes on every
repeat. Pass 3 was as slow as pass 2.

**The paper** (Sec. 4.1), read from the PDF in the repo rather than from
memory:

> "When a contact fails to respond to 5 RPCs in a row, it is considered
> stale. If a k-bucket is not full or its replacement cache is empty,
> Kademlia merely flags stale contacts rather than remove them. This
> ensures, among other things, that if a node's own network connection
> goes down temporarily, the node won't completely void all of its
> k-buckets."

And unresponsive contacts get no further RPCs for a while.

**First attempt, measured and dropped:** count failures per bucket
entry, i.e. only for contacts in the querier's own routing table. On B1
it barely helped. Even with a threshold of 1, pass 3 still had 10 failed
RPCs. The reason: most dead nodes a lookup meets are ones *other* nodes
mention in their replies. They were never in the querier's table, so
there was nothing to count against.

**Change:**
- `stale.go`: a small tracker (no network I/O, like `bucket.go`) of
  consecutive failed RPCs per node ID, for **every** node we send RPCs
  to. After `--stale-after` failures in a row (default 5, the paper's
  number) a node is stale. Any message from it clears the count. It's
  bounded to 4096 IDs.
- `node.go`: a request that times out counts a failure (`sendAndWait`;
  a cancelled caller doesn't). Every received message clears one
  (`touchContact`).
- `routingtable.go`, `bucket.go`: `Closest` leaves stale contacts out,
  so lookups don't start from them and FIND_NODE replies don't hand them
  out. A stale contact keeps its bucket slot (the paper "flags" it). When
  a new contact arrives for a full bucket, a stale one is replaced on the
  spot, with no ping.
- `lookup.go`: a candidate the tracker says is stale is skipped, even if
  another node mentions it. If *every* table contact is stale, the
  lookup uses them anyway. That's the "own network went down" case the
  paper warns about.
- Visible: log events `contact marked stale`, `stale contact answered
  again`, `stale contact replaced`; the console's `table` marks stale
  contacts; `/api/table` has `"stale": true`.
- `SIMPLIFICATION:` no replacement cache and no exponential backoff
  (README simplifications table).

**Tests:**
- `stale_test.go`:
  - the threshold, reset on a message, the zero ID, turning tracking
    off, the bound;
  - a bucket skipping and replacing a stale contact;
  - `Closest` with and without stale contacts;
  - repeated lookups after deaths reaching 0 failed RPCs;
  - the fallback when every contact is stale.
- `api_test.go`: a dead contact shows `stale: true` after 5 failed
  pings.

**Verified on B1:** B1 now repeats the lookups after the kill until a
pass has no failed RPC. That's a small time-to-recovery measurement:

| | passes until no dead node is asked | lookup time spent getting there |
|---|---|---|
| `--stale-after 5` (default, the paper) | 3–5 more passes (pass 5–7, SEED=1 sometimes 8) | 42–56 s |
| `--stale-after 1` | 1 more pass (pass 3) | 8–10 s |

With a threshold of 1, even pass 2 gets cheaper (6–8 failed RPCs, not
14–17): the first lookup's failures spare the later ones. A threshold of
5 tolerates lost UDP packets (the paper's reason); 1 suits a loss-free
LAN. The flag lets the evaluation compare both.

**Still open:**
- **SEED=4's wrong answers stay.** The survivors that hand out the dead
  contacts never send them an RPC themselves, so they never learn. Only
  traffic from those survivors fixes that: **bucket refresh** (Sec. 2.3),
  plan step 4.
- **The first lookups after a mass death are still slow** (2 s per round
  that includes a dead node), because rounds are synchronous. Keeping α
  RPCs in flight would fix that (README: "Synchronous lookup rounds").
- **B2 still needs a manual re-share:** republishing (Sec. 2.5), plan
  step 3.
