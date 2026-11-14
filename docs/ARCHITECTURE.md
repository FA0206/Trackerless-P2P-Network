# Code architecture: what depends on what

The concept docs (`01`–`07`) explain *ideas*. This document explains the
*code*: what each file is responsible for, which files depend on which,
which goroutines exist, and what you have to touch when you change
something.

## Packages

```
cmd/node                 the executable: flags, console, process lifecycle
   │ imports both
   ├──────────────────────────────┐
   ▼                              ▼
internal/transfer   ──────►  internal/kademlia      file sharing uses the DHT's public API
   │                              │                 (Store, FindValue, Self) and nothing else
   │ imports                      │ imports
   ▼                              ▼
internal/framing  ◄───────────────┘                 [length][body] frames, for UDP and TCP
   │
   ▼
Go standard library only
```

Dependencies only point downward:

- `internal/kademlia` knows nothing about files, TCP, or the CLI. It's
  a general DHT storing opaque bytes.
- `internal/transfer` knows nothing about routing tables or RPC
  internals. To it, the DHT is "store bytes under a key, find bytes by
  key".
- `internal/framing` knows nothing about either; it frames bytes.

Go's `internal/` rule means only code in this module can import these.

## `internal/kademlia`, bottom to top

Each file depends only on files in lower layers:

```
 Layer 4   values.go ─── Store / FindValue (built on the lookup)
           lookup.go ─── iterativeFind: the lookup for both FIND_NODE and FIND_VALUE
 Layer 3   node.go ───── RPC engine: send/wait, receive/dispatch, replies, bucket updates, Bootstrap
 Layer 2   routingtable.go   transport.go (→ internal/framing)   store.go   logging.go
 Layer 1   bucket.go   contact.go   messages.go
 Layer 0   id.go         (pure functions on 160-bit IDs)
```

| File | Responsibility | Depends on |
|---|---|---|
| `id.go` | `ID`, `Xor`, `Less`, `BucketIndex`, `HashID`, hex/JSON encoding | stdlib |
| `contact.go` | `Contact` = (ID, IP, UDP port) | `id.go` |
| `bucket.go` | one k-bucket and its update rule. **No I/O.** | `contact.go` |
| `routingtable.go` | 160 buckets, `Closest`, `Snapshot` | `bucket.go`, `id.go` |
| `messages.go` | `Envelope`; payloads for all four RPCs and their replies | `id.go`, `contact.go` |
| `transport.go` | UDP socket; frames via `internal/framing`; read loop → callback. **Knows no RPC semantics.** | `messages.go`, framing |
| `store.go` | this node's share of the DHT's data; value size limits. **No I/O.** | `id.go` |
| `logging.go` | slog logger tagged with the node ID | `id.go` |
| `node.go` | `Node`, `Config`; matching replies to requests (`pending`); `handleMessage` dispatch; `replyFindNode/Store/FindValue`; eviction; `Bootstrap` | all of the above |
| `lookup.go` | `iterativeFind` (alpha-parallel, synchronous rounds), `Lookup`, `LookupResult` | `node.go` |
| `values.go` | `Store` (lookup + STORE to the k closest), `FindValue` (local, then lookup) | `lookup.go`, `node.go` |

## `internal/transfer`

```
 peer.go ────── Peer: Share, Download (+ the per-download `downloader`), announce
   │ uses
   ├── manifest.go   Manifest, pieces, SHA-256 checks, ContentKey      (pure, no I/O except reading the source file)
   ├── provider.go   ProviderRecord: the DHT value format
   ├── library.go    files this peer can serve; reads pieces from disk
   ├── server.go     TCP listener + per-connection handler     ─┐ both speak protocol.go
   ├── client.go     dial a provider, one request/response     ─┘ over internal/framing
   └── kademlia.Node  (Store, FindValue, Self, Logger)
```

| File | Responsibility | Depends on |
|---|---|---|
| `manifest.go` | build/encode/decode/validate a manifest, verify a piece, content key | `kademlia.ID` only |
| `provider.go` | encode/decode/validate provider records | `kademlia.ID` |
| `protocol.go` | TCP request/response shapes, op names, frame size limits | `kademlia.ID` |
| `library.go` | key → (manifest, file path); `readPiece` | `manifest.go` |
| `server.go` | accept loop; `handleConn` request loop; `answer`; the DEBUG ONLY `debugServeDelay` (`--debug-serve-delay`) | `library.go`, `protocol.go`, framing |
| `client.go` | `dialProvider`; `providerConn.fetch` | `protocol.go`, framing |
| `peer.go` | `Share`, `announce`, `Download`, provider failover | all of the above + `kademlia.Node` |

## Public APIs

```go
// internal/kademlia
kademlia.NewNode(kademlia.Config{ID, Port, AdvertiseIP, K, Alpha, RPCTimeout, Logger}) (*Node, error)
(*Node).Start(); Close()
(*Node).Bootstrap(ctx, *net.UDPAddr) (*LookupResult, error)
(*Node).Lookup(ctx, target ID) (*LookupResult, error)
(*Node).Store(ctx, key ID, value []byte) (*StoreResult, error)
(*Node).FindValue(ctx, key ID) (*LookupResult, error)        // result.Values
(*Node).Ping(ctx, Contact) bool; FindNode(ctx, Contact, ID)   // single RPCs
(*Node).Addr(), Self(), Logger(), RoutingTableSize(), RoutingTableSnapshot(), StoredKeyCount()

// internal/transfer
transfer.NewPeer(node, tcpPort, ioTimeout) (*Peer, error)
(*Peer).Share(ctx, path) (contentKey kademlia.ID, error)
(*Peer).Download(ctx, contentKey, outPath) (*DownloadResult, error)
(*Peer).TCPPort(), Library().List(), Close()

// internal/framing
framing.Encode / Decode     // one frame = one datagram
framing.Write / Read        // frames on a stream
```

## How one DHT RPC flows through the files

```
 caller (Lookup, Store, Bootstrap, console)
   ▼
 node.go      sendAndWait ── registers pending[rpcID] ─────────────┐ waits
 transport.go Send: JSON → framing.Encode → WriteToUDP              │
   ═══════════════ network (UDP) ═══════════════                    │
 transport.go Listen: ReadFromUDP → framing.Decode → JSON           │  (other node)
 node.go      handleMessage → touchContact(sender)                  │
                → replyPong / replyFindNode / replyStore / replyFindValue → Send
   ═══════════════ network (UDP) ═══════════════                    │
 transport.go Listen → decode                                       │  (back here)
 node.go      handleMessage → touchContact(sender) → deliverResponse┘
```

## How one file download flows through the files

```
 console.go fetch
   ▼
 peer.go     Download ── kademlia FindValue(key) ── (DHT RPCs as above) ──► provider records
   ▼
 peer.go     downloader.fetch ── client.go dialProvider ══ TCP handshake ══► server.go Accept → go handleConn
             client.go providerConn.fetch: framing.Write(req) ───────────► framing.Read → answer → library.go
                                           framing.Read(hdr, data) ◄──── writeData
   ▼
 manifest.go DecodeManifest / VerifyPiece   (reject → markDead → next provider)
   ▼
 peer.go     WriteAt → Library.add → announce (kademlia Store)
```

[DRY-RUN.md](DRY-RUN.md) shows both with real log lines.

## Goroutines and shared state

| Goroutine | Started by | Lifetime | Touches |
|---|---|---|---|
| main | `cmd/node` | process | waits for SIGTERM; runs Bootstrap synchronously |
| UDP receive loop | `Node.Start` | until `Close` | the UDP socket; spawns one handler per packet |
| UDP message handler | `Transport.Listen` | one packet | routing table, value store, `pending` (all locked); sends replies |
| lookup RPC | `iterativeFind` | one RPC | sends one outcome on the round's channel, nothing else |
| store RPC | `Node.Store` | one RPC | sends one outcome on a channel |
| eviction check | `Node.touchContact` | one ping | routing table (locked) |
| TCP accept loop | `NewPeer` (`go srv.serve()`) | until `Peer.Close` | the listening socket |
| TCP connection handler | `server.serve` | one connection | library (read-locked), the file on disk |
| console | `cmd/node` | until stdin EOF | public `Node` / `Peer` API; runs downloads synchronously |

Locks: `Bucket.mu`, `valueStore.mu`, `Node.pendingMu`, `Library.mu`
(RW). A lookup's candidate state and a download's `downloader` state are
unlocked; each is used by a single goroutine. Keep it that way.

`go test ./... -race` checks all of this.

## Invariants that are easy to break

1. **A received DHT message updates the sender's bucket before anything
   else** (`handleMessage` → `touchContact` first).
2. **Only confirmed IDs enter the routing table.** They come from a
   message that actually arrived, never from a secondhand contact or a
   placeholder. That's why `Bootstrap` PINGs first.
3. **A node never stores or returns itself, and replies never return the
   requester** (`closestExcluding`).
4. **`Bucket`, `valueStore`, and `Manifest` do no network I/O.** Logic
   that's easy to get wrong stays unit-testable.
5. **Register `pending[rpcID]` before sending.**
6. **Every UDP message fits in one datagram** (`maxDatagramSize` = 8 KB).
   That's why values are capped (`maxValueSize`, `maxValuesPerKey`) and
   file data only travels over TCP. `TestLargestFindValueReplyFitsInDatagram`
   enforces it.
7. **Nothing from another peer is trusted unchecked.** Provider records
   are validated (`decodeProviderRecord`), manifests must hash to the
   requested key (`DecodeManifest`), and every piece must match its hash
   (`VerifyPiece`) before it's written.
8. **A download only ever creates a new file** (`O_EXCL`), and deletes it
   on failure. It never overwrites an existing path.
9. **A lookup's or download's own state lives in one goroutine.**

## If you change X, also touch Y

| Change | Also update |
|---|---|
| Add a DHT RPC | `messages.go` (types), `node.go` (`handleMessage` case, `replyXxx`, an `xxxRPC` client), check it fits a datagram, docs `03`/`06`, FAQ |
| Change the bucket rule | `bucket.go`, `bucket_test.go`, `node.go` (`touchContact`/`resolveEviction`), doc `02` |
| Change lookup termination / concurrency | `lookup.go`, `integration_test.go`, `values_test.go`, doc `05`, `DRY-RUN.md` numbers |
| Change value limits or storage rules | `store.go`, `store_test.go`, doc `06` |
| Change the TCP protocol | `protocol.go`, `server.go`, `client.go`, doc `07` |
| Change how pieces/keys are hashed | `manifest.go`, `manifest_test.go`, doc `07`, FAQ |
| Change the join procedure | `node.go` `Bootstrap`, `TestBootstrap*`, doc `04`, `DRY-RUN.md` |
| Change log or console text | `scripts/demo.sh` greps `"bootstrap complete"` and `"TRIGGERED LOOKUP RESULT"`; `scripts/demo-transfer.sh` greps `"bootstrap complete"`, `"put greeting"`, `"get greeting:"`, `"content key:"`, `"fetch"`, `msg="piece received"`; `scripts/lib/nodes.sh` (used by the newer demos) greps `"node started"`, `"bootstrap complete"`, and `msg="piece received"` with an `index=N` field; `scripts/demo-seeder-death.sh` also greps console lines starting `fetch` / `fetched ` / `fetch failed`; `scripts/demo-big-network.sh` parses the console's `lookup <hex>: rounds=…` line and the numbered `   N. <id>@ip:port` lines after it, plus `content key:` and `^fetch`; `scripts/edge/b2-record-holders-die.sh` greps `content key:`, `^fetch`, `^fetch failed: transfer: no providers found`, `^PONG from` / `^no reply from`, and the log messages `"value stored"`, `"lookup converged"`, `"store complete"`, `"find_value finished"` with their `key=`/`target=`, `closest_found=`, `stored_at=`/`stored_locally=` fields; `scripts/edge/b1-half-the-network-dies.sh` parses the console's `lookup <hex>: rounds=… rpcs_sent=… rpcs_failed=…` line, the numbered `   N. <id>@ip:port` lines after it, `lookup failed`, and `table`'s `<id>@` contact lines and `N contacts in M non-empty buckets`; `scripts/edge/a3-bridge-between-islands.sh` uses the same `lookup` and `table` lines (it expects `table` contacts indented by exactly 4 spaces) plus `^PONG from` / `^no reply from`; `scripts/edge/b3-late-joiners-hide-a-value.sh` parses `put <name> (key …)` and the contact lines after it, `get <name>: N value(s) …` (a miss is `0 value(s)`), `get failed`, and `"value stored"` log lines, and relies on a repeated `--id-seed` flag taking its last value; `scripts/edge/a4-tiny-buckets.sh` relies on the same `--id-seed` rule and parses the `lookup` lines; `scripts/edge/a2-thundering-herd.sh` greps `bootstrap complete` with its `routing_table_size=` field, `^error:` (a failed join), the `lookup` lines, and the race detector's `WARNING: DATA RACE`; `scripts/edge/run-all.sh` takes each script's last line starting `PASS`/`FAIL` |
| Change how `--id-seed` maps to an ID | `scripts/demo.sh` recomputes IDs with `shasum` |
| Add a CLI flag or console command | `cmd/node`, README, `DEBUGGING.md`, `.vscode/launch.json` if relevant |
