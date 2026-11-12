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
| `id` | this node's ID and address |
| `table` | every non-empty k-bucket, contacts oldest → newest |
| `lookup <hex-id>` / `lookup self` | run an iterative lookup, print rounds, RPCs, and the closest nodes with their XOR distances |
| `ping <host:port>` | PING an address, report the reply time |

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

**d) Bootstrap mistakes.** `--bootstrap 127.0.0.1:9999` (nobody there)
fails with "did not answer PING" after `--rpc-timeout`. Bootstrapping a
node against its own port fails immediately.

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

---

## Symptom → likely cause

| Symptom | Likely cause / fix |
|---|---|
| `bind: address already in use` | A node from an earlier run is still alive. Find it: `lsof -nP -iUDP:9000`, then `kill <pid>`. |
| `bootstrap node … did not answer PING` | Seed not started yet, wrong port, or seed paused in the debugger with a short `--rpc-timeout`. |
| Lookup shows `rpcs_failed` while debugging | Some node was paused at a breakpoint longer than `--rpc-timeout`. Raise it. |
| A node "disappears" from another's table | It was evicted after failing a ping. Check the `--verbose` log of the node that dropped it. |
| Console output mixed with logs | `--quiet` |
| Background node suspended (`tty input`) | Started with `&` in an interactive shell; add `--console=false`. |
| Test passes without running | Cached; add `-count=1`. |
