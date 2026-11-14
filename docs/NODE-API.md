# The per-node HTTP API

Every node can serve its own state as JSON over HTTP: who it is, its
routing table, the values it holds for the network, and the files it can
serve. It's built for the **visual demo**: a browser page polls each
node's API and draws the network. It also works from `curl`, which makes
it a debugging tool too.

**Status:** increment 1 of 3 is done (the snapshot endpoints below).
Next: **2.** a live event stream (`GET /api/events`: every RPC, stored
value and received piece, as it happens); **3.** control endpoints (POST
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
- **Tests:** `internal/nodeapi/api_test.go` starts real nodes on loopback
  (like `transfer_test.go`). It checks every endpoint against the node's
  own state: table contacts in the right buckets with the right
  distances, values on exactly the nodes a `Store` reached, binary values
  without `text`, the CORS header, 405 and 404.
  `TestValueStoreSnapshotIsACompleteCopy` checks the new store accessor.

## Polling, for now

Until the event stream (increment 2) exists, a visualizer polls: for
example `/api/table` and `/api/store` every 500 ms per node. Each request
takes a snapshot under the same short locks the console uses, so polling
a 20-node network this way is cheap.
