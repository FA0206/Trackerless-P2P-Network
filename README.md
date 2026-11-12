# Kademlia DHT (from scratch, in Go)

A from-scratch, localhost-runnable implementation of the core of
Kademlia — the peer-to-peer distributed hash table described in
Maymounkov & Mazières, *"Kademlia: A Peer-to-peer Information System Based
on the XOR Metric"* (the PDF alongside this repo). Built incrementally,
for a networked-software course project, with an explicit scope: this pass
implements node identity, the XOR metric, k-bucket routing, `PING` /
`FIND_NODE` over UDP, bootstrap/join, and iterative parallel lookup. It
does **not** implement `STORE`/`FIND_VALUE`, actual file/piece transfer, or
any security hardening — see [What's next](#whats-next).

## How to read this project

If you're learning this for the first time, read the `docs/` folder in
order — each file is short, explains one idea, says why it's needed, and
points at the exact function/file that implements it:

1. [docs/01-ids-and-xor-distance.md](docs/01-ids-and-xor-distance.md) —
   160-bit IDs, the XOR distance metric, why XOR specifically.
2. [docs/02-routing-table-and-kbuckets.md](docs/02-routing-table-and-kbuckets.md) —
   the 160 k-buckets, the move-to-tail / ping-and-maybe-evict update rule.
3. [docs/03-message-framing.md](docs/03-message-framing.md) — the wire
   protocol: envelopes, RPC IDs, and length-prefixed JSON over UDP (and
   the nuance of *why*, given that UDP already has message boundaries).
4. [docs/04-bootstrap-and-join.md](docs/04-bootstrap-and-join.md) — how a
   new node, given one contact, joins the network.
5. [docs/05-iterative-parallel-lookup.md](docs/05-iterative-parallel-lookup.md) —
   the α-concurrent lookup algorithm everything else is built on.

This README assumes you've read those, or will alongside it, and instead
focuses on: the end-to-end walkthrough of what actually happens at
runtime, the exact commands to run things, and what's deliberately not
here yet.

## Project layout

```
internal/kademlia/     the actual DHT implementation (see docs/ above)
  id.go                160-bit IDs, XOR distance, bucket indexing
  contact.go           (ID, IP, port) triple exchanged between nodes
  bucket.go            one k-bucket: ordering + update rule
  routingtable.go       160 k-buckets + "closest contacts" lookup
  messages.go          envelope + PING/FIND_NODE request/response shapes
  transport.go         UDP socket, length-prefixed framing
  node.go              RPC orchestration: send/wait, handle incoming, bootstrap
  lookup.go            the iterative alpha-concurrent lookup algorithm
  logging.go           structured stdout logging (log/slog)
  *_test.go            unit tests (id/xor, bucket update rule) +
                       integration_test.go (real nodes over real UDP)
cmd/node/main.go       CLI: run one node, optionally bootstrap, optionally
                       trigger one lookup
scripts/demo.sh        starts 5 node processes, bootstraps them, triggers
                       a lookup, checks the result (the project's "evaluation")
docs/                  one concept per file, in learning order (see above)
```

## End-to-end walkthrough: start, join, look up

This is what actually happens when you run the 5-node demo (next section),
traced through the code.

**1. A node starts.** `cmd/node/main.go` parses flags, picks a node ID
(`--id-seed` hashed via SHA-1, or random — `resolveID`), constructs a
`Node` (`kademlia.NewNode`), and calls `Node.Start()`, which launches
`Transport.Listen` in a background goroutine (`transport.go`). At this
point the node has an empty routing table (`RoutingTable`, 160 empty
`Bucket`s — [docs/02](docs/02-routing-table-and-kbuckets.md)) and is just
sitting there listening on a UDP port.

**2. A second node joins through it.** The new node is given
`--bootstrap host:port`. `Node.Bootstrap` ([docs/04](docs/04-bootstrap-and-join.md))
performs a lookup for *its own ID*, seeded with that one contact. That
lookup ([docs/05](docs/05-iterative-parallel-lookup.md)) sends `FIND_NODE`
RPCs ([docs/03](docs/03-message-framing.md)) to the seed, gets back
whatever contacts it knows of, asks the closest of *those*, and so on,
converging in `O(log n)` rounds. Every RPC the new node sends carries its
own ID, so every node it talks to along the way learns about it too
(`Node.handleMessage` → `touchContact` on the receiving end, applying the
k-bucket update rule from [docs/02](docs/02-routing-table-and-kbuckets.md)).
By the time `Bootstrap` returns, the new node has a populated routing
table, and the rest of the network has started to learn about it.

**3. More nodes join the same way**, each bootstrapping via any one
already-joined node (not necessarily the same one every time — any
contact works, since the lookup discovers the rest from there).

**4. A lookup is triggered.** Given a target ID, `Node.Lookup`
([docs/05](docs/05-iterative-parallel-lookup.md)) runs the same procedure
as step 2, but for an arbitrary target instead of the node's own ID: start
from the closest known contacts, query up to α of them in parallel, merge
what comes back, repeat until the k closest candidates have all been
queried. The result (`LookupResult`) reports the contacts found, and —
what the demo checks — how many rounds and RPCs it took.

Every RPC sent and received, and every k-bucket update (including
ping-then-evict decisions), is logged to stdout as a structured
`key=value` line (`log/slog`, `logging.go`) — this is the debugging tool
for this phase of the project, not a dashboard. Run any node with
`--verbose` to also see bucket updates and RPC timeouts at debug level.

## Running one node

```sh
go run ./cmd/node --port 9000
```

Useful flags (see `go run ./cmd/node --help` for the full list):

- `--id-seed <string>` — deterministic node ID = `SHA-1(string)`, instead
  of a random one. Useful for reproducible demos/tests (this is how
  `scripts/demo.sh` gets predictable IDs to check against).
- `--bootstrap <host:port>` — join through an existing node.
- `--lookup <40-char-hex-id>` (with `--lookup-after <duration>`) — after
  joining, wait a bit, then perform one extra lookup for that ID and log
  the result. This is how the demo script triggers and reads back a
  lookup from a plain CLI without needing any separate control channel.
- `--verbose` — debug-level logging (bucket updates, RPC timeouts).
- `--k`, `--alpha`, `--rpc-timeout` — protocol parameters, defaulting to
  the paper's `k=20`, `alpha=3`, plus a `2s` RPC timeout.

Two nodes, manually, in separate terminals:

```sh
# terminal 1
go run ./cmd/node --port 9000 --id-seed nodeA

# terminal 2
go run ./cmd/node --port 9001 --id-seed nodeB --bootstrap 127.0.0.1:9000
```

Terminal 2's log will show the `FIND_NODE` round trip and then a
`"bootstrap complete"` line once its self-lookup converges.

## Running the 5-node demo

```sh
bash scripts/demo.sh
```

This is the project's entire "evaluation" for this pass — one scenario you
can run and read, no graphs or sweeps. It:

1. Builds the `node` binary.
2. Starts 5 node processes on ports 9000–9004, with deterministic IDs
   (`demo0`..`demo4`), each bootstrapping via the previous one, waiting
   for each to log `"bootstrap complete"` before starting the next.
3. Has node4 (the last one) trigger a lookup for node2's ID.
4. Reads node4's log line for that lookup, extracts the round count, RPC
   counts, and the set of contacts found.
5. Compares that set against the 4 node IDs it independently expects
   (every node except node4 itself, since a node never includes itself in
   a lookup result), via `shasum -a 1` — the exact same `SHA-1(seed)` the
   Go code uses for `--id-seed`.
6. Prints `PASS` or `FAIL` plus the rounds/RPC counts, then stops all 5
   processes.

Example output:

```
=== Triggered lookup, as logged by node4 ===
time=... msg="TRIGGERED LOOKUP RESULT" ... rounds=2 rpcs_sent=4 rpcs_failed=0 closest_count=4 closest_ids=...

Rounds:      2
RPCs sent:   4
RPCs failed: 0

PASS: node4's lookup found exactly the other 4 nodes (demo0, demo1, demo2, demo3).
```

Full per-node logs are kept in a temp directory whose path is printed at
the end of the run, if you want to look at the raw RPC/bucket-update
traces behind the summary.

## Running the tests

```sh
go test ./...          # unit tests (ID/XOR math, bucket update rule) +
                        # an in-process integration test (5 nodes, real UDP)
go test ./... -race     # same, with the race detector (recommended —
                        # there's real concurrency here: parallel lookup
                        # RPCs, the background listen loop, eviction pings)
```

- `internal/kademlia/id_test.go` — XOR metric properties (identity,
  symmetry, triangle inequality via 1000 random triples), ID ordering,
  bucket-index edge cases.
- `internal/kademlia/bucket_test.go` — every branch of the k-bucket update
  rule in isolation: insert-until-full, re-seeing moves to tail,
  `KeepAlive` vs. `ReplaceStale`, and that live nodes are never dropped
  just because new contacts arrive.
- `internal/kademlia/routingtable_test.go` — a node never stores itself;
  `Closest` orders and caps correctly.
- `internal/kademlia/integration_test.go` — the same 5-node
  bootstrap-and-lookup scenario as `scripts/demo.sh`, but as real `Node`s
  in-process over real loopback UDP sockets rather than separate OS
  processes, which is what makes it fast enough to run as part of
  `go test ./...`.

## Deliberate simplifications (and why)

These are called out in more detail in the relevant `docs/` file; summary:

- **Flat 160-bucket routing table, no splitting** — the paper's routing
  table starts as one bucket and splits as it fills (Sec. 2.4); this
  project preallocates all 160 up front. Simpler, costs nothing at this
  scale, loses the ability to remember more than `k` contacts near your
  own ID in a huge network. See [docs/02](docs/02-routing-table-and-kbuckets.md).
- **No post-join bucket refresh** — the paper has a freshly-joined node
  additionally refresh buckets farther than its nearest neighbor
  (Sec. 2.3); this project relies on the self-lookup alone. See
  [docs/04](docs/04-bootstrap-and-join.md).
- **Lookup always queries all k closest once, with no early-exit
  heuristic** for "this round didn't improve, stop early" — same final
  answer, slightly more RPCs in the fast-converging case. See
  [docs/05](docs/05-iterative-parallel-lookup.md).
- **Length-prefixed framing over UDP**, even though a UDP datagram is
  already one atomic message — done for a cheap malformed-packet check
  and so the framing logic could carry over unchanged to a TCP transport
  later. See [docs/03](docs/03-message-framing.md).
- **Loopback-only addressing** (`Contact.IP` hardcoded to `127.0.0.1` in
  `Node.Self`) — no NAT traversal or external-IP discovery; fine for a
  localhost demo, would need revisiting for real multi-machine
  deployment.

## What's next

Deliberately out of scope for this pass, and roughly where each would
plug in if you pick this project back up:

- **`STORE` / `FIND_VALUE` RPCs**, i.e. actually storing and retrieving
  `(key, value)` pairs (paper Sec. 2.3, 2.5). This is the next natural
  step: add `MsgStore`/`MsgFindValue` message types next to
  `MsgFindNode` in `messages.go`, an in-memory `map[ID]Value` store on
  `Node`, a `Node.Store` method that does a `Lookup` for the key then
  sends `STORE` to the k closest nodes found, and a `Node.FindValue` that
  behaves like `Lookup` but short-circuits the moment any queried node
  returns the value directly instead of just contacts. The periodic
  republish logic (paper Sec. 2.5) would be a background goroutine on
  `Node`, started from `Start()`.
- **TCP piece transfer** (actually moving file data, as opposed to
  `(key,value)` metadata) — would live as a separate transport alongside
  `Transport`, likely reusing the exact same length-prefix framing
  function from `transport.go` (see [docs/03](docs/03-message-framing.md)'s
  note on why that framing was chosen with this reuse in mind), with
  `FIND_VALUE` results pointing at a peer and a piece ID, Kademlia used
  purely for peer discovery.
- **Security hardening** — the paper's basic DoS resistance (k-buckets
  resisting flooding, Sec. 2.2) is already here as a side effect of the
  update rule; not here: spoofed-sender validation beyond the RPC-ID
  check already in place, rate limiting, Sybil resistance, or anything
  resembling BitTorrent's tit-for-tat.
- **The visualizer / performance graphs / multi-device demo** — explicitly
  out of scope for this pass per the project brief; `scripts/demo.sh`'s
  pass/fail-plus-counts output is the entire "evaluation" for now.
- **Merkle trees** — not needed until there's actual chunked file content
  to verify (i.e. after piece transfer exists).

## Paper reference

`maymounkov-kademlia-lncs.pdf` (and duplicates) in this directory: Petar
Maymounkov and David Mazières, *Kademlia: A Peer-to-peer Information
System Based on the XOR Metric*, IPTPS 2002. Section numbers cited
throughout the code comments and `docs/` refer to this paper.
