# FAQ: common confusions

Short answers, with pointers to the doc that has the long one.

## Joining the network

### What exactly does a new node need to join? Is it an IP address, or something else?

It needs the **IP address and UDP port of any one node already in the
network**, e.g. `127.0.0.1:9000` or `192.168.1.20:9000`. A hostname that
resolves to an IP works too (`--bootstrap` goes through
`net.ResolveUDPAddr`). That's the only outside knowledge required.

It does **not** need:

- the bootstrap node's node ID (it learns it from the PONG),
- a list of nodes or the size of the network,
- any permission, account, or central server,
- a special "bootstrap server" — any participant works.

It also needs its own node ID, but it makes that up itself, and its own
UDP port. Full walkthrough with real logs:
[04-bootstrap-and-join.md](04-bootstrap-and-join.md).

### How does someone get that first IP:port in practice?

Out of band. In this project you type it on the command line. Real
systems ship a few well-known hostnames (BitTorrent's DHT uses
`router.bittorrent.com:6881`) or remember peers from the last session.
LAN discovery by broadcast is another option, not implemented here.

### Does the bootstrap node have to stay online after I join?

No. After the self-lookup, your routing table has contacts of its own.
The bootstrap node is just one of them.

### Do existing nodes have to "accept" a new node?

No. They don't even know a join is happening. They answer `PING` and
`FIND_NODE` as usual, and record the newcomer because every message they
receive updates their routing table.

### What if two nodes pick the same ID?

With random 160-bit IDs the chance is negligible (≈ 2⁻¹⁶⁰ per pair). With
`--id-seed`, using the same seed twice *will* collide. The protocol
doesn't detect this, so don't do it.

## IDs, distance, buckets

### Is XOR "distance" related to network distance, latency, or geography?

No. It's distance in the ID space only. Two nodes "close" by XOR can be
on different continents. Kademlia uses it to decide *who is responsible
for what* and *who to ask next*, not *who is fast to reach*.

### What's the difference between a node ID, an address, and a contact?

- **Node ID**: who you are in the 160-bit space; decides your position.
- **Address**: how to reach you (IP + UDP port). It can change, e.g. on a
  different network.
- **Contact**: the pair (ID, address), what routing tables store and
  `FIND_NODE` returns (`contact.go`).

### There are 160 buckets, but `table` only ever shows 3 or 4. Is something wrong?

No. Half of all random IDs differ from yours in the top bit (bucket 159),
a quarter in bucket 158, and so on. A bucket below ~150 only gets an
entry in a network of thousands. See "Final state" in
[DRY-RUN.md](DRY-RUN.md).

### Why does a node never appear in its own routing table or lookup results?

You can't route to yourself, and "the k closest *other* nodes" is the
useful answer. `RoutingTable.Update` and the lookup both filter out
`self`.

### Are `k` and `alpha` related?

No. `k` (20) is **how many** closest nodes a lookup finds, and how many
contacts a bucket holds. `alpha` (3) is **how many** RPCs a lookup has
in flight at once. You can change either independently.

### What's an RPC ID vs. a node ID?

Both are 160-bit, but unrelated. The node ID identifies a node. The RPC
ID is a fresh random number per request, echoed back in the reply, so the
sender knows which request a reply answers ([03](03-message-framing.md)).

## The demo and lookups

### Why does every lookup in the demo query every node?

Lookup stops once the `k` closest nodes it has seen have all been
queried. With 5 nodes and `k = 20`, "the 20 closest" means everyone.
`O(log n)` behavior only shows once `n` is much larger than `k`. Use a
small `--k` or many nodes to see it.

### node0 never sends anything in the demo. How does it know everyone?

Every node that joined later sent it a `FIND_NODE` during their
lookups. Receiving a message is enough to learn the sender
([02](02-routing-table-and-kbuckets.md)).

### If a lookup hits a dead node, how long does it stall?

In **this** implementation, one full `--rpc-timeout` (2s) per round that
includes a dead node. A round waits for all of its up-to-α RPCs,
timeouts included, before starting the next. `alpha` still helps: the 3
RPCs overlap, so a round costs the *slowest* reply, not the sum.

The paper's version has no round barrier: it sends a new request as soon
as any reply arrives, so a dead node doesn't hold up the others. That's a
known simplification here (see [05](05-iterative-parallel-lookup.md)).
It's worth fixing before measuring latency or recovery time.

## Wire protocol

### Doesn't UDP lack message boundaries? Isn't that why we length-prefix?

No — that's TCP. One UDP `send` arrives as exactly one UDP `recv`: whole,
never merged with another. The length prefix here is a sanity check and
makes the same framing reusable on TCP, where it *is* required. See
[03-message-framing.md](03-message-framing.md).

### Why record the sender's address from the packet instead of a field in the message?

The packet's source address is what actually worked to reach us, and a
sender can't trivially lie about it and still get replies. A field
inside the message could say anything. `ContactFromUDPAddr` in
`contact.go`.

### Why JSON instead of a compact binary format?

Readability while learning and debugging: you can read a packet in
`tcpdump`. It costs bytes, which will show up when we measure "control
bytes vs. payload bytes". Swapping the encoding later only touches
`transport.go` and `messages.go`.

### Can nodes run on different machines yet?

Untested. Nodes record each other's addresses from real packet sources,
so a LAN may already work *if every node bootstraps via a LAN address*.
Bootstrapping via `127.0.0.1` would hand loopback addresses to remote
machines, which can't use them. Multi-device is a planned later step.

## File transfer (planned — being built on branch `feature/store-findvalue-tcp`)

The main branch only has `PING` and `FIND_NODE`. These answers describe
the design being implemented on the feature branch.

### How does the output of the Kademlia algorithm tell us how to open a TCP connection?

**Indirectly.** A plain lookup (`FIND_NODE`) returns the `k` nodes whose
**IDs are closest to a key**, as contacts `(node ID, IP, UDP port)`.
Those nodes usually *don't have the file*. Their job is to **store small
records about it** — think of them as the file's index cards.

The flow:

1. **Publish.** A node with a file computes a 160-bit **content key**
   (the hash of the file's description). It runs a lookup for that key
   and sends `STORE(key, provider record)` to the k closest nodes. The
   provider record says "I have it, reach me at this TCP address".
2. **Find.** A node that wants the file runs `FIND_VALUE(key)`. This is
   the same iterative lookup, but it stops as soon as any node returns
   the stored records. The result is a list of provider records.
3. **Connect.** For each provider record, `net.Dial("tcp", ip:tcp_port)`.
   The operating system does the TCP three-way handshake. On that
   connection we send length-prefixed requests ("send me the file
   description", "send me piece 7") and get length-prefixed replies.

So Kademlia answers *"who has the file?"*, and TCP carries the file.
That's how BitTorrent uses its DHT too.

### What exact attributes do we receive, and which ones do we use to connect?

| Source | Attributes | Used for |
|---|---|---|
| `FIND_NODE` / lookup result (contact) | node ID, IP, **UDP** port | routing: who to ask next |
| `FIND_VALUE` result (provider record, a stored value) | provider's node ID, IP, **TCP** port | connecting to download |

The provider record carries its own TCP port, so a node can serve files
on a different port from its DHT socket. TCP and UDP port numbers are
separate spaces, so the default reuses the same number.

### Why not send the file itself through STORE / FIND_VALUE?

DHT messages are single UDP datagrams (8 KB max here). UDP has no
retransmission or flow control. And storing whole files on k random
nodes would make them carry data they never asked for. So the DHT holds
only small pointers, and bulk data goes over TCP directly between the
peers that want it.

### How do we know a piece from a stranger isn't corrupted or fake?

The content key is a hash of the file's description, which includes a
hash of every piece. A downloader checks
`hash(description) == key` and then `hash(piece) == description.piece_hash[i]`
for each piece. A peer can't send a fake piece without breaking a hash.

### Do Merkle trees make sense here?

They make sense, but they aren't *necessary* at this project's scale.
Recommendation: a flat list of piece hashes first, Merkle as an optional
later upgrade.

- **What a Merkle tree buys you** (BitTorrent v2 uses one): the content
  key becomes the tree's root hash. Any single piece can be verified with
  only `log₂(pieces)` sibling hashes, without first downloading the full
  list of piece hashes. The root stays 32 bytes however large the file
  gets.
- **What a flat hash list costs instead** (BitTorrent v1's approach): the
  downloader fetches the whole piece-hash list before verifying anything.
  For a 100 MB file in 256 KB pieces, that's 400 hashes ≈ 13 KB, which is
  trivial.
- **When Merkle starts to matter**: very large files (millions of
  pieces), or wanting to verify pieces before having all the metadata.
  Neither applies to a course demo.

The flat-list design keeps an easy upgrade path: replace "list of piece
hashes" with "Merkle root", and add a proof to each piece response. It's
a good stretch goal if you want a concrete thing to show.
