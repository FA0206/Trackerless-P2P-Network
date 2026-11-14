# CLAUDE.md

Trackerless P2P file sharing built on a from-scratch Kademlia DHT in Go
(stdlib only), for a university networked-software course. The paper is
`maymounkov-kademlia-lncs.pdf` in this repo; code comments cite its
section numbers.

## How to work on this project

- **The goal is understanding, then working code, then extensibility —
  in that order.** The owner must be able to explain and extend every
  piece without you. Documentation is a deliverable, not an afterthought.
- **Go step by step. Never jump ahead.** The end goal (below) is
  deliberately reached in small, separately explained increments.
  Implement only the scope asked for in the current request. Mention
  later work in docs or a one-line comment, never as stubs or speculative
  abstractions.
- **Keep docs in sync with code in the same change.** When behavior
  changes, update the concept doc (`docs/0N-*.md`), `docs/DRY-RUN.md`
  (real log lines and round/RPC counts), `docs/ARCHITECTURE.md`
  (dependencies, invariants), `docs/FAQ.md`, and the README status
  table. Prefer real log output over invented examples.
- **Every simplification is labeled** `SIMPLIFICATION:` in a code comment,
  explained in its concept doc, and listed in the README's
  simplifications table with "blocks the end goal?" and "when to fix".
- Small functions, no frameworks or config systems (plain `flag`s), no
  third-party dependencies without asking. Comments explain *why* and
  cite paper sections.
- New features go on a feature branch; `main` stays stable. Don't push
  or merge without being asked.

## End goal (reached incrementally — see README status table for what's done)

Peers share files/messages with no central tracker. Pieces are spread
over several nodes, so killing the original seeder doesn't stop
leechers. Runs across real devices, not just localhost. Visual demo.
Evaluation: per-component validation plus graphs of (1) lookup latency vs.
network size, (2) download time vs. leecher count, P2P vs. centralized,
(3) time to recovery after the seeder is killed, (4) control bytes vs.
payload bytes. Merkle trees are optional (see `docs/FAQ.md`).

## Commands

```sh
go build ./... && go vet ./... && gofmt -l .   # gofmt must print nothing
go test ./... -race -count=1                    # must pass before claiming done
bash scripts/demo.sh                            # join + lookup demo; must print PASS
bash scripts/demo-transfer.sh                   # put/get + file transfer + killed seeder; must print PASS
bash scripts/demo-seeder-death.sh               # seeder dies mid-download; on main PASS means the file was lost
bash scripts/demo-big-network.sh                # 20 nodes, random joins, lookups vs brute force + transfer; must print PASS
bash scripts/edge/a3-bridge-between-islands.sh  # edge case: two islands, one bridge dies; PASS = behaved as docs/EDGE-CASES.md predicts
bash scripts/edge/b1-half-the-network-dies.sh   # edge case: half the nodes die; PASS = behaved as docs/EDGE-CASES.md predicts
bash scripts/edge/b2-record-holders-die.sh      # edge case: record holders die; PASS = behaved as docs/EDGE-CASES.md predicts
bash scripts/edge/b3-late-joiners-hide-a-value.sh  # edge case: closer nodes join late; PASS = behaved as docs/EDGE-CASES.md predicts
```

Packages: `internal/kademlia` (the DHT, opaque values), `internal/transfer`
(files on top of the DHT's public API), `internal/framing` (shared
`[length][body]` frames), `cmd/node` (CLI + console). Dependencies point
only downward; see `docs/ARCHITECTURE.md`.

## Invariants that are easy to break (details: docs/ARCHITECTURE.md)

1. `Node.handleMessage` records the sender (`touchContact`) **before**
   dispatching. Network-wide discovery depends on it.
2. Only IDs confirmed by a received message enter the routing table —
   never secondhand contacts or placeholder IDs. That's why `Bootstrap`
   PINGs before its self-lookup.
3. A node never stores or returns itself, and replies never return the
   requester (`closestExcluding`).
4. `Bucket`, `valueStore`, `Manifest` (and `id.go`) do no network I/O;
   the eviction ping lives in `Node`.
5. Register `pending[rpcID]` before sending a request.
6. A lookup's candidate state, and a download's `downloader` state, live
   in one goroutine; worker goroutines only send on a channel.
7. Each UDP message must fit in one datagram (`maxDatagramSize`, 8 KB),
   hence `maxValueSize`/`maxValuesPerKey`. File data only travels over TCP.
8. Nothing from a peer is trusted: provider records are validated,
   manifests must hash to the content key, pieces must match their
   SHA-256. Downloads only create new files (`O_EXCL`).
9. The demo scripts grep exact log/console text (listed in
   `docs/ARCHITECTURE.md`, "If you change X"), and `demo.sh` recomputes
   node IDs as `shasum(seed)`. Keep them in sync with the Go code.

## Environment gotchas (macOS, zsh)

- The default shell is zsh: unquoted `$VAR` holding several PIDs is **not**
  word-split. Run multi-process shell snippets with `bash -c '...'`.
- Background node processes outlive a tool call. Before a demo, check
  that ports are free (`lsof -nP -i :9000`; each node holds UDP *and* TCP
  on its port number) and kill leftovers.
- Background nodes need `--console=false`, or they read stdin.
- macOS has no `timeout` command.
