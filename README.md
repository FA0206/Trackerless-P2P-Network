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
| Edge-case stress tests: node setups chosen to find what breaks | 🟡 In progress: 1 of 10 cases scripted (B2: record holders die) | `scripts/edge/`, [docs/EDGE-CASES.md](docs/EDGE-CASES.md) |
| `STORE` / `FIND_VALUE` (the DHT holds "who has file X") | ✅ Done | `main` (merged from `feature/store-findvalue-tcp`) — [docs/06](docs/06-store-and-find-value.md) |
| File transfer over TCP: pieces, SHA-256 verification, content keys | ✅ Done | `main` (merged from `feature/store-findvalue-tcp`) — [docs/07](docs/07-file-transfer-over-tcp.md) |
| Share a file / download it from the CLI; transfer demo | ✅ Done | `share` / `fetch` console commands, `scripts/demo-transfer.sh` |
| Download from several peers; survive the seeder being killed | 🟡 Basic version: pieces round-robin over providers, failover to the next provider; finished downloaders become providers | [docs/07](docs/07-file-transfer-over-tcp.md) |
| Pieces spread so **no single node has the whole file** | ⬜ Not started — next step: per-piece availability | — |
| Runs across multiple devices / Wi-Fi | ⬜ Not started — `--advertise-ip` exists; untested off localhost | — |
| Sending messages (as opposed to files) | ⬜ Not started — `put`/`get` store short text, but there's no messaging feature | — |
| Visual demo | ⬜ Not started | — |
| Performance graphs (the four above) | ⬜ Not started | — |
| Merkle trees | ⬜ Not started — optional; see [FAQ](docs/FAQ.md#do-merkle-trees-make-sense-here) | — |

In short: nodes find each other and join; any node can store and find
small values; and a file shared by one node can be downloaded by others
over TCP, verified piece by piece. That still works after the original
seeder is killed, as long as someone who finished downloading is still
online.

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
- [EDGE-CASES.md](docs/EDGE-CASES.md) — node setups that stress the
  code, each with a prediction, a script, real output, and what would
  fix it.
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
   hashes to the key. It fetches pieces round-robin across providers,
   checking each piece's hash and skipping any provider that fails. It
   then announces itself as a provider ([07](docs/07-file-transfer-over-tcp.md)).

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

Why 11/5, and where the 2 s go: [DRY-RUN.md](docs/DRY-RUN.md) part 2.

### The seeder-death demo (the "before" picture)

```sh
bash scripts/demo-seeder-death.sh        # env: BASE_PORT=9200 DELAY=150ms
```

A seeder shares a 16-piece file. Three leechers start downloading one
after another, and the seeder is killed (SIGKILL) while **none of them
has finished**. On `main` the file is lost: downloaders only announce
complete files, pieces are fetched in order (so the leechers' pieces
overlap), and a failed download deletes its partial output. The demo
**passes when that expected failure happens** and no partial file is
left:

```
=== What had spread when the seeder died (from the leechers' logs) ===
  node2: 13/16 pieces  [0-12]
  node3: 8/16 pieces  [0-7]
  node4: 6/16 pieces  [0-5]
  together: 13/16 pieces  [0-12]
...
PASS: the seeder died before any leecher finished, and the file was lost …
```

It runs every node with the DEBUG ONLY `--debug-serve-delay`, so the kill
can land mid-download (localhost is otherwise too fast). Walkthrough:
[DRY-RUN.md](docs/DRY-RUN.md) part 3. Branch
`feature/parallel-partial-seeding` makes the same scenario succeed.

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
bash scripts/edge/b2-record-holders-die.sh     # env: N=8 BASE_PORT=9525 SEED=1
```

Scripts in `scripts/edge/` set up specific node graphs to find the
code's limits. Each one PASSes when the code behaves as its documented
prediction says, including "this breaks". B2 kills the 3 nodes holding a
file's provider record while the seeder stays up. The file can't be
found, and re-sharing doesn't bring it back:

```
  node4: fetch failed: transfer: no providers found for 93accc52…
  node1's re-announce lookup: rounds=1 rpcs_sent=3 rpcs_failed=3 closest_found=0
PASS: with every record holder dead the file could not be found, though 2 providers were online, and re-sharing did not bring it back.
```

Catalogue, predictions and explanations: [docs/EDGE-CASES.md](docs/EDGE-CASES.md).

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
| `--debug-serve-delay <dur>` | **DEBUG ONLY**, off by default: wait this long before answering each piece request, so a demo can kill a node mid-transfer |
| `--id-seed <s>` | node ID = SHA-1(s); random if omitted |
| `--bootstrap <host:port>` | join through this node |
| `--lookup <hex>` `--lookup-after <dur>` | after joining, run one lookup and log it (used by the demo) |
| `--verbose` / `--quiet` | debug-level / warnings-only logging |
| `--console` | read console commands from stdin (default true; use `--console=false` for background processes) |
| `--k`, `--alpha`, `--rpc-timeout` | protocol parameters (20, 3, 2s); raise `--rpc-timeout` when using breakpoints |

### Tests

```sh
go test ./... -race -count=1
```

- `internal/kademlia/id_test.go` — XOR metric properties, ordering, bucket indexes
- `internal/kademlia/bucket_test.go` — every branch of the k-bucket update rule
- `internal/kademlia/routingtable_test.go` — never stores self; `Closest` ordering
- `internal/kademlia/store_test.go` — values per key, limits, datagram fit
- `internal/kademlia/integration_test.go` — real nodes over real loopback
  UDP: joins, lookup, bootstrap failures, replies exclude the requester, ping
- `internal/kademlia/values_test.go` — STORE lands on exactly the k
  closest nodes (checked by brute force); FIND_VALUE finds it from every node
- `internal/framing/framing_test.go` — frames on datagrams and on a byte stream
- `internal/transfer/manifest_test.go` — piece layout, corrupted pieces
  and tampered manifests rejected
- `internal/transfer/transfer_test.go` — real UDP + TCP: one and two
  providers, seeder shut down mid-way, corrupt provider skipped, unknown key

`KADEMLIA_TEST_LOGS=1` shows the nodes' logs during tests.

## Project layout

```
cmd/node/            the executable
  main.go            flags, node + file server startup, bootstrap, --lookup trigger, shutdown
  console.go         interactive commands (table, lookup, put, get, share, fetch, ...)
internal/kademlia/   the DHT (see docs/ARCHITECTURE.md for dependencies)
  id.go              160-bit IDs, XOR distance, bucket index
  contact.go         (ID, IP, UDP port)
  bucket.go          one k-bucket and its update rule (no I/O)
  routingtable.go    160 buckets, Closest()
  messages.go        envelope + payloads for PING, FIND_NODE, STORE, FIND_VALUE
  transport.go       UDP socket
  store.go           this node's share of stored values, size limits
  node.go            Config, RPC engine, receive path + replies, eviction, Bootstrap
  lookup.go          iterative alpha-parallel lookup (FIND_NODE and FIND_VALUE)
  values.go          Store, FindValue
  logging.go         structured stdout logs
internal/framing/    [4-byte length][body] frames, for UDP datagrams and TCP streams
internal/transfer/   file sharing on top of the DHT
  manifest.go        pieces, SHA-256 per piece, content key
  provider.go        provider record = the DHT value {node_id, ip, tcp_port}
  protocol.go        TCP request/response shapes
  library.go         files this peer can serve
  server.go          TCP server
  client.go          TCP client side
  peer.go            Share, Download (round-robin + failover), announce
scripts/demo.sh            5 processes: joining and lookup
scripts/demo-transfer.sh   5 processes: put/get, share, downloads, killed seeder
scripts/demo-seeder-death.sh  seeder killed mid-download: the file is lost on main
scripts/demo-big-network.sh   20 nodes, random joins, lookups checked by brute force, transfer
scripts/lib/nodes.sh       shared helper: start N nodes with console FIFOs (bash 3.2)
scripts/lib/edge.sh        helpers for the edge cases: brute-force closest nodes, wait for the Nth log line
scripts/edge/              edge-case scripts, one per case (docs/EDGE-CASES.md)
docs/                concept docs 01-07 + reference docs
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
| **No bucket refresh** — neither the post-join refresh nor the paper's hourly refresh ([04](docs/04-bootstrap-and-join.md)) | **Yes, for churn.** Dead contacts linger until a full bucket triggers a ping. | Before the seeder-kill experiments. Refresh traffic also belongs in the control-bytes count. |
| No lookup early-exit heuristic ([05](docs/05-iterative-parallel-lookup.md)) | Slightly inflates RPC counts. | Optional, before the graphs. |
| **Values never expire or get republished** ([06](docs/06-store-and-find-value.md)) | **Yes, for churn.** Dead providers' records linger (one wasted connection each), and records don't move to closer nodes that join later. | Before the seeder-kill and recovery experiments. |
| No caching of found values ([06](docs/06-store-and-find-value.md)) | No. | Keep. |
| **Providers must have the whole file** ([07](docs/07-file-transfer-over-tcp.md)) | **Yes:** "no single node has the whole file" needs per-piece availability. | Next feature step. |
| Pieces fetched one at a time ([07](docs/07-file-transfer-over-tcp.md)) | Not for correctness; matters for the download-time graph. | Before the download-time graph. |
| One failure drops a provider; no retries; no resume ([07](docs/07-file-transfer-over-tcp.md)) | Not yet. | Revisit with real networks / Wi-Fi. |
| No security (ID spoofing, Sybil, rate limits) | Not in the goal list. | Later, if at all. |

**Found by the edge cases, not (yet) a labeled simplification:** a
lookup starts from only the k closest contacts in the routing table, and
a contact that times out stays there. When all of those are dead, the
lookup stops with nothing, even though the node knows live nodes farther
away. Edge case B2 shows this losing a file whose seeder is still
online ([EDGE-CASES.md](docs/EDGE-CASES.md#b2--the-provider-record-holders-all-die)).
It blocks the churn and recovery experiments. Fix it in `lookup.go`
together with republishing, before measuring recovery.

**Bottom line:** nothing here had to be undone before adding
`STORE`/`FIND_VALUE` and TCP transfer, and it's all built now. Before the
end goal, in roughly this order:

1. **per-piece availability**, so pieces can be spread out with no
   complete copy anywhere;
2. **synchronous rounds, bucket refresh, and value expiry/republish**,
   so churn (killing the seeder) is handled the way the paper intends
   before measuring recovery time;
3. **addresses on a real LAN**, for the multi-device demo.

Each is a separate, testable step.

## Paper reference

`maymounkov-kademlia-lncs.pdf`: Petar Maymounkov and David Mazières,
*Kademlia: A Peer-to-peer Information System Based on the XOR Metric*,
IPTPS 2002. Section numbers in code comments and docs refer to this
paper.
