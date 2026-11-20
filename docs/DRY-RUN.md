# Dry runs: what happens inside the demo scripts

- **Part 1** — `scripts/demo.sh`: joining and lookup (below).
- **Part 2** — `scripts/demo-transfer.sh`: STORE/FIND_VALUE and TCP file
  transfer, including killing the seeder ([jump](#part-2--scriptsdemo-transfersh)).
- **Part 3** — `scripts/demo-seeder-death.sh`: the seeder dies while
  every leecher is mid-download, and the file is lost
  ([jump](#part-3--scriptsdemo-seeder-deathsh-the-file-is-lost)).
- **Part 4** — `scripts/demo-big-network.sh`: 20 nodes, random joins,
  multi-round lookups checked by brute force
  ([jump](#part-4--scriptsdemo-big-networksh-multi-round-lookups)).
- **Part 5** (branch `feature/parallel-partial-seeding`) — the same
  seeder-death scenario, now surviving, plus `scripts/demo-parallel.sh`
  ([jump](#part-5--swarming-the-seeder-dies-and-the-file-survives)).
- **Part 6** (branch `feature/multi-device`) — two machines, simulated
  with network namespaces: a node detects its own address, joins, and
  downloads across them
  ([jump](#part-6--two-machines-a-join-and-a-download-across-namespaces)).
- **Part 7** (branch `feature/magnet-links`) — `scripts/demo-magnet.sh`:
  fetching with a magnet link, a link whose name lies, and a refused
  BitTorrent link
  ([jump](#part-7--scriptsdemo-magnetsh-fetching-with-a-link)).
- **Part 8** (branch `feature/magnet-links`) — `scripts/demo-give-up-resume.sh`:
  the seeder dies too early, both leechers give up with the same pieces
  and keep serving them, and fetching again resumes
  ([jump](#part-8--scriptsdemo-give-up-resumesh-giving-up-keeps-seeding-fetching-again-resumes)).
- **Part 9** (branch `feature/magnet-links`) — the visualizer: `vizrun`
  runs nine scenes on 21 live nodes, the page replays and draws them, and
  `scripts/demo-viz.sh` checks a headless run
  ([jump](#part-9--the-visualizer-cmdvizrun--viz)).

> **On branch `feature/parallel-partial-seeding`**, parts 2–4 were
> recorded before parallel downloads existed. The piece-by-piece numbers
> in part 2 (round-robin "0 → 9001, 1 → 9003, …", the 11/5 split) and
> part 3's failure are the *old* behavior. Rerunning `demo-transfer.sh`
> on the branch still passes, but pieces arrive rarest-first and in
> parallel (see part 5 and docs/08). Part 3's script on the branch is the
> "after" version.

Each part traces one real run from the first shell line to the last
process exiting, naming the file and function that runs at each step.
All log lines are real output (timestamps and the repeated `node=` field
stripped, IDs shortened to 8 hex digits). Node IDs come from fixed seeds,
so yours will match. RPC IDs, and in part 2 the file's content key,
differ on every run.

# Part 1 — `scripts/demo.sh`

Read [01](01-ids-and-xor-distance.md)–[05](05-iterative-parallel-lookup.md)
first if a term is unfamiliar; this document shows *when* things happen,
not *why* they're designed that way.

## The cast

| Name | `--id-seed` | Node ID (first 8 hex) | UDP port | Joins via |
|---|---|---|---|---|
| node0 | `demo0` | `2b370ab7` | 9000 | — (first node) |
| node1 | `demo1` | `10f71961` | 9001 | 9000 |
| node2 | `demo2` | `db612776` | 9002 | 9001 |
| node3 | `demo3` | `94fab99d` | 9003 | 9002 |
| node4 | `demo4` | `3fb55a72` | 9004 | 9003, then runs the triggered lookup |

## What this demo shows, and what it doesn't

**Shown:**

1. A node can start alone with an empty routing table (node0).
2. A new node can join knowing only one existing node's `IP:port` (nodes
   1–4, each via a *different* node, never via node0 directly).
3. Knowledge spreads as a side effect of messages. node0 never sends a
   single request, yet ends up knowing all four other nodes.
4. Iterative, α-parallel lookup returns the right answer (node4's lookup
   for node2's ID). The script checks this independently by recomputing
   every ID with `shasum`.
5. Request/reply matching with several RPCs in flight at once (rounds with
   3 parallel `FIND_NODE`s).

**Not shown** (and how to see each by hand — see [DEBUGGING.md](DEBUGGING.md)):

- **Eviction.** With `k = 20` and only 4 other nodes, no bucket ever fills,
  so the ping-then-evict rule never fires. Exercise: run with `--k 1`.
- **Failures during a lookup.** Every node stays up, so `rpcs_failed=0`.
  Exercise: kill a node, then `lookup` from another.
- **Logarithmic scaling.** With 5 nodes and `k = 20`, every lookup ends up
  querying *everybody*, because "the k closest nodes" is the whole network.
  Seeing `O(log n)` rounds needs `n` much larger than `k` (or a small `k`).
  That's the future "latency vs. network size" experiment.
- **Storing or transferring data.** That's part 2.

## Phase 0 — the script sets up (bash only, no Go yet)

`scripts/demo.sh`:

1. `cd` to the repo root and create two temp dirs (binary, logs).
2. `trap cleanup EXIT` — whatever happens, all node processes get killed
   at the end.
3. `go build -o $BIN ./cmd/node` — one binary, started 5 times.

## Phase 1 — node0 starts (nothing to bootstrap against)

```
msg="node started" id=2b370ab73e042befc2c56a925ab511b5116aec98 addr=0.0.0.0:9000
```

Call chain, all inside the node0 process:

```
cmd/node/main.go      run()
                        flag.Parse()
                        resolveID("demo0")            → kademlia.HashID = SHA-1("demo0")
                        kademlia.NewLogger(id, Info)   logging.go
                        kademlia.NewNode(...)          node.go
                          NewTransport(9000)           transport.go: net.ListenUDP on 0.0.0.0:9000
                          NewRoutingTable(id, k=20)    routingtable.go: 160 empty Buckets
                        n.Start()                      node.go: go transport.Listen(n.handleMessage)
                        <-ctx.Done()                   main goroutine now just waits for SIGTERM
```

From here node0 is passive: a background goroutine in `Transport.Listen`
blocks in `ReadFromUDP`, waiting for packets.

## Phase 2 — node1 joins via node0, function by function

node1 starts exactly like node0, then `main.go` sees `--bootstrap
127.0.0.1:9000`, resolves it to a `*net.UDPAddr`, and calls
`n.Bootstrap(ctx, addr)` (`node.go`). This is the most detailed trace in
this document: every later RPC follows the same path.

### 2a. PING: find out who is at 127.0.0.1:9000

```
[node1] msg=bootstrapping via=127.0.0.1:9000
[node1] msg="rpc sent" type=PING to=0000000000000000000000000000000000000000@127.0.0.1:9000 rpc_id=f6ba6d98
[node0] msg="rpc received" type=PING from=10f71961bd11dd33c1c95c771b98cf0e09d57b7c@127.0.0.1:9001 rpc_id=f6ba6d98
[node1] msg="rpc received" type=PONG from=2b370ab73e042befc2c56a925ab511b5116aec98@127.0.0.1:9000 rpc_id=f6ba6d98
[node1] msg="bootstrap node identified" id=2b370ab73e042befc2c56a925ab511b5116aec98 addr=127.0.0.1:9000
```

**On node1 (sender):**

```
node.go       Bootstrap
node.go         sendAndWait(ctx, {ID: 0000…, 127.0.0.1:9000}, MsgPing, nil)
id.go             RandomID()                         → rpc_id f6ba6d98…
node.go           pending[rpc_id] = respCh           (register BEFORE sending, so a fast reply can't be missed)
node.go           Envelope{Type: PING, RPCID, SenderID: node1}
transport.go      Transport.Send                     json.Marshal → prepend 4-byte length → WriteToUDP
node.go           select { <-respCh / <-ctx.Done() } (this goroutine now blocks)
```

**On node0 (receiver):**

```
transport.go  Transport.Listen                       ReadFromUDP returns the datagram
transport.go    decodeEnvelope                       check length prefix == body size, json.Unmarshal
transport.go    go handler(from, env)                each packet handled on its own goroutine
node.go       handleMessage
contact.go      ContactFromUDPAddr(env.SenderID, from) → node1's ID + the address the packet really came from
node.go         touchContact(node1)
routingtable.go   RoutingTable.Update → bucketFor → id.go BucketIndex(node0, node1) = 157
bucket.go         Bucket.Update → not present, not full → append at tail → Seen
node.go         switch env.Type: MsgPing → replyPong
transport.go      Transport.Send(PONG, same rpc_id, SenderID: node0)
```

So node0 learned about node1 *before* replying — the receive path always
records the sender first.

**Back on node1:**

```
transport.go  Listen → decodeEnvelope → go handleMessage
node.go       handleMessage
node.go         touchContact(node0)                 node0 filed under its REAL ID, bucket 157
node.go         MsgPong → deliverResponse            looks up pending[rpc_id] → sends env on respCh
node.go       sendAndWait returns the PONG envelope  → Bootstrap reads pong.SenderID
```

### 2b. Self-lookup

```
[node1] msg="rpc sent" type=FIND_NODE to=2b370ab7…@127.0.0.1:9000 rpc_id=79bff9dd
[node0] msg="rpc received" type=FIND_NODE from=10f71961…@127.0.0.1:9001 rpc_id=79bff9dd
[node1] msg="rpc received" type=FIND_NODE_RESP from=2b370ab7…@127.0.0.1:9000 rpc_id=79bff9dd
[node1] msg="lookup converged" target=10f71961… rounds=1 rpcs_sent=1 rpcs_failed=0 closest_found=1
[node1] msg="bootstrap complete" routing_table_size=1 self_lookup_rounds=1 self_lookup_rpcs=1
```

```
lookup.go     Lookup(ctx, target = node1's own ID)
routingtable.go  Closest(target, 20)                → [node0]  (the only contact)
lookup.go       round 1: batch = [node0]
lookup.go         go FindNode(node0, target)        node.go → sendAndWait(MsgFindNode, {Target})
                  ── on node0: handleMessage → touchContact(node1) (moves it to tail)
                  ──           → replyFindNode → Closest(node1's ID, 20) = [node1]
                  ──           → FIND_NODE_RESP {contacts: [node1]}
lookup.go         addCandidate(node1) → skipped: that's ourselves
lookup.go       sort, trim to k; round 2: nothing unqueried → break
```

Result: 1 round, 1 RPC, closest = [node0]. With only two nodes, there's
nothing else to find.

## Phase 3 — node2 and node3 join

The script waits for `"bootstrap complete"` in each log before starting
the next node (`wait_for_log`), so joins never overlap.

**node2 (via node1).** PING/PONG identifies node1. Lookup round 1 asks
node1, which replies with what it knows: node0 (and node2 itself, which
is skipped). Round 2 asks node0. Result: 2 rounds, 2 RPCs, 2 nodes
found.

```
[node2] msg="rpc sent" type=FIND_NODE to=10f71961…@127.0.0.1:9001   ← round 1
[node2] msg="rpc received" type=FIND_NODE_RESP from=10f71961…
[node2] msg="rpc sent" type=FIND_NODE to=2b370ab7…@127.0.0.1:9000   ← round 2: learned from node1's reply
[node2] msg="rpc received" type=FIND_NODE_RESP from=2b370ab7…
[node2] msg="lookup converged" rounds=2 rpcs_sent=2 rpcs_failed=0 closest_found=2
```

node2 never had node0's address. It got it from node1's reply, and
node0 learned node2 from the request.

**node3 (via node2).** Round 1 asks node2, which now knows node0 and
node1. Round 2 asks both **in parallel** (α = 3 allows up to 3 at once):

```
[node3] msg="rpc sent" type=FIND_NODE to=2b370ab7…@127.0.0.1:9000   ← round 2, both sent
[node3] msg="rpc sent" type=FIND_NODE to=10f71961…@127.0.0.1:9001   ←   before either reply
[node3] msg="rpc received" type=FIND_NODE_RESP from=2b370ab7…
[node3] msg="rpc received" type=FIND_NODE_RESP from=10f71961…
[node3] msg="lookup converged" rounds=2 rpcs_sent=3 rpcs_failed=0 closest_found=3
```

This is where the parallelism is visible. Each send in a round runs in
its own goroutine (`lookup.go`, `go func(c *lookupCandidate)`). The main
loop then reads exactly `len(batch)` outcomes from the `outcomes` channel
before merging.

## Phase 4 — node4 joins, then runs the triggered lookup

node4's join (via node3) is the worked example in
[04-bootstrap-and-join.md](04-bootstrap-and-join.md): round 1 asks
node3, round 2 asks nodes 2, 0, and 1 in parallel. That takes 2 rounds
and 4 RPCs, and node4's table is complete.

Then `main.go`'s `--lookup` goroutine waits `--lookup-after` (300 ms) and
calls `n.Lookup(ctx, node2's ID)`.

**Why the candidates are queried in this order.** The lookup sorts by XOR
distance to the target `db612776…`. The first byte decides it here:

| Candidate | First byte | XOR with `db` | Meaning |
|---|---|---|---|
| node2 | `db` | `00` | distance 0 — it *is* the target |
| node3 | `94` | `4f` = `0100 1111` | top bit clear → closer than anything with top bit set |
| node1 | `10` | `cb` = `1100 1011` | |
| node0 | `2b` | `f0` = `1111 0000` | farthest (`f0 > cb`) |

Round 1 takes the α = 3 closest unqueried: node2, node3, node1.
Round 2 takes the remaining one: node0. No replies contain anything new,
so the loop stops once all four are queried.

```
[node4] msg="rpc sent" type=FIND_NODE to=10f71961…   ┐
[node4] msg="rpc sent" type=FIND_NODE to=db612776…   ├ round 1
[node4] msg="rpc sent" type=FIND_NODE to=94fab99d…   ┘
[node4] msg="rpc received" type=FIND_NODE_RESP …     (×3, in whatever order they arrive)
[node4] msg="rpc sent" type=FIND_NODE to=2b370ab7…   ← round 2
[node4] msg="rpc received" type=FIND_NODE_RESP from=2b370ab7…
[node4] msg="lookup converged" target=db612776… rounds=2 rpcs_sent=4 rpcs_failed=0 closest_found=4
[node4] msg="TRIGGERED LOOKUP RESULT" … closest_ids=db61…,94fa…,10f7…,2b37…
```

`closest_ids` is in exactly the order of the table above.

Notice that node4 already knew all four nodes before this lookup, and it
still queried all four. The termination rule is "the k closest seen have
all been queried", and with `k = 20` the k closest is everyone. That's
correct behavior, and it's why this demo can't show `log n` scaling.

## Phase 5 — the script checks the answer (bash only)

1. `grep "TRIGGERED LOOKUP RESULT"` in node4's log; `extract` pulls out
   `rounds=`, `rpcs_sent=`, `rpcs_failed=`, `closest_ids=`.
2. Expected set: `printf demo0 | shasum -a 1` … for demo0–demo3. That's
   the same SHA-1 the Go code computes, but in a separate program, so the
   check doesn't trust the code it's checking.
3. Sort both lists and compare → `PASS`.
4. "Who learned about whom": counts distinct `from=<id>@` in each log.
   Every node heard directly from all 4 others — including node0, which
   never initiated anything.

## Phase 6 — shutdown

`cleanup` (the `EXIT` trap) runs `kill <pid>` on each node, which sends
SIGTERM:

```
main.go       signal.NotifyContext → ctx is cancelled → <-ctx.Done() returns
main.go         logger.Info("shutting down"); deferred n.Close()
transport.go    conn.Close() → ReadFromUDP fails with net.ErrClosed → Listen returns nil
```

## Final state: real routing tables

Captured with the console's `table` command (see [DEBUGGING.md](DEBUGGING.md))
after all five nodes had joined:

```
node0 (2b370ab7)                                   node4 (3fb55a72)
bucket 159: db612776… (node2), 94fab99d… (node3)   bucket 159: 94fab99d… (node3), db612776… (node2)
bucket 157: 10f71961… (node1)                      bucket 157: 10f71961… (node1)
bucket 156: 3fb55a72… (node4)                      bucket 156: 2b370ab7… (node0)
```

Things to notice:

- **Only 3 of 160 buckets are used.** With random IDs, half of all nodes
  land in bucket 159, a quarter in 158, and so on. Low-numbered buckets
  only fill in huge networks. That's why the flat 160-bucket table costs
  nothing here (see [02](02-routing-table-and-kbuckets.md)).
- **Symmetry.** node0 files node4 in bucket 156, and node4 files node0 in
  bucket 156. `d(x, y) = d(y, x)`, so the bucket index is the same from
  both sides.
- **Bucket order is recency.** In node0's bucket 159, node2 comes before
  node3: node3 was seen more recently. The head of the list is the
  eviction candidate.

---

# Part 2 — `scripts/demo-transfer.sh`

Five nodes again (same IDs as part 1), but started with **`--k 3`**, so
every value lives on only 3 of the 5 nodes and `FIND_VALUE` has to
search. Each node also runs a TCP file server on the same port number as
its UDP socket (TCP and UDP port numbers are separate spaces).

## How the script types into five nodes

Each node's console reads stdin. The script makes stdin a **named pipe**:

```bash
mkfifo node1.in
node --port 9001 ... < node1.in > node1.log &   # the node reads commands from the pipe
exec 4> node1.in                                 # the script holds the write end open on fd 4
printf 'share /path/original.bin\n' >&4          # = typing "share ..." into node1's terminal
```

Holding the write end open matters: if it closed, the node would see
end-of-file and its console would stop. Console output goes into the
same log file as the structured log lines, which is where the script
reads results from.

## Who is "closest" to what

Both demo keys are compared against the five fixed IDs. For the text key
`SHA-1("greeting") = a0f7e779…`, the XOR distance's first byte decides:

| Node | ID starts | `a0 XOR id` | Rank |
|---|---|---|---|
| node3 | `94` | `34` | 1 |
| node2 | `db` | `7b` | 2 |
| node0 | `2b` | `8b` | 3 |
| node4 | `3f` | `9f` | 4 |
| node1 | `10` | `b0` | 5 |

With k = 3, "greeting" belongs on node3, node2, and node0.

One more effect of k = 3 shows up in the joins. node4's self-lookup only
keeps the 3 candidates closest to *its own* ID: node0, node1, node3.
node2 is the farthest (`3f XOR db = e4…`), so node4 never contacts it,
and node2 isn't in node4's routing table at the start. Filling far
buckets like that is the job of the paper's post-join bucket refresh,
which we skip (README, simplifications).

## Phase 1 — `put greeting hello from node0` (node0)

```
console.go     consoleEnv.put        key = kademlia.HashID("greeting") = a0f7e779…
values.go      Node.Store
lookup.go        Lookup(key) → iterativeFind(findValue=false)   1 round, 3 FIND_NODEs
values.go        selfAmongClosest → true (node0 is 3rd closest) → valueStore.put locally,
                 drop the farthest target so the total stays k = 3
node.go          storeRPC × 2, each in its own goroutine
                 ── on node2/node3: handleMessage → replyStore → valueStore.put → STORE_RESP {stored:true}
```

```
[node0] msg="lookup converged" target=a0f7e779… rounds=1 rpcs_sent=3 rpcs_failed=0 closest_found=3
[node0] msg="rpc sent" type=STORE to=94fab99d…@127.0.0.1:9003
[node0] msg="rpc sent" type=STORE to=db612776…@127.0.0.1:9002
[node3] msg="value stored" key=a0f7e779… bytes=16
[node0] msg="store complete" key=a0f7e779… stored_at=2 stored_locally=true failed=0
    put greeting (key a0f7e779…): stored on 2 other node(s), locally=true, failed=0
```

## Phase 2 — `get greeting` (node4)

```
console.go     consoleEnv.get
values.go      Node.FindValue        local store has nothing for this key
lookup.go        iterativeFind(findValue=true)
node.go            findValueRPC × 3 (alpha) to node4's 3 closest known: node3, node0, node1
                   ── node3, node0: replyFindValue → Values = ["hello from node0"]
                   ── node1:        replyFindValue → no values → Contacts (closestExcluding)
lookup.go          appendDistinct merges both copies into one; values found → stop after this round
```

```
[node4] msg="rpc sent" type=FIND_VALUE to=10f71961…@127.0.0.1:9001
[node4] msg="rpc sent" type=FIND_VALUE to=94fab99d…@127.0.0.1:9003
[node4] msg="rpc sent" type=FIND_VALUE to=2b370ab7…@127.0.0.1:9000
[node4] msg="find_value finished" key=a0f7e779… values_found=1 rounds=1 rpcs_sent=3 rpcs_failed=0
    get greeting: 1 value(s) via network: rounds=1 rpcs_sent=3 rpcs_failed=0
        "hello from node0"
```

## Phase 3 — `share original.bin` (node1)

The script writes a random 1 MiB file first, so the content key differs
on every run (it was `5951f7ca…` in this one).

```
console.go     consoleEnv.share
peer.go        Peer.Share
manifest.go      BuildManifest       16 pieces × 64 KiB, SHA-256 each
manifest.go      EncodeManifest → 1146 bytes; ContentKey → 5951f7ca…
library.go       Library.add         node1 can now serve it
peer.go          announce → kademlia Node.Store(key, {"node_id":"10f71961…","ip":"127.0.0.1","tcp_port":9001})
```

```
[node1] msg="lookup converged" target=5951f7ca… rounds=1 rpcs_sent=3 rpcs_failed=0 closest_found=3
[node1] msg="store complete" key=5951f7ca… stored_at=2 stored_locally=true failed=0     ← holders: node1, node0, node4
[node1] msg="provider announced" key=5951f7ca… tcp_addr=127.0.0.1:9001 replicas=3
[node1] msg="share complete" key=5951f7ca… name=original.bin size=1048576 pieces=16
```

## Phase 4 — `fetch <key> node3.bin` (node3), the full TCP path

```
console.go     consoleEnv.fetch
peer.go        Peer.Download
values.go        node.FindValue(key)           round 1 asks node0, node1, node4 — all three are holders
peer.go          decodeProviders               → 1 provider: node1 @ 127.0.0.1:9001
peer.go          downloader.fetchManifest → fetch(start 0, GET_MANIFEST)
peer.go            downloader.conn → client.go dialProvider → net.Dialer.DialContext("tcp4", "127.0.0.1:9001")
                   ══ TCP three-way handshake ══
                   ── node1: server.go serve: Accept() returns → go handleConn
client.go          providerConn.fetch: framing.Write(request) → framing.Read(header) → framing.Read(data)
                   ── node1: handleConn: framing.Read(request) → answer → Library.lookup → writeData
manifest.go        DecodeManifest: ContentKey(bytes) == key ✓, Validate ✓
peer.go          fetchPiecesInto: os.OpenFile(out, O_CREATE|O_EXCL)
                   for i in 0..15: fetch(start i, GET_PIECE i) → Manifest.VerifyPiece(i) → WriteAt(offset)
                   ── node1: answer → Library.readPiece (ReadAt from original.bin)
library.go       Library.add(out)              node3 can now serve it
peer.go          announce                      node3's provider record → the 3 holders
```

The downloader:

```
[node3] msg="find_value finished" key=5951f7ca… values_found=1 rounds=1 rpcs_sent=3 rpcs_failed=0
[node3] msg="providers found" key=5951f7ca… count=1
[node3] msg="tcp connected to provider" provider=127.0.0.1:9001
[node3] msg="piece received" index=0 of=16 from=127.0.0.1:9001 bytes=65536
          … 14 more …
[node3] msg="piece received" index=15 of=16 from=127.0.0.1:9001 bytes=65536
[node3] msg="provider announced" key=5951f7ca… tcp_addr=127.0.0.1:9003 replicas=3
[node3] msg="download complete" … pieces=16 providers=1 failed_providers=0 duration=19ms
```

The provider, at the same moment:

```
[node1] msg="tcp connection accepted" from=127.0.0.1:57976
[node1] msg="tcp request served" to=127.0.0.1:57976 op=GET_MANIFEST index=0 bytes=1146
[node1] msg="tcp request served" to=127.0.0.1:57976 op=GET_PIECE index=0 bytes=65536
          … one line per piece …
```

Notice `from=127.0.0.1:57976`, not `:9003`. The *client* end of a TCP
connection gets a temporary port picked by the OS. A provider therefore
can't learn a downloader's server port from the connection, which is one
reason provider records carry `tcp_port` explicitly.

## Phase 5 — `fetch` again (node4): two providers

node4 is one of the 3 holders of this key, so `FindValue` needs no
network at all:

```
[node4] msg="find_value answered from local store" key=5951f7ca… values_found=2   ← node1's and node3's records
[node4] msg="tcp connected to provider" provider=127.0.0.1:9001
[node4] msg="tcp connected to provider" provider=127.0.0.1:9003
  index=0 from=127.0.0.1:9001
  index=1 from=127.0.0.1:9003
  index=2 from=127.0.0.1:9001      … alternating, 8 + 8
```

`downloader.candidates(i)` starts at provider `i mod 2`, so consecutive
pieces alternate. Each provider has one open connection, reused for all
of its pieces.

## Phase 6 — kill node1, then `fetch` (node2)

The script sends node1 SIGTERM. Its sockets close; its provider record
stays in the DHT (no expiry).

```
[node2] msg="rpc sent" type=FIND_VALUE to=2b370ab7…@127.0.0.1:9000
[node2] msg="rpc sent" type=FIND_VALUE to=10f71961…@127.0.0.1:9001        ← node1: dead
[node2] msg="rpc sent" type=FIND_VALUE to=3fb55a72…@127.0.0.1:9004
[node2] msg="find_value finished" key=5951f7ca… values_found=3 rounds=1 rpcs_sent=3 rpcs_failed=1
[node2] msg="providers found" key=5951f7ca… count=3                        ← node1, node3, node4
[node2] msg="provider failed, trying the next one" provider=127.0.0.1:9001 error="dial tcp4 127.0.0.1:9001: connect: connection refused"
[node2] msg="tcp connected to provider" provider=127.0.0.1:9003
[node2] msg="tcp connected to provider" provider=127.0.0.1:9004
[node2] msg="download complete" … providers=3 failed_providers=1 duration=2.022s
    11 piece(s) from 127.0.0.1:9003
    5 piece(s) from 127.0.0.1:9004
    provider 127.0.0.1:9001 failed and was skipped
```

Reading it:

- **Where the 2 seconds went.** Not the transfer, which takes ~20 ms.
  The `FIND_VALUE` round included dead node1, and a round waits for all
  its RPCs, so it sat out the 2 s RPC timeout. That's the synchronous-rounds
  simplification ([05](05-iterative-parallel-lookup.md)), and exactly
  what a "time to recovery" measurement would pick up. The announce
  afterwards hits node1 again (another 2 s), outside the reported
  duration.
- **Why 11 / 5.** The provider order is `[node1, node3, node4]`. Pieces
  with `i mod 3 = 0` (0, 3, … 15: six pieces) start at dead node1 and
  fall through to node3. node3 also gets its own five (`i mod 3 = 1`), so
  11 in total; node4 gets its five.
- **Connection refused is instant on localhost**, because the OS answers
  for the closed port. Across a real network, a crashed machine would
  instead cost a full dial timeout.
- After the download, node2's announce found that, with node1 gone, node2
  itself is now among the 3 closest to the key (`stored_locally=true`).

## Phase 7 — verification and cleanup

`cmp` compares each downloaded file with the original byte for byte, and
the script prints `PASS`. The `EXIT` trap kills the nodes. Everything
(logs, FIFOs, downloaded files) stays in the printed temp directory.

## What part 2 doesn't show

- **A file with no complete copy anywhere.** Every provider here has the
  whole file. That needs per-piece availability (next step;
  [07](07-file-transfer-over-tcp.md)).
- **Stale records going away.** node1's record would be returned forever
  (no expiry).
- **Parallel downloading.** Pieces arrive one at a time, even from two
  providers.
- **Other machines.** Everything is `127.0.0.1`.

---

# Part 3 — `scripts/demo-seeder-death.sh`: the file is lost

Recorded on `main` at commit `57b06a9`. This is the "before" picture:
branch `feature/parallel-partial-seeding` makes the same scenario
succeed.

Part 2 killed the seeder *after* other nodes had finished downloading,
so complete copies survived. Here the seeder dies while **no one has
finished**. Five nodes on ports 9200–9204, `k = 3`:

| Node | Role |
|---|---|
| node0 | plain DHT node; everyone joins through it |
| node1 | the seeder: shares a 16-piece (1 MiB) file |
| node2, node3, node4 | leechers, started one after another |

## Making "mid-download" reproducible

On localhost a 1 MiB transfer takes ~20 ms, too fast to kill anything
mid-way. So every node runs with **`--debug-serve-delay 150ms`**, a
DEBUG ONLY flag that is off by default: the file server sleeps before
answering each `GET_PIECE` (`server.answer` in
`internal/transfer/server.go`). The script also never sleeps to guess
timing. It watches the logs:

1. `send 2 "fetch …"`; wait until node2 has logged 4 `piece received`.
2. `send 3 "fetch …"`; wait until node3 has 2.
3. `send 4 "fetch …"`; wait until node4 has 1.
4. **Kill rule**: kill the seeder the first moment the leechers
   *together* hold every piece, or the leading leecher holds ¾ of them
   (12), whichever comes first. If anyone has finished by then, the demo
   fails, because the timing assumption broke.

The helper `scripts/lib/nodes.sh` starts each node with a console FIFO
(same trick as part 2, on file descriptor `10 + i`) and kills with
`SIGKILL`, an abrupt death with no goodbye.

## The run

```
=== Waiting for the kill moment ===
  kill moment: the leading leecher has 12 of 16 pieces

=== Killing the seeder (node1, SIGKILL) ===
  node2: fetch failed: transfer: piece 13: transfer: every provider failed
  node3: fetch failed: transfer: piece 8: transfer: every provider failed
  node4: fetch failed: transfer: piece 6: transfer: every provider failed

=== What had spread when the seeder died (from the leechers' logs) ===
  node2: 13/16 pieces  [0-12]
  node3: 8/16 pieces  [0-7]
  node4: 6/16 pieces  [0-5]
  together: 13/16 pieces  [0-12]

=== Verdict (main: the file is expected to be lost) ===
  node2: download failed and left no partial file  (expected)
  ...
PASS: the seeder died before any leecher finished, and the file was lost …
```

## Why "together" is just node2's prefix

On `main`, `downloader.fetchPiecesInto` (`internal/transfer/peer.go`)
asks for pieces `0, 1, 2, …` in order. So every leecher holds a
**prefix** of the file, and the prefixes nest: node4's `[0-5]` ⊂ node3's
`[0-7]` ⊂ node2's `[0-12]`. Three leechers with 27 pieces between them
cover only 13 distinct pieces. Pieces 13–15 existed only on the seeder.
In four runs, the union was 12, 13, 14, and 13 pieces, always the leading
leecher's prefix.

The "together hold every piece" half of the kill rule therefore never
fires on `main`: by the time the union is complete, the leading leecher
is complete too. It's there for the branch, where pieces are fetched
rarest-first and the union *can* be complete while nobody is.

## The seeder's side: three connections, one piece every 150 ms each

```
04:45:09.839 WARN msg="DEBUG: delaying every piece this node serves" delay=150ms
04:45:10.360 INFO msg="share complete" key=91883c4c… name=original.bin size=1048576 pieces=16
04:45:10.507 INFO msg="tcp connection accepted" from=127.0.0.1:61125              ← node2
04:45:10.661 INFO msg="tcp request served" to=127.0.0.1:61125 op=GET_PIECE index=0 bytes=65536
04:45:11.309 INFO msg="tcp connection accepted" from=127.0.0.1:61126              ← node3
04:45:11.721 INFO msg="tcp connection accepted" from=127.0.0.1:61127              ← node4
   …
04:45:12.610 INFO msg="tcp request served" to=127.0.0.1:61126 op=GET_PIECE index=7 bytes=65536
04:45:12.610 INFO msg="tcp request served" to=127.0.0.1:61125 op=GET_PIECE index=12 bytes=65536
04:45:12.709 INFO msg="tcp request served" to=127.0.0.1:61127 op=GET_PIECE index=5 bytes=65536
                                                       ← SIGKILL: no further line, not even "shutting down"
```

Each connection is handled on its own goroutine (`server.handleConn`), so
the three leechers are served in parallel. Each one, though, waits 150 ms
per piece.

## A leecher's side: how the failure unfolds

```
04:45:10.502 INFO msg="providers found" key=91883c4c… count=1              ← only the seeder
04:45:10.504 INFO msg="tcp connected to provider" provider=127.0.0.1:9201
04:45:10.663 INFO msg="piece received" index=0 of=16 from=127.0.0.1:9201 bytes=65536
   …
04:45:12.610 INFO msg="piece received" index=12 of=16 from=127.0.0.1:9201 bytes=65536
04:45:12.717 WARN msg="provider failed, trying the next one" provider=127.0.0.1:9201 error="read response header: EOF"
fetch failed: transfer: piece 13: transfer: every provider failed
```

Function by function, inside node2:

```
peer.go     Peer.Download → kademlia FindValue(key)            1 provider record: the seeder's
            (no other leecher has announced: Peer.announce runs only after a complete download)
peer.go     downloader.fetchPiecesInto: os.OpenFile(node2.bin, O_CREATE|O_EXCL)
              i = 13: downloader.fetch → providerConn.fetch (client.go)
                framing.Read(header) → EOF        the kernel closed the dead seeder's socket
              downloader.markDead(seeder) → candidates(13) is now empty
              → "transfer: every provider failed"
peer.go     fetchPiecesInto's deferred cleanup: f.Close(); os.Remove(node2.bin)
            Library.add and announce never run
console.go  consoleEnv.fetch prints "fetch failed: …"
```

Three rules combine to lose the file:

1. **Only complete files are announced and served.** `Peer.announce` and
   `Library.add` run after the last piece (`Peer.Download`). node2's 13
   verified pieces were never offered to node3 or node4, which needed
   exactly those.
2. **Pieces are fetched in order**, so the leechers' pieces overlap
   instead of covering the file.
3. **A failed download deletes its output** (`fetchPiecesInto`). With no
   resume, the 27 pieces received are simply gone.

The branch removes 1 and 2. Rule 3 stays, but it only bites if a
download really fails.

---

# Part 4 — `scripts/demo-big-network.sh`: multi-round lookups

Parts 1–3 used 5 nodes, where "the k closest" is nearly everyone, so a
lookup ends after a round or two having asked every node. This demo uses
**20 nodes** and **`k = 4`**, on ports 9300–9319. Node 0 starts alone,
and each later node bootstraps through a **randomly chosen node that is
already running**. Here's the join list from the run below
(`SEED=95330`; rerun with that `SEED` to repeat it):

```
joins: node0: first node, node1<-node0, node2<-node1, node3<-node1, node4<-node2, node5<-node1,
       node6<-node0, node7<-node5, node8<-node1, node9<-node6, node10<-node5, node11<-node6, …
```

## How the answers are checked

Four random nodes each look up three targets: `lookup <hex>` typed into
their console. For every answer the script computes the truth itself.
Each node ID is `SHA-1("big<i>")` (the same as `--id-seed big<i>`); the
script XORs each with the target. It does that 8 hex digits at a time,
because bash integers are 64-bit. It sorts the distances as hex strings
(equal-length hex sorts numerically) and keeps the 4 smallest, excluding
the querying node, since a node never returns itself. The console's
answer must be exactly that set.

```
=== Lookups checked against a brute-force answer ===
  node19 -> ea64cae1…: rounds=3 rpcs_sent=7 rpcs_failed=0  ok
  node15 -> 10cd54e8…: rounds=3 rpcs_sent=7 rpcs_failed=0  ok
  …
  12 of 12 lookups needed more than one round
```

All 12 matched in each of 6 runs with different seeds, and also with
`N=30 K=3`.

## One lookup, round by round

node19 looking up `ea64cae1…`. It joined via node15 and its routing
table had 5 contacts:

```
04:49:08.977 msg="rpc sent" type=FIND_NODE to=8d9525b0…@127.0.0.1:9308    ┐ round 1: the α = 3 closest
04:49:08.977 msg="rpc sent" type=FIND_NODE to=bcc19592…@127.0.0.1:9315    │ node19 already knew
04:49:08.977 msg="rpc sent" type=FIND_NODE to=8c2fbba0…@127.0.0.1:9309    ┘
04:49:08.978 msg="rpc received" type=FIND_NODE_RESP from=8d9525b0…   (×3)
04:49:08.979 msg="rpc sent" type=FIND_NODE to=c171dbbe…@127.0.0.1:9300    ┐ round 2: nodes learned
04:49:08.979 msg="rpc sent" type=FIND_NODE to=e321952e…@127.0.0.1:9305    │ from round 1's replies
04:49:08.979 msg="rpc sent" type=FIND_NODE to=f4cd1c48…@127.0.0.1:9304    ┘
04:49:08.980 msg="rpc received" type=FIND_NODE_RESP from=e321952e…   (×3)
04:49:08.980 msg="rpc sent" type=FIND_NODE to=f165d62b…@127.0.0.1:9307    ← round 3: learned in round 2
04:49:08.980 msg="rpc received" type=FIND_NODE_RESP from=f165d62b…
04:49:08.980 msg="lookup converged" target=ea64cae1… rounds=3 rpcs_sent=7 rpcs_failed=0 closest_found=4
lookup ea64cae1…: rounds=3 rpcs_sent=7 rpcs_failed=0
   1. e321952e…@127.0.0.1:9305  distance=09455fcf…
   2. f165d62b…@127.0.0.1:9307  distance=1b011cca…
   3. f4cd1c48…@127.0.0.1:9304  distance=1ea9d6a9…
   4. c171dbbe…@127.0.0.1:9300  distance=2b15115f…
```

What this shows that parts 1–3 couldn't:

- **Round 1's nodes are stepping stones.** `8d…`, `bc…`, `8c…` are at
  XOR distances starting `67…`, `56…`, `66…` from the target `ea…`. None
  of them is in the final answer. Their job was to point at closer
  nodes: every final node starts `09…` to `2b…`.
- **The candidate list moves.** Round 2 asks only nodes node19 learned
  about in round 1. Round 3 asks `f165…`, learned in round 2.
- **It stops on its own.** After round 3, all 4 closest candidates have
  been asked (`iterativeFind`'s stopping rule in `lookup.go`). That took
  7 RPCs, not 19. With 20 nodes the saving is small; it grows with the
  network, because lookups need about `log₂(N/k)` rounds.
- The **round barrier** is visible: round 2 starts only after all three
  round-1 replies are in (the synchronous-rounds simplification, docs
  05). On localhost each round takes ~1 ms, so it costs nothing here.

## The transfer across the network

One random node shares a 1 MiB file; three others fetch it at the same
time. With `k = 4`, the provider record lives on 4 of the 20 nodes, and
each downloader's `FIND_VALUE` has to find it. All three copies matched
the original in every run.

---

# Part 5 — Swarming: the seeder dies and the file survives

Branch `feature/parallel-partial-seeding`: the code is at commits
`f881a4a` and `cae73ad`, the scripts at `463f5e8`. Design and rationale:
[08-swarming.md](08-swarming.md).

## `scripts/demo-seeder-death.sh`, after

Same scenario as part 3: five nodes on 9200–9204, `k = 3`, every node
with `--debug-serve-delay 150ms`, three staggered leechers, and the
seeder SIGKILLed while nobody has finished. Two differences, both
visible in `git diff main feature/parallel-partial-seeding --
scripts/demo-seeder-death.sh`:

1. **The kill rule** is "the first moment the leechers together hold
   every piece". On `main` that moment can't come before someone finishes,
   so `main` had a "leading leecher is ¾ done" fallback. Here the fallback
   would only kill the seeder before the pieces had spread. (An early run
   of the branch script showed exactly that: the fallback fired with the
   union at 14/16, and the file was lost.)
2. **Every node runs `--max-inflight 1`.** Each leecher's single request
   then goes to the rarest piece, one only the seeder has, so the swarm
   runs ahead of every individual leecher. With 4 requests in flight,
   leechers copied each other so fast that "together they hold
   everything" and "someone holds everything" happened in the same
   instant (1 run of 6 failed that way). That's a property of a healthy
   swarm, but it makes "kill the seeder mid-download" unreliable.

```
=== Waiting for the kill moment ===
  kill moment: the leechers together hold all 16 pieces; none has finished ( node2:10/16 node3:6/16 node4:3/16 )

=== Killing the seeder (node1, SIGKILL); the leechers carry on from each other ===
  node2: fetched "original.bin" (1048576 bytes, 16 pieces) in 2.486s
      11 piece(s) from 127.0.0.1:9201          ← the seeder, before it died
      4 piece(s) from 127.0.0.1:9203           ← node3
      1 piece(s) from 127.0.0.1:9204           ← node4
      provider 127.0.0.1:9201 failed and was skipped
  node3: fetched … in 2.632s   (6 from the seeder, 9 from node2, 1 from node4)
  node4: fetched … in 2.555s   (4 from the seeder, 8 from node2, 4 from node3)
...
PASS: the seeder died before any leecher finished and no node had the whole file, yet every leecher completed it from the others.
```

### Before and after, side by side

| | `main` (part 3) | branch (this run) |
|---|---|---|
| Kill moment | leading leecher at 12/16 | leechers together at 16/16 |
| Pieces held when the seeder died | 13 + 8 + 6 = **27**, covering only **13** distinct (`[0-12]`) | 10 + 6 + 3 = **19**, covering all **16** |
| Why | everyone fetched `0, 1, 2, …`: nested prefixes | rarest-first, random ties: each leecher asked the seeder for pieces others lacked |
| Could leechers help each other? | no: only complete files are served or announced | yes: each announced after its first piece and served every verified piece |
| After the kill | all 3: `fetch failed … every provider failed`; partial files deleted | all 3 complete and byte-identical, ~0.8–1.9 s after the kill |

### node4's side: from the seeder, then from its peers

```
05:09:30.474 INFO msg="providers found" key=c79e2d74… count=3             ← seeder, node2, node3 (both announced already)
05:09:30.635 INFO msg="piece received" index=15 of=16 from=127.0.0.1:9201 bytes=65536 have=1 inflight=0
05:09:30.638 INFO msg="provider announced" key=c79e2d74… tcp_addr=127.0.0.1:9204 replicas=3   ← after its FIRST piece
05:09:30.787 INFO msg="piece received" index=1 of=16 from=127.0.0.1:9201 …  have=2
05:09:30.939 INFO msg="piece received" index=4 of=16 from=127.0.0.1:9201 …  have=3
05:09:31.092 INFO msg="piece received" index=10 of=16 from=127.0.0.1:9201 … have=4
05:09:31.172 WARN msg="provider failed, trying the next one" provider=127.0.0.1:9201 error="read response header: EOF"
05:09:31.327 INFO msg="piece received" index=7 of=16 from=127.0.0.1:9202 …  have=5    ← node2
05:09:31.945 INFO msg="piece received" index=13 of=16 from=127.0.0.1:9203 … have=9    ← node3
   …
05:09:33.027 INFO msg="piece received" index=8 of=16 from=127.0.0.1:9203 …  have=16
05:09:33.028 INFO msg="download complete" … providers=3 failed_providers=1 max_inflight=1 duration=2.555s
```

Notice:

- **Pieces 15, 1, 4, 10 first: rarest-first.** node4 asked the seeder
  for pieces that, as far as its fetched bitfields said, only the seeder
  had. Compare `main`, where every leecher starts at 0.
- **No stall after the kill.** node4 already knew node2 and node3, and
  their bitfields said they had what it needed. The very next request
  went to node2.
- **The predicted DHT gaps stayed in the background.** The last line of
  node4's log, a rediscovery `find_value finished … rpcs_failed=1`, is a
  lookup that asked the dead seeder and waited out its RPC timeout. It
  ran in a worker, so no piece waited for it.

### node2 (the first leecher): how it found the others

```
05:09:29.460 INFO msg="providers found" key=c79e2d74… count=1             ← only the seeder at the start
05:09:30.462 INFO msg="new providers found" … added=1 known=2             ← 1 s rediscovery: node3
05:09:31.172 WARN msg="provider failed, trying the next one" provider=127.0.0.1:9201 …
05:09:31.481 INFO msg="new providers found" … added=1 known=3             ← node4, found after the kill
05:09:31.945 INFO msg="piece received" index=2 of=16 from=127.0.0.1:9203 … have=16
```

The seeder's log shows it served 21 pieces (11 + 6 + 4, the
"from 127.0.0.1:9201" counts above) for 16 distinct pieces, plus three
manifests. The 5 extra copies come from leechers working on stale
bitfields; they're polled every 500 ms (a SIMPLIFICATION in docs/08).

Function by function, for one piece in node4 (all in
`internal/transfer/download.go` unless noted):

```
swarmDownload.run            event loop
  schedule → pickPiece (picker.go)    rarest needed piece with an idle holder → (piece 7, node2)
  startPiece                           log "piece requested", lend node2's connection to a worker
    worker: ensureConn → providerConn.fetch(GET_PIECE 7) (client.go) → Manifest.VerifyPiece
            sends event{evPiece, piece 7, data, conn}
  wait → handle(evPiece)               give the connection back, WriteAt(PieceOffset(7)),
                                       have.Set(7), Library.markHave (library.go) ← now servable
  schedule …                           next request to node2
```

## `scripts/demo-parallel.sh`: three providers at once

node1 shares; node2 and node3 download it, so three nodes hold the whole
file; every provider serves with a 150 ms delay. Then node4 downloads.
The script prints node4's timeline:

```
=== node4's timeline (ms since its first piece request) ===
  +    0ms  request  piece  2 -> 127.0.0.1:9402   in flight after: 1
  +    0ms  request  piece 11 -> 127.0.0.1:9401   in flight after: 2
  +    0ms  request  piece  6 -> 127.0.0.1:9403   in flight after: 3
  +  152ms  received piece  2 <- 127.0.0.1:9402   in flight after: 2
  +  152ms  request  piece 15 -> 127.0.0.1:9402   in flight after: 3
  +  154ms  received piece 11 <- 127.0.0.1:9401   in flight after: 2
  +  154ms  request  piece 10 -> 127.0.0.1:9401   in flight after: 3
  +  154ms  received piece  6 <- 127.0.0.1:9403   in flight after: 2
  +  154ms  request  piece  8 -> 127.0.0.1:9403   in flight after: 3
   …
  +  929ms  received piece  9 <- 127.0.0.1:9402   in flight after: 0

  most requests in flight at once: 3   providers used: 3
PASS: node4's copy is identical, built from 3 providers with up to 3 pieces in flight at once.
```

Reading it:

- **Three requests leave at +0 ms**, one per provider. That's the
  per-provider limit of 1 (one connection, replies carry no index) times
  three providers. The total limit of 4 isn't reached.
- **Each provider takes ~150 ms per piece** (the debug delay), and its
  next request goes out the instant its reply arrives (same timestamp).
  The event loop refills slots as soon as a worker reports back.
- **16 pieces in 929 ms**, against ≥ 2.4 s (16 × 150 ms) one at a time:
  about 16/3 rounds.
- **Pieces arrive out of order** (2, 11, 6, 15, 10, 8, …), and each is
  written at its own offset (`WriteAt` in `handle`).

# Part 6 — two machines: a join and a download across namespaces

What changes when the nodes are on different machines
([09](09-multi-device.md)). There's no demo script: this run used the
network-namespace lab from `scripts/netns/`, in its no-sudo sandbox
variant, with two namespaces standing in for two devices. Commands went
through each node's HTTP API ([NODE-API.md](NODE-API.md)) instead of a
console.

## The cast

| Node | Seed | ID | Namespace / address | Started with |
|---|---|---|---|---|
| seeder | `seeder` | `96cfb5fc…` | `p2p1`, `10.10.0.1` | `--advertise-ip 10.10.0.1` |
| leecher | `leecher` | `4bf85c99…` | `p2p2`, `10.10.0.2` | `--bootstrap 10.10.0.1:9000`, **no** `--advertise-ip` |

Both use port 9000: each namespace has its own ports, as separate
machines would.

## Phase 1 — the seeder starts

`run` (`cmd/node/main.go`) calls `chooseAdvertiseIP`. The flag was given
(`flagWasSet`), so `parseAdvertiseIP` checks that `10.10.0.1` is an IPv4
address another machine can dial, and that's the advertise IP.
`NewNode` stores it and notes that it isn't loopback, so
`warnIfAdvertisingLoopback` will never fire. `NewTransport` binds UDP on
every interface:

```
[seeder] msg="node started" id=96cfb5fc… addr=0.0.0.0:9000 tcp_port=9000
[seeder] msg="api listening" addr=10.10.0.1:8000
```

## Phase 2 — the leecher detects its address and joins

`run` parses `--bootstrap` **before** creating the node, because the
advertise IP is fixed when the node is built. `chooseAdvertiseIP` sees
no `--advertise-ip` and a bootstrap address on another machine, so it
calls `routeSourceIP`: a UDP socket "connected" to `10.10.0.1:9000`
sends nothing, but the OS picks the route and the local address it would
send from.

```
[leecher] msg="advertise ip auto-detected" ip=10.10.0.2 via=10.10.0.1:9000
[leecher] msg="node started" id=4bf85c99… addr=0.0.0.0:9000 tcp_port=9000
[leecher] msg=bootstrapping via=10.10.0.1:9000
[leecher] msg="rpc sent" type=PING to=00000000…@10.10.0.1:9000
[seeder]  msg="rpc received" type=PING from=4bf85c99…@10.10.0.2:9000
[leecher] msg="rpc received" type=PONG from=96cfb5fc…@10.10.0.1:9000
[leecher] msg="bootstrap node identified" id=96cfb5fc… addr=10.10.0.1:9000
[leecher] msg="rpc sent" type=FIND_NODE to=96cfb5fc…@10.10.0.1:9000
[leecher] msg="bootstrap complete" routing_table_size=1 self_lookup_rounds=1 self_lookup_rpcs=1
```

The seeder's `handleMessage` builds the sender's contact with
`ContactFromUDPAddr` from the **packet's source address**,
`10.10.0.2:9000`. Nobody told it that address; the packet carried it.
That's the observed address, and it's why the DHT needed no change for
several machines. `warnIfAdvertisingLoopback` returns at once (the
seeder advertises a LAN IP), then `touchContact` and `replyPong` run as
in part 1.

## Phase 3 — `share` on the seeder

`Peer.Share` → `announce` → `Node.Store`: a lookup for the content key
(1 round, 1 RPC), then a `STORE` to the leecher. The record's IP comes
from `Node.Self()`, which is the advertise IP: the self-reported address.

```
[seeder]  msg="lookup converged" target=5813effb… rounds=1 rpcs_sent=1 rpcs_failed=0 closest_found=1
[seeder]  msg="rpc sent" type=STORE to=4bf85c99…@10.10.0.2:9000
[leecher] msg="value stored" key=5813effb… bytes=87
[seeder]  msg="store complete" key=5813effb… stored_at=1 stored_locally=true failed=0
[seeder]  msg="provider announced" key=5813effb… tcp_addr=10.10.0.1:9000 replicas=2
```

Had the seeder been started without `--advertise-ip`, this line would
say `tcp_addr=127.0.0.1:9000`, and the leecher would dial its own
machine ([09](09-multi-device.md), F1).

## Phase 4 — `fetch` on the leecher

With two nodes and k = 20, both hold the record, so `FindValue` answers
from the leecher's own store. `dialProvider` connects over `tcp4` to the
record's address. The seeder sees that connection come from an
OS-chosen port, which is why the record has to state its TCP port
(FAQ, "Why not take the TCP address from the connection itself?").

```
[leecher] msg="find_value answered from local store" key=5813effb… values_found=1
[leecher] msg="providers found" key=5813effb… count=1
[leecher] msg="tcp connected to provider" provider=10.10.0.1:9000
[seeder]  msg="tcp connection accepted" from=10.10.0.2:38400
```

After its first piece, the leecher announces itself as a provider
([08](08-swarming.md)). Its record carries the **detected** address, so
a third machine could now download from it:

```
[leecher] msg="rpc sent" type=STORE to=96cfb5fc…@10.10.0.1:9000
[leecher] msg="provider announced" key=5813effb… tcp_addr=10.10.0.2:9000 replicas=2
[leecher] msg="download complete" key=5813effb… name=original.bin bytes=1048576 pieces=16 providers=1 failed_providers=0 max_inflight=1 duration=5ms
```

The API's reply says `"pieces_from": {"10.10.0.1:9000": 16}`, and `cmp`
found the copy byte-identical. `max_inflight=1`: one provider, and at
most one request per provider.

## What part 6 doesn't show

- The failure cases F1–F4: [09](09-multi-device.md) shows each with real
  logs.
- Several providers, a seeder dying mid-download, and real Wi-Fi: those
  are in 09's test checklist, for the lab and then for real devices.

---

# Part 7 — `scripts/demo-magnet.sh`: fetching with a link

Branch `feature/magnet-links`. The concept is in
[10-magnet-links.md](10-magnet-links.md). The point of this part: **a
magnet link costs nothing on the wire.** It is parsed into the content
key on the downloading node, and from then on the fetch is the same as
one typed with the bare key.

Three nodes with k = 3, so every node is among the k closest to every
key, and every provider record reaches all three. The file's content key
was `637e2199…` in this run. It differs every run, because the file is
random.

## The cast

| Node | Ports | ID (seed) | Role |
|---|---|---|---|
| node0 | 9600 | `2b370ab7…` (`demo0`) | first node; later fetches with a link whose name lies |
| node1 | 9601 | `10f71961…` (`demo1`) | shares `holiday.bin` (200 000 bytes, 4 pieces) |
| node2 | 9602 | `db612776…` (`demo2`) | fetches with the link, then is handed a BitTorrent link |

## Phase 1 — `share holiday.bin` (node1)

```
console.go     consoleEnv.share
peer.go          Peer.Share            BuildManifest → 4 pieces; EncodeManifest → 340 bytes; ContentKey → 637e2199…
peer.go          announce → Node.Store  lookup (1 round, 2 FIND_NODE), then STORE to node0 and node2
console.go       consoleEnv.magnetFor   Library.List → SharedFile.Magnet → MagnetLink(key, "holiday.bin")
```

```
[node1] msg="lookup converged" target=637e2199… rounds=1 rpcs_sent=2 rpcs_failed=0 closest_found=2
[node1] msg="store complete" key=637e2199… stored_at=2 stored_locally=true failed=0
[node1] msg="provider announced" key=637e2199… tcp_addr=127.0.0.1:9601 replicas=3
[node1] msg="share complete" key=637e2199… name=holiday.bin size=200000 pieces=4
shared /tmp/tmp.KwtOD0BkDi/holiday.bin
  content key: 637e2199d6798bd6c983adb58129deb1dfb32242
  magnet link: magnet:?xt=urn:tp2p:637e2199d6798bd6c983adb58129deb1dfb32242&dn=holiday.bin
  others can run: fetch 637e2199d6798bd6c983adb58129deb1dfb32242 <out-path>
```

The `magnet link:` line is new, and the lines around it are unchanged
(the other demos grep `content key:`). The script takes the link with
`awk '{print $3}'`. It's one field because `MagnetLink` escapes the
name, so a link never contains a space.

## Phase 2 — `fetch <link> node2.bin` (node2)

```
console.go   consoleEnv.fetch
magnet.go      ParseKeyOrMagnet    starts with "magnet:" → ParseMagnet
magnet.go        ParseMagnet       url.Parse → Scheme "magnet", RawQuery "xt=urn:tp2p:637e…&dn=holiday.bin"
                                   url.ParseQuery → xt = [urn:tp2p:637e…], dn = [holiday.bin]
                                   one xt, prefix urn:tp2p: → kademlia.IDFromHex → key 637e2199…
                                   (dn "holiday.bin" is returned; ParseKeyOrMagnet drops it)
peer.go        Peer.Download(637e2199…, node2.bin)    from here on, exactly a key-based fetch (docs 07, 08)
```

node2 was one of the k closest in phase 1 (`[node2] msg="value stored"
key=637e2199… bytes=87`), so `FIND_VALUE` is answered from its own store
with no RPC. It then downloads from node1, and after its first piece
announces itself as a provider ([08](08-swarming.md)):

```
[node2] msg="find_value answered from local store" key=637e2199… values_found=1
[node2] msg="providers found" key=637e2199… count=1
[node2] msg="tcp connected to provider" provider=127.0.0.1:9601
[node2] msg="find_value answered from local store" key=637e2199… values_found=1
[node2] msg="piece requested" index=3 from=127.0.0.1:9601 inflight=1
[node2] msg="piece received" index=3 of=4 from=127.0.0.1:9601 bytes=3392 have=1 inflight=0
[node2] msg="piece requested" index=1 from=127.0.0.1:9601 inflight=1
[node2] msg="rpc sent" type=FIND_NODE to=10f71961…@127.0.0.1:9601 rpc_id=349588a2
[node2] msg="rpc sent" type=FIND_NODE to=2b370ab7…@127.0.0.1:9600 rpc_id=2ded9aad
[node2] msg="rpc received" type=FIND_NODE_RESP from=2b370ab7…@127.0.0.1:9600 rpc_id=2ded9aad
[node2] msg="rpc received" type=FIND_NODE_RESP from=10f71961…@127.0.0.1:9601 rpc_id=349588a2
[node2] msg="lookup converged" target=637e2199… rounds=1 rpcs_sent=2 rpcs_failed=0 closest_found=2
[node2] msg="rpc sent" type=STORE to=10f71961…@127.0.0.1:9601 rpc_id=3c806b32
[node2] msg="rpc sent" type=STORE to=2b370ab7…@127.0.0.1:9600 rpc_id=2ccb148b
[node2] msg="piece received" index=1 of=4 from=127.0.0.1:9601 bytes=65536 have=2 inflight=0
[node2] msg="piece requested" index=2 from=127.0.0.1:9601 inflight=1
[node2] msg="rpc received" type=STORE_RESP from=2b370ab7…@127.0.0.1:9600 rpc_id=2ccb148b
[node2] msg="rpc received" type=STORE_RESP from=10f71961…@127.0.0.1:9601 rpc_id=3c806b32
[node2] msg="store complete" key=637e2199… stored_at=2 stored_locally=true failed=0
[node2] msg="provider announced" key=637e2199… tcp_addr=127.0.0.1:9602 replicas=3
[node2] msg="piece received" index=2 of=4 from=127.0.0.1:9601 bytes=65536 have=3 inflight=0
[node2] msg="piece requested" index=0 from=127.0.0.1:9601 inflight=1
[node2] msg="piece received" index=0 of=4 from=127.0.0.1:9601 bytes=65536 have=4 inflight=0
[node2] msg="download complete" key=637e2199… name=holiday.bin bytes=200000 pieces=4 providers=1 failed_providers=0 max_inflight=1 duration=5ms
fetched "holiday.bin" (200000 bytes, 4 pieces) into /tmp/tmp.KwtOD0BkDi/node2.bin in 5ms
    4 piece(s) from 127.0.0.1:9601
```

node1's side of the same TCP connection:

```
[node1] msg="tcp connection accepted" from=127.0.0.1:48044
[node1] msg="tcp request served" to=127.0.0.1:48044 op=GET_MANIFEST index=0 bytes=340
[node1] msg="tcp request served" to=127.0.0.1:48044 op=GET_PIECE index=3 bytes=3392
[node1] msg="tcp request served" to=127.0.0.1:48044 op=GET_PIECE index=1 bytes=65536
[node1] msg="tcp request served" to=127.0.0.1:48044 op=GET_PIECE index=2 bytes=65536
[node1] msg="tcp request served" to=127.0.0.1:48044 op=GET_PIECE index=0 bytes=65536
```

**What node2's fetch sent:**

| | Messages |
|---|---|
| DHT: finding providers | 0 (local store) |
| DHT: announcing itself (lookup + STORE) | 2 `FIND_NODE` + 2 `STORE` |
| TCP | 1 connection: 1 `GET_MANIFEST` (340 bytes) + 4 `GET_PIECE` |

The link itself accounts for none of these. A `fetch 637e2199… node2.bin`
would have sent the same messages.

## Phase 3 — the same link with `dn=lies.txt` (node0)

The script rewrites the link with `sed 's/&dn=.*/\&dn=lies.txt/'`.
`ParseMagnet` returns the name `lies.txt`, and `ParseKeyOrMagnet` drops it
with the rest of the link. By now node2 is a provider too, so node0
finds two:

```
[node0] msg="find_value answered from local store" key=637e2199… values_found=2
[node0] msg="providers found" key=637e2199… count=2
[node0] msg="download complete" key=637e2199… name=holiday.bin bytes=200000 pieces=4 providers=2 failed_providers=0 max_inflight=2 duration=3ms
fetched "holiday.bin" (200000 bytes, 4 pieces) into /tmp/tmp.KwtOD0BkDi/node0.bin in 3ms
    2 piece(s) from 127.0.0.1:9601
    2 piece(s) from 127.0.0.1:9602
```

`name=holiday.bin` comes from the manifest, which was checked against
the key. The link's `dn` never reached the downloader.

## Phase 4 — a BitTorrent link (node2)

The script sends `fetch magnet:?xt=urn:btih:637e2199…&dn=holiday.bin
node2-btih.bin`: the same 40 hex characters, labelled as a BitTorrent
info hash. `ParseMagnet` sees the `urn:btih:` prefix and returns an
error, and `consoleEnv.fetch` prints it and returns before
`Peer.Download`. Here is node2's log from that line to the end, in full:

```
transfer: this is a BitTorrent magnet link (xt=urn:btih:...): it names a torrent in the BitTorrent network, not a file in this one, whose links use xt=urn:tp2p:<key>
[node2] msg="shutting down"
```

No RPC and no TCP connection, and no `node2-btih.bin` was created.

## Phase 5 — verification

`cmp` finds `node2.bin` and `node0.bin` identical to `holiday.bin`.
node0's `fetched` line names `holiday.bin`, and `node2-btih.bin` doesn't
exist:

```
PASS: both links delivered identical copies, the lying name was ignored (the manifest says "holiday.bin"), and the BitTorrent link was refused.
```

## What part 7 doesn't show

- The HTTP API taking a link: [NODE-API.md](NODE-API.md#post-apishare-and-post-apifetch)
  has real `curl` output, and `TestFetchAcceptsMagnetLink` tests it.
- A link carried to another machine: parsing is local, so it's the same
  on any device; [09](09-multi-device.md) covers running across devices.

# Part 8 — `scripts/demo-give-up-resume.sh`: giving up keeps seeding, fetching again resumes

Same branch. The concept is in [08](08-swarming.md#why-giving-up-mustnt-delete-two-leechers-end-with-different-pieces).
The point of this part: **when a download can't finish, it stops
downloading but keeps serving what it has.** So a leecher that is still
copying from it isn't cut off, and both end with the same pieces. Later
the same `fetch` resumes.

node0–node2 serve one piece a second (`--debug-serve-delay 1s`), and the
leechers fetch one piece at a time (`--max-inflight 1`), as in field
test 4. node3, which joins at the end, serves at full speed. The file is
2 MiB, 32 pieces. Its key was `0ac21332…` in this run. The script's own
output, then each phase from the node logs (timestamps cut to the time of
day, `node=` dropped):

```
  node0 killed: node1 has 22, node2 has 2, together 23 of 32
  node1: fetch failed: transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest
  node2: fetch failed: transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest
  fetched "original.bin" (2097152 bytes, 32 pieces) into /tmp/tmp.Q9O0qPW9SR/node1.bin in 2.009s
      9 piece(s) from 127.0.0.1:9653
      23 piece(s) were already on disk (resumed)
      provider 127.0.0.1:9650 failed and was skipped
  both leechers gave up with 23 of 32 pieces
  'files' still lists the partial file on both, at 23/32: they keep serving it
  node1's second fetch resumed from 23 pieces and is byte-identical to the original
PASS: the leechers that lost their seeder ended with the same pieces and kept serving them, and fetching again resumed.
```

## The cast

| Node | Ports | ID (seed) | Role |
|---|---|---|---|
| node0 | 9650 | `2b370ab7…` (`demo0`) | the seeder; killed once node2 has 2 pieces |
| node1 | 9651 | `10f71961…` (`demo1`) | leecher, starts first |
| node2 | 9652 | `db612776…` (`demo2`) | leecher, starts once node1 has 20 pieces |
| node3 | 9653 | `94fab99d…` (`demo3`) | joins at the end, no serve delay, shares the same file |

## Phase 1 — share, two fetches, the kill

node0 shares, and node1 fetches. When node1 has 20 pieces, node2
fetches. Rarest-first sends node2's one request to a piece only the
seeder has. When node2 has 2 pieces, the script SIGKILLs node0. Both
leechers' in-flight requests to it fail at once:

```
21:12:30.198 [node1] msg="provider failed, trying the next one" provider=127.0.0.1:9650 error="read response header: EOF"
21:12:30.198 [node2] msg="provider failed, trying the next one" provider=127.0.0.1:9650 error="read response header: EOF"
```

Together they hold 23 of 32 pieces. The other 9 existed only on node0, so
the file can't be finished.

## Phase 2 — node1 gives up first, and keeps serving

node1 needs only node2's one extra piece. It has it a second later, and
then nothing it needs exists anywhere. Its 15 s clock starts:

```
21:12:31.419 [node1] msg="piece received" index=15 of=32 from=127.0.0.1:9652 bytes=65536 have=23 inflight=0
21:12:31.419 [node1] msg="download stalled: no live provider has the missing pieces; looking for more" have=23 of=32
21:12:46.514 [node1] msg="download gave up, still serving its pieces" key=0ac21332… have=23 of=32 error="transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest"
```

`stopButKeepServing` closes the file but leaves it on disk and in the
library. Before this change, the same moment deleted the file and
removed the library entry.

## Phase 3 — node2 is still copying from node1

node2 needs 21 pieces from node1, one a second (from 21:12:31 on), so
it's still copying when node1 gives up. Its last five pieces all come
from node1 **after** node1 gave up:

```
21:12:47.239 [node2] msg="piece received" index=30 of=32 from=127.0.0.1:9651 bytes=65536 have=19 inflight=0
21:12:48.240 [node2] msg="piece received" index=9 of=32 from=127.0.0.1:9651 bytes=65536 have=20 inflight=0
21:12:49.242 [node2] msg="piece received" index=8 of=32 from=127.0.0.1:9651 bytes=65536 have=21 inflight=0
21:12:50.245 [node2] msg="piece received" index=27 of=32 from=127.0.0.1:9651 bytes=65536 have=22 inflight=0
21:12:51.247 [node2] msg="piece received" index=5 of=32 from=127.0.0.1:9651 bytes=65536 have=23 inflight=0
21:12:51.247 [node2] msg="download stalled: no live provider has the missing pieces; looking for more" have=23 of=32
21:13:06.259 [node2] msg="download gave up, still serving its pieces" key=0ac21332… have=23 of=32 error="transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest"
```

With the old code, the request after 21:12:46.5 would have been refused
(`provider refused: unknown content key`), and node2 would have ended at
18. That's the replay in [08](08-swarming.md#why-giving-up-mustnt-delete-two-leechers-end-with-different-pieces),
and what this script reports on the old code: `FAIL`, with counts like
24 vs 19.

## Phase 4 — `files` on both leechers

```
[node1] 0ac2133235ffc1bab10fd39b1f3d7192248079e6  original.bin  2097152 bytes, has 23/32 pieces  (/tmp/tmp.Q9O0qPW9SR/node1.bin)
[node2] 0ac2133235ffc1bab10fd39b1f3d7192248079e6  original.bin  2097152 bytes, has 23/32 pieces  (/tmp/tmp.Q9O0qPW9SR/node2.bin)
```

Both still list the partial file, so both still serve its 23 pieces.

## Phase 5 — node3 shares the same file; node1 fetches again

node3 joins through node1 and shares the same bytes, so the content key
is the same. Then node1 runs exactly the same `fetch`:

```
21:13:10.462 [node3] msg="provider announced" key=0ac21332… tcp_addr=127.0.0.1:9653 replicas=3
21:13:10.496 [node1] msg="download resumed" key=0ac21332… have=23 of=32
21:13:10.497 [node1] msg="rpc sent" type=FIND_NODE to=…@127.0.0.1:9652        ← the announce's lookup, started at once
21:13:10.497 [node1] msg="rpc sent" type=FIND_NODE to=…@127.0.0.1:9653
21:13:10.497 [node1] msg="rpc sent" type=FIND_NODE to=…@127.0.0.1:9650        ← the dead seeder: 2 s RPC timeout
21:13:10.497 [node1] msg="provider failed, trying the next one" provider=127.0.0.1:9650 error="dial tcp4 127.0.0.1:9650: connect: connection refused"
21:13:10.498 [node1] msg="piece received" index=12 of=32 from=127.0.0.1:9653 bytes=65536 have=24 inflight=0
             … 7 more from node3 …
21:13:10.502 [node1] msg="piece received" index=28 of=32 from=127.0.0.1:9653 bytes=65536 have=32 inflight=0
21:13:12.499 [node1] msg="lookup converged" target=0ac21332… rounds=1 rpcs_sent=3 rpcs_failed=1 closest_found=2
21:13:12.501 [node1] msg="download complete" key=0ac21332… name=original.bin bytes=2097152 pieces=32 providers=3 failed_providers=1 max_inflight=1 duration=2.009s
```

- **Resume.** `Library.claimForResume` found the stopped partial at the
  same path. `Download` skipped the manifest fetch and `O_EXCL`, and
  `reopenFile` re-hashed the 23 pieces on disk (all matched; this is
  silent) before starting with them.
- **The 9 missing pieces** arrived from node3 within 5 ms.
- **The dead seeder.** Its provider record never expires, so it's tried
  and dropped once.
- **node2** is a provider too, but it has nothing node1 needs.
- **The 2 s `duration`.** node1 already had pieces, so its first loop
  pass started the announce again (harmless: the same record) before
  requesting anything. The 2 s is `Download` waiting for that announce,
  whose lookup waited on the dead node0 (`rpcs_failed=1`).

## What part 8 doesn't show

- **Resume after a restart.** The library lives in memory, so a
  restarted node forgets its partial file (a SIMPLIFICATION in
  [08](08-swarming.md#new-simplifications)).
- **A refused resume.** If the partial file was changed or replaced
  after the download gave up, the re-hash refuses the resume and leaves
  the file alone (`TestResumeRefusesAChangedFile`).
- **node2 resuming too.** The same `fetch` on node2 would work the same
  way; the script shows only node1.
- **Real devices.** Field test 4 in `scripts/multi-device/field-tests.html`
  is the same scenario on laptops.

---

# Part 9 — the visualizer (`cmd/vizrun` + `viz/`)

Same branch. The concept is in [11](11-visualizer.md). Scene 8,
give-up/resume, was taken out for a while after it hit a core bug, and
came back once the core was fixed; the runs below include the failures
that found the bug, and runs after the fix. Two kinds of run are traced
here: `vizrun --auto` (headless, what `scripts/demo-viz.sh`
does; ports 9740–9760 / 8740–8760) and a full step-mode run in Chrome
(`go run ./cmd/vizrun`, ports 9700–9720 / 8700–8720). Node IDs are
`SHA-1("viz<i>")`, so yours match: viz0 `34fbf035`, viz7 `0157e7dc`,
viz10 `eb716d9b`, viz11 `617ea758`… RPC IDs, file keys and piece order
differ per run.

## The runner's own output (headless)

```
vizrun: scene 1: No tracker, just peers
vizrun: check: ok join: 20 nodes running, 0 with an empty routing table
vizrun: scene 2: Who you know: k-buckets
vizrun: check: ok buckets: viz0 knows 12 nodes in 4 buckets, none over k=4
vizrun: scene 3: Find anyone in a few hops
vizrun: check: ok lookup: viz7 → viz10: 3 'lookup round starting' events, 'lookup converged' rounds=3, response rounds=3, rpcs_sent=8
vizrun: check: ok lookup: the closest node found is viz10 itself
vizrun: scene 4: Storage with no server
vizrun: check: ok store: viz4 put "greeting" on 3 node(s) (+self: true); viz12 got it back over the network in 2 round(s)
vizrun: scene 5: Share a file, get a magnet link
vizrun: check: ok share: viz11 shared lecture-notes.bin (32 pieces), key 4cc38429
vizrun: scene 6: Swarming
vizrun: scene 7: Kill the seeder. The file survives.
vizrun: check: ok kill: SIGKILL viz11: the leechers together hold all 32 pieces, and none has finished map[viz15:10 viz3:17 viz9:12]
vizrun: check: ok sha256: viz3's copy matches the original (c260ed875d41c510…)
vizrun: check: ok sha256: viz9's copy matches the original (c260ed875d41c510…)
vizrun: check: ok sha256: viz15's copy matches the original (c260ed875d41c510…)
vizrun: scene 8: Giving up keeps seeding; fetching again resumes
vizrun: check: ok give up: viz6 and viz17 both stopped short and kept their pieces: 11 and 11 of 16
vizrun: check: ok sha256: viz6's copy matches the original (5b0d2e6744318b0a…)
vizrun: check: ok resume: viz6's second fetch logged 'download resumed'
vizrun: scene 9: Live, not faked
vizrun: check: ok events recorded=4243 dropped=0 (dropped must be 0)
vizrun: PASS
```

Then `vizrun: stopped 21 node(s)`, and `ss -lunt` shows none of the
ports. The lines below are from that run's `events.jsonl` (one JSON
object per event, written here as log lines; `[viz7]` is the stream it
came on).

## Scene 1 — a join, seen from the receiving side

viz1 joins through viz0. Its own stream isn't open yet (it joins within
milliseconds of starting), so the join is what **viz0** logged:

```
23:48:02.234 [viz0] msg="rpc received" from=749d0d9d…@127.0.0.1:9741 rpc_id=73fcc6e8 type=PING
23:48:02.234 [viz0] msg="rpc received" from=749d0d9d…@127.0.0.1:9741 rpc_id=a35eedc5 type=FIND_NODE
```

A PING, then the FIND_NODE of viz1's self-lookup: `Bootstrap` in
`node.go`. In the page these become two comets viz1 → viz0, then lines
from viz1 to the contacts in its `/api/table`. Each later newcomer that
asked viz0 shows up the same way, half a second apart
(`23:48:02.744 [viz0] … from=af50cd66…` is viz2's self-lookup).

## Scene 3 — the lookup the page replays

viz7 looks up viz10 (`eb716d9b`), the node farthest from it that isn't in
its table. Every request appears twice, once on each side, with the same
`rpc_id` (replies, logged by viz7 as `FIND_NODE_RESP`, are left out
here):

```
23:48:11.881 [viz7]  msg="lookup round starting" batch=3 round=1 target=eb716d9b…
23:48:11.881 [viz7]  msg="rpc sent" rpc_id=f3624a44 to=5ca89cdd…@127.0.0.1:9752 type=FIND_NODE
23:48:11.881 [viz7]  msg="rpc sent" rpc_id=b8e73b2c to=4fe02097…@127.0.0.1:9749 type=FIND_NODE
23:48:11.881 [viz7]  msg="rpc sent" rpc_id=21b18621 to=749d0d9d…@127.0.0.1:9741 type=FIND_NODE
23:48:11.882 [viz12] msg="rpc received" from=0157e7dc…@127.0.0.1:9747 rpc_id=f3624a44 type=FIND_NODE
23:48:11.882 [viz9]  msg="rpc received" from=0157e7dc…@127.0.0.1:9747 rpc_id=b8e73b2c type=FIND_NODE
23:48:11.882 [viz1]  msg="rpc received" from=0157e7dc…@127.0.0.1:9747 rpc_id=21b18621 type=FIND_NODE
23:48:11.883 [viz7]  msg="lookup round starting" batch=3 round=2 target=eb716d9b…
23:48:11.883 [viz7]  msg="rpc sent" rpc_id=488562e5 to=af50cd66…@127.0.0.1:9742 type=FIND_NODE
23:48:11.883 [viz7]  msg="rpc sent" rpc_id=9c00cc41 to=e32ed232…@127.0.0.1:9748 type=FIND_NODE
23:48:11.883 [viz7]  msg="rpc sent" rpc_id=a0c594c0 to=a9853e91…@127.0.0.1:9745 type=FIND_NODE
23:48:11.884 [viz7]  msg="lookup round starting" batch=2 round=3 target=eb716d9b…
23:48:11.884 [viz7]  msg="rpc sent" rpc_id=280fc741 to=c8affceb…@127.0.0.1:9756 type=FIND_NODE
23:48:11.884 [viz7]  msg="rpc sent" rpc_id=e5cc4044 to=eb716d9b…@127.0.0.1:9750 type=FIND_NODE
23:48:11.885 [viz7]  msg="lookup converged" closest_found=4 rounds=3 rpcs_sent=8 rpcs_failed=0 target=eb716d9b…
```

Four milliseconds. `rpc.js` turns the window's 50 events into these
steps (printed by running it under Node on that `events.jsonl`):

```
round 1
launch FIND_NODE viz7->viz12 viz7->viz1 viz7->viz9
reply            viz7->viz12 viz7->viz9 viz7->viz1
round 2
launch FIND_NODE viz7->viz8 viz7->viz2 viz7->viz5
reply            viz7->viz2 viz7->viz8 viz7->viz5
round 3
launch FIND_NODE viz7->viz16 viz7->viz10
reply            viz7->viz10 viz7->viz16
launch PING      viz7->viz2
label  lookup converged
reply            viz7->viz2
```

Three rounds, as `lookup converged` says; the page shows `✓ replayed
rounds match 'lookup converged'`. Round 3 reaches viz10 itself. The PING
near the end isn't part of the lookup: viz7's bucket was full when a
reply introduced a new contact, so it pinged its oldest one (the
eviction check, [02](02-routing-table-and-kbuckets.md)). It's real
traffic, so it's drawn.

## Scenes 6–7 — the kill and the finish

The runner polls its own record of `piece received` every 50 ms. At the
first moment the three leechers together hold all 32 pieces (17 + 12 +
10, overlapping), it SIGKILLs viz11. All three in-flight requests to it
fail in the same millisecond:

```
23:48:30.656 [viz15] msg="provider failed, trying the next one" error="read response header: EOF" provider=127.0.0.1:9751
23:48:30.656 [viz3]  msg="provider failed, trying the next one" error="read response header: EOF" provider=127.0.0.1:9751
23:48:30.656 [viz9]  msg="provider failed, trying the next one" error="read response header: EOF" provider=127.0.0.1:9751
23:48:45.696 [viz3]  msg="download complete" bytes=2097152 duration=32.103s failed_providers=1 … pieces=32 providers=3
23:48:50.708 [viz9]  msg="download complete" bytes=2097152 duration=33.082s failed_providers=1 … pieces=32 providers=3
23:48:52.711 [viz15] msg="download complete" bytes=2097152 duration=33.067s failed_providers=1 … pieces=32 providers=3
```

viz9 is one of the 4 nodes closest to the file's key, so its once-a-second
rediscovery is answered from its own store, and the record count there
grows as the others announce: `find_value answered from local store …
values_found=2`, then `=3`, then `=4`.

In the browser run, the `/api/fetch` answers split the pieces like this
(the page writes them under each grid): viz3 got 8 from viz9, 18 from
viz11, 6 from viz15; viz9 got 9 from viz3, 14 from viz11, 9 from viz15;
viz15 got 14 from viz3, 5 from viz9, 13 from viz11. Every copy's SHA-256
matched.

## Scene 8 — give up, keep serving, resume

viz13 shares 16 pieces; it is SIGKILLed when the second leecher has 2.

```
23:49:06.812 [viz17] msg="provider failed, trying the next one" error="read response header: EOF" provider=127.0.0.1:9753
23:49:06.813 [viz6]  msg="provider failed, trying the next one" error="read response header: EOF" provider=127.0.0.1:9753
23:49:06.813 [viz6]  msg="download stalled: no live provider has the missing pieces; looking for more" have=10 of=16
23:49:08.339 [viz6]  msg="download stalled: no live provider has the missing pieces; looking for more" have=11 of=16
23:49:14.827 [viz17] msg="download stalled: no live provider has the missing pieces; looking for more" have=10 of=16
23:49:15.828 [viz17] msg="download stalled: no live provider has the missing pieces; looking for more" have=11 of=16
23:49:23.437 [viz6]  msg="download gave up, still serving its pieces" … have=11 of=16 error="transfer: stalled for 15s with 11 of 16 pieces: …"
23:49:30.891 [viz17] msg="download gave up, still serving its pieces" … have=11 of=16 error="transfer: stalled for 15s with 11 of 16 pieces: …"
23:49:30.956 [viz6]  msg="download resumed" have=11 key=eba42e20… of=16
23:49:30.956 [viz6]  msg="provider failed, trying the next one" error="dial tcp4 127.0.0.1:9753: connect: connection refused" provider=127.0.0.1:9753
23:49:30.962 [viz6]  msg="download complete" bytes=1048576 duration=8ms failed_providers=1 … pieces=16 providers=3
```

viz6 stalled at 10, got viz17's one extra piece (`have=11`), and stalled
again: each stall is logged afresh, because the node resets its 15 s
clock whenever it can progress (`download.go` `run`). That's why the
page's countdown restarts on each `download stalled…` event and stops
when a piece arrives. Then viz20 (no serve delay) shared the same bytes,
and the same fetch resumed with 11 pieces on disk and finished in 8 ms.

**A run that failed, and what it showed.** In an earlier version, scene 8
always used viz6 and viz17. In one run viz6 was among the 4 nodes closest
to the file's key, so it held a provider record itself. Its second fetch
was answered from its own store, saw only the dead viz13's record, and
failed at once:

```
23:47:04.161 [viz20] msg="provider announced" key=6060f440… tcp_addr=127.0.0.1:9760 replicas=3
23:47:04.162 [viz6]  msg="find_value answered from local store" key=6060f440… values_found=1
```

viz20's record went to 3 nodes, but viz6 never asked them. Picking
leechers that hold no record wasn't enough: in two later runs the chosen
leechers (viz5 and viz2; then viz4) got records anyway, from their own
announces:

```
23:58:06.892 [viz5] msg="store complete" key=75f66e91… stored_at=… stored_locally=true
23:58:14.936 [viz2] msg="store complete" key=75f66e91… stored_at=… stored_locally=true
23:58:36.095 [viz20] msg="lookup converged" closest_found=2 rounds=3 rpcs_failed=2 rpcs_sent=7 target=75f66e91…
23:58:36.097 [viz5] msg="find_value answered from local store" key=75f66e91… values_found=1
```

viz11 (dead since scene 7) was among the 4 IDs closest to that key, so
lookups for it ended with fewer than 4 live nodes (`closest_found=2`),
and `selfAmongClosest` counts "fewer than k found" as "I'm among the
closest": every announcer kept its own record, and viz5's fetch, seeing
only that, failed with `no providers found`. The runner now shares fresh
bytes until the key has no dead node among its k closest, and picks the
resuming leecher at resume time
([11](11-visualizer.md#the-scenes-as-built)). In that same run the
two leechers also gave up unequal, 10 vs 11: viz17's last piece came from
the seeder, and its provider record took 4 s to store (lookups touching
viz11, dead since scene 7, wait out the 2 s RPC timeout), so viz6's
rediscovery never found it.

## Scene 8 after the core fix

With the lookup fix and `FindAllValues` (commit `b399f73`), scene 8 came
back with its original fixed cast (viz6, viz17). Five headless runs in a
row passed. One of them, from its `events.jsonl` (key `f35aa08f…`):

```
02:11:20.474 [viz6]  msg="store complete" failed=0 key=f35aa08f… stored_at=4 stored_locally=false
02:11:28.529 [viz17] msg="store complete" failed=0 key=f35aa08f… stored_at=4 stored_locally=false
02:11:29.277 [viz6]  msg="new providers found" added=1 key=f35aa08f… known=2
02:11:30.872 [viz6]  msg="download stalled: no live provider has the missing pieces; looking for more" have=11 of=16
02:11:38.572 [viz17] msg="download stalled: no live provider has the missing pieces; looking for more" have=11 of=16
02:11:45.969 [viz6]  msg="download gave up, still serving its pieces" … have=11 of=16
02:11:53.617 [viz17] msg="download gave up, still serving its pieces" … have=11 of=16
02:11:53.694 [viz20] msg="provider announced" key=f35aa08f… replicas=4 tcp_addr=127.0.0.1:9760
02:11:53.700 [viz6]  msg="providers found" count=3 key=f35aa08f…
02:11:53.703 [viz6]  msg="download resumed" have=11 key=f35aa08f… of=16
02:11:53.717 [viz6]  msg="download complete" bytes=1048576 duration=21ms failed_providers=1 … pieces=16 providers=3
```

- Every announce stores on 4 nodes and **none keeps a local copy**
  (`stored_locally=false`): no dead node shortens the lookups any more.
- viz6's rediscovery finds viz17's record a second after viz17 announced
  (`new providers found added=1`), so both end at 11: in all five runs
  the leechers gave up with the same count.
- The resume's provider lookup asks the network (`FindAllValues`; viz6
  logged 29 `find_value finished … ask_all=true` lookups in that run and
  no `answered from local store`), finds 3 providers including viz20,
  and finishes in 21 ms.

That run's viz20 served without a delay, so the resume was over before
anyone could see it, and only viz6 fetched again. The scene now gives
viz20 the same 1 s per piece as everyone else and has **both** leechers
fetch again, viz17 one piece after viz6. In a browser run both gave up at
12/16, then went 12 → 16 one piece a second, with their new pieces
painted in viz20's colour; viz6's tally read `viz13 10 · viz17 2 · viz20
4`, and both copies' SHA-256 matched.

## The page, checked against the file

After the browser run, the page's own per-message counters
(`Viz.app.counts`) and the runner's `events.jsonl`:

```
rpc sent               file=  701 page=  701 =
rpc received           file= 1497 page= 1497 =
lookup converged       file=   11 page=   11 =
value stored           file=   37 page=   37 =
provider announced     file=    9 page=    9 =
piece received         file=  122 page=  122 =
download complete      file=    4 page=    4 =
tcp request served     file=  426 page=  426 =
download resumed       file=    1 page=    1 =
bucket updated         file= 1261 page= 1261 =
total 5054 max dropped 0
```

21 event streams on one page, none dropping an event.

## `scripts/demo-viz.sh`

```
=== Checks ===
  vizrun's checks: all ok
  events recorded: 4608
    636  rpc sent
   1361  rpc received
    159  lookup round starting
     11  lookup converged
  …
      2  download gave up, still serving its pieces
      1  download resumed
   1226  bucket updated
  dropped: 0 on every event
  SHA-256: 4 of 4 downloaded copies match their originals
  no port left bound after the run

PASS: every scene ran on live nodes, every event kind the page draws was recorded, nothing was dropped, and every copy matched.
```

## What part 9 doesn't show


- **Nodes on other laptops.** The page takes any API addresses
  (`--watch`), but the runner, the hashes and `--api-control` are
  localhost-only for now ([11](11-visualizer.md#later)).
- **A recorded video.** Deferred; `?mode=auto` is ready for it.
- **The scene-7 VOID path in the browser.** It happens only if the
  presenter waits until a leecher holds the whole file; the runner's
  `recorder.spread` test covers the rule.

