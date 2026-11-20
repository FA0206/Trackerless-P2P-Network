# 7. File transfer over TCP

Prerequisite: [06-store-and-find-value.md](06-store-and-find-value.md).

> **On this branch (`feature/parallel-partial-seeding`)** a download
> fetches pieces rarest-first and in parallel, and peers serve pieces
> before they have the whole file. This doc covers what didn't change:
> manifests, keys, provider records, the TCP protocol, and verification.
> Piece selection, concurrency, and partial seeding are in
> [08-swarming.md](08-swarming.md). The "Seen in the demo" numbers below
> were recorded on `main`, before that change.

## What it is

The DHT can now answer "what's stored under key X?". File sharing uses
that for exactly one thing: **finding out who has a file**. The file's
bytes then move directly between the two peers over TCP. The DHT never
carries file data.

```
              "who has file X?"                        "send me piece 7"
 downloader ─────────────────────► DHT (UDP)    downloader ════════════► provider
            ◄─────────────────────                        ◄════════════
              provider records:                           64 KiB of file data
              (node ID, IP, TCP port)                     (TCP, length-prefixed frames)
```

Code: package [`internal/transfer`](../internal/transfer). It uses
`internal/kademlia` only through its public API (`Store`, `FindValue`,
`Self`), and the DHT knows nothing about files. See
[ARCHITECTURE.md](ARCHITECTURE.md).

## Vocabulary

| Term | Meaning | Code |
|---|---|---|
| **piece** | A fixed-size chunk of a file (64 KiB; the last piece holds the remainder). The unit of transfer and verification. | `DefaultPieceSize` |
| **manifest** | The file's description: name, size, piece size, and the SHA-256 of every piece. ~1.1 KB for a 1 MiB file. | `Manifest` in `manifest.go` |
| **content key** | The file's DHT key = SHA-256(manifest bytes), truncated to 160 bits. What you hand to someone so they can download, usually inside a magnet link ([10](10-magnet-links.md)). | `ContentKey` |
| **provider record** | The DHT value under a content key: `{"node_id", "ip", "tcp_port"}`. One per peer holding the file. ~90 bytes. | `ProviderRecord` in `provider.go` |
| **provider / seeder** | A peer that has the whole file and serves it over TCP. | `Peer`, `server.go` |
| **downloader / leecher** | A peer fetching the file. It becomes a provider when it finishes. | `Peer.Download` |

## Sharing a file (`share <path>`)

`Peer.Share` in [`peer.go`](../internal/transfer/peer.go):

1. `BuildManifest` reads the file in 64 KiB pieces and records the
   SHA-256 of each.
2. `EncodeManifest` → bytes; `ContentKey(bytes)` → the 160-bit key.
3. Add the file to the peer's **library**: files it can serve,
   `library.go`.
4. **Announce**: `node.Store(key, providerRecord)`. That's a lookup for
   the key, then `STORE` to the k closest nodes ([06](06-store-and-find-value.md)).
   The record contains this node's ID, its advertise IP
   (`--advertise-ip`, or detected when the node joins through another
   machine; [09](09-multi-device.md)), and its TCP port.

No file data has moved yet. Sharing only puts a ~90-byte pointer into
the DHT. The console prints the content key and a **magnet link**
carrying it (`magnet:?xt=urn:tp2p:<key>&dn=<name>`), which is what you
hand to others ([10](10-magnet-links.md)).

## Downloading a file (`fetch <key|magnet> <out-path>`)

`fetch` takes the content key or a magnet link. A link is parsed into
the key locally (`ParseKeyOrMagnet`, `magnet.go`), so everything below
is the same either way ([10](10-magnet-links.md#what-fetch-does-with-a-link)).

`Peer.Download` in [`peer.go`](../internal/transfer/peer.go):

### 1. Ask the DHT who has it

`node.FindValue(key)` returns the stored values: one provider record per
peer that has announced the file. `decodeProviders` parses them. It
rejects malformed records (anyone can `STORE` anything), duplicates, and
our own record.

### 2. Open a TCP connection to a provider

This is the step where Kademlia's output turns into a connection:

```go
// provider record from the DHT:   {"node_id":"94fab99d…","ip":"127.0.0.1","tcp_port":9003}
d := net.Dialer{Timeout: timeout}
conn, err := d.DialContext(ctx, "tcp4", "127.0.0.1:9003")   // client.go: dialProvider
```

`DialContext` asks the operating system to run TCP's three-way handshake
(SYN → SYN-ACK → ACK) with that address. On the provider, the listening
socket opened at startup (`net.Listen("tcp4", ":9003")`, `server.go`)
completes the handshake. Its `Accept()` loop then returns the new
connection and hands it to a goroutine (`handleConn`). If nothing
listens there any more, the OS replies with a reset and `Dial` fails at
once with `connection refused`. That's exactly what happens when the
seeder has been killed (demo part 3).

Connections are opened lazily, at most one per provider, and reused for
every request to that provider during the download.

### 3. Fetch and verify the manifest

Send `GET_MANIFEST`. Check `ContentKey(received bytes) == key`
(`DecodeManifest`). This is what makes it safe to take the manifest from
a stranger: a peer can't send a different manifest without changing its
hash, and the hash is the key you asked for.

### 4. Fetch every piece, from several providers at once

Each piece is checked against the manifest's SHA-256
(`Manifest.VerifyPiece`) and written at its offset in the output file.
*Which* piece is fetched next, from whom, and how many at once is
[08](08-swarming.md)'s subject: rarest-first, one request per provider,
up to `--max-inflight` in total. (On `main`, pieces went `0, 1, 2, …`
one at a time, round-robin over providers.)

If a provider fails — can't connect, times out, refuses, or sends a piece
whose hash doesn't match — it's marked dead for the rest of this download
(`markDead`), and that piece is requested again from another holder.

### 5. Become a provider

After its first verified piece, the downloader announces a provider
record and serves the pieces it has ([08](08-swarming.md)). When the
download finishes, the file is complete in the library.

## The TCP wire protocol

One connection carries any number of request/response exchanges, one at
a time. Every message is a frame from
[`internal/framing`](../internal/framing/framing.go): `[4-byte
length][body]`, the same format the UDP side uses ([03](03-message-framing.md)).

```
client → server   frame: {"op":"GET_PIECE","key":"5951f7ca…","index":7}
server → client   frame: {"ok":true}
server → client   frame: <65536 raw bytes of piece 7>
```

On an error the server sends `{"ok":false,"error":"…"}` and no data
frame. Definitions: [`protocol.go`](../internal/transfer/protocol.go).

On TCP the length prefix isn't optional. TCP delivers a byte stream, not
messages: a 65 536-byte piece can arrive across many `read()` calls, or
glued to the next header. `framing.Read` reads exactly 4 bytes for the
length, then exactly that many bytes for the body (`io.ReadFull`).
`TestStreamKeepsFramesApart` in `framing_test.go` checks this.

The data frame is raw bytes, not JSON, so pieces aren't inflated by
base64. Requests and headers are JSON for readability.

## The integrity chain

```
content key  ──(= SHA-256 of)──►  manifest bytes  ──(contains SHA-256 of)──►  every piece
 (you got it                       (from any provider,                         (from any provider,
  from someone you trust)           checked against the key)                    checked against the manifest)
```

Trust flows from the key alone. Providers are untrusted, and a provider
that serves a corrupted piece is detected and dropped
(`TestCorruptProviderIsSkipped`). This is BitTorrent v1's design (a flat
list of piece hashes). A Merkle tree would replace the list with one root
hash; see [FAQ](FAQ.md#do-merkle-trees-make-sense-here) for why that's
optional here.

## How the downloader knows where each piece goes

Arrival order never decides where a piece goes. Its **index**, and the
manifest, do. The exact path through the code:

1. **The order is fixed when the manifest is built.**
   `BuildManifest` (`manifest.go`) reads the file front to back in
   `PieceSize` chunks and appends each chunk's SHA-256 to
   `PieceHashes` (line ~65). So `PieceHashes[i]` is the hash of the
   `i`-th chunk, and the order is part of the content key, because the
   key hashes the whole manifest.
2. **The position of piece `i` is pure arithmetic** on the manifest:
   `Manifest.PieceOffset(i)` = `i × PieceSize`, and
   `Manifest.PieceLength(i)` is `PieceSize` except for a shorter last
   piece (`manifest.go`, ~86–94).
3. **The downloader asks for a piece by index.** The picker
   (`pickPiece`, `picker.go`) chooses index `i`, and
   `swarmDownload.startPiece` (`download.go`) hands it to a worker. The
   worker sends `{"op":"GET_PIECE","key":…,"index":i}` from
   `providerConn.fetch` (`client.go`).
4. **The provider reads exactly that range, if it has it.**
   `server.answer` (`server.go`) → `Library.readPiece(key, i)`
   (`library.go`). It refuses unless piece `i`'s bit is set, then calls
   `ReadAt(buf, PieceOffset(i))` with `len(buf) = PieceLength(i)`.
5. **The downloader checks the bytes really are piece `i`.**
   `Manifest.VerifyPiece(i, data)` compares the length with
   `PieceLength(i)` and the SHA-256 with `PieceHashes[i]`. A provider that
   sends piece 3's bytes when asked for piece 7 fails this check.
6. **…then writes them at piece `i`'s offset.** In
   `swarmDownload.handle` (`download.go`): `file.WriteAt(data,
   PieceOffset(i))`, then `Library.markHave(key, i)`. The file was
   created empty (`O_EXCL`). Writing at an offset past the current end
   simply extends the file, so pieces can be written in any order.

The reply frame doesn't repeat the index. The downloader knows which
piece it is because a connection carries **one request at a time**, so
the next data frame on that connection must answer the request just sent
(TCP delivers bytes in order).

## Can pieces arrive out of order?

**The format and the provider: yes.** Nothing above depends on order.
A provider answers any index at any time, verification is per index, and
`WriteAt` places each piece independently.

**The downloader on this branch: yes, routinely.** It picks pieces
rarest-first with random tie-breaks and has several requests in flight
on different connections. So pieces arrive in whatever order the
providers answer (DRY-RUN part 5: `2, 11, 6, 15, 10, 8, …`). On `main`
it requested `0, 1, 2, …` strictly one after another.

Two constraints still hold:

- **One outstanding request per connection.** Replies carry no index, so
  pipelining on one connection would need the index in the reply, or
  strict first-in-first-out matching. Parallelism comes from several
  connections, one per provider.
- **Partial files were deleted on failure.** Pieces that arrived were
  thrown away if the download couldn't finish. That's gone too: a
  download that gives up keeps its file and keeps serving it, and the
  same `fetch` resumes it ([08](08-swarming.md#why-giving-up-mustnt-delete-two-leechers-end-with-different-pieces)).

## Seen in the demo

`scripts/demo-transfer.sh` **on `main`** (one request at a time,
round-robin), `k = 3`, 1 MiB file = 16 pieces (full trace:
[DRY-RUN.md](DRY-RUN.md), part 2). On this branch the same script passes
with pieces arriving rarest-first and in parallel.

- **node3 downloads** while only node1 has the file: 1 provider, all 16
  pieces from `127.0.0.1:9001`, 19 ms. node3 then announces itself.
- **node4 downloads** with 2 providers (node1, node3): pieces alternate,
  0 → 9001, 1 → 9003, 2 → 9001, … 8 + 8.
- **node1 (the original seeder) is killed; node2 downloads**:

  ```
  providers found count=3                                     ← node1's record is still in the DHT (no expiry)
  provider failed, trying the next one provider=127.0.0.1:9001 error="… connection refused"
  tcp connected to provider provider=127.0.0.1:9003
  tcp connected to provider provider=127.0.0.1:9004
  fetched "original.bin" … in 2.022s
      11 piece(s) from 127.0.0.1:9003
      5 piece(s) from 127.0.0.1:9004
  ```

  Providers are `[node1 (dead), node3, node4]`. Pieces with `i mod 3 = 0`
  would have started at node1, so they fall through to node3. node3 gets
  6 + 5 = 11, node4 gets 5. The 2 seconds are not the transfer: node2's
  `FIND_VALUE` round included the dead node1 and waited out the 2 s RPC
  timeout (synchronous rounds, [05](05-iterative-parallel-lookup.md)).

## SIMPLIFICATIONS (and whether they block the end goal)

| Simplification | Blocks the end goal? |
|---|---|
| *(Removed on this branch: "providers must have the whole file" and "pieces fetched one at a time". New simplifications of the swarm are listed in [08](08-swarming.md#new-simplifications).)* | |
| One failure drops a provider for the rest of the download; no retries | Fine for now. Revisit with packet loss / Wi-Fi. |
| *(Removed: "failed download deletes the partial file; no resume". A download that gives up keeps serving, and fetching again resumes; only resume after a restart is missing, listed in [08](08-swarming.md#new-simplifications).)* | |
| Provider records never expire ([06](06-store-and-find-value.md)) | Costs one failed connection per dead provider; matters for recovery-time measurements. |
| The advertise IP comes from `--advertise-ip` (default `127.0.0.1`), or from the route to the bootstrap node when a node joins through another machine | No, on one subnet: the first node is started with its IP, the others detect theirs, and bad values are refused at startup. No NAT traversal ([09](09-multi-device.md)). |
| No upload limits, no tit-for-tat | Not in the goal list. |

## Where this lives in the code

| Concept | Function / type | File |
|---|---|---|
| Pieces, hashes, content key | `BuildManifest`, `VerifyPiece`, `ContentKey`, `DecodeManifest` | `manifest.go` |
| DHT value format | `ProviderRecord`, `decodeProviderRecord` | `provider.go` |
| TCP message shapes | `request`, `responseHeader`, op names, size limits | `protocol.go` |
| Files this peer can serve (complete or partial) | `Library`, `readPiece`, `markHave` | `library.go` |
| Accepting connections, answering requests | `server.serve`, `handleConn`, `answer` | `server.go` |
| Dialing a provider, one request/response | `dialProvider`, `providerConn.fetch` | `client.go` |
| Share, announce | `Peer.Share`, `Peer.announce` | `peer.go` |
| Download (event loop, workers, failover) | `Peer.Download`, `swarmDownload` | `download.go` (see [08](08-swarming.md)) |
| Frames on a stream | `framing.Write`, `framing.Read` | `internal/framing/framing.go` |
| Magnet links: build, parse, key-or-link | `MagnetLink`, `ParseMagnet`, `ParseKeyOrMagnet` | `magnet.go` (see [10](10-magnet-links.md)) |
| Console `share` / `fetch` / `files` | `consoleEnv.share`, `.fetch`, `.files` | `cmd/node/console.go` |

Tests: `manifest_test.go` (piece layout, empty file, corrupted /
misplaced / truncated pieces rejected, tampered manifest rejected,
deterministic key) and `transfer_test.go`, real UDP + TCP between 3–5
peers. That covers a download with one provider and then two, a
download after the seeder shuts down, a provider serving corrupted
pieces, and an unknown key.

## What's next

[08-swarming.md](08-swarming.md): parallel downloads, rarest-first
piece selection, and partial seeding, which together let a file survive
its seeder. [10-magnet-links.md](10-magnet-links.md) covers how the
content key travels from person to person.
