# The per-node HTTP API

Every node can serve its own state as JSON over HTTP: who it is, its
routing table, the values it holds for the network, and the files it can
serve. It also streams what it does as it happens, and, if you allow it,
takes commands. It's built for the **visual demo**: a browser page
reads each node's API, draws the network, and lets you click to make
nodes act. It also works from `curl`, which makes it a debugging tool
too. That page is `viz/` ([13-visualizer.md](13-visualizer.md)): it reads
snapshots and one `/api/events?level=debug` stream per node, while
`cmd/vizrun` drives the nodes through the control endpoints. Both use
only what this document describes.

| Part | Endpoints | Flag |
|---|---|---|
| [Snapshots](#snapshots) | `GET /api/node`, `/api/table`, `/api/store`, `/api/files` | `--api` |
| [Live events](#get-apievents-what-happens-as-it-happens) | `GET /api/events` (Server-Sent Events) | `--api` |
| [Control](#control-endpoints-make-the-node-act) | `POST /api/lookup`, `/ping`, `/put`, `/get`, `/share`, `/fetch` | `--api` **and** `--api-control` |

## Turning it on

The API is **off by default**. Start a node with `--api host:port`:

```sh
go run ./cmd/node --port 9000 --id-seed demo0 --api 127.0.0.1:8000
go run ./cmd/node --port 9001 --id-seed demo1 --bootstrap 127.0.0.1:9000 --api 127.0.0.1:8001 --api-control
```

```
level=INFO msg="api listening" node=2b370ab7 addr=127.0.0.1:8000
```

Without `--api`, a node opens no HTTP socket, starts no extra goroutine,
and prints exactly the same output as before the API existed.
`--api-control` without `--api` is refused (`error: -api-control needs
-api`).

**Use `127.0.0.1`.** The API has no authentication, and every response
carries `Access-Control-Allow-Origin: *` so a visualizer page from any
origin can read it. That's fine on localhost. Binding it to a LAN
address would let anyone on the network read the node's state (and, with
`--api-control`, control it). The one exception is the network-namespace
lab ([09](09-multi-device.md)): binding to a node's `10.10.0.N` address
is safe there, because that subnet exists only on this laptop.

## Snapshots

All are `GET` (any other method gets `405 Method Not Allowed`) and
return `Content-Type: application/json`, indented for reading with
`curl`. IDs and keys are 40-character hex strings, as everywhere else.

Real output below is from three nodes started with `k = 3`: node0
(`--api 127.0.0.1:8700`), node1 (`--api 127.0.0.1:8701`), and node2 (no
API), both joining via node0. Then node0 ran `put greeting hello from
node0`, and node1 ran `share notes.txt`.

### `GET /api/node`: who this node is

```
$ curl -s 127.0.0.1:8700/api/node
{
  "id": "2b370ab73e042befc2c56a925ab511b5116aec98",
  "ip": "127.0.0.1",
  "udp_port": 9700,
  "tcp_port": 9700,
  "k": 3,
  "alpha": 3,
  "rpc_timeout_ms": 2000,
  "uptime_ms": 539,
  "routing_table_size": 2,
  "stored_keys": 2,
  "files": 0
}
```

| Field | Meaning | From |
|---|---|---|
| `id`, `ip`, `udp_port` | the node's contact: ID, advertised IP (`--advertise-ip`), UDP port | `Node.Self()` |
| `tcp_port` | its file server | `Peer.TCPPort()` |
| `k`, `alpha`, `rpc_timeout_ms` | protocol settings | the `--k`, `--alpha`, `--rpc-timeout` flags (see below) |
| `uptime_ms` | time since the API started, a moment after the node | |
| `routing_table_size`, `stored_keys`, `files` | counts; the next three endpoints give the details | `Node`, `Peer` |

`k`, `alpha` and the RPC timeout are passed in by `cmd/node` from its
flags. `kademlia.Node` keeps them unexported and has no getters, and
adding getters to the DHT just for display wasn't worth a core change.

### `GET /api/table`: the routing table

```
$ curl -s 127.0.0.1:8700/api/table
{
  "id": "2b370ab73e042befc2c56a925ab511b5116aec98",
  "k": 3,
  "size": 2,
  "buckets": [
    {
      "index": 159,
      "contacts": [
        {
          "id": "db61277694fea41b42a8ac82cbb678baac683990",
          "ip": "127.0.0.1",
          "port": 9702,
          "distance": "f0562dc1aafa8ff4806dc6109103690fbd02d508",
          "stale": true
        }
      ]
    },
    {
      "index": 157,
      "contacts": [
        {
          "id": "10f71961bd11dd33c1c95c771b98cf0e09d57b7c",
          "ip": "127.0.0.1",
          "port": 9701,
          "distance": "3bc013d68315f6dc030c36e5412ddebb18bf97e4",
          "stale": false
        }
      ]
    }
  ]
}
```

(This capture killed node2, then ran 5 lookups from node0 with
`--rpc-timeout 300ms`, so node2 failed 5 RPCs in a row.)

Only non-empty buckets are listed, **farthest first** (the console's
`table` order). Within a bucket, contacts are **oldest first**: the first
one is the next eviction candidate ([02](02-routing-table-and-kbuckets.md)).
Bucket `i` holds contacts at XOR distance in [2^i, 2^(i+1)), which is
why a distance starting `f0…` (top bit set) is in bucket 159, and one
starting `3b…` (`0011 1011`, top bit two places lower) is in 157.
`distance` is included so a visualizer doesn't need 160-bit XOR in
JavaScript.

**`stale: true`** means the contact failed `--stale-after` RPCs in a row
(default 5), hasn't been heard from since, and its back-off (30 s at
first, doubling) hasn't run out. It stays in its bucket, but this node's
lookups and replies skip it until then
([02](02-routing-table-and-kbuckets.md#stale-contacts-a-node-that-stops-answering)).
A visualizer can draw it as a dead or fading link. Once node2 was
restarted and messaged node0, the same contact read `"stale": false`
again.

### `GET /api/store`: values held for the network

```
$ curl -s 127.0.0.1:8700/api/store
{
  "keys": [
    {
      "key": "a0f7e779f9247566c84036f07f7bdf4a40a869bd",
      "values": [
        {
          "text": "hello from node0",
          "base64": "aGVsbG8gZnJvbSBub2RlMA=="
        }
      ]
    },
    {
      "key": "d991d59dcf1f4055cc48f09016fd35a2b48a0e27",
      "values": [
        {
          "text": "{\"node_id\":\"10f71961bd11dd33c1c95c771b98cf0e09d57b7c\",\"ip\":\"127.0.0.1\",\"tcp_port\":9701}",
          "base64": "eyJub2RlX2lkIjoiMTBmNzE5NjFiZDExZGQzM2MxYzk1Yzc3MWI5OGNmMGUwOWQ1N2I3YyIsImlwIjoiMTI3LjAuMC4xIiwidGNwX3BvcnQiOjk3MDF9"
        }
      ]
    }
  ]
}
```

These are the values this node keeps **because it is one of the k
closest nodes to each key** ([06](06-store-and-find-value.md)). They
aren't necessarily values it published. Here node0 holds the `greeting`
text it `put` (key = SHA-1("greeting")), and **node1's provider record**
for `notes.txt` (key = the file's content key). That record is what
`fetch` finds through `FIND_VALUE`.

Keys are in ascending order, and each key's values are oldest first. The
DHT stores opaque bytes, so every value comes as `base64`. When the bytes
are valid UTF-8, which `put` text and provider records (JSON) always
are, it also comes as `text`. Binary values have no `text` field.

This is the one endpoint that needed something new in the DHT:
`Node.StoredValues()` (`values.go`), a read-only copy of the node's value
store. It was added for this API, and the DHT itself never calls it.

### `GET /api/files`: files this node can serve

This is node2 one piece into downloading a 4-piece file (the seeder ran
with `--debug-serve-delay 400ms` so the download is slow enough to catch):

```
$ curl -s 127.0.0.1:8722/api/files
{
  "files": [
    {
      "key": "16fc6af0cf2598518cfc151c1ba6d408c8be4541",
      "name": "photo.bin",
      "size": 200000,
      "pieces": 4,
      "have": 1,
      "path": "copy.bin",
      "magnet": "magnet:?xt=urn:tp2p:16fc6af0cf2598518cfc151c1ba6d408c8be4541&dn=photo.bin"
    }
  ]
}
```

The files the node shared, plus the ones it is downloading or has
downloaded (`Peer.Library().List()`), sorted by name. `key` is the
content key, and `magnet` is the same key as a link to hand to someone
else ([12](12-magnet-links.md)). `fetch` accepts either. With no files,
it's `{"files": []}`, an empty list rather than `null`.

`magnet` contains a raw `&`. Go's JSON encoder would normally write it as
`&` (an escape meant for JSON embedded in HTML pages). That decodes
to the same string, but a link copied from `curl`'s output into a console
would no longer parse, so `writeJSON` turns that escaping off
(`SetEscapeHTML(false)`). The `/api/events` stream is unchanged.

**`have` is how many pieces the node holds** and can serve. With partial
seeding ([08](08-swarming.md)) a download enters the library as soon as
it starts, and the node serves each piece once it's written, so
`have < pieces` means "still downloading, already seeding what it has",
or a download that gave up and keeps seeding what it has (fetching it
again resumes it, [08](08-swarming.md#resuming)).
A shared or finished file has `have == pieces`. Polling this is the
simplest way to draw download progress; the `piece received` event
below carries the same count as it changes.

### `GET /api/events`: what happens, as it happens

A [Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)
stream: the connection stays open, and the node writes one event per log
record. A browser reads it with `new EventSource(url)`; from a shell, use
`curl -N` (no buffering).

```
$ curl -sN 127.0.0.1:8710/api/events
: events from node 2b370ab73e042befc2c56a925ab511b5116aec98, level INFO and above

id: 1
data: {"seq":1,"time":"2026-11-14T18:00:43.884580038+05:30","level":"INFO","msg":"rpc received","attrs":{"from":"10f71961bd11dd33c1c95c771b98cf0e09d57b7c@127.0.0.1:9711","rpc_id":"2928ff11","type":"PING"},"dropped":0}

id: 3
data: {"seq":3,"time":"2026-11-14T18:00:43.886225419+05:30","level":"INFO","msg":"rpc received","attrs":{"from":"10f71961bd11dd33c1c95c771b98cf0e09d57b7c@127.0.0.1:9711","rpc_id":"3c7afb69","type":"FIND_NODE"},"dropped":0}

id: 5
data: {"seq":5,"time":"2026-11-14T18:00:43.999015948+05:30","level":"INFO","msg":"rpc received","attrs":{"from":"db61277694fea41b42a8ac82cbb678baac683990@127.0.0.1:9712","rpc_id":"1a28829d","type":"PING"},"dropped":0}
```

That's node0 watching node1, then node2, join through it (each PINGs
first, then looks itself up, [04](04-bootstrap-and-join.md)). Later in
the same run, node1 shared a file and node0, one of the 3 nodes closest
to its key, stored the provider record:

```json
{
    "seq": 13,
    "time": "2026-11-14T18:00:44.052065618+05:30",
    "level": "INFO",
    "msg": "value stored",
    "attrs": {
        "bytes": 87,
        "key": "d991d59dcf1f4055cc48f09016fd35a2b48a0e27"
    },
    "dropped": 0
}
```

**One event = one log record.** `msg` is the log message, and `attrs`
holds its `key=value` pairs. Numbers stay numbers (`"bytes": 87`),
durations become strings (`"2.004s"`), and IDs, addresses and errors are
strings. The same record printed by the node looks like this:

```
level=INFO msg="value stored" node=2b370ab7 key=d991d59dcf1f4055cc48f09016fd35a2b48a0e27 bytes=87
```

**Level.** `?level=debug|info|warn|error`, default `info`. `debug` adds
things like every bucket update and every RPC timeout. A node prints
DEBUG lines only with `--verbose`, but the stream sends them to a viewer
that asks, either way. In the run above, a second viewer asked for
`debug` and got 18 events (8 of them DEBUG), while node0's own output
still had **0 DEBUG lines**.

**`seq` and `dropped`.**
- `seq` counts every event the node published, at any level. That's why
  the INFO stream above goes 1, 3, 5: the even numbers were DEBUG
  events, sent only to the debug viewer. **A gap in `seq` is not a loss.**
- `dropped` is the real loss count: how many events *this* viewer missed
  because it fell more than 1024 behind. The node never waits for a
  viewer (see "How it's built"), so a viewer that stops reading starts
  losing events, and `dropped` says how many.

**No history.** A stream starts with events from the moment you connect.
For the state at that moment, read the snapshot endpoints first. Every
15 seconds without events, the node sends an SSE comment
(`: heartbeat`), so a quiet node can be told apart from a dead
connection.

**What isn't an event.** Anything the code doesn't log:
- **Replies a node sends.** node0 above answered every PING and
  FIND_NODE, but only the *requesters* log the reply (as `rpc received`
  with `type=PONG`, `FIND_NODE_RESP`, …). To draw both directions of an
  exchange, subscribe to both nodes.
- **Console output.** `lookup`'s printed answer is not a log record. The
  lookup itself shows up as its `rpc sent` events and one
  `lookup converged`.

#### The events a visualizer will use

Taken from the log calls in the code (event name = log message). The
full list is every `logger.Debug/Info/Warn/Error` call in
`internal/` and `cmd/node`.

| `msg` | Level | Key attrs | Where it's logged | Means |
|---|---|---|---|---|
| `rpc sent` | INFO | `type`, `to` (`id@ip:port`), `rpc_id` | `node.go` `sendAndWait` | this node sent a request: PING, FIND_NODE, STORE, FIND_VALUE |
| `rpc received` | INFO | `type`, `from`, `rpc_id`, `bytes` | `node.go` `handleMessage` | any message arrived: a request, or a reply to one of ours (`*_RESP`, `PONG`). `bytes` is the datagram's size on the wire; summed over every node it's all DHT traffic, each message once ([10](10-performance-graphs.md), graph 4) |
| `rpc timed out` | DEBUG | `type`, `to`, `rpc_id` | `node.go` | no reply in `--rpc-timeout`: likely a dead node |
| `bucket updated` | DEBUG | `contact` | `node.go` `touchContact` | a contact was added or moved to the tail of its bucket |
| `bucket full, pinging…`, `stale contact still alive…`, `stale contact unresponsive, evicted` | DEBUG | `stale`, `candidate` / `kept` / `evicted`, `replacement` | `node.go` | the eviction check ([02](02-routing-table-and-kbuckets.md)). Here "stale" means the least-recently-seen contact, not a stale-flagged one |
| `contact marked stale` | INFO | `contact`, `retry_in` (how long it's skipped before a retry) | `node.go` `noteFailure` | a node failed `--stale-after` RPCs in a row; lookups and replies skip it from now on |
| `stale contact answered again` | INFO | `contact` | `node.go` `touchContact` | a stale node sent us a message, so it's back |
| `stale contact replaced` | INFO | `evicted`, `replacement` | `node.go` `touchContact` | a new contact took a stale contact's slot in a full bucket, without a ping |
| `lookup round starting` | DEBUG | `target`, `round`, `batch` | `lookup.go` | one α-wide round of a lookup |
| `lookup converged` | INFO | `target`, `rounds`, `rpcs_sent`, `rpcs_failed`, `closest_found` | `lookup.go` | a FIND_NODE lookup finished |
| `find_value finished` | INFO | `key`, `values_found`, `rounds`, `rpcs_sent`, `rpcs_failed`, `ask_all` | `lookup.go` | a FIND_VALUE lookup finished. `ask_all=true` is `FindAllValues` (a download looking for providers: it asks all k closest live nodes); `false` is `FindValue` (`get`: stops at the first holder) |
| `value stored` | INFO | `key`, `bytes` | `node.go` `replyStore` | this node now holds a value for the network |
| `buckets refreshed` | INFO | `reason` (`join` / `periodic`), `buckets`, `rpcs_sent`, `rpcs_failed`, `routing_table_size`, `learned` | `refresh.go` | one bucket-refresh pass: a lookup for a random ID in each idle bucket. Each lookup also produces its own `lookup converged` |
| `republish complete` | INFO | `key`, `reason` (`publisher` / `holder`), `values`, `stored_at`, `stored_locally`, `failed` | `republish.go` | this node stored a key's values again on the k closest nodes (every `--republish-interval`) |
| `republish failed` | WARN | `key`, `reason`, `error` | `republish.go` | the republish lookup failed (e.g. nobody answered) |
| `store complete` | INFO | `key`, `stored_at`, `stored_locally`, `failed` | `values.go` | this node finished publishing a value |
| `provider announced` | INFO | `key`, `tcp_addr`, `replicas` | `peer.go` | this node is now a provider of a file: after `share`, and for a downloader after its first piece (partial seeding, [08](08-swarming.md)) |
| `providers found`, `tcp connected to provider` | INFO | `key`, `count` / `provider` | `download.go` | a download found providers / opened a connection |
| `new providers found` | INFO | `key`, `added`, `known` | `download.go` | a running download rediscovered more providers (it re-asks the DHT every second) |
| `piece requested` | INFO | `index`, `from`, `inflight` | `download.go` | a piece request went out; draw it as an arrow in flight |
| `piece received` | INFO | `index`, `of`, `from`, `bytes`, `have`, `inflight`, `proof` | `download.go` | one piece arrived, passed its Merkle proof check, and is now served; `have` is the new count, `proof` how many hashes its proof had ([11](11-merkle-trees.md)) |
| `provider failed, trying the next one` | WARN | `provider`, `error` | `download.go` | a provider was dropped mid-download |
| `download stalled: no live provider has the missing pieces; looking for more` | WARN | `have`, `of` | `download.go` | nobody known has the missing pieces; the download gives up after 15 s of this |
| `download gave up, still serving its pieces` | WARN | `key`, `have`, `of`, `error` | `download.go` `stopButKeepServing` | the download stopped without finishing: usually a 15 s stall (after `download stalled…`), but also the transfer timeout, a cancelled `/api/fetch`, or a write error (`error` says which). Its partial file stays, its `have` pieces are still served, and a fetch into the same path resumes it. With no pieces yet, nothing is kept and this isn't logged ([08](08-swarming.md#why-giving-up-mustnt-delete-two-leechers-end-with-different-pieces)) |
| `download resumed` | INFO | `key`, `have`, `of` | `download.go` `reopenFile` | a fetch continued one that had given up, starting with `have` pieces on disk, each re-verified first (Merkle proof) ([08](08-swarming.md#resuming)) |
| `announce failed; will retry` | WARN | `key`, `error` | `download.go` | a downloader couldn't announce itself as a provider yet |
| `download complete` | INFO | `key`, `name`, `bytes`, `pieces`, `providers`, `failed_providers`, `max_inflight`, `duration` | `download.go` | a download finished |
| `tcp request served` | INFO, DEBUG for `bitfield` | `to`, `op`, `index`, `bytes` | `server.go` | this node served a manifest, a piece, or (polled every ~500 ms, so DEBUG) its bitfield |
| `node started`, `bootstrap complete` | INFO | `id`, `addr` / `routing_table_size`, … | `cmd/node/main.go` | lifecycle. `node started` comes before the API is up, so no viewer ever sees it |
| `advertise ip auto-detected` | INFO | `ip`, `via` | `cmd/node/main.go` | the node took its advertise IP from the route to its bootstrap node ([09](09-multi-device.md)). Logged before the API is up, like `node started`, so no viewer sees it |
| `advertise ip is loopback but a remote peer contacted us` | WARN | `advertise_ip`, `from` | `node.go` `warnIfAdvertisingLoopback` | once per node: a peer on another machine showed up while this node advertises a 127.x address, so its provider records send remote peers to their own machine. Fix: restart this node with `--advertise-ip <its IPv4 address>`, then restart every node ([09](09-multi-device.md), rules 2 and 3) |

**Because events are log records, log text is part of this API.**
Renaming a log message or attribute changes the event a visualizer
sees, just as it already breaks the demo scripts that grep it.
ARCHITECTURE.md's "If you change X" table lists both.

## Control endpoints: make the node act

With `--api-control` as well as `--api`, a node also takes six commands.
Each one does what the **console command of the same name** does
(`cmd/node/console.go`), through the same `Node` / `Peer` method, with
the same timeouts. It answers with the result as JSON instead of printed
text.

| Endpoint | Body | Does | Console | Method called |
|---|---|---|---|---|
| `POST /api/lookup` | `{"target": "<40 hex>"}` or `{"target": "self"}` | iterative FIND_NODE lookup | `lookup` | `Node.Lookup` |
| `POST /api/ping` | `{"addr": "ip:port"}` (UDP) | one PING | `ping` | `Node.Ping` |
| `POST /api/put` | `{"name": "…", "text": "…"}` | STORE text under SHA-1(name) | `put` | `Node.Store` |
| `POST /api/get` | `{"name": "…"}` | FIND_VALUE for SHA-1(name) | `get` | `Node.FindValue` |
| `POST /api/share` | `{"path": "…"}` | publish a local file | `share` | `Peer.Share` |
| `POST /api/fetch` | `{"key": "<40 hex or magnet link>", "out": "…"}` | download a file | `fetch` | `Peer.Download` |

**Requests** must send `Content-Type: application/json` (otherwise
**415**; why is under "Safety" below). Unknown fields are rejected, so a
typo like `{"targt": …}` is a **400**, not silently ignored. Paths
(`share`'s `path`, `fetch`'s `out`) are on the node's machine. Relative
ones are relative to the node's working directory.

**Responses:**
- **200** with the result;
- **400** `{"error": …}` when the request itself is wrong (bad hex, a magnet link that isn't ours, missing field);
- **500** `{"error": "<command> failed: …"}` when the node tried and failed (timeout, no providers, a value over 256 bytes, an existing `out` file).

The request blocks until the operation finishes, just as the console
command does. If the client hangs up, the operation is cancelled (the
request's context).

Real output below: four nodes with `k = 3`. node0 (UDP 9720, API 8720),
node1 (9721/8721) and node2 (9722/8722) run with `--api-control`. node3
(9723/8723) runs with `--api` only.

### `POST /api/lookup`

```
$ curl -s -X POST -H 'Content-Type: application/json' -d '{"target":"self"}' 127.0.0.1:8722/api/lookup
{
  "target": "db61277694fea41b42a8ac82cbb678baac683990",
  "rounds": 1,
  "rpcs_sent": 3,
  "rpcs_failed": 0,
  "closest": [
    {
      "id": "94fab99d6ca8abab7d217af7e3c5532697bf1a7a",
      "ip": "127.0.0.1",
      "port": 9723,
      "distance": "4f9b9eebf8560fb03f89d67528732b9c3bd723ea"
    },
    {
      "id": "10f71961bd11dd33c1c95c771b98cf0e09d57b7c",
      "ip": "127.0.0.1",
      "port": 9721,
      "distance": "cb963e1729ef79288361f0f5d02eb7b4a5bd42ec"
    },
    {
      "id": "2b370ab73e042befc2c56a925ab511b5116aec98",
      "ip": "127.0.0.1",
      "port": 9720,
      "distance": "f0562dc1aafa8ff4806dc6109103690fbd02d508"
    }
  ]
}
```

`closest` is nearest first, and each `distance` is **to the target**
(here node2's own ID). In `/api/table`, by contrast, `distance` is to
the node being asked.

### `POST /api/ping`

```
$ curl -s -X POST -H 'Content-Type: application/json' -d '{"addr":"127.0.0.1:9721"}' 127.0.0.1:8720/api/ping
{
  "addr": "127.0.0.1:9721",
  "alive": true,
  "id": "10f71961bd11dd33c1c95c771b98cf0e09d57b7c",
  "rtt_ms": 0.895
}
```

Like the console's `ping`, it knows only an address. The PONG reveals
who's there, and `id` is read back from the routing table, where the
PONG put it. A silent address gives `{"addr": …, "alive": false}` after
one `--rpc-timeout`, and a status of **200**: "not alive" is an answer,
not an error.

### `POST /api/put` and `POST /api/get`

```
$ curl -s -X POST -H 'Content-Type: application/json' -d '{"name":"greeting","text":"hello over http"}' 127.0.0.1:8720/api/put
{
  "key": "a0f7e779f9247566c84036f07f7bdf4a40a869bd",
  "stored_at": [
    { "id": "db61277694fea41b42a8ac82cbb678baac683990", "ip": "127.0.0.1", "port": 9722, "distance": "7b96c00f…" },
    { "id": "94fab99d6ca8abab7d217af7e3c5532697bf1a7a", "ip": "127.0.0.1", "port": 9723, "distance": "340d5ee4…" }
  ],
  "stored_locally": true,
  "failed": 0
}

$ curl -s -X POST -H 'Content-Type: application/json' -d '{"name":"greeting"}' 127.0.0.1:8722/api/get
{
  "key": "a0f7e779f9247566c84036f07f7bdf4a40a869bd",
  "values": [
    { "text": "hello over http", "base64": "aGVsbG8gb3ZlciBodHRw" }
  ],
  "from": [
    { "id": "db61277694fea41b42a8ac82cbb678baac683990", "ip": "127.0.0.1", "port": 9722, "distance": "7b96c00f…" }
  ],
  "from_local_store": true,
  "rounds": 0,
  "rpcs_sent": 0,
  "rpcs_failed": 0
}
```

(Contacts shortened to one line each here; the real output is indented
like the lookup above.) The value went to 3 nodes, the k closest to
SHA-1("greeting"): node2, node3, and node0 itself (`stored_locally`).
So node2's `get` was answered **from its own store**: `from` is node2
itself, and no RPCs were sent. From a node that doesn't hold it, `get`
runs a FIND_VALUE lookup, and `from` lists the nodes that returned the
value. A key nobody holds gives `"values": []` with status 200.

### `POST /api/share` and `POST /api/fetch`

All three nodes ran in one directory, so the relative paths below
(`photo.bin`, `copy.bin`) are relative to it.

```
$ curl -s -X POST -H 'Content-Type: application/json' -d '{"path":"photo.bin"}' 127.0.0.1:8721/api/share
{
  "key": "16fc6af0cf2598518cfc151c1ba6d408c8be4541",
  "name": "photo.bin",
  "size": 200000,
  "pieces": 4,
  "have": 4,
  "path": "photo.bin",
  "magnet": "magnet:?xt=urn:tp2p:16fc6af0cf2598518cfc151c1ba6d408c8be4541&dn=photo.bin"
}

$ curl -s -X POST -H 'Content-Type: application/json' -d '{"key":"magnet:?xt=urn:tp2p:16fc6af0…&dn=photo.bin","out":"copy.bin"}' 127.0.0.1:8722/api/fetch
{
  "key": "16fc6af0cf2598518cfc151c1ba6d408c8be4541",
  "name": "photo.bin",
  "size": 200000,
  "pieces": 4,
  "out": "copy.bin",
  "providers_found": 1,
  "pieces_from": {
    "127.0.0.1:9721": 4
  },
  "failed_providers": [],
  "max_inflight": 1,
  "already_had": 0,
  "duration_ms": 1609
}
```

(This is the slow download from the `/api/files` example: 4 pieces at
400 ms each.) `share` answers with the same shape as an `/api/files`
entry, `magnet` included. `fetch`'s `key` takes the 40-hex content key or
a magnet link (`transfer.ParseKeyOrMagnet`, the same parser as the
console). Only the key inside the link is used; its `dn` is ignored
([12](12-magnet-links.md)). `fetch`'s `pieces_from` is keyed by each
provider's TCP address, and `max_inflight` is the most piece requests it
had outstanding at once: here 1, because a download keeps at most one
request per provider ([08](08-swarming.md)). `already_had` is how many
pieces were on disk when it started: 0 here, more when the fetch resumed
one that gave up ([08](08-swarming.md#resuming)). The copy was
byte-for-byte identical.

A `fetch` can fail without downloading a piece in four ways; the first
three fail before any network traffic. On the same node again, the file
is already in its library (shared, downloading, or downloaded):

```
{
  "error": "fetch failed: transfer: 16fc6af0cf2598518cfc151c1ba6d408c8be4541 is already in the library"
}
```

On the same node into a different `out`, after an earlier fetch of that
key gave up: only its own path resumes.

```
{
  "error": "fetch failed: transfer: 16fc6af0cf2598518cfc151c1ba6d408c8be4541 has a stopped partial download at copy.bin; fetch into that path to resume it"
}
```

On another node into the same `out` path, the path exists, and
**downloads never overwrite** (`O_EXCL`, [07](07-file-transfer-over-tcp.md)).
This one is checked only after the lookup and the manifest fetch, because
creating the file needs the manifest:

```
{
  "error": "fetch failed: open copy.bin: file exists"
}
```

A BitTorrent magnet link (`xt=urn:btih:…`), even one with the same 40 hex
characters, is a **400**: it names a torrent in BitTorrent's network, not
a file in this one ([12](12-magnet-links.md#why-tp2p-and-not-btih)):

```
{
  "error": "key must be 40 hex characters or a magnet link: transfer: this is a BitTorrent magnet link (xt=urn:btih:...): it names a torrent in the BitTorrent network, not a file in this one, whose links use xt=urn:tp2p:<key>"
}
```

### Safety: what `--api-control` exposes

The control endpoints make the node do things on your machine and on the
network: `share` publishes any file the node's process can read, and
`fetch` creates new files wherever it can write. So:

1. **They are off unless `--api-control` is given.** Without it the POST
   routes don't exist (**404**), and node3 above answered `404` to the
   same lookup. A CORS preflight isn't approved either.
2. **They demand `Content-Type: application/json`.** A web page on
   another origin can make your browser send a plain form POST to
   `127.0.0.1` without asking anyone. It can't send a JSON one without a
   CORS **preflight** (an `OPTIONS` request first), and those plain
   requests get **415**. Real run: `415` for a `text/plain` body.
3. **But the preflight is approved** (`Access-Control-Allow-Origin: *`),
   so the visualizer page can work from any origin. That means any web
   page open in your browser while a node runs with `--api-control`
   could make it act. Use the flag for demos on your own machine, and
   not on a node holding files you care about.

Restricting `share` / `fetch` to one directory, or adding a token, would
close that gap. Neither is needed for a localhost demo, so neither is
built. If the API ever listens on a LAN address for the multi-device
demo, revisit this first.

## How it's built

```
cmd/node  ──(--api)──►  internal/nodeapi  ──►  internal/transfer  ──►  internal/kademlia
                         HTTP handlers,         Peer: TCPPort,          Node: Self, RoutingTableSnapshot,
                         JSON shapes            Library().List()        StoredValues, counts
```

- **`internal/nodeapi/api.go`**: one handler per endpoint, each reading a
  snapshot through the public API and converting it to its own JSON
  types (`NodeInfo`, `Table`, `Store`, `Files`). Core types keep their
  own JSON (or none): `transfer.SharedFile`, for example, has no JSON tags
  and isn't changed.
- **`cmd/node/api.go`**: `startAPI` opens the listener and serves in a
  goroutine. `main.go` calls it only when `--api` is set, after `node
  started` and before bootstrapping, so a viewer can watch the join.
- **`internal/nodeapi/control.go`**: the six POST handlers, registered
  only with `Config.Control`. Each decodes and validates the body, calls
  the one `Node` / `Peer` method its console command calls, under the
  console's timeout (`OpTimeout`, `TransferTimeout`, passed in by
  `cmd/node`), and converts the result. The CORS preflight answer lives
  here too.
- **Nothing in `internal/kademlia` or `internal/transfer` knows the API
  exists.** Snapshot handlers only read, through the same locked
  accessors the console's `table` and `files` use. Control handlers call
  the same methods the console calls, and those were already safe to run
  concurrently: each lookup and each download keeps its own state
  (ARCHITECTURE.md invariant 10). A slow HTTP client can't block packet
  handling.

### The event stream: a tap on the logger

```
              --api only                                                      ┌─► stdout (unchanged)
 kademlia / transfer ──log record──► TeeHandler ──Enabled at the printed level?┤
                                    (nodeapi/events.go)                       └─► Events.publish ──► viewer 1 (buffered chan) ──► SSE
                                                                                   (only if a viewer      viewer 2 (buffered chan) ──► SSE
                                                                                    wants this level)
```

The DHT and the transfer code already log everything a visualizer needs,
so the events come from the logger, and **neither package changed**.
With `--api`, `cmd/node` wraps the node's log handler in
`nodeapi.TeeHandler` *before* creating the node, so both packages log
through it (the transfer code uses `node.Logger()`).

Three rules make it safe. Each has a test in `events_test.go`:

1. **The printed output doesn't change.** The original handler receives
   exactly the records it would receive without the tap, because the tee
   asks *its* `Enabled` first. `TestTeeLeavesPrintedOutputUnchanged` logs
   the same mix of records with and without the tap, with a DEBUG viewer
   attached, at three levels, and compares the output byte for byte.
2. **No viewer, no cost.** A DEBUG record is only built while some viewer
   wants DEBUG (`Events.wants`, an atomic read on every log call).
   (`TestTeeCostsNothingWithoutSubscribers`)
3. **Logging never waits for a viewer.** Log calls run on the node's UDP
   handler goroutines, so a blocked one would stall packet handling.
   `publish` offers each event to each viewer's buffered channel (1024)
   with a non-blocking send. If the buffer is full, the event is dropped
   and counted. (`TestPublishNeverBlocks` logs 1034 records at a viewer
   that never reads: no wait, 1024 buffered, 10 dropped.)

A viewer that disconnects is unsubscribed when its request ends
(`TestEventStreamUnsubscribesWhenTheViewerLeaves`). Then, with no
viewers left, rule 2 applies again.

### Tests

`internal/nodeapi/api_test.go`, `events_test.go` and `control_test.go`
start real nodes on loopback (like `transfer_test.go`), wired the same
way `cmd/node` does. They check every endpoint against the node's own
state:
- table contacts in the right buckets, with the right distances, and a
  dead contact flagged `stale` after 5 failed pings;
- values on exactly the nodes a `Store` reached;
- binary values without `text`;
- a real PING appearing on the receiver's stream, with the right sender;
- no DEBUG events on an INFO stream, and DEBUG ones with `?level=debug`;
- a control `lookup` returning exactly what `Node.Lookup` returns;
- `put` on one node, `get` on another;
- `share` on one node, `fetch` on another, with the file compared byte
  for byte, and a second fetch refused without touching the existing
  file;
- `fetch` with the `magnet` link `share` returned, and with a copy whose
  `dn` lies: both download the real file, named by its manifest
  (`TestFetchAcceptsMagnetLink`); a `btih` link is a 400;
- `/api/files` entries carry `magnet`, with `&` written as-is in the raw
  body;
- `/api/files` showing a slow download with `0 < have < pieces`, then
  `have == pieces` once it's done;
- `ping` of a live and a dead address;
- the safety rules: 404 without `Control`, 415, an unknown field, a
  preflight approved only with `Control`;
- 16 concurrent control requests on one node, under `-race`;
- a control action showing up on the event stream;
- CORS, 400, 404 and 405.

`TestValueStoreSnapshotIsACompleteCopy` (in `internal/kademlia`) checks
the store accessor.

## Polling or streaming

A visualizer reads each node's snapshots once (`/api/node`,
`/api/table`, `/api/store`, `/api/files`), then follows its
`/api/events`. It re-reads a snapshot when an event says it changed:
for example `value stored` → `/api/store`. On a `?level=debug` stream,
`bucket updated` → `/api/table`. On an INFO stream, `rpc received`
works as the hint: every received message may have changed the table. Each snapshot request takes copies under the same short
locks the console uses, so this is cheap even for a 20-node network.
