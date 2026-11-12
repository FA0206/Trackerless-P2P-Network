# 8. Swarming: parallel downloads and partial seeding

Prerequisite: [07-file-transfer-over-tcp.md](07-file-transfer-over-tcp.md),
and the "before" demo in [DRY-RUN part 3](DRY-RUN.md).

**Why a new doc instead of extending 07.** Doc 07 explains how *one*
downloader moves a file from providers that each hold all of it: one
request at a time, in order. DRY-RUN parts 2 and 3 are built on exactly
that behavior. This doc is a different idea layered on top: a **swarm**,
where many peers hold different parts of a file and trade them. Keeping
07 as the simple baseline preserves the learning order (two peers →
many peers). It also keeps the before/after comparison readable.

## The problem (from DRY-RUN part 3)

When the seeder died mid-download on `main`, three leechers held 27
pieces between them, yet the file was lost:

1. **Only complete files are announced or served**, so no leecher could
   get anything from another leecher.
2. **Pieces were fetched in order**, so the leechers' pieces overlapped
   (prefixes `[0-12]`, `[0-7]`, `[0-5]`) and together covered only 13
   of 16.
3. A failed download deletes its partial file.

This design removes 1 and 2. Rule 3 stays, but now it only bites when a
download really can't finish.

## Design

### When a peer becomes a provider

- **The original sharer** announces at `share`, as before.
- **A downloader** announces as soon as it has **verified its first
  piece**. Before that it has nothing to offer. Announcing once is enough:
  the provider record `{node_id, ip, tcp_port}` doesn't change as more
  pieces arrive, so the DHT side is untouched (same `STORE`, same value
  format).
- A downloader adds the file to its **library as a partial entry** as
  soon as it has fetched and verified the manifest. From then on its
  server answers `GET_MANIFEST`, `GET_BITFIELD` (below), and `GET_PIECE`
  for pieces it has.

### Who has which pieces: a bitfield request

A new TCP op, **`GET_BITFIELD`**. The reply's data frame is one bit per
piece, `ceil(pieces/8)` bytes, with the high bit of byte 0 for piece 0
(the same layout as BitTorrent's `bitfield` message, BEP 3). Spare bits
at the end must be zero. Like everything from a peer, it's validated
before use (length and spare bits).

A downloader asks a provider for its bitfield:

- **when it first connects**, before asking for any piece;
- **again whenever that provider is idle and its bitfield is older than
  500 ms**. A provider whose bitfield is already full is never asked
  again, since a complete file can't lose pieces.

Bitfields only grow, so a stale bitfield is a *subset* of the truth.
Asking for a piece a stale bitfield says the provider has is always safe.

**New providers** are found by re-running `FIND_VALUE(content key)`:
every 1 s while downloading, and immediately when stalled (below). Any
record not seen before becomes a new provider whose bitfield is fetched
on first contact.

### Which piece next: rarest first, ties random

Among the pieces we still need, that aren't already being fetched, and
that some *idle* provider has: pick the one held by the **fewest**
providers (counted over the bitfields we know of). Break ties at random.
Then fetch it from a random idle provider that has it.

Why: rarest-first is what spreads *distinct* pieces through the swarm,
the exact opposite of `main`'s overlapping prefixes. At the start every
piece is held only by the seeder, so all are tied. The random tie-break
then makes leechers that start with the same view ask the seeder for
*different* pieces. A piece that exists on only one node is also the one
most likely to be lost if that node dies, so it's worth copying first.
BitTorrent clients do the same (rarest-first plus random first pieces).

### Concurrency limits

- **Per provider: one outstanding request**, a piece or a bitfield. A
  connection carries one request at a time, because replies don't carry
  the index (doc 07). So parallelism comes from talking to several
  providers at once, each over its own connection.
- **In total: at most `MaxInFlight` piece requests** per download
  (default 4, CLI `--max-inflight`). Bitfield requests don't count; they
  are tiny. With 3 providers and the default, 3 pieces are in flight at
  once.

### When a needed piece has no live holder

The download is **stalled** when nothing is in flight (no piece, no
bitfield request) and no needed piece can be assigned to an idle
provider. Then:

1. immediately re-run provider discovery (if it isn't already running),
   and keep refreshing bitfields as they go stale;
2. **wait**: new providers may announce, or a known one may get the
   missing piece;
3. **give up** after 15 s of uninterrupted stall: delete the partial
   file and return an error. Any progress (a piece arriving) resets the
   15 s.

A provider that fails once (connection refused, timeout, refusal, a bad
piece or bitfield) is dropped for the rest of the download, as on `main`.

### Partial files on disk, without overwriting anything

The output file is still created with `O_CREATE|O_EXCL`. That happens
right after the manifest is verified, and the download never touches a
path that already exists. Pieces are written at their offsets
(`WriteAt`), in whatever order they arrive.

**Write, then publish:** a piece's bit is set in the library only after
its `WriteAt` has completed. So the server only ever reads, and serves,
pieces fully on disk. On failure, the library entry is removed first (to
stop serving), then the file is closed and deleted. A download of a key
that's already in the library is refused rather than starting a second,
conflicting entry.

### Fetching the manifest from a partial provider

That works as before. A partial provider has the manifest from the
moment it starts downloading, and the downloader still checks
`ContentKey(manifest) == key` (`DecodeManifest`). So it doesn't matter
whether the manifest comes from the seeder or from another leecher.

### How the invariants still hold (CLAUDE.md)

- **Invariant 6: one goroutine owns the download's bookkeeping.** All
  state lives in the download's event loop: providers, their bitfields
  and connections, which pieces are needed or in flight, the output file,
  and counters. Workers each do one job and send one result on the loop's
  `events` channel:
  - fetch a piece (and verify its hash),
  - fetch a bitfield (and validate it),
  - run `FIND_VALUE`,
  - announce.

  A provider's connection is *lent* to exactly one worker at a time (the
  per-provider limit of 1 guarantees that) and comes back in its result.
  The `Library` is shared with the TCP server's goroutines, so it keeps
  its own mutex.
- **Invariant 8: nothing from a peer is trusted.** Bitfields are
  validated, pieces are hash-checked before `WriteAt`, and a peer only
  ever serves pieces it verified. Downloads still create only new files.
- **Invariant 4: pure logic, no I/O.** The bitfield type and the piece
  picker are pure functions, unit-tested without sockets.
- **The DHT invariants (1–3, 5, 7) are untouched.** Nothing in
  `internal/kademlia` changes.

### One more change: `Peer.Close` closes open connections

On `main`, closing a peer stops *accepting* connections, but a
connection already open keeps being served until it idles out. For a
real process that dies, the OS closes everything anyway. But it means an
in-process test can't "kill" a seeder. Now the server tracks its
connections and `Peer.Close` closes them all.

## New SIMPLIFICATIONS

| Simplification | Effect | Blocks the end goal? |
|---|---|---|
| One outstanding request per provider (no pipelining) | A single provider's throughput is limited by one round trip per piece | No; matters for throughput graphs |
| Bitfields are **polled** (every 500 ms when idle), not pushed (BitTorrent sends `have` messages) | Up to ~0.5 s of staleness; some duplicate requests to the seeder | No |
| Providers are rediscovered by polling `FIND_VALUE` every 1 s | ~1 DHT lookup per second per download (more control traffic) | No; shows up in the control-bytes graph |
| Fixed timers: 500 ms bitfield age, 1 s rediscovery, 15 s stall timeout, 100 ms loop tick | Not tuned for real networks | Revisit for Wi-Fi |
| Rarest-first uses only this downloader's (possibly stale) view | Not global rarity | No |
| A provider that fails once stays dead for the whole download | A briefly unreachable peer is never retried | Revisit for Wi-Fi |
| No choking, upload limits, or fairness | A peer serves everyone, as fast as it can | Not in the goal list |

## Predicted effects of the known gaps (out of scope here)

- **Synchronous lookup rounds** (doc 05): after the seeder dies, every
  `FIND_VALUE` and every announce whose round includes the dead seeder
  waits a full RPC timeout (2 s). Both run in workers, so pieces keep
  flowing from known providers. But discovering *new* providers after the
  death can lag by ≥ 2 s per lookup.
- **No bucket refresh:** the dead seeder stays in routing tables, so it
  keeps being chosen for lookups, and each time costs that 2 s timeout.
- **Provider records never expire:** every rediscovery returns the dead
  seeder's record again. The downloader already marked it dead, so this
  costs nothing beyond the first failed connection (instant on
  localhost).

Expected consequence for the "after" demo: leechers finish, with a
pause of a few seconds after the kill while workers wait out timeouts.
None of the three gaps should make the demo impossible. That is
re-checked against the real run (see "What it took" below).

## What it took to make the "after" demo work

*(Filled in after implementation; see below.)*
