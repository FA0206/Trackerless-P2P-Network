# Trackerless P2P Network (Kademlia DHT, from scratch in Go)

A peer-to-peer file-sharing network with no central tracker. It's built
step by step on a from-scratch implementation of Kademlia, the
distributed hash table from Maymounkov & Mazières, *"Kademlia: A
Peer-to-peer Information System Based on the XOR Metric"* (PDF in this
repo). This is a networked-software course project. The goal is to
understand every piece well enough to explain and extend it, so the
documentation matters as much as the code.

## Where the project stands

**The end goal:** peers share files (or messages) with no central
server. Files are split into pieces spread across several nodes, so
downloads keep working when the original seeder is killed. It should be
demonstrable across real devices (not just localhost), with a visual
demo and an evaluation that explains how each part was validated. That
evaluation includes performance graphs:

1. lookup latency vs. network size
2. download time vs. number of leechers, P2P vs. centralized
3. time to recovery after the seeder is killed
4. control bytes (DHT traffic) vs. payload bytes (file data)

**Status against that goal:**

| Part of the goal | Status | Where |
|---|---|---|
| **Peer discovery (the DHT core):** 160-bit IDs, XOR metric, k-bucket routing table, `PING`/`FIND_NODE` over UDP, joining via one known node, iterative α-parallel lookup | ✅ Done | `main` — this README, [docs 01–05](#documentation) |
| Unit + integration tests for the core; runnable demos (5-process chain, 20-node random joins with brute-force-checked lookups) | ✅ Done | `internal/kademlia/*_test.go`, `scripts/demo.sh`, `scripts/demo-big-network.sh` |
| Debugging tooling: logs, per-node console, VSCode debug configs | ✅ Done | [docs/DEBUGGING.md](docs/DEBUGGING.md) |
| Edge-case stress tests: node setups chosen to find what breaks | ✅ Done: 6 cases (A2 thundering herd, A3 bridge between islands, A4 tiny buckets, B1 half the network dies, B2 record holders die, B3 late joiners), ranked by fix priority. `scripts/edge/run-all.sh` runs them all. Fixing them, on `feature/edge-case-fixes` ([FIXES.md](docs/FIXES.md)): lookups no longer come back short after deaths (B1, B2), nodes that stop answering are skipped once stale (B1), values are republished, so B2's and B3's lost values come back by themselves, and buckets are refreshed (A3, A4) | `scripts/edge/`, [docs/EDGE-CASES.md](docs/EDGE-CASES.md) |
| `STORE` / `FIND_VALUE` (the DHT holds "who has file X") | ✅ Done | `main` (merged from `feature/store-findvalue-tcp`) — [docs/06](docs/06-store-and-find-value.md) |
| File transfer over TCP: pieces, SHA-256 verification, content keys | ✅ Done | `main` (merged from `feature/store-findvalue-tcp`) — [docs/07](docs/07-file-transfer-over-tcp.md) |
| Share a file / download it from the CLI; transfer demo | ✅ Done | `share` / `fetch` console commands, `scripts/demo-transfer.sh` |
| Download from several peers **in parallel**; survive the seeder being killed | ✅ Done on branch `feature/parallel-partial-seeding`: rarest-first, one request per provider, up to `--max-inflight` in total, failover | [docs/08](docs/08-swarming.md), `scripts/demo-parallel.sh` |
| Pieces spread so **no single node has the whole file** | ✅ Done on branch `feature/parallel-partial-seeding`: peers serve and announce pieces before they're complete; the seeder can die once the leechers *together* hold every piece | [docs/08](docs/08-swarming.md), `scripts/demo-seeder-death.sh` (`main` has the failing "before" version) |
| Runs across multiple devices / Wi-Fi | ⬜ Not started — `--advertise-ip` exists; untested off localhost | — |
| Sending messages (as opposed to files) | ⬜ Not started — `put`/`get` store short text, but there's no messaging feature | — |
| Per-node HTTP API (for the visual demo) | ✅ Done: `--api` serves node info, routing table, stored values, files (with download progress) as JSON, plus a live event stream (SSE) of every RPC, stored value and piece; `--api-control` adds POST lookup/ping/put/get/share/fetch | `internal/nodeapi`, [docs/NODE-API.md](docs/NODE-API.md) |
| Visual demo | ⬜ Not started | — |
| Performance graphs (the four above) | ⬜ Not started | — |
| Merkle trees | 🟡 Steps 1–2 of 4 on branch `feature/merkle`: the tree, proofs and verification (RFC 6962, tested against the standard test vectors), and transfers use them. The manifest carries one root (138 bytes for a 1 MiB file, against 1,146 before), every piece travels with its proof, and partial seeders pass proofs on | [docs/09](docs/09-merkle-trees.md), `internal/transfer/merkle.go` |

In short: nodes find each other and join, and any node can store and
find small values. A file shared by one node is downloaded by others
over TCP, verified piece by piece, from several providers at once. On
this branch, a file also survives its seeder dying mid-download, as long
as the leechers together hold every piece, even if none of them holds
them all.

## Documentation

**Concepts, in learning order.** Each doc is short: what it is, why it's
needed, and which function implements it.

1. [01 — IDs and XOR distance](docs/01-ids-and-xor-distance.md)
2. [02 — Routing table and k-buckets](docs/02-routing-table-and-kbuckets.md)
3. [03 — Messages and framing over UDP](docs/03-message-framing.md)
4. [04 — Bootstrap and join: what a new node needs](docs/04-bootstrap-and-join.md)
5. [05 — Iterative parallel lookup](docs/05-iterative-parallel-lookup.md)
6. [06 — STORE and FIND_VALUE](docs/06-store-and-find-value.md)
7. [07 — File transfer over TCP](docs/07-file-transfer-over-tcp.md)
8. [08 — Swarming: parallel downloads and partial seeding](docs/08-swarming.md)
9. [09 — Merkle trees: one hash for the whole file, a short proof per piece](docs/09-merkle-trees.md)

**Reference:**

- [DRY-RUN.md](docs/DRY-RUN.md) — both demo scripts traced from start
  to finish, with real logs, naming every function and file as it runs.
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — which file depends on which,
  goroutines and locks, invariants, "if you change X also touch Y".
- [DEBUGGING.md](docs/DEBUGGING.md) — logs, a live network in VSCode
  terminals, breakpoints without timeouts, debugging tests, packet
  capture, plus hands-on exercises (forcing an eviction, killing a node).
- [FAQ.md](docs/FAQ.md) — common confusions, how Kademlia output leads
  to a TCP connection, what a joining node needs, Merkle trees.
- [NODE-API.md](docs/NODE-API.md) — the optional per-node HTTP API
  (`--api`): every endpoint with real output, and how it's built.
- [EDGE-CASES.md](docs/EDGE-CASES.md) — node setups that stress the
  code, each with a prediction, a script, real output, and what would
  fix it.
- [FIXES.md](docs/FIXES.md) — how each edge-case problem was fixed, in
  plain words: the problem, where in the code it came from, the fix, the
  evidence, what's still open, and every place the fixes differ from the
  paper.
- [CLAUDE.md](CLAUDE.md) — project scope and ground rules, for AI
  assistants and new teammates alike.

## End-to-end: what happens when a node starts, joins, and looks something up

(Function-by-function version with real logs: [DRY-RUN.md](docs/DRY-RUN.md).)

1. **Start.** `cmd/node/main.go` picks an ID (SHA-1 of `--id-seed`, or
   random), creates a `Node` (UDP socket + 160 empty k-buckets), and
   starts a background loop reading packets.
2. **Join.** Given `--bootstrap host:port`, `Node.Bootstrap` PINGs that
   address to learn who's there (the PONG carries its ID), then looks up
   its *own* ID. The `FIND_NODE` replies fill its routing table. Every
   node it queries records it as a sender, which is how the network
   learns it exists ([04](docs/04-bootstrap-and-join.md)).
3. **Look up.** `Node.Lookup(target)` asks the α = 3 closest known nodes
   in parallel for *their* closest contacts. It merges the answers, keeps
   the k = 20 closest, and repeats until all of those have been asked
   ([05](docs/05-iterative-parallel-lookup.md)).

4. **Store / find a value.** `Node.Store(key, value)` looks up the k
   closest nodes to the key and sends each a `STORE`. `Node.FindValue(key)`
   runs the same lookup with `FIND_VALUE` and stops at the first node that
   has it ([06](docs/06-store-and-find-value.md)).
5. **Share a file.** `Peer.Share` cuts the file into 64 KiB pieces,
   hashes each, and computes the content key (a hash of that
   description). It then `Store`s a provider record `{node_id, ip,
   tcp_port}` under the key.
6. **Download.** `Peer.Download` runs `FindValue(key)` to get provider
   records, opens TCP connections to them, and checks that the manifest
   hashes to the key. It fetches pieces from several providers in
   parallel, rarest first, checking each piece's hash and dropping any
   provider that fails. After its first piece it announces itself as a
   provider and serves the pieces it has ([07](docs/07-file-transfer-over-tcp.md), [08](docs/08-swarming.md)).

Throughout, every received message updates the sender's k-bucket
(move-to-tail, or ping-the-oldest-and-maybe-evict when full;
[02](docs/02-routing-table-and-kbuckets.md)), and every RPC is logged.

## Running it

### The 5-node demo

```sh
bash scripts/demo.sh
```

This starts 5 node processes on ports 9000–9004. Each joins via the
previous one (node1 via node0, node2 via node1, …). node4 then looks up
node2's ID. The script checks the result against IDs it computes
independently with `shasum`, prints rounds and RPC counts, and shows
that every node learned about every other.

```
Rounds:      2
RPCs sent:   4
RPCs failed: 0

=== Who learned about whom (distinct peers each node received RPCs from) ===
node0 heard directly from 4 of the other 4 nodes
...
PASS: node4's lookup found exactly the other 4 nodes (demo0, demo1, demo2, demo3).
```

**What the demo shows:** starting a first node, four nodes joining with
only one address each, knowledge spreading as a side effect (node0 never
sends a request yet learns everyone), and a correct parallel lookup.
**What it doesn't:** eviction, failures, and `log n` scaling. With 5
nodes and k = 20 those never come up. [DRY-RUN.md](docs/DRY-RUN.md)
explains this, and [DEBUGGING.md](docs/DEBUGGING.md) has exercises that
trigger each by hand.

### The file-transfer demo

```sh
bash scripts/demo-transfer.sh
```

This starts 5 nodes with `k = 3`, so values live on 3 of the 5 nodes and
`FIND_VALUE` has to search. Then:

1. node0 `put`s a text value and node4 `get`s it.
2. node1 shares a random 1 MiB file (16 pieces).
3. node3 downloads it (one provider), then node4 (two providers, pieces
   alternating).
4. node1, the original seeder, is killed, and node2 downloads anyway
   from node3 and node4.
5. Every copy is checked byte for byte against the original.

```
  [node2] > fetch 5951f7ca… $WORK/node2.bin
    fetched "original.bin" (1048576 bytes, 16 pieces) into $WORK/node2.bin in 2.022s
        11 piece(s) from 127.0.0.1:9003
        5 piece(s) from 127.0.0.1:9004
        provider 127.0.0.1:9001 failed and was skipped
...
PASS: three downloads completed and verified, the last one after the seeder was killed.
```

Why 11/5, and where the 2 s go: [DRY-RUN.md](docs/DRY-RUN.md) part 2 (recorded on `main`; on this branch pieces arrive rarest-first and in parallel, so the split varies, and the last download takes ~4 s because `Download` now waits for its announce).

### The seeder-death demo ("after" on this branch; "before" on `main`)

```sh
bash scripts/demo-seeder-death.sh        # env: BASE_PORT=9200 DELAY=150ms MAXINFLIGHT=1
```

A seeder shares a 16-piece file. Three leechers start downloading one
after another, and the seeder is killed (SIGKILL) while **none of them
has finished**: at the first moment the leechers together hold every
piece. On this branch every leecher still completes the file from the
others:

```
  kill moment: the leechers together hold all 16 pieces; none has finished ( node2:10/16 node3:6/16 node4:3/16 )
  node2: fetched "original.bin" (1048576 bytes, 16 pieces) in 2.486s
      11 piece(s) from 127.0.0.1:9201          ← the seeder, before it died
      4 piece(s) from 127.0.0.1:9203
      1 piece(s) from 127.0.0.1:9204
...
PASS: the seeder died before any leecher finished and no node had the whole file, yet every leecher completed it from the others.
```

**The failing "before" version of this script is on `main`.** There,
downloaders only announced complete files and fetched pieces in order, so
the leechers' pieces overlapped and the file was lost. The script keeps
its name here, so `git diff main feature/parallel-partial-seeding --
scripts/demo-seeder-death.sh` shows exactly what changed: the kill rule
and the verdict. This version runs with `--max-inflight 1` so the swarm
stays ahead of each individual leecher (why: [DRY-RUN.md](docs/DRY-RUN.md)
part 5). Every node uses the DEBUG ONLY `--debug-serve-delay` so the kill
can land mid-download.

### The parallel-download demo

```sh
bash scripts/demo-parallel.sh            # env: BASE_PORT=9400 DELAY=150ms
```

Three nodes hold a file, and a fourth downloads it. The script prints
the downloader's timeline: three requests leave at once, one per
provider, and each is replaced the moment its piece arrives.

```
  +    0ms  request  piece  2 -> 127.0.0.1:9402   in flight after: 1
  +    0ms  request  piece 11 -> 127.0.0.1:9401   in flight after: 2
  +    0ms  request  piece  6 -> 127.0.0.1:9403   in flight after: 3
  +  152ms  received piece  2 <- 127.0.0.1:9402   in flight after: 2
  +  152ms  request  piece 15 -> 127.0.0.1:9402   in flight after: 3
...
PASS: node4's copy is identical, built from 3 providers with up to 3 pieces in flight at once.
```

16 pieces in ~0.93 s, against ≥ 2.4 s one at a time.

### The big-network demo (20 nodes, random joins)

```sh
bash scripts/demo-big-network.sh        # env: N=20 K=4 BASE_PORT=9300 SEED=<number>
```

`N` nodes (default 20) on ports 9300 onward. Each node joins through a
**randomly chosen** running node, not a chain, and `k` is small (4), so
lookups take several rounds. Four random nodes each look up three
targets. Every answer is compared with the true 4 closest nodes, which
the script computes itself by brute force over all node IDs. Then one
node shares a 1 MiB file and three others download it at once.

```
  node19 -> ea64cae1…: rounds=3 rpcs_sent=7 rpcs_failed=0  ok
  …
  12 of 12 lookups needed more than one round
...
PASS: 20 nodes, random joins, every lookup returned the true 4 closest nodes, and the file reached all leechers intact.
```

A failing run prints its `SEED`; rerun with `SEED=<that number>` to
repeat the same joins and targets. Walkthrough of a 3-round lookup:
[DRY-RUN.md](docs/DRY-RUN.md) part 4.

### Edge cases (what breaks)

```sh
bash scripts/edge/run-all.sh                   # all six, one verdict line each (~3 min)
bash scripts/edge/a2-thundering-herd.sh        # env: N=20 K=4 BASE_PORT=9425 SEED=1 RACE=1
bash scripts/edge/a3-bridge-between-islands.sh # env: BASE_PORT=9450 REFRESH=3s
bash scripts/edge/a4-tiny-buckets.sh           # env: N=20 BASE_PORT=9475 SEED=1
bash scripts/edge/b1-half-the-network-dies.sh  # env: N=16 K=4 KILL=8 TARGETS=4 BASE_PORT=9500 SEED=1
bash scripts/edge/b2-record-holders-die.sh     # env: N=8 BASE_PORT=9525 SEED=1 REPUBLISH=15s
bash scripts/edge/b3-late-joiners-hide-a-value.sh  # env: N=8 LATE=4 BASE_PORT=9550 SEED=1 REPUBLISH=20s
```

Scripts in `scripts/edge/` set up specific node graphs to find the
code's limits. Each one PASSes when the code behaves as its documented
prediction says, including "this breaks".

A2 starts 19 nodes joining through one node at the same instant, every
process built with the race detector. It **holds up**: all join, the
lookups are correct, and there are no data races. A4 builds the same
20-node network with k = 1 (with and without the join refresh), 2 and 4.
With nothing dead and no join refresh, k = 1 gets about half its
lookups wrong (9–14 of 20 correct), because routing tables are nearly
empty. The paper's join refresh (now built) brings that to 14–20 of 20.
k = 4 gets all 20 either way.

A3 joins two separately built
islands through one bridge node and kills it. If nobody had looked
anything up across the bridge, the islands are cut apart for good (0 of
5 found, every time). If one lookup had crossed it, the islands survive.
The same holds with no lookups at all if every node refreshes its
buckets (`--refresh-interval 3s` in the script): two refresh passes, and
every island-A node knows all of island B.

B1 kills 8 of 16 nodes and repeats the same lookups until none asks a
dead node. On the original code, every lookup afterwards returned fewer
than the k closest live nodes, and repeats were no better. Now no answer
is short (the lookup fix), and the querier learns: a node that fails 5
RPCs in a row is stale and gets skipped (stale contacts):

```
before the fixes:
  pass 2  6da4c727…  rounds=2  rpcs=4   failed=3    2.06s  SHORT: got [0] want [0 3 6 8]
with them (--stale-after 5, the default):
  pass 1  total: 0 failed RPCs, 39ms for 4 lookups
  pass 2: 15 failed, 26.41s      ← right after the kill
  pass 3: 10 failed, 16.30s
  pass 4: 2 failed, 4.33s
  pass 7: 0 failed, 32ms         ← recovered
```

With `--stale-after 1`, pass 3 is already clean. How each fix was found
and checked: [FIXES.md](docs/FIXES.md).

B2 kills the 3 nodes holding a file's provider record while the seeder
stays up. The file can't be found, until the seeder's next republish
(paper Sec. 2.5) stores the record on the 3 closest live nodes:

```
  node4: fetch failed: transfer: no providers found for 93accc52…
  node1 republished 12.86s after step 3 ended
  its republish lookup: rounds=2 rpcs_sent=6 rpcs_failed=3 closest_found=3
PASS: with every record holder dead the file could not be found, though 2 providers were online, until the seeder's next republish brought it back.
```

B3 adds 4 nodes whose IDs are closer to a stored value's key than its
holders. Nobody dies, yet most newcomers can't find the value, until
the next republish moves it to them:

```
    node8  MISS  0 value(s) via network: rounds=1 rpcs_sent=2 rpcs_failed=0
  as predicted: 3 of 4 newcomers missed it, though both holders are alive
  node1 republished 16.23s after step 3 ended: values=1 stored_at=2 stored_locally=false failed=0
  as predicted: the republish reached the 2 closest nodes, which are newcomers
```

Catalogue, predictions, explanations, and the cases ranked by fix
priority (lookup failure handling first, then republish, then bucket
refresh): [docs/EDGE-CASES.md](docs/EDGE-CASES.md#fix-priority).

### One node at a time (e.g. in split VSCode terminals)

```sh
go run ./cmd/node --port 9000 --id-seed nodeA
go run ./cmd/node --port 9001 --id-seed nodeB --bootstrap 127.0.0.1:9000
```

Then type console commands into a node's terminal:

| Command | |
|---|---|
| `id`, `table`, `lookup <hex-id\|self>`, `ping <host:port>` | inspect the DHT |
| `put <name> <text…>`, `get <name>` | STORE / FIND_VALUE text under key SHA-1(name) |
| `share <path>`, `fetch <key> <out-path>`, `files` | share and download files |
| `help` | list them all |

| Flag | Meaning |
|---|---|
| `--port` | UDP port (default 9000) |
| `--tcp-port` | TCP port for the file server (default: same number as `--port`) |
| `--advertise-ip` | IP others should use to reach this node, put in provider records (default `127.0.0.1`) |
| `--max-inflight <n>` | most piece requests a download keeps in flight at once (default 4; always at most 1 per provider) |
| `--api <host:port>` | serve this node's state as JSON over HTTP, e.g. `127.0.0.1:8000`, plus a live event stream; off by default ([NODE-API.md](docs/NODE-API.md)) |
| `--api-control` | with `--api`: also accept POST commands (lookup, ping, put, get, share, fetch). Any web page in your browser could use them too: demo use only |
| `--debug-serve-delay <dur>` | **DEBUG ONLY**, off by default: wait this long before answering each piece request, so a demo can kill a node mid-transfer |
| `--id-seed <s>` | node ID = SHA-1(s); random if omitted |
| `--bootstrap <host:port>` | join through this node |
| `--lookup <hex>` `--lookup-after <dur>` | after joining, run one lookup and log it (used by the demo) |
| `--verbose` / `--quiet` | debug-level / warnings-only logging |
| `--console` | read console commands from stdin (default true; use `--console=false` for background processes) |
| `--k`, `--alpha`, `--rpc-timeout` | protocol parameters (20, 3, 2s); raise `--rpc-timeout` when using breakpoints |
| `--refresh-interval <d>` | how often to refresh k-buckets no lookup has touched in that time, by looking up a random ID in each (default 1h, the paper's; negative = off). `--join-refresh=false` skips the refresh at join, for experiments only ([02](docs/02-routing-table-and-kbuckets.md#bucket-refresh-keeping-quiet-buckets-alive)) |
| `--republish-interval <d>` | how often a node stores again what it published and what it holds for others (default 1h, the paper's number; negative = off). Lets values survive their holders dying and reach closer newcomers ([06](docs/06-store-and-find-value.md#republishing-keeping-values-on-the-k-closest-live-nodes)) |
| `--stale-after <n>` | failed RPCs in a row before a contact is stale: kept in the table, but lookups and replies skip it until it's heard from again or its back-off (`--stale-backoff`) runs out (default 5, the paper's number; negative = never) ([02](docs/02-routing-table-and-kbuckets.md#stale-contacts-a-node-that-stops-answering)) |
| `--stale-backoff <d>` | how long a stale contact is skipped before lookups try it again; doubles after each failed retry, up to 1h (default 30s; negative = never retry). The paper's "exponentially increasing backoff interval" (Sec. 4.1) |

### Tests

```sh
go test ./... -race -count=1
```

- `internal/kademlia/id_test.go` — XOR metric properties, ordering, bucket indexes
- `internal/kademlia/bucket_test.go` — every branch of the k-bucket update rule
- `internal/kademlia/routingtable_test.go` — never stores self; `Closest` ordering
- `internal/kademlia/store_test.go` — values per key, limits, datagram fit, snapshots are copies
- `internal/kademlia/integration_test.go` — real nodes over real loopback
  UDP: joins, lookup, bootstrap failures, replies exclude the requester, ping
- `internal/kademlia/values_test.go` — STORE lands on exactly the k
  closest nodes (checked by brute force); FIND_VALUE finds it from every node
- `internal/framing/framing_test.go` — frames on datagrams and on a byte stream
- `internal/transfer/manifest_test.go` — piece layout, corrupted pieces
  and tampered manifests rejected
- `internal/transfer/swarm_unit_test.go` — bitfield layout and validation;
  the rarest-first picker (prefers rare pieces, skips busy holders,
  spreads ties)
- `internal/transfer/swarm_test.go` — real UDP + TCP: a 3-provider
  download that must overlap in time; the seeder dying after pieces have
  spread, with every leecher still finishing; a download that must give
  up when no one has the rest
- `internal/transfer/transfer_test.go` — real UDP + TCP: one and two
  providers, seeder shut down mid-way, corrupt provider skipped, unknown key
- `internal/nodeapi/api_test.go` — every API endpoint against real nodes
  on loopback: table buckets and distances, values on exactly the
  nodes a STORE reached, binary values, files, CORS, 405/404
- `internal/nodeapi/events_test.go` — the log tap leaves printed output
  byte-identical, costs nothing without viewers, never blocks on a slow
  viewer; real RPCs on the stream; levels; unsubscribe on disconnect
- `internal/nodeapi/control_test.go` — each command against the node's
  own methods; put/get and share/fetch across nodes; no overwriting;
  the safety rules (404 without the flag, 415, unknown fields);
  concurrent requests

`KADEMLIA_TEST_LOGS=1` shows the nodes' logs during tests.

## Project layout

```
cmd/node/            the executable
  main.go            flags, node + file server startup, bootstrap, --lookup trigger, shutdown
  console.go         interactive commands (table, lookup, put, get, share, fetch, ...)
  api.go             starts the per-node HTTP API when --api is given
internal/kademlia/   the DHT (see docs/ARCHITECTURE.md for dependencies)
  id.go              160-bit IDs, XOR distance, bucket index
  contact.go         (ID, IP, UDP port)
  bucket.go          one k-bucket and its update rule (no I/O)
  routingtable.go    160 buckets, Closest() (skips stale contacts)
  stale.go           failed RPCs in a row per node; stale nodes (no I/O)
  messages.go        envelope + payloads for PING, FIND_NODE, STORE, FIND_VALUE
  transport.go       UDP socket
  store.go           this node's share of stored values, size limits
  node.go            Config, RPC engine, receive path + replies, eviction, Bootstrap
  lookup.go          iterative alpha-parallel lookup (FIND_NODE and FIND_VALUE)
  values.go          Store, FindValue
  republish.go       republish loop: publisher and holder passes (paper Sec. 2.5)
  refresh.go         bucket refresh: at join and every --refresh-interval (paper Sec. 2.3)
  logging.go         structured stdout logs
internal/framing/    [4-byte length][body] frames, for UDP datagrams and TCP streams
internal/transfer/   file sharing on top of the DHT
  manifest.go        pieces, SHA-256 per piece, content key
  provider.go        provider record = the DHT value {node_id, ip, tcp_port}
  protocol.go        TCP request/response shapes
  library.go         files this peer can serve
  server.go          TCP server
  client.go          TCP client side
  peer.go            Config, Share, announce
  download.go        Download: event loop + workers (parallel, rarest-first, partial seeding)
  picker.go          rarest-first piece picker (pure)
  bitfield.go        which pieces a peer has (BitTorrent's layout)
  merkle.go          Merkle tree over pieces: root, proofs, verification (RFC 6962)
internal/nodeapi/    the optional per-node HTTP API (docs/NODE-API.md)
  api.go             JSON snapshot endpoints, the SSE endpoint
  events.go          log tap (TeeHandler) + non-blocking event fan-out to viewers
  control.go         POST endpoints mirroring the console (only with --api-control)
scripts/demo.sh            5 processes: joining and lookup
scripts/demo-transfer.sh   5 processes: put/get, share, downloads, killed seeder
scripts/demo-seeder-death.sh  seeder killed mid-download: leechers finish from each other
scripts/demo-big-network.sh   20 nodes, random joins, lookups checked by brute force, transfer
scripts/demo-parallel.sh   one download from 3 providers at once, with a timeline
scripts/lib/nodes.sh       shared helper: start N nodes with console FIFOs (bash 3.2)
scripts/lib/edge.sh        helpers for the edge cases: brute-force closest nodes, wait for the Nth log line
scripts/edge/              edge-case scripts, one per case, and run-all.sh (docs/EDGE-CASES.md)
docs/                concept docs 01-09 + reference docs
.vscode/launch.json  debug configurations
```

## Simplifications: which must be undone for the end goal?

Every simplification is called out in the code and in its concept doc.
Here is whether each one blocks the end goal:

| Simplification | Blocks the end goal? | When to fix |
|---|---|---|
| Flat 160-bucket table, no splitting ([02](docs/02-routing-table-and-kbuckets.md)) | No. Splitting only matters with millions of nodes. | Keep. |
| `Closest()` sorts every known contact | No; at most a few thousand contacts. | Keep. |
| Length-prefixed framing on UDP ([03](docs/03-message-framing.md)) | No — it gets reused for TCP. | Keep. |
| JSON encoding | No, but it inflates the "control bytes" measurement. | Keep; say so in the evaluation. |
| **Advertise IP set by hand** (`--advertise-ip`, default `127.0.0.1`; demos bootstrap via `127.0.0.1`) | **Yes, for multi-device.** Each node must be started with its LAN IP, and bootstrap via LAN addresses. No NAT traversal. | Before the multi-device demo: test on a LAN, maybe auto-detect the LAN IP. |
| **Synchronous lookup rounds** ([05](docs/05-iterative-parallel-lookup.md)) | **Yes, for the latency and recovery graphs.** A dead node stalls a round by a full RPC timeout, and the seeder-kill scenario is exactly that. | Before measuring latency or recovery time. |
| Stale contacts: no replacement cache ([02](docs/02-routing-table-and-kbuckets.md#stale-contacts-a-node-that-stops-answering)) | No. A stale contact is replaced only when a new contact for its bucket messages us; until its back-off runs out, lookups skip it. | Keep; refresh refills buckets. |
| Bucket refresh: one timer per node, not per bucket ([02](docs/02-routing-table-and-kbuckets.md#bucket-refresh-keeping-quiet-buckets-alive)) | No. One pass every `--refresh-interval` refreshes every bucket idle that long, so a bucket can wait up to two intervals. | Keep. Count refresh traffic in the control-bytes graph. |
| No lookup early-exit heuristic ([05](docs/05-iterative-parallel-lookup.md)) | Slightly inflates RPC counts. | Optional, before the graphs. |
| **Values never expire** ([06](docs/06-store-and-find-value.md)) | Not for recovery (republish handles that), but dead providers' records linger: a downloader wastes one failed connection on each, and holders keep republishing them. Needs each STORE to carry the remaining lifetime. | Before long-running churn experiments. |
| Republish: one interval for holders and the original publisher (paper: 1 h and 24 h); no lookup-free republish ([06](docs/06-store-and-find-value.md)) | No. More publisher traffic than the paper's, and one lookup per key per interval. | Keep; count republish traffic in the control-bytes graph. |
| No caching of found values ([06](docs/06-store-and-find-value.md)) | No. | Keep. |
| One failure drops a provider for the whole download; no retries; no resume ([07](docs/07-file-transfer-over-tcp.md), [08](docs/08-swarming.md)) | Not yet. | Revisit with real networks / Wi-Fi. |
| One outstanding request per provider, no pipelining ([08](docs/08-swarming.md)) | No; limits one provider's throughput. | Before throughput/download-time graphs, if it shows. |
| Bitfields polled every 500 ms instead of pushed `have` messages; providers rediscovered by polling `FIND_VALUE` every 1 s ([08](docs/08-swarming.md)) | No, but adds control traffic and some duplicate seeder uploads. | Count it in the control-bytes graph; push-based updates if it dominates. |
| Fixed swarm timers: 500 ms / 1 s / 15 s stall ([08](docs/08-swarming.md)) | No. | Tune for Wi-Fi. |
| Rarest-first uses only the downloader's own (possibly stale) view ([08](docs/08-swarming.md)) | No. | Keep. |
| No choking, upload limits, or fairness ([08](docs/08-swarming.md)) | Not in the goal list. | Later, if ever. |
| No security (ID spoofing, Sybil, rate limits) | Not in the goal list. | Later, if at all. |

**Found by the edge cases, and fixed** ([FIXES.md](docs/FIXES.md)):
- Lookups used to come back short after nodes died, because failed
  candidates kept their places among the k closest. A failed candidate
  now gives up its place to the next-closest one
  ([05](docs/05-iterative-parallel-lookup.md), B1 and B2).
- A node that stopped answering was asked again on every lookup. Now
  it's stale after 5 failed RPCs in a row (`--stale-after`), and lookups
  and replies skip it ([02](docs/02-routing-table-and-kbuckets.md), B1).
- Values lived only on the nodes chosen when they were stored. Now
  publishers and holders republish them (`--republish-interval`,
  [06](docs/06-store-and-find-value.md)), so they survive their holders
  dying and reach closer newcomers (B2, B3).

- Buckets only filled as a side effect of lookups. Now a node refreshes
  them: every bucket farther than its closest neighbor at join, and
  every idle bucket each `--refresh-interval`
  ([02](docs/02-routing-table-and-kbuckets.md#bucket-refresh-keeping-quiet-buckets-alive)).
  Quiet islands connect before their bridge dies (A3), k = 1 tables fill
  up (A4), and every node tests its own contacts (B1).

**Found by the edge cases, not fixed yet:** value expiry, hand-over on
join, and synchronous lookup rounds (see the table above and
[FIXES.md](docs/FIXES.md)).

**Bottom line:** per-piece availability, the first item that blocked the
end goal, is done on this branch. Before the end-goal measurements, in
roughly this order:

1. **synchronous rounds and value expiry** (lookup failure handling,
   stale contacts, republish and bucket refresh are done), so churn (killing the seeder) is handled the way the paper intends
   before measuring recovery time. The swarm already hides them during
   downloads (they run in workers), but they still cost seconds in
   discovery after a death.
2. **instrumentation** (bytes and timings) and the evaluation harness;
3. **addresses on a real LAN**, for the multi-device demo.

Each is a separate, testable step.

## Paper reference

`maymounkov-kademlia-lncs.pdf`: Petar Maymounkov and David Mazières,
*Kademlia: A Peer-to-peer Information System Based on the XOR Metric*,
IPTPS 2002. Section numbers in code comments and docs refer to this
paper.
