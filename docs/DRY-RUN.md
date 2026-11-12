# Dry runs: what happens inside the demo scripts

- **Part 1** — `scripts/demo.sh`: joining and lookup (below).
- **Part 2** — `scripts/demo-transfer.sh`: STORE/FIND_VALUE and TCP file
  transfer, including killing the seeder ([jump](#part-2--scriptsdemo-transfersh)).

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
