# Architecture

This is a short overview of how the code is put together. The project
has two main parts: a Kademlia DHT that lets nodes find each other, and a
file sharing layer built on top of it.

## Packages

```
cmd/node            the program: command-line options and terminal commands
   |
   +--> internal/nodeapi    optional HTTP interface (--api)
   |
   +--> internal/transfer   file sharing
   |         |
   |         v
   +--> internal/kademlia   the DHT
             |
             v
        internal/framing    packs messages into [length][data] frames
```

Each package only uses the ones below it. The DHT doesn't know anything
about files; to it, a value is just some bytes stored under a key. The
file sharing code only uses the DHT through a few functions (store a
value, find a value, find nodes), so either side can be changed without
touching the other.

The visualizer (`cmd/vizrun` and `viz/`) sits outside all of this. It
starts normal node processes and watches them through their HTTP
interface, so the nodes don't know they're being shown.

## The DHT (internal/kademlia)

Every node has a 160-bit ID. The "distance" between two IDs is their XOR,
as in the paper.

- `id.go`, `contact.go`: IDs, XOR distance, and a contact (ID + IP + port).
- `bucket.go`, `routingtable.go`: the routing table. Contacts are kept in
  160 buckets of up to k nodes each, grouped by distance. When a bucket is
  full, the oldest contact is pinged and only replaced if it doesn't
  answer.
- `stale.go`: a contact that fails 5 requests in a row is marked stale
  and skipped until it answers again.
- `messages.go`, `transport.go`: the four messages (PING, FIND_NODE,
  STORE, FIND_VALUE), sent as JSON over UDP.
- `node.go`: sends requests and waits for replies, answers incoming
  requests, and handles joining (`Bootstrap`).
- `lookup.go`: the lookup. It asks 3 nodes at a time (alpha) for nodes
  closer to the target and keeps going until the k closest nodes it has
  found have all answered. Nodes that don't answer are dropped and the
  next closest one is asked instead.
- `values.go`, `store.go`: storing a value on the k nodes closest to its
  key, and finding it again.
- `refresh.go`, `republish.go`: background jobs. Buckets that haven't been
  used for a while are refreshed with a lookup, and stored values are
  stored again every hour so they survive nodes leaving.

Joining works like this: the new node pings the node it was given, then
looks up its own ID. The nodes it talks to during that lookup add it to
their routing tables, which is how the rest of the network learns about
it.

## File sharing (internal/transfer)

Sharing a file:

1. The file is cut into 64 KB pieces.
2. A Merkle tree is built over the pieces. Its root goes into a small
   description of the file called the manifest (name, size, piece size,
   root).
3. The file's key is the hash of the manifest.
4. The node stores a record "I have this file, here is my IP and port" in
   the DHT under that key.

Downloading a file:

1. Look up the key in the DHT to get the list of nodes that have it.
2. Get the manifest from one of them over TCP and check that it hashes to
   the key.
3. Ask the nodes which pieces they have, and request pieces from several
   of them at once, rarest piece first.
4. Every piece comes with a Merkle proof. The piece is checked against
   the root from the manifest before it's written to disk. A node that
   sends a bad piece is dropped.
5. As soon as the first piece is saved, this node announces itself as a
   source too and starts serving the pieces it has. This is why a file
   survives the original sharer going offline, as long as the
   downloaders together have all the pieces.

If a download gets stuck it gives up after 15 seconds, but keeps the
pieces it has and keeps serving them. Running the same `fetch` again
continues where it stopped.

The main files:

- `manifest.go`, `merkle.go`: the manifest, the key, and the Merkle tree
  checks.
- `peer.go`: sharing and announcing.
- `download.go`: the download loop. One goroutine owns all of the
  download's state; small worker goroutines do the network requests and
  report back on a channel.
- `picker.go`, `bitfield.go`: choosing the next piece, and tracking which
  pieces each node has.
- `library.go`: the files this node can serve.
- `server.go`, `client.go`, `protocol.go`: the TCP requests for the
  manifest, the list of pieces a node has, and the pieces themselves.
- `magnet.go`: magnet links, which are just the key in a link format.

## The HTTP interface (internal/nodeapi)

Started with `--api`. It shows a node's routing table, stored values and
files as JSON, plus a live stream of everything the node logs. With
`--api-control` it also accepts the same commands as the terminal. The
visualizer uses this to drive and watch the nodes.

## Things to keep in mind when changing the code

- A node adds the sender of every message it receives to its routing
  table. This is how nodes learn about each other, so don't skip it.
- Only add a node to the routing table after hearing from it directly,
  never just because another node mentioned it.
- A node never returns itself, or the node asking, in a reply.
- Every UDP message has to fit in one packet (8 KB). File data only goes
  over TCP.
- Never trust data from other nodes: check provider records, piece
  lists, the manifest and every piece before using them.
- Only mark a piece as available after it has been fully written to disk.
- The demo scripts look for exact log lines, so changing a log message
  can break them.

## Known limitations

- Lookups wait for a whole round of requests before starting the next
  one, so a dead node slows a lookup down by the timeout (2 s).
- Stored values never expire.
- There's no NAT traversal, so all computers have to be on the same
  network.
- Partial downloads are only remembered while the node is running.
