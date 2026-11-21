# Debugging guide (VSCode + terminal)

A distributed system is hard to debug with breakpoints alone. Every node
is a separate process, and pausing one makes the others time out. So the
tools here go from least intrusive to most:

1. **Read the logs** — every RPC sent/received is already logged.
2. **Poke live nodes** from their terminals with the built-in console.
3. **Breakpoints and logpoints** in VSCode, on one node at a time.
4. **Debug tests**, which run many nodes in one process.
5. **Capture packets** to see the actual bytes on the wire.

Start with 1 and 2. Reach for 3 when you need to see variable values
inside a function.

## Setup (once)

- Install the **Go** extension for VSCode (by the Go team at Google).
- Command palette → **Go: Install/Update Tools** → select `dlv` (Delve,
  the Go debugger).
- On macOS, if attaching to a process fails with a permissions error, run
  `sudo DevToolsSecurity -enable` once.

---

## 1. Logs

Every node writes one `key=value` line per event to stdout (`log/slog`,
`internal/kademlia/logging.go`).

| Flag | What you see |
|---|---|
| *(default)* | node start, every `rpc sent` / `rpc received`, lookup/bootstrap summaries |
| `--verbose` | plus every k-bucket update, eviction decisions, lookup rounds, RPC timeouts |
| `--quiet` | warnings and errors only (useful while typing console commands) |

Every line carries `node=<first 8 hex of ID>`, and every RPC line carries
`rpc_id=…`, which is identical on the sender and the receiver. That gives
two useful recipes on the demo's log directory (printed at the end of
`scripts/demo.sh`):

```sh
cd /var/folders/.../tmp.XXXX          # the path the demo printed

# Follow ONE request across processes: the send, the receive, the reply.
grep -h 'rpc_id=f6ba6d98' node*.log

# Merge all five logs into one timeline (lines start with time=<ISO>, so plain sort works).
cat node*.log | sort | sed -E 's/^time=[^ ]+ //' | less
```

The merged timeline is the closest thing to watching the whole network
at once. [DRY-RUN.md](DRY-RUN.md) walks through one.

To see logs from the **integration tests** (normally discarded):

```sh
KADEMLIA_TEST_LOGS=1 go test ./internal/kademlia -run '^TestBootstrapLearnsRealSeedID$' -count=1 -v
```

---

## 2. A live network in separate VSCode terminals

Open the terminal panel and split it (the ⊞ split icon, or `Cmd+\` with
the terminal focused) into three or four panes. Run one node per pane:

```sh
# pane 1
go run ./cmd/node --port 9000 --id-seed demo0

# pane 2
go run ./cmd/node --port 9001 --id-seed demo1 --bootstrap 127.0.0.1:9000

# pane 3
go run ./cmd/node --port 9002 --id-seed demo2 --bootstrap 127.0.0.1:9001 --quiet
```

Each node reads **console commands** from its terminal
(`cmd/node/console.go`). Type into any pane:

| Command | What it does |
|---|---|
| `help` | list commands |
| `id` | this node's ID, UDP address, TCP port |
| `table` | every non-empty k-bucket, contacts oldest → newest; how many keys it holds values for. A contact that failed `--stale-after` RPCs in a row ends with `(stale: lookups skip it)` |
| `lookup <hex-id>` / `lookup self` | run an iterative lookup, print rounds, RPCs, and the closest nodes with their XOR distances |
| `ping <host:port>` | PING an address, report the reply time |
| `put <name> <text…>` | STORE the text under key SHA-1(name); prints which nodes kept it |
| `get <name>` | FIND_VALUE for SHA-1(name); says whether it was answered locally or via the network |
| `share <path>` | publish a file; prints its content key and a magnet link carrying it ([12](12-magnet-links.md)) |
| `fetch <key\|magnet> <out-path>` | download a file by content key or magnet link (the link's name is ignored); prints pieces per provider and any failed providers |
| `files` | files this node can serve, with how many pieces it has (`has 7/16` while downloading) and each one's magnet link |

Log lines and console output share the terminal. Use `--quiet` on the
pane you're typing into. If you start a node in the background with `&`
in an interactive shell, pass `--console=false`, or the shell will
suspend it for reading the terminal.

### Exercises that show what the demo can't

**a) Watch a join happen.** With panes 1–2 running, type `table` in pane
1 (one contact: node1). Start pane 3, then type `table` in pane 1 again:
node2 appeared even though node2 bootstrapped via node1, not node0.
That's the "every message updates the sender's bucket" rule
([02](02-routing-table-and-kbuckets.md)).

**b) Force a k-bucket eviction.** The demo's buckets never fill (`k=20`).
Make them tiny:

```sh
# pane 1 — k=1 so each bucket holds ONE contact; short timeout so eviction is quick
go run ./cmd/node --port 9000 --id-seed demo0 --k 1 --verbose --rpc-timeout 1s
# pane 2
go run ./cmd/node --port 9002 --id-seed demo2 --bootstrap 127.0.0.1:9000 --quiet
# pane 3
go run ./cmd/node --port 9003 --id-seed demo3 --bootstrap 127.0.0.1:9000 --quiet
```

demo2 and demo3 both fall in node0's bucket 159, which now has room for
one. Pane 1 shows:

```
DEBUG msg="bucket full, pinging least-recently-seen contact before evicting" stale=db612776…(demo2) candidate=94fab99d…(demo3)
DEBUG msg="stale contact still alive, kept; new contact discarded" kept=db612776… discarded=94fab99d…
```

Now stop pane 2 (`Ctrl+C`) and type `ping 127.0.0.1:9000` in pane 3:

```
DEBUG msg="rpc timed out" type=PING to=db612776…
DEBUG msg="stale contact unresponsive, evicted" evicted=db612776… replacement=94fab99d…
```

That's paper Sec. 2.2 exactly: live contacts are never replaced, and dead
ones are replaced only after a failed ping.

**c) A lookup with a dead node.** Run three nodes normally, stop one,
then `lookup self` on another. You'll see `rpcs_failed=1`, and the dead
node won't be in the result. The lookup still finishes, after one RPC
timeout.

**d) Share a file by hand, then kill the seeder.** Four panes, `k = 3`
so values don't land everywhere:

```sh
go run ./cmd/node --port 9000 --id-seed demo0 --k 3 --quiet
go run ./cmd/node --port 9001 --id-seed demo1 --k 3 --quiet --bootstrap 127.0.0.1:9000
go run ./cmd/node --port 9002 --id-seed demo2 --k 3 --quiet --bootstrap 127.0.0.1:9000
go run ./cmd/node --port 9003 --id-seed demo3 --k 3 --quiet --bootstrap 127.0.0.1:9000
```

- pane 2: `share README.md` → copy the printed content key (or the
  magnet link: `fetch` takes either)
- pane 3: `fetch <key> /tmp/copy1.md` → all pieces from `127.0.0.1:9001`
- any pane: `table` ends with `holding values for N keys`, which is
  non-zero on the 3 nodes holding the provider records
- `Ctrl+C` pane 2 (the seeder), then pane 4: `fetch <key> /tmp/copy2.md`
  → `provider 127.0.0.1:9001 failed and was skipped`, pieces from 9002

Drop `--quiet` on the downloading pane to watch every `FIND_VALUE`, TCP
connection, and piece arrive.

**e) Bootstrap mistakes.** `--bootstrap 127.0.0.1:9999` (nobody there)
fails with "did not answer PING" after `--rpc-timeout`. Bootstrapping a
node against its own port fails immediately, and so does bootstrapping
through a node started with the same `--id-seed` ("has the same ID as this
node"). `--bootstrap 10.10.0.1` (no port) or an IPv6 address stops the node
at startup with `parsing -bootstrap`. Every join scenario, with its test:
[04](04-bootstrap-and-join.md#join-scenarios-what-if).

### Reading a node's state over HTTP: `--api`

The console's `table` and `files` show one node at a time, in its own
terminal. Start nodes with `--api` and you can read any of them from
anywhere, as JSON ([NODE-API.md](NODE-API.md)):

```sh
go run ./cmd/node --port 9000 --id-seed demo0 --api 127.0.0.1:8000
go run ./cmd/node --port 9001 --id-seed demo1 --bootstrap 127.0.0.1:9000 --api 127.0.0.1:8001

curl -s 127.0.0.1:8000/api/table        # node0's buckets, with XOR distances
curl -s 127.0.0.1:8001/api/store        # values node1 holds for the network
```

Handy for checking exercise (d) above: `/api/store` shows exactly which
nodes hold a file's provider record.

To watch one node live, without its terminal, follow its event stream.
`-N` stops curl from buffering. `?level=debug` shows bucket updates and
timeouts even if the node runs without `--verbose`:

```sh
curl -sN 127.0.0.1:8000/api/events               # every RPC in/out, value stored, piece received
curl -sN '127.0.0.1:8000/api/events?level=debug' | grep -E 'timed out|evicted'
```

With `--api-control` too, any node takes console commands over HTTP, so
one terminal can drive the whole network:

```sh
curl -s -X POST -H 'Content-Type: application/json' -d '{"target":"self"}' 127.0.0.1:8001/api/lookup
curl -s -X POST -H 'Content-Type: application/json' -d '{"name":"greeting","text":"hi"}' 127.0.0.1:8000/api/put
```

Don't forget `-H 'Content-Type: application/json'`: without it the
answer is `415` (on purpose; NODE-API.md, "Safety"). Pick API ports away from the node
ports (8000s for APIs, 9000s for nodes): a node's UDP and TCP ports
already use its own number.

### Playing a dishonest provider: `--debug-corrupt-pieces`

`--debug-corrupt-pieces` is a **DEBUG ONLY** flag, **off by default**.
The node's file server flips one byte of every piece it sends, with the
genuine Merkle proof (`server.answer`; `Peer.SetDebugCorruptPieces`).
The file on disk is untouched. It logs a `WARN` at startup, and one per
piece it corrupts:

```
WARN msg="DEBUG: this node corrupts every piece it serves (genuine proofs, wrong bytes)"
WARN msg="DEBUG: serving a corrupted piece" index=8
```

A downloader that asks it for a piece logs `provider failed, trying the
next one … failed its Merkle proof check` and finishes from someone else.
`scripts/demo-merkle.sh` does exactly this ([11](11-merkle-trees.md)).

### Slowing transfers down on purpose: `--debug-serve-delay`

On localhost a 1 MiB file moves in ~20 ms, so you can't kill a seeder or
set a breakpoint "mid-download" by hand. `--debug-serve-delay <duration>`
is a **DEBUG ONLY** flag, **off (0) by default**. It makes this node's
file server wait that long before answering each `GET_PIECE`
(`server.answer`, `internal/transfer/server.go`; set through
`Peer.SetDebugServeDelay`). The node logs a `WARN` at startup so you
can't forget it's on:

```
WARN msg="DEBUG: delaying every piece this node serves" delay=150ms
```

Example: repeat exercise (d) with a 16-piece file
(`head -c 1048576 /dev/urandom > /tmp/big.bin`, then `share /tmp/big.bin`)
and `--debug-serve-delay 300ms` on every pane. Now a download takes a few
seconds, long enough to `Ctrl+C` the seeder in the middle:

- **One downloader:** nobody else has the missing pieces. You'll see
  `download stalled …` warnings, and after 15 s
  `fetch failed: … stalled`.
- **Two downloaders** (start the second `fetch` before the kill): type
  `files` in each pane to see `has N/16`. If together they already hold
  all 16, they finish from each other after the kill. If not, the pieces
  nobody had are gone and both stall.

Don't use the flag for timing measurements: it's an artificial delay.

`--max-inflight <n>` (default 4) caps how many pieces one download
requests at once. Set it to 1 to watch rarest-first one piece at a time
in the logs (`piece requested` / `piece received`).

### Giving a node a real uplink: `--debug-upload-rate`

Loopback has no bandwidth limit: a localhost "server" can feed any
number of downloaders at once, which hides the bottleneck P2P exists to
remove. `--debug-upload-rate <bytes/s>` is a **DEBUG ONLY** flag, **off
(0) by default**. It caps this node's total file upload, across all its
connections, at that rate: every response reserves its share of one
shared timeline before it's written (`uplink` in
`internal/transfer/server.go`; set through `Peer.SetDebugUploadRate`).
The node logs a `WARN` at startup:

```
WARN msg="DEBUG: capping this node's upload" bytes_per_sec=10000000
```

Unlike `--debug-serve-delay`, it is meant for measurements: graph 2 of
[10-performance-graphs.md](10-performance-graphs.md) gives every node and
the centralized baseline the same cap. Downloads stay uncapped.

### Scripts with many nodes: `scripts/lib/nodes.sh`

The newer demos source a small helper. `start_node i port boot [flags…]`
starts node `i` with its stdin on a FIFO held open on file descriptor
`10 + i`, and waits for `node started` (and `bootstrap complete`).
`send i "command"` types into its console. `kill_node i` sends SIGKILL.
`pieces_received i` lists the piece indexes in its log. Gotchas it
handles:

- **bash 3.2** (macOS's default): no associative arrays, no `{fd}>`
  syntax. Per-node variables are `PID3` etc., read with
  `eval echo \$PID$i`. Descriptors above 9 work fine in 3.2.
- **Never `sleep` for readiness.** Wait for a log line (`wait_for_log`).
  A freshly built binary can be slow to start on macOS.
- **Job notices.** Killing a background job makes bash print
  `Killed: 9`. `kill_node` reaps it with `wait` to keep output clean.

For `scripts/demo-big-network.sh` specifically:

- **Ports.** It uses `9300…9300+N-1` (`BASE_PORT`), away from the other
  demos' 9000–9004 and 9200–9204. If a run is interrupted, check for
  leftovers with `lsof -nP -i :9300-9399`.
- **Reproducing a failure.** Joins, queriers, and targets come from
  bash's `RANDOM`, seeded with `SEED` (printed in the first line). Rerun
  with `SEED=<number>` to get the same network and the same lookups. Node
  IDs are fixed (`SHA-1("big<i>")`), so the same seed means the same run.
- **Reading one node's view.** Each node logs to `$WORK/node<i>.log`.
  For a mismatch, look in the querying node's log for the lookup's
  `FIND_NODE` sends and replies (DRY-RUN part 4 shows how one reads).
  Then use the console's `table` on the nodes you expected to be found.

For the edge-case scripts (`scripts/edge/`, [EDGE-CASES.md](EDGE-CASES.md)):

- **Ports.** Each case has its own block from 9425 up (A2: 9425…9444,
  A3: 9450…9471, A4: 9475…9494, B1: 9500…9515, B2: 9525…9532,
  B3: 9550…9561);
  the list is in EDGE-CASES.md. Check leftovers with
  `lsof -nP -i :9400-9699`.
- **Extra helpers** live in `scripts/lib/edge.sh`, sourced after
  `nodes.sh`. `node_ids_init N` computes every node's ID once (into
  `ID0`, `ID1`, …). `closest_nodes <key> <k> <indexes…>` is the
  brute-force answer. `wait_for_count <file> <regex> <n> <s>` waits for
  the *n*-th match, for when one node runs the same command twice (plain
  `wait_for_log` would see the first, old line). `holders_of <key> <n>`
  lists the nodes that logged `value stored` for a key. `lookup_answer
  <i> <target> <n>` reads the IDs from node i's *n*-th `lookup` of a
  target. `now_ms` is a monotonic clock for timing. Don't time with the
  wall clock: under WSL it gets re-synced mid-run, and a 2 s lookup once
  measured 1.34 s.
- **A node with a hand-picked ID.** `start_node i port boot --id-seed
  <seed>` overrides the default `<SEED_PREFIX><i>`, because Go's `flag`
  package keeps the last value of a repeated flag. Set `ID<i>` to match
  yourself. B3 uses this for nodes chosen by brute force to be close to
  a key. A4 uses it to give three networks the same IDs.
- **Starting many nodes at the same moment.** `start_node_nowait` (in
  `edge.sh`) is `start_node` without the waits. Afterwards, wait for
  each node's `bootstrap complete` yourself (A2 does).
- **Race detector on real processes.** `export GOFLAGS=-race` before
  `nodes_init`, and the node binary is built with `-race`. A race prints
  `WARNING: DATA RACE` and a stack trace into that node's log. A2 does
  this by default (`RACE=0` to skip). It needs a C compiler (cgo), and
  each process uses more memory.
- **`set -o pipefail` gotcha.** `grep` exits 1 when nothing matches, and
  with `pipefail` that kills the script inside `$(…)`. Write
  `{ grep … || true; }` wherever "no match" is a normal answer. Two edge
  scripts died on exactly this while being written.
- **A FAIL is information, not necessarily a bug in the script.** It
  means the code didn't behave as predicted. Read the node logs in the
  printed work directory, as for the demos.

---

## 3. Breakpoints in VSCode

`.vscode/launch.json` has ready-made configurations (Run and Debug panel,
`Cmd+Shift+D`):

| Configuration | What it runs |
|---|---|
| `node0 :9000 (first node)` | node0 under the debugger, `--verbose` |
| `node1 :9001 (joins via 9000)` | node1, bootstraps via node0 |
| `node2 :9002 (joins via 9001)` | node2, bootstraps via node1 |
| `test: TestEndToEndBootstrapAndLookup` | one integration test under the debugger |
| `attach to a running node process` | pick an already-running node and attach |

All of them use `"console": "integratedTerminal"`, so the node's console
commands work in the terminal VSCode opens.

**Start them one at a time, in order.** Wait for `node started` in node0
before launching node1. If node1 starts first, its PING goes to a port
nobody is listening on yet. It then waits a full `--rpc-timeout` for a
reply that never comes.

### The one gotcha: timeouts

When a breakpoint pauses node0, node0 stops answering. Any node waiting
on it gives up after `--rpc-timeout` (default **2s**) and treats node0 as
failed. That can trigger eviction or `rpcs_failed`. Debugging would then
change the very behavior you're looking at. The launch configs therefore
pass `--rpc-timeout 5m`. Do the same if you launch by hand:

```sh
go run ./cmd/node --port 9001 --bootstrap 127.0.0.1:9000 --rpc-timeout 5m
```

(Lookups and bootstrap use 5× the RPC timeout as their overall deadline,
so they stretch too.)

### A practical setup

Run node0 and node1 **normally** in terminals. Run only the node you
care about (e.g. node2) under the debugger. Fewer paused processes, fewer
surprises.

### Where to put breakpoints

| To see… | Break at |
|---|---|
| every incoming message, decoded | `Node.handleMessage` (`node.go`) |
| the k-bucket rule deciding | `Bucket.Update` (`bucket.go`) — inspect `b.contacts` |
| an eviction decision | `Node.resolveEviction` (`node.go`) |
| a reply being matched to its request | `Node.deliverResponse` (`node.go`) — inspect `n.pending` |
| each lookup round | the `for {` loop in `Node.Lookup` (`lookup.go`) — inspect `order`, `candidates`, `batch` |
| a join | `Node.Bootstrap` (`node.go`) |
| raw bytes before JSON decoding | `decodeEnvelope` (`transport.go`) — inspect `packet` |
| a value arriving / being served | `Node.replyStore`, `Node.replyFindValue` (`node.go`) |
| FIND_VALUE stopping early | the `if len(result.Values) > 0` check in `iterativeFind` (`lookup.go`) |
| a provider answering a TCP request | `server.handleConn` / `server.answer` (`internal/transfer/server.go`) |
| the downloader choosing the next piece and provider | `pickPiece` (`picker.go`), called from `swarmDownload.schedule` (`download.go`) |
| a result coming back from a worker | `swarmDownload.handle` (`download.go`); inspect `ev`, `d.inflight`, `d.providers` |
| why a download is (not) considered stalled | `swarmDownload.canProgress` (`download.go`) |
| a peer refusing a piece it doesn't have yet | `Library.readPiece` (`library.go`) |
| a piece failing verification | `Manifest.VerifyPiece` (`manifest.go`) |

Useful VSCode features:

- **Conditional breakpoint** (right-click the gutter → *Add Conditional
  Breakpoint*): e.g. `env.Type == "FIND_NODE"` in `handleMessage`, to
  skip PINGs.
- **Logpoint** (right-click → *Add Logpoint*): prints a message such as
  `bucket {i}: {len(b.contacts)}` **without pausing**. No pause means no
  timeouts, so this is often better than a breakpoint here.
- **Call Stack panel**: lists every goroutine. You'll see the
  `Transport.Listen` loop, one goroutine per message being handled, and
  one per in-flight lookup RPC. It's a good way to see the concurrency
  described in [05](05-iterative-parallel-lookup.md).

### Attaching to a node you started yourself

Build with optimizations off, so variables aren't "optimized away":

```sh
go build -gcflags=all="-N -l" -o /tmp/kad-node ./cmd/node
/tmp/kad-node --port 9003 --bootstrap 127.0.0.1:9002 --rpc-timeout 5m
```

Then run `attach to a running node process` and pick `kad-node` from the
list.

---

## 4. Tests

Go runs tests **per package**, not per file. All of
`internal/kademlia/*_test.go` is one package, so "run one test file" in
practice means "run the tests whose names match a pattern" with `-run`
(a regex). Different terminals can run different patterns:

```sh
go test ./internal/framing -v                              # frames on datagrams and streams
go test ./internal/transfer -run 'Manifest|Piece|Content' -v   # piece hashing, no network
go test ./internal/transfer -run 'Share|Download|Corrupt' -v   # real UDP + TCP transfers
go test ./internal/kademlia -run 'Store|FindValue|ValueStore' -v   # STORE / FIND_VALUE
go test ./internal/kademlia -run '^TestBucket' -v          # bucket.go's update rule
go test ./internal/kademlia -run '^TestXor|^TestLess|^TestBucketIndex|^TestID|^TestHash' -v   # id.go
go test ./internal/kademlia -run '^TestRoutingTable' -v    # routingtable.go
go test ./internal/kademlia -run '^TestEndToEnd|^TestBootstrap|^TestPing' -v   # real UDP
go test ./... -race -count=1                               # everything, race detector, no cache
```

`-count=1` disables Go's test-result cache. Without it, an unchanged
test prints `(cached)` and doesn't actually run.

In VSCode, every `func TestXxx` has small **run test | debug test** links
above it. *debug test* runs just that test under Delve, so breakpoints in
`bucket.go` etc. are hit. The integration tests run 3–5 nodes inside one
process, so one debugging session sees every node. Pausing there pauses
all of them together, which avoids the timeout problem from section 3.

---

## 5. Packets on the wire

Every message is `[4-byte length][JSON]` in one UDP datagram
([03](03-message-framing.md)), so it's readable in a capture:

```sh
sudo tcpdump -i lo0 -A 'udp portrange 9000-9004'
```

Or Wireshark: interface **Loopback: lo0**, display filter
`udp.port >= 9000 && udp.port <= 9004`. The first 4 bytes of each
payload are the length, and the rest is the JSON envelope.

For file transfers, capture TCP on the same ports:

```sh
sudo tcpdump -i lo0 'tcp portrange 9000-9004'
```

You'll see the three-way handshake (`[S]`, `[S.]`, `[.]`) when a
downloader connects, then the request/header/data frames. In Wireshark,
*Follow → TCP Stream* on one connection shows the whole exchange. A
dead provider shows up as a `[S]` answered by `[R.]` (reset), which is
what "connection refused" is.

---

## Symptom → likely cause

| Symptom | Likely cause / fix |
|---|---|
| `bind: address already in use` | A node from an earlier run is still alive (it holds both UDP and TCP on that port number). Find it: `lsof -nP -i :9000`, then `kill <pid>`. |
| `fetch failed: … file exists` | Downloads never overwrite; pick a new output path. |
| `no providers found` | Wrong key, or every node holding the provider records is gone. The providers' next republish (`--republish-interval`, default 1 h) puts the records back; until then the file can't be found. |
| A download pauses ~2 s before starting | Its `FIND_VALUE` round included a dead node (synchronous rounds; see DRY-RUN part 2). |
| `download stalled: no live provider has the missing pieces` | Every node that had the missing pieces is gone. It keeps rediscovering for 15 s, then gives up (`download gave up, still serving its pieces`): the partial file stays, and the node keeps serving the pieces it has. |
| `fetch failed: … is already in the library` | This node already shares, has finished, or is downloading that key. |
| `fetch failed: … has a stopped partial download at <path>` | An earlier fetch of this key gave up. Fetch into that path to resume it, or restart the node and delete the file to start over ([08](08-swarming.md#resuming)). |
| `fetch failed: transfer: resume … changed since the download stopped` | The partial file was edited, truncated, or replaced after its download gave up, so a piece on disk no longer matches its hash. Nothing was written to it, and its library entry was dropped. Move the file away and fetch again. |
| `fetch failed: transfer: resume … (still serving its pieces; …)` | The partial file is there but couldn't be opened for writing (e.g. read-only). Its pieces are still served; fix the cause and run the same fetch again. |
| `N piece(s) were already on disk (resumed)` | This fetch continued one that had given up, and fetched only the missing pieces. |
| `transfer: this is a BitTorrent magnet link …` | The link says `xt=urn:btih:`, a BitTorrent info hash. This network's links say `xt=urn:tp2p:` ([12](12-magnet-links.md#why-tp2p-and-not-btih)). |
| `transfer: want a 40-hex-char content key or a magnet:?xt=urn:tp2p:... link: …` | `fetch`'s first argument is neither: a mistyped key, or a link missing its `magnet:` start. |
| `usage: fetch …` after pasting a link | The pasted link contains a space, so the console saw extra arguments. Links printed by `share` and `files` never contain one (a space in a name is `+`). |
| After a long pause at a breakpoint in the download loop, providers get marked dead | While the loop is paused no new requests go out. Providers close connections idle for their IO timeout (10 s), and the next request on such a connection fails. Use logpoints, or pause only briefly. |
| `bootstrap node … did not answer PING` | Seed not started yet, wrong port, or seed paused in the debugger with a short `--rpc-timeout`. |
| Lookup shows `rpcs_failed` while debugging | Some node was paused at a breakpoint longer than `--rpc-timeout`. Raise it. |
| A node "disappears" from another's table | It was evicted after failing a ping. Check the `--verbose` log of the node that dropped it. |
| Console output mixed with logs | `--quiet` |
| Background node suspended (`tty input`) | Started with `&` in an interactive shell; add `--console=false`. |
| Test passes without running | Cached; add `-count=1`. |
