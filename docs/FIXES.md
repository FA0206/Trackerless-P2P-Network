# How the edge-case problems were fixed

[EDGE-CASES.md](EDGE-CASES.md) shows **what** went wrong when we stressed
the network. This document explains, for each problem:

- **The problem**, in plain words: what a user of the network would notice.
- **Where it came from**: the exact place in the code that caused it.
- **How we fixed it**: what changed, and why that's the right change.
- **How we know it works**: the test or measurement that proves it.
- **What's still open**: a fix often uncovers the next problem.

At the end, [Where we differ from the paper](#where-we-differ-from-the-paper)
lists every place where these fixes do something other than what the
Kademlia paper says, and why.

All of this is on branch `feature/edge-case-fixes`.

## The five fixes at a glance

| # | Problem in one line | Where it came from | Fix in one line | Commit |
|---|---|---|---|---|
| 1 | Edge case B3 failed about 1 run in 6, for no clear reason | the B3 **test script** read the wrong lines of output | read the output by its shape, skip log lines | `1ec8628` |
| 2 | After nodes died, lookups returned too few nodes, sometimes none | `lookup.go`, `iterativeFind`: dead nodes kept their places in the lookup's shortlist | a dead node gives up its place to the next-closest one | `de5a42d` |
| 3 | Dead nodes were asked again on every lookup, forever | `node.go`: a timeout was forgotten as soon as it happened | remember failures; after 5 in a row, stop asking that node | `3606ea6` |
| 4 | A file vanished when the few nodes holding its record died; new nodes near a key couldn't see its value | `values.go`, `Store`: a value was stored once and never again | every hour, store values again on whoever is closest now | `ffdb038` |
| 5 | Quiet parts of the network never learned about each other | `node.go`, `Bootstrap`, and the lack of any background activity | look up a random ID in each idle bucket, at join and every hour | `5e2ba88` |

Fixes 2–5 are the paper's own mechanisms, which the first version of
this project had left out. Each one fixed something the edge cases
measured, and each one also showed us the next gap.

**A few words used below:**
- **Lookup:** a node asking around for "the k nodes closest to some
  ID". It's how every node finds anything ([05](05-iterative-parallel-lookup.md)).
- **Routing table, bucket, contact:** each node's address book. It's
  split into 160 buckets by distance, and each entry is a contact
  ([02](02-routing-table-and-kbuckets.md)).
- **k:** how many contacts a bucket holds, and how many nodes a lookup
  returns.

---

## 1. B3 failed now and then: a bug in the test script

### The problem

Edge case B3 is a test script. It normally passed, but about once in
every six runs it reported FAIL. A test that fails at random is worse
than no test: when a real fix later changes B3's result, we wouldn't be
able to tell it apart from bad luck.

### Where it came from

Not in the DHT at all. It was in **how the script read the node's
output**.

When a node stores a value, its console prints a header line and then
one line per node it stored on. B3's step 4 took "the 2 lines after the
header" (`grep -A2 "^put …"` in
`scripts/edge/b3-late-joiners-hide-a-value.sh`). But the node also
writes log lines to the same file, from other parts of the program, and
one can land in the middle:

```
put b3-1-3 (key 0ead8775…): stored on 2 other node(s), locally=false, failed=0
time=… level=INFO msg="rpc sent" … type=PING …     ← a log line, in the middle
    0eb1a3aebfcbdc55a6cc85d006d59b5fe446ffe8@127.0.0.1:9558
    0f11c5bf112711b5369b5a057fa96a9496b4cbe7@127.0.0.1:9559
```

The script took the log line and the first contact, missed the second
contact, and decided the value had reached only one node. The node had
done exactly the right thing.

To find this, we ran B3 30 times in a loop, half of them with the CPU
kept busy. Any failed run's output and logs were saved somewhere WSL
doesn't wipe. 5 of 30 failed, all at this same check, and the saved log
showed the stray line.

### How we fixed it

The script now skips anything that looks like a log line (`time=…`).
It also waits until it has as many contacts as the header announced
("stored on 2 other node(s)"). `scripts/demo-parallel.sh` counted
providers the same fragile way, so it got the same fix.

(Later, fix 4 replaced B3's step 4 entirely, so this parsing code is
gone from B3 now. The lesson stays: **console output and log lines share
one file, so parse console blocks by their own shape.**)

### How we know it works

30 of 30 runs passed, against 25 of 30 before.

---

## 2. Lookups came back short after nodes died

### The problem

Kill 8 of 16 nodes (edge case B1), then look things up. Every lookup
returned **fewer nodes than it should**: the right ones in the right
order, but live nodes missing. One lookup found **nobody at all**, while
8 nodes were alive.

That matters well beyond lookups. Storing a value, finding a file and
joining all run on lookups. In edge case B2, even sharing a file again
by hand didn't bring it back after its record holders died.

### Where it came from

`internal/kademlia/lookup.go`, function `iterativeFind`. Two things:

1. **It only started from the k closest contacts in the routing table**
   (`n.rt.Closest(target, n.k)`). If those k were dead, it never tried
   anyone else in the table, even though it knew live nodes.
2. **After every round it threw away everything except the k closest
   candidates, dead ones included**:

   ```go
   // the old code, after each round
   if len(order) > n.k {
       for _, id := range order[n.k:] { delete(candidates, id) }
       order = order[:n.k]
   }
   ```

   A dead node stayed in the shortlist because it was close. The live
   node that should have taken its place was the one thrown away. Once
   everything left had been asked, the lookup stopped, and dropped the
   dead ones from the answer. That left too few nodes.

The paper says the opposite (Sec. 2.3): nodes that fail to respond "are
removed from consideration until and unless they do respond".

### How we fixed it

Think of it as a guest list with k seats. A guest who doesn't show up
should give their seat to the next person in line, not keep it empty.

- **Nothing is thrown away.** The lookup keeps every candidate it has
  heard of, sorted by distance.
- **The "working set" is the k closest candidates that haven't
  failed.** Only those get asked. When one fails, the next-closest
  candidate moves up.
- **It starts from the whole routing table**, not just the k closest.
  Farther contacts cost nothing unless closer ones fail.
- **It's done when everyone in the working set has answered.** That set
  is the answer.

With no dead nodes, the lookup asks exactly the same nodes as before.
`demo.sh`'s numbers didn't change.

### How we know it works

- A new Go test, `TestLookupAfterHalfTheNetworkDies` (`lookup_test.go`),
  closes 8 of 16 nodes and checks that every lookup returns k live
  nodes. On the old code it failed with answers of 0–3 nodes; now it
  passes.
- **B1:** no short answers over seeds 1–4.
- **B2:** sharing the file again by hand now works.

### What it uncovered

- **Lookups got slower right after deaths.** The lookup now keeps going
  past every dead node it meets, and each one costs a 2-second timeout.
  Lookups took 2–8 s, and a few ran out of the console's 10 s limit.
- **An answer could still miss a live node** (B1, SEED=4). We looked at
  the survivors' routing tables. node2 knew the missing node7, but its 4
  closest contacts to the target were all dead, and a reply only carries
  4 contacts. So nobody ever mentioned node7.

Both have the same root cause: **nobody ever forgets a dead contact.**
That's fix 3.

---

## 3. Dead nodes were asked again and again

### The problem

After fix 2, lookups were correct but slow, and **they never got
faster**. Repeat the same lookups and you pay the same timeouts to the
same dead nodes, forever. Like a phone book that keeps the numbers of
people who moved away: you keep calling them.

### Where it came from

`internal/kademlia/node.go`, function `sendAndWait`. When a request
timed out, it logged `rpc timed out` and returned an error, and that
was it. Nothing remembered that this node had failed. The routing table
(`routingtable.go`, `Closest`) kept offering the dead contact to every
lookup, and the node kept putting it in its replies to others.

A dead contact only ever left the table in one narrow case: a new
contact arrived for a full bucket, and a ping to the oldest entry
failed.

### How we fixed it

The paper's rule (Sec. 4.1): "When a contact fails to respond to 5 RPCs
in a row, it is considered stale. … Kademlia merely flags stale
contacts rather than remove them." Flagging instead of deleting matters:
if *your own* network drops for a minute, every contact fails, and you
don't want to wake up with an empty address book.

- **`stale.go` (new):** a small notebook of "failed requests in a row"
  per node. After `--stale-after` failures (default 5, the paper's
  number) the node is **stale**. Any message from it wipes its count:
  it's clearly alive.
- **`node.go`:** a timed-out request adds one failure (`noteFailure`).
  Every received message clears the sender's count (`touchContact`).
- **`routingtable.go`, `bucket.go`:** `Closest` leaves stale contacts
  out. Our lookups don't start from them, and our replies don't hand
  them to others. A stale contact keeps its slot in the bucket. When a
  new contact arrives for a full bucket, a stale one is replaced on the
  spot, with no ping, because we already know it doesn't answer.
- **`lookup.go`:** skips a stale node even when another node's reply
  mentions it. If *every* contact is stale (our own network was
  probably down), it uses them anyway.
- **You can see it:** log events `contact marked stale`,
  `stale contact answered again` and `stale contact replaced`; stale
  contacts are marked in the console's `table`; `/api/table` shows
  `"stale": true`.

**A first attempt that didn't work, and why:** we first counted failures
only for contacts in the node's own routing table. It barely helped.
Even at a threshold of 1, repeat lookups still hit 10 dead nodes. Most
dead nodes a lookup meets are ones *other* nodes mention in their
replies, so they were never in our table and had nothing to count
against. Now every node we send a request to is counted.

### How we know it works

- Tests in `stale_test.go`:
  - the counting rules;
  - a bucket replacing a stale contact;
  - repeated lookups after deaths reaching zero failures;
  - the fallback when every contact is stale.
- `api_test.go` shows a dead contact turning `stale: true`.
- **B1** now repeats its lookups after the kill until none asks a dead
  node, which is a small "time to recover" measurement:

| `--stale-after` | repeats until no dead node is asked | lookup time spent getting there |
|---|---|---|
| 5 (the paper's default) | 3–6 | 42–56 s |
| 1 | 1 | 8–10 s |

5 tolerates the odd lost packet on a real network; 1 suits a reliable
LAN. The flag lets the evaluation compare both.

### What's still open

The querier learns, but the **other** survivors don't. They only answer
requests and never send any to their own dead contacts, so they keep
handing them out. That's the SEED=4 problem from fix 2, and fix 5 deals
with it.

---

## 4. Values got lost, or stayed where nobody looked

### The problem

Two symptoms:

- **B2:** a file's "who has it" record is stored on the 3 nodes closest
  to its key. Kill those 3, and the file **can't be found**, even though
  the seeder that has the whole file is still online.
- **B3:** new nodes join with IDs closer to a key than the current
  holders. Lookups now lead to the newcomers, who don't have the value,
  so **most of them can't find it**, though nothing died.

In both cases, storing the value again by hand fixed it.

### Where it came from

`internal/kademlia/values.go`, function `Store`. A value was sent to
the k closest nodes **once**, at the moment it was stored, and never
again. Its doc comment said so: `SIMPLIFICATION: no republishing`.
Whatever happened to the network afterwards, the value stayed exactly
where it first landed.

### How we fixed it

The paper's rule (Sec. 2.5): store values again periodically, so they
always sit on whoever is closest **now**. Every
`--republish-interval` (default 1 hour), each node does two rounds in
`republish.go`:

- **"Things I published":** every value this node stored itself is
  stored again. `Store` now remembers each one, in `publications`. This
  is what saves B2: all the holders are dead, but the publisher isn't.
- **"Things I hold for others":** every value this node keeps for others
  is stored again, unless someone sent it that value during the last
  interval. If they did, the other holders got it too, so there's no
  need. This keeps a value alive after its publisher leaves.

Each republish starts with a **fresh lookup**. That's what makes it
work: the lookup skips the dead holders (thanks to fixes 2 and 3) and
finds the newcomers (B3), so the value moves to the right nodes.

Smaller changes:
- `store.go` remembers when each key last got a STORE.
- `Store`'s "send to the k closest" part is now `storeOnClosest`, shared
  with republishing.
- `node.go` starts the loop in `Start` and stops it in `Close`.

**One trap avoided:** Go's log format only puts quotes around a message
that contains a space. A message called `republished` would print as
`msg=republished`, while the scripts look for `msg="…"`. So it's
`republish complete`, like the existing `store complete`.

### How we know it works

- Tests in `republish_test.go`, including B2 and B3 rebuilt as Go tests.
- **B2 and B3 no longer need anyone to store anything by hand.** Their
  step 4 is now "wait for the next republish":

| | before the republish | after it, with nobody doing anything |
|---|---|---|
| B2 | file unfindable, 2 copies online | 13–15 s later the record was back on the 3 closest live nodes, and a new node downloaded the file |
| B3 | 3 of 4 newcomers can't find the value | 16–19 s later it was on the 2 newcomers closest to the key, and the rest found it |

(The scripts give only the seeder or putter a short interval, 15 s or
20 s, and time the deaths or joins right after one of its republishes,
so the "before" step still shows the problem.)

### What's still open

- **Between republishes, a key whose holders all died can't be found.**
  The paper accepts that too: it picks k so that k nodes rarely all die
  within an hour.
- **Old records never expire** (see the paper differences below), so a
  record from a provider that left keeps being handed out. A downloader
  wastes one failed connection on it.
- **A newcomer waits for the next republish** to get the values it's now
  closest to. The paper hands them over at join time.

---

## 5. Quiet corners of the network stayed in the dark

### The problem

A node only learns about others when messages pass. Parts of the
network that nobody looks up stay unknown:

- **A3:** two groups of nodes joined by a single "bridge" node. If
  nobody looks anything up across the bridge, the groups never learn
  about each other. When the bridge dies, they're **cut apart for good**.
- **A4:** with k = 1 (a one-entry address book per bucket), a new node
  learns almost nobody when it joins. 9–14 of 20 lookups gave the wrong
  answer, with nothing dead.
- **B1, SEED=4:** survivors never test their own dead contacts (fix 3's
  open item), so they keep handing them out.

### Where it came from

- `internal/kademlia/node.go`, function `Bootstrap`: joining was "ping
  the bootstrap node, look up your own ID, done". A self-lookup mostly
  finds nodes near you, so the far buckets of a new node stayed nearly
  empty.
- **Nothing ran in the background.** A node with no lookups to do sent
  no messages at all, so its buckets never changed.

### How we fixed it

The paper's rule (Sec. 2.3), in `refresh.go`: **refreshing** a bucket
means looking up a random ID that falls in that bucket's range.

- **At join** (end of `Bootstrap`): refresh every bucket farther away
  than your closest neighbor. In a 20-node network that's about 4 extra
  lookups.
- **Every `--refresh-interval`** (default 1 hour): refresh every bucket
  that no lookup has touched during the interval. Every lookup records
  which bucket it targeted, so busy buckets are left alone.

A refresh lookup does three useful things at once:
1. It fills our bucket.
2. Every node it asks learns about us.
3. Its requests test our contacts: dead ones fail and go stale (fix 3).

That third point is what fixes the B1 survivors.

`--join-refresh=false` turns the join refresh off. It exists only so A4
can show the before and after side by side.

### How we know it works

- Tests in `refresh_test.go`:
  - the random IDs land in the right bucket;
  - joining refreshes every bucket it should;
  - **A3 rebuilt in Go:** with refresh, the islands learn each other
    and survive the bridge closing.
- **The edge cases:**

| Case | Before | After |
|---|---|---|
| A3 | an unused bridge's death split the network forever | a third test network refreshes every 3 s: within two refreshes every node on one side knew every node on the other, and the bridge died harmlessly |
| A4 | k = 1: 9–14 of 20 lookups right | 15–20 of 20 with the join refresh |
| A2 | address book right after joining: about 5 contacts | about 12 |
| B1, SEED=4 | survivors hid a live node behind dead contacts | with a 5 s refresh: nothing hidden, and recovery after 2 repeats instead of 3–5 |

Refresh **can't** reconnect the A3 islands *after* the bridge is dead,
because there's no path left. It has to run before. So the refresh
interval decides how long a brand-new bridge is a weak point.

### What's still open

- k = 1 is still imperfect (one contact per bucket and per reply leaves
  no slack). The demos use k ≥ 3.
- Refresh traffic should be counted in the "control bytes" graph.

---

## Where we differ from the paper

The default settings match the paper's numbers: 5 failures to go stale,
1 h republish, 1 h refresh. The edge-case scripts use shorter settings
only so they finish in seconds. Beyond that, these fixes made some
choices of their own. Some are labeled `SIMPLIFICATION:` in the code
(and listed in the README's simplifications table). Others are
**deliberate differences**: the paper says one thing, and we chose
another.

### Deliberate differences (not simplifications)

| Fix | The paper says | We do | Why | What it changes |
|---|---|---|---|---|
| 2 | A lookup "starts by picking α nodes from its closest non-empty k-bucket" (Sec. 2.3) | start from the whole routing table, sorted by distance, and fall back to farther contacts as closer ones fail | the paper doesn't say what to do when the closest contacts are all dead; we needed the lookup to keep going (B1) | nothing when nobody's dead: the first round asks the same α nodes. After deaths, the lookup can reach live nodes the paper's version might never try |
| 3 | stale = 5 failures in a row for a **contact** (a routing-table entry) (Sec. 4.1) | count failures for **every** node we send a request to, in our table or not | most dead nodes a lookup meets were never in our table (they came from other nodes' replies); counting only table entries barely helped (measured) | lookups learn to skip dead nodes they only heard about secondhand. The notebook is capped at 4096 nodes |
| 3 | an unresponsive contact gets no RPCs for "an exponentially increasing backoff interval", then is tried again (Sec. 4.1) | a stale node is never asked again until **it** sends us a message | simpler, and a live node normally messages us again soon (its own lookups and refreshes reach us) | a node that was briefly unreachable, and then never talks to us, stays skipped. The fallback (all contacts stale → use them) still protects a node whose own network dropped |
| 3 | says nothing about stale contacts in replies | leave stale contacts **out of our FIND_NODE replies** | pointing others at a node we know doesn't answer just costs them a timeout | others get fewer dead pointers from us |
| 3 | a full bucket's stale entry is replaced from a **replacement cache** (Sec. 4.1) | replace it with the next new contact that messages us, without a ping | there's no replacement cache (see simplifications) | refilling a bucket waits for the next new contact instead of using a stored spare |
| 4 | the **original publisher** republishes every **24 h**; holders every hour (Sec. 2.3, 2.5) | the publisher republishes at the **same** interval as holders (1 h by default) | one setting is simpler, and it's what lets B2 recover within one interval | more republish traffic from publishers; faster recovery when all holders die |
| 4 | values **expire** 24 h after publication (Sec. 2.3) | values **never expire** | expiry needs each STORE to carry the value's remaining lifetime (a change to the message format); B2 and B3 didn't need it | records of providers that left linger; a downloader wastes one failed connection on each |
| 4 | when a node joins, nodes holding values it's now closest to **send them to it** (Sec. 2.5) | not built; newcomers get values at the next republish | a separate step | newcomers miss nearby values for up to one republish interval (B3) |
| 5 | refresh **any** bucket idle for an hour (Sec. 2.3) | refresh only from the bucket holding our closest neighbor up to the farthest | lower buckets would need a node closer to us than our closest neighbor; in the paper's growing tree those buckets don't exist, so it's the same set | none in practice |
| 5 | the join refresh is part of joining (Sec. 2.3) | the same, but `--join-refresh=false` can turn it off | only so experiment A4 can compare before and after | none unless the flag is used |

### Simplifications these fixes added (labeled in the code)

| Simplification | Where | Paper | Effect |
|---|---|---|---|
| No replacement cache, no exponential backoff | `stale.go` | Sec. 4.1 | see the table above |
| One republish interval for publisher and holders; no expiry | `republish.go`, `store.go` | Sec. 2.3, 2.5 | see the table above |
| Every republish starts with a lookup | `republish.go` | Sec. 2.5 skips it using split buckets | one lookup per key per interval |
| One refresh timer per node, not per bucket | `refresh.go` | Sec. 2.3 | a bucket can wait up to two intervals |

Older simplifications that these fixes ran into, but didn't change:
- **synchronous lookup rounds:** a round waits for its slowest request,
  so a dead node costs 2 s per round;
- **a flat 160-bucket table** with no splitting.

See the README's simplifications table.

---

## What's still open, all together

| What | Shows up as | Paper |
|---|---|---|
| Value expiry | records of departed providers linger | Sec. 2.3 |
| Hand-over on join | newcomers miss nearby values until the next republish (B3) | Sec. 2.5 |
| Lookups that don't wait for a whole round | the first lookups after many deaths take 2 s per round with a dead node (B1) | Sec. 2.3 |
| Exponential backoff for stale nodes | a briefly unreachable node that never messages us again stays skipped | Sec. 4.1 |
