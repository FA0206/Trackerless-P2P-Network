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

This design removes 1 and 2. Rule 3 stayed at first, and only bit when
a download really couldn't finish. It was replaced later too: a download
that gives up now keeps its file and keeps serving it, because deleting
it let one leecher cut another off (see
["Why giving up mustn't delete"](#why-giving-up-mustnt-delete-two-leechers-end-with-different-pieces)).

## Design

### When a peer becomes a provider

- **The original sharer** announces at `share`, as before.
- **A downloader** announces as soon as it has **verified its first
  piece** (in a worker, so the download doesn't wait for the DHT; a
  resumed download, which starts with pieces on disk, announces at once). A
  finished download also waits for that announce to complete before
  `Download` returns, so the next downloader is guaranteed to find it. Before that it has nothing to offer. Announcing once is enough:
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

The download is **stalled** when no piece request is in flight and no
live provider has — or, if its bitfield hasn't been fetched yet, might
have — a piece we still need (`swarmDownload.canProgress`). Bitfield
polling alone does *not* count as progress (see "What it took" below for
why that matters). Then:

1. immediately re-run provider discovery (if it isn't already running),
   and keep refreshing bitfields as they go stale;
2. **wait**: new providers may announce, or a known one may get the
   missing piece;
3. **give up** after 15 s of uninterrupted stall: stop downloading and
   return an error, but **keep the partial file and keep serving its
   pieces** (see "Why giving up mustn't delete" below). Any progress (a
   piece arriving) resets the 15 s.

A provider that fails once (connection refused, timeout, refusal, a bad
piece or bitfield) is dropped for the rest of the download, as on `main`.

### Partial files on disk, without overwriting anything

A fresh download still creates its output file with `O_CREATE|O_EXCL`,
right after the manifest is verified, so it never touches a path that
already exists. The one exception is a resume, which reopens this node's
own partial file after re-checking it (see "Resuming" below). Pieces are
written at their offsets (`WriteAt`), in whatever order they arrive;
slots of missing pieces stay zeros.

**Write, then publish:** a piece's bit is set in the library only after
its `WriteAt` has completed. So the server only ever reads, and serves,
pieces fully on disk.

When a download gives up, its file is closed but stays on disk and in
the library, so the pieces it has are still served
(`swarmDownload.stopButKeepServing`). The same happens when it stops for
any other reason: the console's or API's transfer timeout, a cancelled
`/api/fetch`, a write error. Two cases are deleted instead (`abort`: off
the library, then off the disk): a download that stopped before its
first piece, which has nothing to serve or resume, and a finished file
that can't be closed. If the caller stops waiting after the last piece
is written (while the download waits for its announce), the download
counts as complete. A download of a key that's already in the library
is refused rather than starting a second, conflicting entry, with one
exception: a download that gave up, fetched again into the same path,
resumes (below).

### Why giving up mustn't delete: two leechers end with different pieces

Take one seeder and two leechers, and kill the seeder before the leechers
*together* hold every piece. The file is lost: some pieces existed only
on the seeder. You'd expect both leechers to end with the same pieces,
the union of what they held, missing exactly the ones that died with the
seeder. Until this change they didn't: in one reported run, one leecher
ended with 3 pieces missing and the other with 6.

**Why.** Each leecher gives up on its own 15 s clock, and giving up used
to delete the file and stop serving it. The leecher that needs less from
the other finishes copying first, so its clock starts first, and it gives
up first. When it deletes its file, the other leecher is still copying
from it. That leecher's next request is refused, it drops the provider
(one failure is enough), and it ends with fewer pieces. **The leecher
that's ahead abandons the one that's behind.**

The scenario on localhost, as `scripts/demo-give-up-resume.sh` runs it:
32 pieces, the seeder and both leechers serving one piece a second
(`--debug-serve-delay 1s`), leechers at `--max-inflight 1`. B starts
first, C starts once B has 20 pieces, and the seeder is killed once C has
2. A replay with the code from before this change (commit `c21848f`); at
the kill, B had 22 and C had 2, 23 of 32 together:

```
20:49:28.697 [B] msg="download stalled: no live provider has the missing pieces; looking for more" have=23 of=32   ← got C's piece; B's 15 s clock starts
             … B gives up ~15 s later and deletes its file; C, copying from B one piece a second, is at 18
20:49:44.725 [C] msg="provider failed, trying the next one" provider=127.0.0.1:9401 error="provider refused: unknown content key 5da1b2bf…"
20:49:44.725 [C] msg="download stalled: no live provider has the missing pieces; looking for more" have=18 of=32
[B] fetch failed: transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest
[C] fetch failed: transfer: stalled for 15s with 18 of 32 pieces: no live provider has the rest
```

The same scenario with this change, from `scripts/demo-give-up-resume.sh`
itself (B is node1, C is node2; at the kill again 22 + 2, 23 together;
full trace in [DRY-RUN part 8](DRY-RUN.md#part-8--scriptsdemo-give-up-resumesh-giving-up-keeps-seeding-fetching-again-resumes)):

```
21:12:31.419 [B] msg="download stalled: no live provider has the missing pieces; looking for more" have=23 of=32
21:12:46.514 [B] msg="download gave up, still serving its pieces" key=0ac21332… have=23 of=32 error="transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest"
21:12:47.239 [C] msg="piece received" index=30 of=32 from=127.0.0.1:9651 bytes=65536 have=19 inflight=0   ← still copying from B
             … 3 more from B, one a second …
21:12:51.247 [C] msg="piece received" index=5 of=32 from=127.0.0.1:9651 bytes=65536 have=23 inflight=0
21:12:51.247 [C] msg="download stalled: no live provider has the missing pieces; looking for more" have=23 of=32   ← caught up
21:13:06.259 [C] msg="download gave up, still serving its pieces" key=0ac21332… have=23 of=32 error="transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest"
[B] fetch failed: transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest
[C] fetch failed: transfer: stalled for 15s with 23 of 32 pieces: no live provider has the rest
```

The outcome doesn't change: the file is lost either way. What changes is
that no leecher's pieces disappear with it. Every leecher reaches the
union, and the swarm keeps everything it had for when a source comes
back (resuming, below).

**What real BitTorrent does** (clients' visible behavior, **not
verified** against their source). A client never gives up on its own or
deletes a stalled download. The torrent stays in the list, "stalled",
seeding what it has. Clients show its *availability*, how many full
copies the reachable peers make together; below 1.0 means some piece
exists nowhere reachable. The download continues when a peer with the
missing pieces shows up. Ours now matches the "keeps seeding" part. It
still stops downloading after 15 s, so `fetch` returns and scripts can
finish.

### Resuming

Running the same `fetch` again on the node whose download gave up
continues it (`Peer.Download`):

1. `Library.claimForResume` finds the stopped partial and, in one locked
   step, marks it as downloading again. Two `fetch`es of one key can
   never both run.
2. The manifest comes from the library, and the file is reopened
   without `O_EXCL` (`swarmDownload.reopenFile`). Before anything is
   written, **every piece the library says is on disk is re-hashed**
   against the manifest. Since the download stopped, the file may have
   been truncated, edited, or deleted and replaced by another file: the
   library only remembers a path. If any piece fails, the resume is
   refused, nothing is written, and the entry is dropped so the file is
   no longer served (invariant 8: not even our own disk is trusted).
3. The download starts with the pieces already on disk and fetches only
   the missing ones. The event loop needs no change: `needed()` already
   skips pieces in `have`.

The end of that demo run: a new node (node3, serving at full speed)
shares the same file (same bytes, same key), and B runs the same `fetch`:

```
[B] msg="download resumed" key=0ac21332… have=23 of=32
fetched "original.bin" (2097152 bytes, 32 pieces) into /tmp/tmp.Q9O0qPW9SR/node1.bin in 2.009s
    9 piece(s) from 127.0.0.1:9653
    23 piece(s) were already on disk (resumed)
    provider 127.0.0.1:9650 failed and was skipped
```

The rules:

| You run | Result |
|---|---|
| the same `fetch` into the **same path** | resumes |
| into **another path** | refused: `… has a stopped partial download at <path>; fetch into that path to resume it` |
| for a file that's complete, or still downloading | refused: `… is already in the library` |
| after deleting the partial file by hand | the entry is dropped with an error; the next `fetch` starts over |
| after the file was changed or replaced | refused: `resume … changed since the download stopped (piece N: …)`; nothing written, entry dropped, file left alone (move it away to fetch again) |
| when the file can't be opened for writing (e.g. read-only) | refused, but the entry stays and its pieces are still served; fix it and fetch again |
| when another download now uses that path | refused: `… now belongs to the library entry for <key>` |
| for a download that stopped before its first piece | nothing to resume: it left no file or entry, so this is a fresh fetch |
| after **restarting** the node | not a resume: see the SIMPLIFICATION below |

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
  ever serves pieces it verified. Downloads still create only new files,
  except a resume reopening its own partial file, whose pieces on disk
  are re-hashed first.
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
| No resume after a restart. The library lives in memory, so a restarted node forgets a partial file and doesn't serve it, and the file blocks its path (downloads never overwrite) | Delete the file by hand; the download starts from zero | No. Fix when demoing node restarts: write to `<out>.part`, and on a re-fetch re-hash the `.part` file against the manifest |

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

Nothing outside this design: no change to `internal/kademlia`, and none of
the three known gaps had to be fixed. What the real runs did turn up:

1. **A stall-detection bug, found by the demo.** The first version
   counted the download as stalled only when *nothing at all* was in
   flight. The periodic bitfield refreshes kept something in flight every
   500 ms, so the stall timer reset forever. In the first branch run of
   `demo-seeder-death.sh`, leechers stuck at 14/16 waited out the
   script's 60 s instead of giving up after 15 s. Fixed by
   `canProgress` (commit `cae73ad`), with a regression test,
   `TestDownloadGivesUpWhenNoOneHasTheRest`.
2. **The kill rule.** `main`'s script also kills when the leading leecher
   is ¾ done. On the branch that fallback once fired with the leechers
   holding only 14 of 16 pieces, and the file was lost: a correct
   outcome, but not the scenario being tested. The branch script kills
   only once the leechers together hold every piece.
3. **`--max-inflight 1` for that demo.** With 4 requests in flight, the
   leechers copied each other so fast that the union and the first
   complete copy arrived almost together; 1 run in 6 had a leecher finish
   before the kill. With one request each, rarest-first keeps every
   leecher's request on a seeder-only piece, so the swarm runs well ahead
   of each leecher: 8/8 runs passed, with leechers at roughly 11, 7, and
   5 of 16 pieces when the seeder died. Concurrency itself is shown
   separately by `scripts/demo-parallel.sh`.

**The predictions, checked against the logs (DRY-RUN part 5):**

- Synchronous rounds and no bucket refresh: as predicted, background
  `FIND_VALUE`s that asked the dead seeder waited out the RPC timeout
  (`rpcs_failed=1`). Because they run in workers, no piece request waited
  on them, and no leecher stalled after the kill. They would matter if a
  leecher's only other holders were still undiscovered at the kill.
- Records never expiring: every rediscovery returned the dead seeder's
  record again. It was already marked dead, so it cost nothing.
- Announce waiting: on the branch, `demo-transfer.sh`'s final download
  takes ~4 s instead of ~2 s. `Download` now waits for its announce,
  whose lookup also asks the dead seeder.

## Where this lives in the code

| Concept | Function / type | File |
|---|---|---|
| Bitfield (BitTorrent layout), validation | `Bitfield`, `NewBitfield`, `FullBitfield`, `decodeBitfield` | `internal/transfer/bitfield.go` |
| Rarest-first picker (pure) | `pickPiece`, `holderView` | `picker.go` |
| Partial library entries, write-then-publish | `Library.addPartial`, `markHave`, `remove`, `bitfield`, `readPiece` | `library.go` |
| Giving up keeps serving; resuming | `Library.claimForResume`, `stopDownloading`; `swarmDownload.stopButKeepServing`, `reopenFile` | `library.go`, `download.go` |
| `GET_BITFIELD`, closing open connections | `server.answer`, `server.close`, `server.track` | `server.go`, `protocol.go` |
| The download event loop | `Peer.Download`, `swarmDownload.run`, `schedule`, `handle`, `canProgress` | `download.go` |
| Workers | `startPiece`, `startBitfield`, `startDiscovery`, `startAnnounce`, `ensureConn` | `download.go` |
| Limits and timers | `Config.MaxInFlight` (`--max-inflight`), `bitfieldMaxAge`, `rediscoverEvery`, `stallTimeout` | `peer.go`, `download.go`, `cmd/node/main.go` |

## Tests

- **Unit (no sockets):** `swarm_unit_test.go`. Bitfield layout matches
  BEP 3; malformed bitfields rejected; the picker prefers the rarest
  piece, skips busy holders and pieces not needed, and spreads ties
  across downloaders.
- **Integration (real UDP + TCP, `-race`):** `swarm_test.go`.
  - `TestParallelDownloadFromThreeProviders`: 3 providers at 60 ms per
    piece. It requires 3 requests in flight at once, pieces from all
    three, and a duration under ¾ of the sequential time. Typical:
    199 ms vs. ≥ 540 ms.
  - `TestPartialSeedingSurvivesSeederDeath`: 4 leechers. The seeder is
    closed the moment their union covers the file with none complete
    (typically each holds 9–10 of 16), and every leecher must finish
    with a byte-identical file.
  - `TestDownloadGivesUpWhenNoOneHasTheRest`: the stall regression test.
    It also checks the download keeps its file and can still serve its
    pieces after giving up.
  - `TestGivingUpKeepsServing`: the "different pieces" case. A second
    leecher fetching after the first gave up gets exactly the first one's
    pieces from it.
  - `TestFetchAgainResumes`: after the leecher gives up at k of 8 pieces,
    another node shares the same file, and the same fetch completes with
    `AlreadyHad == k`, fetching only 8 − k pieces. Fetching into another
    path, or after completion, is refused; a read-only partial is refused
    but stays in the library.
  - `TestResumeRefusesAChangedFile`: the partial is replaced by the
    user's own bytes; the resume is refused, writes nothing, and drops
    the entry.
  - `TestGivingUpWithNoPiecesLeavesNothing`: no file, no entry.
  - `TestCancelAfterLastPieceStillCompletes`: cancelled while waiting for
    the announce, after the last piece: success, not a failure.
- **Unit:** `library_test.go` `TestClaimForResume`: fresh, resume,
  refusals (including a path now used by another entry), and one claim
  at a time.
- The older `transfer_test.go` tests (seeder shutdown, corrupt provider,
  two providers, unknown key) pass unchanged against the new downloader.

## What's next

Per-piece availability is done, so the remaining items before the
end-goal measurements are the three known gaps (synchronous lookup
rounds, bucket refresh, record expiry/republish). After those come
instrumentation and the evaluation harness. See the README's
simplifications table.
