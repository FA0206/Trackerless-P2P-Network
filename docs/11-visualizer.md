# 11. The visualizer: watching live nodes

Prerequisite: [NODE-API.md](NODE-API.md) (snapshots, the event stream,
the control endpoints). The scenes show what
[04](04-bootstrap-and-join.md), [05](05-iterative-parallel-lookup.md),
[06](06-store-and-find-value.md), [08](08-swarming.md) and
[10](10-magnet-links.md) explain.

> **Status:** built, increments 1–7 (plan, page, ring, comets + replay,
> runner, scenes, headless check), plus a recorded video of one
> continuous run ("How this was recorded" below). The team presents live
> in step mode. Real runs are in
> [DRY-RUN.md part 9](DRY-RUN.md#part-9--the-visualizer-cmdvizrun--viz).

## What it is, in one paragraph

A web page that draws a network of **real nodes of this codebase** while
they work, one mechanism at a time: joining, k-buckets, a lookup,
put/get, sharing, swarming, the seeder dying, giving up and resuming. The
page only **reads** each node's HTTP API (`--api`): snapshots and the
live event stream. A small Go program, `cmd/vizrun`, starts the nodes,
serves the page, and **acts**: when the presenter presses →, it runs the
next scene by calling the nodes' control endpoints (`--api-control`) or
killing a process. Nothing on screen is invented: every dot, arrow and
number comes from an event a node logged, a snapshot it served, or a
fact the runner reports (a PID it killed, a file hash it computed).

**Analogy.** A sports broadcast. The nodes are the players: they play a
real game and don't know a camera exists. `vizrun` is the referee: it
blows the whistle for each play (share, fetch, kill). The page is the
camera crew: it follows the ball, shows slow-motion replays of plays too
fast to see, labels them "replay", and never draws a goal that didn't
happen.

## Run it

```sh
go run ./cmd/vizrun            # builds ./cmd/node, starts viz0, serves the page
# open http://127.0.0.1:8790/ in Chrome, full screen (F11)
# → (or Space, PageDown, Enter; presentation clickers send PageDown) runs the next scene
# Ctrl-C in the terminal stops every node; nothing stays bound
```

- **Step mode** (default): each key press runs one scene; the page then
  **holds** on the scene's last frame until the next press, so the
  presenter moves the slides and the visualizer in lockstep. The hint at
  the top left says when a scene is done (`→ next`).
- **Continuous mode:** `http://127.0.0.1:8790/?mode=auto` advances by
  itself after a 4 s hold. For a later recording.
- **Headless check:** `bash scripts/demo-viz.sh` runs every scene without
  a browser (`vizrun --auto`) and prints PASS (~90 s).
- **Watch nodes started by hand:** `go run ./cmd/vizrun --watch
  nodes.json` with `{"nodes":[{"name":"a","api":"127.0.0.1:8700"}]}`:
  the page then only watches (no scenes). Or, with no runner at all,
  serve `viz/` with any static server and open
  `/?nodes=a@127.0.0.1:8700,b@127.0.0.1:8701`.

**Every run is fresh.** A node can fetch a given file only once per
process, and dead nodes stay dead. To run the scenes again, stop vizrun
(Ctrl-C) and start it again: new processes, new random file bytes.
Reload the page after restarting the runner.

**Before presenting:** close other browser tabs (the nodes run with
`--api-control`, and CORS is open, so any page could drive them; see
NODE-API.md "Safety"); check that ports 9700–9720, 8700–8720 and 8790 are
free (`ss -lunt`, or `lsof -nP -i :9700` on macOS). Keep the tab in
front and the laptop awake: in one test the browser suspended a
background tab's network I/O (`net::ERR_NETWORK_IO_SUSPENDED`), every
stream closed, and the page, by design, showed every node as dead rather
than silently reconnecting. If that happens, Ctrl-C vizrun, start it
again, and reload.

### Presenter's notes, scene by scene

| # | Title (= slide title) | Press → when | On screen, about |
|---|---|---|---|
| 1 | No tracker, just peers | you start | 30 s: 3 joins slowly, 16 quickly |
| 2 | Who you know: k-buckets | the hint shows `→ next` | 10 s |
| 3 | Find anyone in a few hops | `→ next` | 10 s |
| 4 | Storage with no server | `→ next` | 25 s (put, then get) |
| 5 | Share a file, get a magnet link | `→ next` | 15 s |
| 6 | Swarming | `→ next` | ~10 s, then it moves on to 7 **by itself** |
| 7 | Kill the seeder. The file survives. | no key: starts on its own | ~30 s until every copy is verified |
| 8 | Giving up keeps seeding; fetching again resumes | `→ next` | ~40 s, with two real 15 s stalls: talk over the countdown |
| 9 | Live, not faked | `→ next` | holds |

**Scenes 6 and 7 run back to back, without a key press in between.**
The moment that proves the point (the leechers *together* hold every
piece, but none holds them all) lasts only about 15 s at one piece a
second. When the kill waited for the presenter's key, it often came too
late: all three leechers already had 32/32, so killing the seeder proved
nothing (the banner said VOID). Now the runner goes straight from
scene 6 into scene 7 (`chain` in `cmd/vizrun/scenes.go`) and SIGKILLs the
seeder itself at that moment; the title switches to scene 7 on its own,
so move to the next slide when it does. Press → again only for scene 8.
If a leecher ever did finish first, the seeder would still be killed and
the banner would say **VOID** in red.

## Decisions (asked and answered before any code)

| # | Question | Decision |
|---|---|---|
| 1 | Rendering | **SVG + GSAP 3.12.5** from cdnjs. The team asked for "the coolest"; GSAP's easing and sequencing make the motion look polished. SVG keeps nodes and text crisp at 1080p. Comets are computed along quadratic arcs, so no GSAP plugin is needed. No build step: plain `<script>` tags. |
| 2 | Fonts | Google Fonts (Space Grotesk, IBM Plex Sans, JetBrains Mono). The page waits for `document.fonts.ready`. The presentation room has internet, so nothing is vendored. |
| 3 | Where it lives | `viz/` for the page, `cmd/vizrun` for the runner. Stdlib Go; it talks to nodes **only over HTTP and signals**, never imports `internal/`. |
| 4 | Event transport | One `EventSource` per node, from the page straight to each node's `/api/events?level=debug`. 21 streams on one page worked without trouble. |
| 5 | API additions | **None.** Every gap is worked around (below). |
| 6 | Branch | `feature/demo-ready`. |
| 7 | Video | Not now (see Status). |

Also decided by the team: ~20 nodes with **k = 4** (so "the k closest"
is a real subset), a
newcomer's join is watched from the **receiving** nodes' streams with no
new node flag, and scene titles match the slide titles.

## The pieces

```
                        ┌──────────────────────────── browser (one tab) ─────────────────────────────┐
                        │  viz/index.html + viz/*.js                                                  │
                        │   • GET /viz/config, SSE /viz/status                     (vizrun)            │
                        │   • GET /api/node, /api/table                            (each node)         │
                        │   • EventSource /api/events?level=debug                  (each node)         │
                        │   • on →: POST /viz/next;  POST /viz/subscribed           (vizrun only)      │
                        └──────────┬───────────────────────────────────────────┬──────────────────────┘
                                   │ same origin                                │ CORS, read only
                                   ▼                                            ▼
  cmd/vizrun  (127.0.0.1:8790) ──── POST /api/lookup, put, get, share, fetch ──►  21 × cmd/node
   • builds ./cmd/node, starts nodes                                            --api 127.0.0.1:87xx
   • serves viz/ and /viz/config, /viz/status                                   --api-control
   • runs scene N on /viz/next                                                  --console=false
   • subscribes to every node → events.jsonl                                    --k 4 --max-inflight 1
   • SIGKILLs the seeder at the right moment                                    --debug-serve-delay 1s
   • SHA-256 of original vs. downloaded files
   • SIGTERMs every node on exit
```

Rules that keep it honest and simple:

1. **The page never makes a node act.** It POSTs only to vizrun. So
   `--api-control` has exactly one client: a Go program on localhost.
2. **The runner never draws.** It acts, records, and reports facts the
   API can't give (the kill, file hashes, the magnet link from the
   `/api/share` answer) on `/viz/status`.
3. **Scenes are data on both sides**, matched by `id`: one Go function
   in `cmd/vizrun/scenes.go` (what to do), one JS object in
   `viz/scenes.js` (what to draw). Titles live only in `scenes.go`.
4. **Subscribe before acting.** Streams replay nothing. The page asks
   for a scene only after every stream is open. When the runner starts a
   node, it subscribes itself, announces `node up`, and waits (up to 3 s)
   for the page's `POST /viz/subscribed` before anything else happens.

### `cmd/vizrun`

| File | Does |
|---|---|
| `main.go` | flags, signal handling (Ctrl-C → clean up) |
| `runner.go` | the run: start viz0, serve, run scenes on request (or all, with `--auto`), checks, cleanup |
| `nodes.go` | start a node process (flags below), wait for its API and, if it joins, its `bootstrap complete` log line; SIGKILL / SIGTERM |
| `recorder.go` | one `/api/events?level=debug` subscription per node: every event to `events.jsonl`, counts, `dropped`, each node's pieces (for the kill rule), taps for scenes |
| `server.go` | the HTTP side: page, `/viz/*` endpoints, the status hub; the HTTP client for nodes |
| `scenes.go` | the nine scenes' actions and checks |

Every node runs `cmd/node --port <9700+i> --id-seed viz<i> --k 4 --api
127.0.0.1:<8700+i> --api-control --console=false --max-inflight 1
--debug-serve-delay 1s [--bootstrap <a running node>]`.

Flags of vizrun: `--n 20`, `--k 4`, `--base-port 9700`, `--api-base 8700`,
`--http 127.0.0.1:8790`, `--serve-delay 1s`, `--max-inflight 1`,
`--seed-prefix viz`, `--join-seed 1` (who each newcomer bootstraps
through), `--web viz` (served from disk: `go:embed` can't reach a
directory above `cmd/vizrun`), `--out <dir>` (logs, `events.jsonl`,
files; default a new temp dir), `--watch <file>`, `--auto`, `--node-bin`.

| Endpoint | What |
|---|---|
| `GET /viz/config` | `{"nodes":[{"name":"viz0","api":"127.0.0.1:8700"}],"k":4,"alpha":3,"serve_delay":"1s","max_inflight":1,"seeds":"viz0..viz19","commit":"15d5604+dirty","scenes":9,"next_scene":0,"watch":false}` |
| `POST /viz/next` | 202, runs the next scene in the background; 409 while one runs, after the last, or in watch mode |
| `POST /viz/subscribed` | `{"name":"viz3"}`: the page has opened that node's stream |
| `GET /viz/status` | SSE, one JSON object per runner fact, `type` = `scene started` (`scene`, `index`, `title`), `scene done`, `node up` (`name`, `api`, `id`), `window open` / `window closed` (replay brackets; the close carries the scene's facts: `newcomer`, `boot`, `from`, `target`, `key`, `value`…), `magnet`, `fetch started`, `fetch done` (with the `/api/fetch` answer), `killed` (`node`, `reason`, `void`, piece counts), `sha256` (`want`, `got`, `match`), `summary` |

### `viz/`

| File | Does | Network? |
|---|---|---|
| `index.html`, `viz.css` | the fixed 1920×1080 stage, scaled to the window: ring, right-hand panel, 4-line log ticker of real log lines (hidden in scenes 6–8, whose grids need the room and show the same events), title and caption bottom left, badge top right, hint top left | fonts + GSAP from CDNs |
| `format.js` | 40-hex → 8-hex, an event's colour family, time → ns for ordering, `id@ip:port` parsing | no |
| `config.js` | the node list: `/viz/config`, else `?nodes=` | GET |
| `streams.js` | `Viz.getJSON`, `Viz.connectNode` (one `EventSource`; on error it closes instead of reconnecting, so a dead node stays dead and nothing is silently skipped) | read only |
| `ring.js` | nodes at angle = ID / 2^160, label lanes, comets, edges, key markers, dimming | no |
| `rpc.js` | turns a window's events into replay steps, pairing by `rpc_id` | no |
| `replay.js` | the replay buffer and queue | no |
| `scenes.js` | the nine scenes' drawing: panels, grids, captions | no |
| `main.js` | glue: streams, status stream, keys, live (non-replayed) drawing | GET, POST to vizrun |

## Ground rules for what's on screen

- **Allowed:** live events drawn as they arrive; replays of real,
  timestamped events, labeled on the badge; real slowness from
  `--debug-serve-delay`, disclosed on the badge and in the outro.
- **Forbidden:** any dot, arrow, count or timer no event, snapshot or
  runner fact produced.

### Replay: DHT traffic takes milliseconds

A 3-round lookup on localhost takes about **4 ms** (DRY-RUN part 9). A
slow-motion factor big enough to see it (≈1000×) would turn a 2 ms
scheduler hiccup into a 2-second pause. So the DHT scenes use an
**ordered instant replay**:

- The runner brackets each action with `window open` / `window closed`.
  The page buffers every event that arrives in between, sorts by `time`,
  and replays the events **in their real order** at a fixed pace:
  requests of one round leave together, replies return together, round
  markers and results get their own beat (`rpc.js`, `replay.js`).
- Pressing → makes the live nodes do the work *right then*; the replay
  starts within a second. It's a TV replay of a goal that just happened,
  not a recording made beforehand.
- The badge says so: `REPLAY · in order · real 3.5 ms`, the duration
  being the last event's time minus the first's.
- **SIMPLIFICATION:** the replay keeps order, not proportions. What the
  scenes teach is order (round 2 asks nodes learned in round 1), not
  timing.

Scenes 6–8 (file transfer) are **live**, slowed by `--debug-serve-delay
1s` on every serving node and `--max-inflight 1`, never replayed.
Scene 2 replays viz0's own `/api/table` snapshots, taken after each join
in scene 1.

### Who fed whom: piece colours

In scenes 6–8 every node in the grids has a colour: the seeder amber
(the file-payload colour), the others lavender, rose and ivory, in the
order they appear. Each received piece is painted in the **sender's**
colour, from the `from` of its `piece received` event, and its comet on
the ring takes the same colour; the node's name in its grid is written
in its own colour, so the grids double as a legend. Under each leecher, a
tally counts pieces per sender. When a download completes, the page
compares that tally with the download's own count (`pieces_from` in the
`/api/fetch` answer) and says whether they agree, so the colours are
checked against a second, independent source.

### Pairing events across nodes

Events carry no node ID: the stream an event came on says which node
logged it, and `/api/node` maps stream → ID.

| Drawn as | Requester's stream | Receiver's stream | Paired by |
|---|---|---|---|
| request comet (cyan) | `rpc sent` (`type`, `to`, `rpc_id`) | `rpc received` (`type`, `from`, `rpc_id`) | `rpc_id` |
| reply comet, back along the same arc (pale, dashed) | `rpc received` `*_RESP` / `PONG` (`rpc_id`) | not logged by the replier | `rpc_id` |
| piece comet, in the sender's colour | receiver: `piece received` (`index`, `from` = provider's TCP address) | (provider: `tcp request served`) | the provider's TCP address (`/api/node`'s `tcp_port`) |

A request seen only on the receiver's side (a newcomer whose stream isn't
open yet) still becomes a comet; its reply, logged only by the newcomer,
isn't seen and isn't drawn. A bootstrapping node's PING goes to an
all-zero ID, so names are resolved by ID first, then by `ip:port`. RPC
events name a node's UDP address, while a piece's `from` is the
provider's TCP address (its file server); `/api/node` gives both
(`udp_port`, `tcp_port`), so either matches, even for a node started with
its own `--tcp-port`.

## The scenes, as built

21 nodes in all: viz0 starts with the runner; scene 1 adds viz1…viz19;
scene 8 adds viz20. Ports 9700–9720 (UDP and TCP),
8700–8720 (API). Destructive scenes come last: provider records never
expire, and a dead contact stalls each lookup round that touches it for
the 2 s RPC timeout.

| # | Runner does | Page draws, from | Mode |
|---|---|---|---|
| 1 | Starts viz1…viz19 one by one, each with `--bootstrap` to a random running node (`--join-seed`). | The newcomer fades onto the ring; its PING and self-lookup FIND_NODEs as comets, from the receivers' `rpc received`; for the first three, its routing table right after joining (`/api/table`) as lines. | replay (3 slow, 16 fast) |
| 2 | Checks viz0's table (no bucket over k). | viz0's table growing: bucket rows (index, band, dots, n/k) and lines on the ring, from its `/api/table` after every join; then the eviction pings it logged (`bucket full…`, `stale contact still alive…`). | replay of snapshots |
| 3 | viz7 looks up the node farthest from it (XOR) that isn't in its table (`/api/table`): viz10 with these seeds. | Target marker, each round's FIND_NODEs out and replies back, a row per round; `lookup converged`'s rounds and RPCs, with ✓ when the replayed round count equals it. | replay |
| 4 | viz4 puts `greeting`; then the node farthest from the key that `/api/store` shows isn't a holder gets it. | Key marker, STOREs to the k closest, `value stored` holders, `store complete`; the getter's FIND_VALUE rounds, `find_value finished`; the value (from the `/api/get` answer). | replay |
| 5 | viz11 shares 2 MiB of fresh random bytes (32 pieces). | Key marker, the announce's STOREs, record holders, `provider announced`; the magnet link from the `/api/share` answer. | replay |
| 6 | viz3, viz9, viz15 fetch the link: the second when the first has 4 pieces, the third when the second has 2. | Piece grids (seeder full); each received piece painted, and its comet drawn, in the colour of the node that sent it (`piece received`'s `from`), with a live per-sender tally under each grid; "now also a provider" on each leecher's `provider announced`; the union count. | live |
| 7 | Waits for "together every piece, none complete", SIGKILLs viz11, waits for all three fetches, hashes the files. | The banner with the runner's reason; viz11 greyed (its stream closed); `provider failed…` on each leecher; each grid completing, with a check that its per-sender tally (from the events) equals the download's own count (`pieces_from` in the `/api/fetch` answer); `✓ SHA-256 = original` on each grid's last line, green when it matches; the killed seeder's grid collapses to its header. | live |
| 8 | viz13 shares 1 MiB of fresh bytes (16 pieces); viz6 fetches, viz17 starts once viz6 has 6 pieces; viz13 is SIGKILLed once viz17 has 2. Both stall 15 s and give up. viz20 starts and shares the same bytes (same 1 s per piece, so the transfer is visible); viz6 fetches again into the same file, and viz17 once viz6 has its first new piece. | Grids; the kill banner (too early: the union is short); each `download stalled…` with a **real** countdown from that event's time, stopped when a piece arrives (the node resets its clock then); `download gave up, still serving its pieces`; viz20's grid; `download resumed` on both; their missing pieces arriving in viz20's colour; `already_had` from each answer; both SHA-256 checks. | live |
| 9 | Sends the totals. | **The finale.** Every live node's routing table (`/api/table`, read right then) drawn as one glowing mesh, every node gently pulsing; the headline "No tracker. No server. Just peers."; six tiles, each appearing with its final number: nodes, DHT messages (`rpc sent`), pieces moved (`piece received`), seeders killed (`killed` status), copies verified (`sha256` matches), log events recorded; one footnote with the honest fine print (replay rule, serve delay, k, α, seeds, date). | static |

**Scene 8's history.** Its first version failed 3 of its first 7 runs at
the resume, because of two bugs in the core (below): the leecher held a
provider record itself, answered its own `FIND_VALUE` from it, and never
saw the new seeder. Rather than stage the scene around the bug (pick a
lucky file and lucky leechers), it was taken out, the core was fixed
(lookups skip dead nodes; downloads use `FindAllValues`), and the scene
came back unchanged, with its fixed cast: **5 of 5** headless runs then
passed, and both leechers gave up with the same count every time (11/11,
10/10, 12/12, 9/9, 8/8), because rediscovery now finds the other
leecher's record.
The failed runs are in
[DRY-RUN.md part 9](DRY-RUN.md#part-9--the-visualizer-cmdvizrun--viz).

## Working around what the API doesn't say

| Gap | Done here, with no API change |
|---|---|
| Repliers don't log their replies | Subscribe to every node; draw the reply when the requester logs `rpc received *_RESP` with the same `rpc_id`. |
| A FIND_NODE_RESP's contacts aren't logged | Only real RPCs are drawn; "who pointed to whom" shows as the next round's targets. |
| Piece events have no `key`; `/api/files` has no bitfield | One download per node at a time; grids are built from that node's `piece received` (`index`). |
| No UDP byte counters | Not shown. Control-vs-payload bytes (evaluation graph 4) is a later increment. |
| `rpc sent` has no lookup ID | Scenes 3–4 run on an idle network (file traffic starts in scene 5). |
| The magnet link and the get's value are in no event | Taken from the `/api/share` and `/api/get` answers and sent on `/viz/status`. |
| No endpoint hashes a whole file | The runner hashes the files on disk. **SIMPLIFICATION:** works because every node is on this machine; the cross-laptop increment needs each laptop to hash its own copy. |

## What the runs showed (worth knowing beyond the visualizer)

Both of these are **fixed in the core** now: a lookup drops a node that
doesn't answer and asks the next live one ([05](05-iterative-parallel-lookup.md)),
and downloads find providers with `FindAllValues`, which asks the k
closest live nodes even when this node holds records itself
([06](06-store-and-find-value.md#reading-a-list-findallvalues)). What
they were:

- **A record holder never looks past its own store.** `FindValue`
  returns early when the node holds any value for the key. A downloader
  that holds a record for its file's key therefore rediscovers only the
  providers whose records reached *it*. In scenes 6–7 that worked (viz9
  saw its local record count grow 2 → 4 as the other leechers
  announced), but a provider whose record lands elsewhere is invisible
  to it, and a resumed fetch can fail with `no providers found` while a
  live provider exists. Scene 8 hit exactly this.
- **One dead node near a key makes every announcer keep a copy.** If a
  dead node is among the k closest to a key, lookups for it converge with
  fewer than k live nodes, and `selfAmongClosest` returns true for
  anyone, so every `Store` for that key also stores locally. Combined
  with the point above, those nodes stopped asking the network.
- **Leechers don't always give up with the same pieces.** DRY-RUN part 8
  shows them ending equal. One scene-8 run ended 10 vs 11: the second
  leecher's last piece came from the seeder just before the kill, and its
  provider record took ~4 s to store (lookups touching a node killed
  earlier wait out the 2 s RPC timeout), so the first leecher's
  rediscovery never found it within the 15 s.

## How to add a scene

1. **Runner side**, `cmd/vizrun/scenes.go`: write `func sceneX(ctx
   context.Context, r *runner) error` and add `{"x", "Slide title",
   sceneX, false}` to `allScenes()` at the right place (`true` instead of
   `false` makes the next scene start as soon as this one ends, without a
   key press, as scene 6 does). Make nodes act only
   through `postJSON(ctx, node.API, "/api/…", body, &answer)` and
   `node.kill(sig)`. Wrap a fast action in `r.window("x", "slow",
   fields, func() (map[string]any, error) {…})` so the page replays it;
   leave slow (file) actions unwrapped so they're drawn live. Report
   facts with `r.emit("type", fields)` and verify with `r.check(ok,
   "…")`.
2. **Page side**, `viz/scenes.js`: add `x: { caption, enter(app) {…},
   onWindowStart, onStep, onWindowEnd, onLive, onStatus }` to
   `Viz.scenes` (any subset; see the comment at the top of the file).
   Draw with `app.ring` (`light`, `comet`, `edge`, `marker`, `dim`) and
   `app.setPanel(html)`.
3. If it needs a new event kind, check it's in `scripts/demo-viz.sh`'s
   list, and run `go test ./cmd/vizrun` and `bash scripts/demo-viz.sh`.

## How it's checked

- `go test ./cmd/vizrun -race`: SSE parsing, XOR ordering, the kill rule
  (`recorder.spread`), `events.jsonl` and `dropped` accounting, watch
  mode, too few nodes refused.
- `bash scripts/demo-viz.sh` (headless, ~90 s): vizrun's own checks
  (every node joined; no bucket over k; scene 3's `lookup round
  starting` count = `lookup converged` rounds = the answer's rounds; the
  get came over the network; the kill moment was valid; 4 of 4 SHA-256
  sums match; both scene-8 leechers stopped short; the resume logged
  `download resumed`), every event kind the page draws was recorded,
  `dropped` is 0 on every event, and no port is left bound.
- In the browser (one full step-mode run, DRY-RUN part 9): the page's
  per-message counters equalled `events.jsonl`'s for all ten message
  kinds compared (5054 events, `dropped` 0), and the lookup verdict
  showed ✓.

## How this was recorded

One continuous run (`?mode=auto`), recorded on this laptop with no screen
of its own involved:

1. A fresh `vizrun` on spare ports, so a run on the default ports can keep
   going: `go run ./cmd/vizrun --base-port 9800 --api-base 8800 --http
   127.0.0.1:8792`.
2. A virtual X screen (`xvfb-run -s "-screen 0 1920x1108x24"`) with
   Chrome for Testing in kiosk mode on `http://127.0.0.1:8792/?mode=auto`.
   Chrome for Testing shows a 56 px banner at the top that no flag
   removes, so the page's viewport is 1920×1052 and the stage is drawn at
   97.4% scale, centred.
3. `ffmpeg -f x11grab -draw_mouse 0 -framerate 60 -video_size 1920x1080
   -i "$DISPLAY+0,28" -c:v libx264rgb -preset ultrafast -crf 0 raw.mkv`
   (lossless), stopped 22 s after the runner prints `scene 9:`.
4. Encoded for sharing: the first 3 s (fonts loading) trimmed, the stage
   cropped out exactly (`crop=1870:1052:25:28`) and scaled back to
   1920×1080, then `-c:v libx264 -preset medium -crf 20 -pix_fmt yuv420p
   -movflags +faststart` (bt709). Result: 3 min 56 s, 1080p60, 18 MB.

Nothing was cut or sped up inside the run: what the video shows is one
live run, in real time, with the labeled replays and the disclosed
1 s serve delay. A browser tab that isn't visible gets no animation
frames, which is why it's recorded on a virtual screen rather than in a
background tab. The video itself isn't in the repository.

## Later

- **Watching nodes on several laptops.** Same page, a different node
  list (`--watch nodes.json` with LAN API addresses); the runner stays
  localhost-only. Each laptop starts its node with `--api
  <its-LAN-IP>:87xx`, opens its firewall for that port as well as the
  node's UDP/TCP port, and only the laptop driving the demo uses
  `--api-control` (NODE-API.md "Safety"). Ordering across machines must
  pair events (`rpc_id`; provider address + piece index), never trust
  `time` alone: clocks differ. File hashes must come from each laptop.
- **A ← key** to re-show an earlier scene's final frame isn't built: a
  scene can't be un-run, and the slides carry the recap.
