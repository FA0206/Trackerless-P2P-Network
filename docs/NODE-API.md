# The per-node HTTP API

Every node can serve its own state as JSON over HTTP: who it is, its
routing table, the values it holds for the network, and the files it can
serve. It's built for the **visual demo**: a browser page polls each
node's API and draws the network. It also works from `curl`, which makes
it a debugging tool too.

**Status:** increments 1 and 2 of 3 are done: the snapshot endpoints,
and the live event stream (`GET /api/events`: every RPC, stored value
and received piece, as it happens). Next: **3.** control endpoints (POST
`lookup`, `ping`, `put`, `get`, `share`, `fetch`, the same as the
console).

## Turning it on

The API is **off by default**. Start a node with `--api host:port`:

```sh
go run ./cmd/node --port 9000 --id-seed demo0 --api 127.0.0.1:8000
```

```
level=INFO msg="api listening" node=2b370ab7 addr=127.0.0.1:8000
```

Without `--api`, a node opens no HTTP socket, starts no extra goroutine,
and prints exactly the same output as before the API existed.

**Use `127.0.0.1`.** The API has no authentication, and every response
carries `Access-Control-Allow-Origin: *` so a visualizer page from any
origin can read it. That's fine on localhost. Binding it to a LAN
address would let anyone on the network read the node's state (and, once
increment 3 lands, control it).

## Endpoints

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
          "distance": "f0562dc1aafa8ff4806dc6109103690fbd02d508"
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
          "distance": "3bc013d68315f6dc030c36e5412ddebb18bf97e4"
        }
      ]
    }
  ]
}
```

Only non-empty buckets are listed, **farthest first** (the console's
`table` order). Within a bucket, contacts are **oldest first**: the first
one is the next eviction candidate ([02](02-routing-table-and-kbuckets.md)).
Bucket `i` holds contacts at XOR distance in [2^i, 2^(i+1)), which is
why a distance starting `f0…` (top bit set) is in bucket 159, and one
starting `3b…` (`0011 1011`, top bit two places lower) is in 157.
`distance` is included so a visualizer doesn't need 160-bit XOR in
JavaScript.

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

```
$ curl -s 127.0.0.1:8701/api/files
{
  "files": [
    {
      "key": "d991d59dcf1f4055cc48f09016fd35a2b48a0e27",
      "name": "notes.txt",
      "size": 11,
      "pieces": 1,
      "path": "/tmp/tmp.TCTm6CXctj/notes.txt"
    }
  ]
}
```

The files the node shared, plus the ones it finished downloading
(`Peer.Library().List()`), sorted by name. `key` is the content key
others pass to `fetch`. With no files, it's `{"files": []}`, an empty
list rather than `null`.

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
| `rpc received` | INFO | `type`, `from`, `rpc_id` | `node.go` `handleMessage` | any message arrived: a request, or a reply to one of ours (`*_RESP`, `PONG`) |
| `rpc timed out` | DEBUG | `type`, `to`, `rpc_id` | `node.go` | no reply in `--rpc-timeout`: likely a dead node |
| `bucket updated` | DEBUG | `contact` | `node.go` `touchContact` | a contact was added or moved to the tail of its bucket |
| `bucket full, pinging…`, `stale contact still alive…`, `stale contact unresponsive, evicted` | DEBUG | `stale`, `candidate` / `kept` / `evicted`, `replacement` | `node.go` | the eviction check ([02](02-routing-table-and-kbuckets.md)) |
| `lookup round starting` | DEBUG | `target`, `round`, `batch` | `lookup.go` | one α-wide round of a lookup |
| `lookup converged` | INFO | `target`, `rounds`, `rpcs_sent`, `rpcs_failed`, `closest_found` | `lookup.go` | a FIND_NODE lookup finished |
| `find_value finished` | INFO | `key`, `values_found`, `rounds`, … | `lookup.go` | a FIND_VALUE lookup finished |
| `value stored` | INFO | `key`, `bytes` | `node.go` `replyStore` | this node now holds a value for the network |
| `store complete` | INFO | `key`, `stored_at`, `stored_locally`, `failed` | `values.go` | this node finished publishing a value |
| `provider announced` | INFO | `key`, `tcp_addr`, `replicas` | `peer.go` | this node is now a provider of a file |
| `providers found`, `tcp connected to provider` | INFO | `key`, `count` / `provider` | `peer.go` | a download found providers / opened a connection |
| `piece received` | INFO | `index`, `of`, `from`, `bytes` | `peer.go` | one verified piece arrived |
| `provider failed, trying the next one` | WARN | `provider`, `error` | `peer.go` | a provider was dropped mid-download |
| `download complete` | INFO | `key`, `name`, `bytes`, `pieces`, `providers`, `failed_providers`, `duration` | `peer.go` | a download finished |
| `tcp request served` | INFO | `to`, `op`, `index`, `bytes` | `server.go` | this node served a manifest or a piece |
| `node started`, `bootstrap complete` | INFO | `id`, `addr` / `routing_table_size`, … | `cmd/node/main.go` | lifecycle. `node started` comes before the API is up, so no viewer ever sees it |

**Because events are log records, log text is part of this API.**
Renaming a log message or attribute changes the event a visualizer
sees, just as it already breaks the demo scripts that grep it.
ARCHITECTURE.md's "If you change X" table lists both.

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
- **Nothing in `internal/kademlia` or `internal/transfer` knows the API
  exists.** The handlers only read, through the same locked accessors
  the console's `table` and `files` use. A slow HTTP client therefore
  can't block packet handling.

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

`internal/nodeapi/api_test.go` and `events_test.go` start real nodes on
loopback (like `transfer_test.go`), wired the same way `cmd/node` does.
They check every endpoint against the node's own state:
- table contacts in the right buckets, with the right distances;
- values on exactly the nodes a `Store` reached;
- binary values without `text`;
- a real PING appearing on the receiver's stream, with the right sender;
- no DEBUG events on an INFO stream, and DEBUG ones with `?level=debug`;
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
