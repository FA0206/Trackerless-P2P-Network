# Code architecture: what depends on what

The concept docs (`01`–`05`) explain *ideas*. This document explains the
*code*: what each file is responsible for, which files depend on which,
which goroutines exist, and what you have to touch when you change
something.

## Packages

```
cmd/node            the executable: flags, console, process lifecycle
   │  imports
   ▼
internal/kademlia   the DHT itself: knows nothing about flags, terminals, or files
   │  imports
   ▼
Go standard library only (net, encoding/json, crypto/sha1, log/slog, ...)
```

The dependency only goes downward. `internal/kademlia` never imports
`cmd/node`, so the DHT can be driven from a test, a script, or a future
file-transfer layer without dragging the CLI along. Go's `internal/`
directory rule enforces that only code inside this module can import it.

## Files inside `internal/kademlia`, bottom to top

Each file depends only on files in lower layers (verified by grepping
which identifiers each file uses):

```
 Layer 4   lookup.go ─────────────┐   iterative alpha-parallel lookup (a method on Node)
                                  │ uses Node.FindNode, rt.Closest, Xor, Less
 Layer 3   node.go ───────────────┤   RPC engine: send/wait, receive/dispatch, bucket updates, Bootstrap
             │ uses ↓             │
 Layer 2   routingtable.go   transport.go   logging.go
             │ uses ↓            │ uses ↓
           bucket.go          messages.go
             │ uses ↓            │ uses ↓
 Layer 1   contact.go ◄──────────┘
             │ uses ↓
 Layer 0   id.go          (pure functions on 160-bit IDs; no dependencies)
```

| File | Responsibility | Depends on | Depended on by |
|---|---|---|---|
| `id.go` | `ID` type, `Xor`, `Less`, `BucketIndex`, `HashID`, hex/JSON encoding | stdlib only | everything |
| `contact.go` | `Contact` = (ID, IP, UDP port); build one from a packet's source address | `id.go` | bucket, messages, node, lookup |
| `bucket.go` | one k-bucket: ordering by recency, the update rule, `KeepAlive`/`ReplaceStale`. **No networking.** | `contact.go` | `routingtable.go` |
| `routingtable.go` | 160 buckets indexed by `BucketIndex`; `Closest(target, n)`; `Snapshot` | `bucket.go`, `id.go` | `node.go`, `lookup.go` |
| `messages.go` | wire shapes: `Envelope`, `FindNodeRequest/Response`, message type names | `id.go`, `contact.go` | `transport.go`, `node.go` |
| `transport.go` | UDP socket; `[length][JSON]` framing; read loop that hands decoded envelopes to a callback. **Knows nothing about RPC semantics.** | `messages.go` | `node.go` |
| `logging.go` | `NewLogger` (slog, stdout, tagged with node ID) | `id.go` | `node.go`, `cmd/node` |
| `node.go` | `Node`: owns routing table + transport; matches replies to requests (`pending` map); applies the bucket rule on every receive; eviction pings; `Ping`, `FindNode`, `Bootstrap` | all of the above | `lookup.go`, `cmd/node` |
| `lookup.go` | `Node.Lookup`: the iterative, alpha-parallel algorithm and `LookupResult` | `node.go` (methods), `routingtable.go`, `id.go` | `node.go` (`Bootstrap` calls it), `cmd/node` |

The two **pure** layers, `id.go` and `bucket.go`, have no I/O. That's
deliberate: they hold the logic that's easiest to get subtly wrong (bit
math, the eviction rule), so they're unit-tested directly with no
sockets involved (`id_test.go`, `bucket_test.go`). Anything that needs
the network is tested end-to-end in `integration_test.go`.

## The public API (what code outside the package can call)

```go
kademlia.NewNode(id, port, k, alpha, rpcTimeout, logger) (*Node, error)
(*Node).Start()                       // launch the receive loop
(*Node).Close() error
(*Node).Bootstrap(ctx, *net.UDPAddr) (*LookupResult, error)
(*Node).Lookup(ctx, target ID) (*LookupResult, error)
(*Node).Ping(ctx, Contact) bool
(*Node).FindNode(ctx, Contact, target ID) ([]Contact, error)   // a single RPC, not a lookup
(*Node).Addr(), Self(), RoutingTableSize(), RoutingTableSnapshot()

kademlia.HashID, RandomID, IDFromHex, Xor, Less, NewLogger, DefaultK, DefaultAlpha, DefaultRPCTimeout
```

`cmd/node` uses only this API. Anything new built on the DHT (file
transfer, for example) should do the same.

## How one RPC flows through the files

```
 caller (Lookup, Bootstrap, console)
   │
   ▼
 node.go     sendAndWait ── registers pending[rpcID] ──┐
   │                                                    │ waits on channel
   ▼                                                    │
 transport.go Send: JSON → [len][json] → WriteToUDP     │
   ═══════════════ network ═══════════════              │
 transport.go Listen: ReadFromUDP → decodeEnvelope      │   (on the OTHER node)
   ▼                                                    │
 node.go     handleMessage → touchContact(sender)       │
   │            → replyPong / replyFindNode → Send ═══╗ │
   ═══════════════ network ═══════════════            ║ │
 transport.go Listen → decodeEnvelope  ◄═══════════════╝ │   (back on the caller's node)
 node.go     handleMessage → touchContact(sender)       │
               → deliverResponse → pending[rpcID] ──────┘
```

[DRY-RUN.md](DRY-RUN.md) shows this with real log lines.

## Goroutines and shared state

| Goroutine | Started by | Lifetime | Touches |
|---|---|---|---|
| main | `cmd/node` | process | waits for SIGTERM; runs Bootstrap synchronously |
| receive loop | `Node.Start` | until `Close` | the socket; spawns one handler per packet |
| message handler | `Transport.Listen` (`go handler(...)`) | one packet | routing table (locked), `pending` (locked), sends replies |
| lookup RPC | `Node.Lookup` (`go func(c *lookupCandidate)`) | one RPC | sends one result on the round's `outcomes` channel, nothing else |
| eviction check | `Node.touchContact` (`go n.resolveEviction`) | one ping | routing table (locked) |
| console | `cmd/node` (`go runConsole`) | until stdin EOF | public `Node` API only |
| `--lookup` trigger | `cmd/node` | one lookup | public `Node` API only |

Locks:

- `Bucket.mu` — every bucket operation. `RoutingTable` itself has no lock;
  its array of bucket pointers never changes after construction.
- `Node.pendingMu` — the `pending` map of in-flight RPCs.

A lookup's own state (`candidates`, `order`) is **not** locked. It's
only touched by the goroutine running `Lookup`. The per-RPC goroutines
just send results on a channel. Keep it that way if you modify the
lookup: it's why it doesn't need a mutex.

`go test ./... -race` checks all of this. Run it after any change to
`node.go`, `lookup.go`, or `bucket.go`.

## Invariants that are easy to break

1. **A received message updates the sender's bucket before anything
   else.** `handleMessage` calls `touchContact` first. Every other
   mechanism (how nodes learn about each other, how joining announces
   you) depends on it.
2. **Only confirmed IDs enter the routing table.** IDs come from the
   `SenderID` of a message that actually arrived, paired with the
   address it actually came from. Never insert a contact you've only
   heard about secondhand (e.g. from a `FIND_NODE` reply) or one with a
   placeholder ID. That's why `Bootstrap` PINGs first.
3. **A node never stores or returns itself.** `RoutingTable.Update` and
   `Lookup.addCandidate` both filter `self`.
4. **`Bucket` never does I/O.** Anything that needs the network (the
   eviction ping) lives in `Node`, which reports back via `KeepAlive` or
   `ReplaceStale`.
5. **Register `pending[rpcID]` before sending.** Otherwise a fast reply
   can arrive before anyone is waiting for it and be dropped.
6. **A message must fit in one datagram** (`maxDatagramSize` = 8 KB in
   `transport.go`). Anything big must not travel over this UDP channel.

## If you change X, also touch Y

| Change | Also update |
|---|---|
| Add an RPC type | `messages.go` (type + payload structs), `node.go` (`handleMessage` switch, a `replyXxx`, a client method using `sendAndWait`), docs `03` and FAQ |
| Change the bucket rule | `bucket.go`, `bucket_test.go`, `node.go` (`touchContact`/`resolveEviction`), docs `02` |
| Change lookup termination or concurrency | `lookup.go`, `integration_test.go` (expected RPC counts), docs `05`, `DRY-RUN.md` (round/RPC numbers) |
| Change the join procedure | `node.go` `Bootstrap`, `TestBootstrap*`, docs `04`, `DRY-RUN.md` |
| Change log message text | `scripts/demo.sh` greps for `"bootstrap complete"` and `"TRIGGERED LOOKUP RESULT"` — keep those exact strings |
| Change how `--id-seed` maps to an ID | `scripts/demo.sh` recomputes IDs with `shasum`; they must agree |
| Add a CLI flag | `cmd/node/main.go`, README "Running one node", `.vscode/launch.json` if relevant |
