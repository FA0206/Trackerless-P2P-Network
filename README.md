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
| Unit + integration tests for the core, one runnable 5-process demo | ✅ Done | `internal/kademlia/*_test.go`, `scripts/demo.sh` |
| Debugging tooling: logs, per-node console, VSCode debug configs | ✅ Done | [docs/DEBUGGING.md](docs/DEBUGGING.md) |
| `STORE` / `FIND_VALUE` (the DHT can hold "who has file X") | 🚧 In progress | branch `feature/store-findvalue-tcp` |
| File transfer over TCP (pieces, integrity checks) | 🚧 In progress | branch `feature/store-findvalue-tcp` |
| Download from several peers; survive the seeder being killed | ⬜ Not started (the branch lays groundwork) | — |
| Pieces spread so **no single node has the whole file** | ⬜ Not started | — |
| Runs across multiple devices / Wi-Fi | ⬜ Not started — needs address handling (see simplifications) | — |
| Visual demo | ⬜ Not started | — |
| Performance graphs (the four above) | ⬜ Not started | — |
| Merkle trees | ⬜ Not started — optional; see [FAQ](docs/FAQ.md#do-merkle-trees-make-sense-here) | — |

In short: nodes can find each other, join, and locate the nodes
responsible for any ID. Nothing is stored or transferred yet on `main`.

## Documentation

**Concepts, in learning order.** Each doc is short: what it is, why it's
needed, and which function implements it.

1. [01 — IDs and XOR distance](docs/01-ids-and-xor-distance.md)
2. [02 — Routing table and k-buckets](docs/02-routing-table-and-kbuckets.md)
3. [03 — Messages and framing over UDP](docs/03-message-framing.md)
4. [04 — Bootstrap and join: what a new node needs](docs/04-bootstrap-and-join.md)
5. [05 — Iterative parallel lookup](docs/05-iterative-parallel-lookup.md)

**Reference:**

- [DRY-RUN.md](docs/DRY-RUN.md) — `scripts/demo.sh` traced from start to
  finish, with real logs, naming every function and file as it runs.
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — which file depends on which,
  goroutines and locks, invariants, "if you change X also touch Y".
- [DEBUGGING.md](docs/DEBUGGING.md) — logs, a live network in VSCode
  terminals, breakpoints without timeouts, debugging tests, packet
  capture, plus hands-on exercises (forcing an eviction, killing a node).
- [FAQ.md](docs/FAQ.md) — common confusions, how Kademlia output leads
  to a TCP connection, what a joining node needs, Merkle trees.
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

### One node at a time (e.g. in split VSCode terminals)

```sh
go run ./cmd/node --port 9000 --id-seed nodeA
go run ./cmd/node --port 9001 --id-seed nodeB --bootstrap 127.0.0.1:9000
```

Then type console commands into a node's terminal: `table`, `lookup self`,
`lookup <hex-id>`, `ping 127.0.0.1:9000`, `help`.

| Flag | Meaning |
|---|---|
| `--port` | UDP port (default 9000) |
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

- `id_test.go` — XOR metric properties, ordering, bucket indexes
- `bucket_test.go` — every branch of the k-bucket update rule
- `routingtable_test.go` — never stores self; `Closest` ordering
- `integration_test.go` — real nodes over real loopback UDP: joins,
  lookup, bootstrap failures, ping

`KADEMLIA_TEST_LOGS=1` shows the nodes' logs during tests.

## Project layout

```
cmd/node/            the executable
  main.go            flags, bootstrap, --lookup trigger, shutdown
  console.go         interactive debug commands (table, lookup, ping)
internal/kademlia/   the DHT (see docs/ARCHITECTURE.md for dependencies)
  id.go              160-bit IDs, XOR distance, bucket index
  contact.go         (ID, IP, UDP port)
  bucket.go          one k-bucket and its update rule (no I/O)
  routingtable.go    160 buckets, Closest()
  messages.go        envelope + PING/FIND_NODE payloads
  transport.go       UDP socket, [length][JSON] framing
  node.go            RPC engine, receive path, eviction, Bootstrap
  lookup.go          iterative alpha-parallel lookup
  logging.go         structured stdout logs
scripts/demo.sh      the 5-process demo
docs/                concept docs 01-05 + reference docs
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
| **Loopback addresses** (`Node.Self()` hardcodes `127.0.0.1`; demos bootstrap via `127.0.0.1`) | **Yes, for multi-device.** File-sharing also needs a node to advertise an address *others* can reach. | Partly on the feature branch (`--advertise-ip`); finish before the multi-device demo. |
| **Synchronous lookup rounds** ([05](docs/05-iterative-parallel-lookup.md)) | **Yes, for the latency and recovery graphs.** A dead node stalls a round by a full RPC timeout, and the seeder-kill scenario is exactly that. | Before measuring latency or recovery time. |
| **No bucket refresh** — neither the post-join refresh nor the paper's hourly refresh ([04](docs/04-bootstrap-and-join.md)) | **Yes, for churn.** Dead contacts linger until a full bucket triggers a ping. | Before the seeder-kill experiments. Refresh traffic also belongs in the control-bytes count. |
| No lookup early-exit heuristic ([05](docs/05-iterative-parallel-lookup.md)) | Slightly inflates RPC counts. | Optional, before the graphs. |
| No security (ID spoofing, Sybil, rate limits) | Not in the goal list. | Later, if at all. |

**Bottom line:** none of these block the next step, `STORE`/`FIND_VALUE`
plus TCP transfer. Addresses, synchronous rounds, and bucket refresh
must be fixed before the multi-device demo and the performance
measurements. They're fixed then rather than now, so each change can be
tested and explained on its own.

## Paper reference

`maymounkov-kademlia-lncs.pdf`: Petar Maymounkov and David Mazières,
*Kademlia: A Peer-to-peer Information System Based on the XOR Metric*,
IPTPS 2002. Section numbers in code comments and docs refer to this
paper.
