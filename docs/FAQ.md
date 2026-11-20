# FAQ: common confusions

Short answers, with pointers to the doc that has the long one.

Claims about outside systems (BitTorrent BEPs, libtorrent, eMule's Kad,
research papers) link to their sources. Anything we could not check
against a primary source is marked **(not verified)**.

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

The exception is a node that is the *only* link between two groups.
Knowledge spreads only through messages: a node learns of you when you
send it something. If two groups were built separately and only one node
talks to both, its death splits the network for good, because nothing
here re-bootstraps or refreshes buckets. But if anyone has done even one
lookup across that node, the lookup contacted the far side directly, and
those nodes recorded the querier. Then the link node can die harmlessly.
[EDGE-CASES.md, case A3](EDGE-CASES.md#a3--two-islands-and-one-bridge)
shows both.

### Do existing nodes have to "accept" a new node?

No. They don't even know a join is happening. They answer `PING` and
`FIND_NODE` as usual, and record the newcomer because every message they
receive updates their routing table.

### What if two nodes pick the same ID?

With random 160-bit IDs the chance is negligible (≈ 2⁻¹⁶⁰ per pair). With
`--id-seed`, using the same seed twice *will* collide. The protocol
doesn't detect this, so don't do it. What happens if you do:

- Joining *through* your twin fails, because its PONG carries your own ID
  and a node can't tell itself from a twin (`the node at … has the same
  ID as this node`).
- Joining through anyone else works for both twins. That node keeps one
  contact per ID, the twin that spoke last, so everything addressed to
  that ID reaches only one of them.

Both are tested (`TestBootstrapThroughNodeWithOurOwnID`,
`TestDuplicateIDNewestSenderWins`); every other join scenario is in
[04](04-bootstrap-and-join.md#join-scenarios-what-if).

## IDs, distance, buckets

### Is XOR "distance" related to network distance, latency, or geography?

No. It's distance in the ID space only. Two nodes "close" by XOR can be
on different continents. Kademlia uses it to decide *who is responsible
for what* and *who to ask next*, not *who is fast to reach*. See the next
answer for what real systems do about latency.

### Do real implementations pick node IDs so that nearby IDs mean low latency? Does Kademlia even try?

**No, and Kademlia deliberately doesn't.** IDs are random (paper
Sec. 2.1: "Node IDs are currently just random 160-bit identifiers"; here,
`RandomID` / `HashID` in `internal/kademlia/id.go`). That randomness is
the point. Keys and nodes are spread uniformly, so every node is
responsible for about the same share of keys whatever its location, and
an attacker can't easily pick where they land. If IDs encoded location,
all of one region's nodes would crowd one part of the ID space, and one
region would end up responsible for a skewed share of keys.

What Kademlia offers instead is **flexibility, which can be spent on
latency**:

- Any contact in the right bucket makes the same progress toward a target.
  So among those equally good candidates you can prefer the fast ones. The
  paper says this itself (Sec. 2.3): Kademlia "can route for lower latency
  because it has the flexibility of choosing any one of k nodes", and
  footnote 1: "Bucket entries and FIND replies could be augmented with
  round trip time estimates for use in selecting the α nodes."
- The literature names two techniques
  ([Gummadi et al., SIGCOMM 2003](https://www.cs.yale.edu/homes/ramki/sigcomm03.pdf)):
  **proximity neighbor selection (PNS)**, which fills routing-table slots
  with low-latency nodes, and **proximity route selection (PRS)**, which
  picks the low-latency next hop among candidates. The paper argues that
  geometries with more freedom of choice (the ring, and by extension
  XOR) make both more effective.

What real systems do:

- **libtorrent** (Mainline DHT) uses round-trip time as a tie-breaker when
  maintaining its routing table. In `src/kademlia/routing_table.cpp`
  (branch `RC_2_0`, read directly), `replace_node_impl` has a last-resort
  rule: "if the node we found matching our bit prefix has higher RTT than
  the new node, replace it". `fill_from_replacements` also promotes the
  lowest-RTT replacement node. That's a mild form of PNS. Node IDs
  themselves still don't encode location.
- **Azureus/Vuze's DHT** computed **Vivaldi network coordinates**:
  synthetic positions whose distances predict RTT. It used them to steer
  DHT traversal toward nearby nodes. Source: summaries of
  [Ledlie et al., NSDI 2007](https://static.usenix.org/events/nsdi07/tech/full_papers/ledlie/ledlie_html/index.html)
  found via search, **(not verified)** against the paper's text.
- Research prototypes add PNS to Kademlia's iterative lookup, e.g. by
  preferring same-ISP peers
  ([Kaune et al., P2P 2008](https://www.kom.tu-darmstadt.de/papers/KLKP08_1016.pdf);
  summary from search, **(not verified)**).

A subtlety for *iterative* lookups like ours: every RPC goes from the
searcher straight to each queried node. So what matters is the
searcher's RTT to each node, not node-to-node RTT along a path.

**What does BEP 42 aim at, then?** Security, not latency.
[BEP 42](https://www.bittorrent.org/beps/bep_0042.html) ties a node's ID
to its external IP: the first 21 bits of the ID must match a CRC32C hash
of the masked IP plus a random value `r` (0–7). The aim is to stop an
attacker choosing IDs right next to a target infohash. Such an attacker
could become the "k closest" nodes for it, to snoop or censor (a Sybil /
eclipse attack). The BEP doesn't mention latency at all. And because the
IP goes through a hash, neighboring IPs don't get neighboring IDs.
Whether libtorrent enforces BEP 42 by default **(not verified)**: it's
missing from Wikipedia's libtorrent BEP list, and we didn't find it in
libtorrent's own docs.

**In this codebase:** no RTT is recorded. Buckets keep the oldest live
contacts (`bucket.go`), and `iterativeFind` (`lookup.go`) picks the α
closest by XOR, whatever their latency. That matters for the planned
"lookup latency vs. network size" graph.

### What's the non-simplified way to find the k closest nodes in the routing table?

`RoutingTable.Closest` (`internal/kademlia/routingtable.go`) collects
every contact and sorts them all. The efficient way ranks *buckets* by
distance to the target first, and only sorts what it takes:

1. the target's own bucket;
2. then every bucket below it, merged;
3. then the buckets above it, in increasing order.

libtorrent's `find_node` does this, and the paper's bucket-splitting
tree changes how many contacts there are, not how to walk them. The
derivation, the cost comparison (`O(C log C)` over all `C` contacts vs.
roughly `O(k log k)`), and what splitting adds are in
[02, "The non-simplified way"](02-routing-table-and-kbuckets.md#the-non-simplified-way-visit-buckets-in-xor-order).

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

### Can I use a very small `k`, like 1 or 2?

You can, but lookups become unreliable even with nobody dead. With
k = 1, every reply carries one contact, so a joining node reaches only a
few others, and most of its buckets stay empty. A lookup that reaches a
node with an empty bucket in the target's direction stops there, at the
wrong node.
[EDGE-CASES.md, case A4](EDGE-CASES.md#a4--tiny-buckets) builds the same
20-node network three times: k = 1 got 9–14 of 20 lookups right, and
k = 4 got all 20. The paper's post-join bucket refresh, skipped here,
is what fixes it. The demos use k = 3 or more; A4 didn't test k = 3.

### Is it safe for many nodes to join at once?

Yes.
[EDGE-CASES.md, case A2](EDGE-CASES.md#a2--a-thundering-herd-of-joins)
starts 19 nodes joining through one node at the same instant, each
process built with the race detector. All joined, lookups were correct,
and no data race was reported. A node joining in that crowd may finish
knowing only 1–2 contacts, but the traffic that follows fills its table
in.

### What's an RPC ID vs. a node ID?

Both are 160-bit, but unrelated. The node ID identifies a node. The RPC
ID is a fresh random number per request, echoed back in the reply, so the
sender knows which request a reply answers ([03](03-message-framing.md)).

## The demo and lookups

### Why does every lookup in the demo query every node?

Lookup stops once the `k` closest nodes it has seen have all been
queried. With 5 nodes and `k = 20`, "the 20 closest" means everyone.
`O(log n)` behavior only shows once `n` is much larger than `k`. Use a
small `--k` or many nodes to see it. `scripts/demo-big-network.sh` does
both (20 nodes, `k = 4`): its lookups take 2–3 rounds and ask about 7 of
the 20 nodes ([DRY-RUN part 4](DRY-RUN.md#part-4--scriptsdemo-big-networksh-multi-round-lookups)).

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

It also doesn't get better with time. A timeout doesn't remove the dead
contact from the routing table, so the next lookup asks it again and
waits again. And the dead node keeps its place among the lookup's k
closest candidates, so the answer comes back with fewer than k nodes.
[EDGE-CASES.md, case B1](EDGE-CASES.md#b1--half-the-network-dies-at-once)
kills half of a 16-node network. The same lookups go from ~130 ms to
~10 s, stay there on a repeat, and every answer is short.

### How can a program (or the visual demo) see what a node knows?

Start the node with `--api 127.0.0.1:<port>`, and it serves its ID,
routing table, stored values and files as JSON over HTTP
([NODE-API.md](NODE-API.md)), plus `/api/events`, a live stream of
every RPC, stored value and received piece. It's off by default. With it
off, the node behaves exactly as before. With it on, the API only reads
copies through the node's public methods. The event stream is a tap on
the node's logger that never waits for a viewer, so it can't change or
slow the DHT.

Add `--api-control` and a program can also make the node act: POST
`lookup`, `ping`, `put`, `get`, `share` or `fetch`, the console's
commands. That's how the visual demo will let you click a node to make
it look something up. Leave it off when you don't need it: any web page
open in your browser could send those commands too.

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

## Several machines

### Can nodes run on different machines?

Yes, on one subnet (one router, hotspot or switch), with **one node per
device**. The DHT needed no change, because nodes record each other's
addresses from real packet sources. Only provider records carry an
address a node states about itself (`--advertise-ip`). The first node
must be given it, and nodes that join through another machine detect
it. The rules, the failure modes with real logs, and a lab that
simulates several machines on one laptop:
[09-multi-device.md](09-multi-device.md).

### My download fails with "connection refused" or "unknown content key" at 127.0.0.1. Why?

The provider's record says `127.0.0.1`, so your node dialed *its own*
machine: nothing listening means "connection refused", and your own file
server answering means "unknown content key". The provider was started
without `--advertise-ip`, usually because it was the first node, which has
nothing to detect its address from. It logs
`advertise ip is loopback but a remote peer contacted us` when the first
remote peer joins. Restart it with `--advertise-ip <its IPv4 address>`,
and restart every other node too: the old record never expires, and it
wins over the new one ([09](09-multi-device.md), F1 and F3).

### How do I test several machines on one laptop?

With Linux network namespaces: `sudo bash scripts/netns/up.sh 5` makes
five "machines", `10.10.0.1`–`10.10.0.5`, each with its own IP, its own
`127.0.0.1` and its own ports. That's enough to reproduce every
multi-device problem, including IP fragmentation, which localhost hides.
`tc netem` adds Wi-Fi-like delay and loss. On macOS, Docker Compose does
the same job. Real devices come last.
[09](09-multi-device.md#the-lab-several-machines-on-one-laptop).

### Can this run over the internet?

Not directly. Home routers use NAT: a device behind one has no address
that others can reach, so nobody can bootstrap through it or download
from it. An overlay VPN such as Tailscale or ZeroTier gives every device
a reachable address, and then it works like one subnet, with no code
change. Real NAT traversal (hole punching, relays) is a project of its
own.

## STORE / FIND_VALUE

### Where does a value actually live?

On the k nodes whose IDs are closest (by XOR) to the key — not on the
node that stored it, unless that node happens to be one of them. The
publisher and the searcher both run a lookup toward the key, so they end
up at the same nodes ([06](06-store-and-find-value.md)).

### Why did `get` say "from this node's own store"?

That node is itself one of the k closest to the key, so it holds a
copy. `FindValue` checks locally before asking anyone. With `k = 20` and
5 nodes every node holds every value, which is why
`scripts/demo-transfer.sh` uses `--k 3`.

### Can a key hold more than one value?

Yes, up to 16 distinct values of at most 256 bytes each. That's how
"every peer that has file X" fits under file X's key. The limits keep a
full `FIND_VALUE` reply inside one UDP datagram.

### What if the k nodes holding a value all die? Can I just store it again?

The value is gone: nothing republishes it. And on the current code,
storing it again from a node that had stored it before **doesn't work
either**. That node's routing table still lists the dead holders as its
closest contacts to the key. A lookup starts from only those k contacts,
they all time out, and the lookup ends with nothing. `Store` then keeps
the value only locally, where nobody else's lookup will look.
[EDGE-CASES.md, case B2](EDGE-CASES.md#b2--the-provider-record-holders-all-die)
shows this with real output and lists the fixes.

### A node joins with an ID closer to a key than the nodes holding it. Does it get the value?

No. A value stays on the nodes chosen when it was stored. Nothing copies
it to newcomers, because there's no republish and no hand-over on join
(the paper has both, Sec. 2.5). Worse, the newcomers can't *find* it.
Their lookups start from their closest contacts, which are other
newcomers, and with a small k they may never get past them to a holder.
[EDGE-CASES.md, case B3](EDGE-CASES.md#b3--late-joiners-closer-to-a-key)
shows 13 of 16 such lookups failing while the holders were alive, and a
re-`put` fixing it.

## File transfer (TCP)

### How do I know which key to ask for? If I want an Ace Attorney file, what do I query?

**You can't find it by name. Someone has to give you the key, usually
inside a magnet link.** Each file has exactly one key, its content key.
That key is the first 160 bits of SHA-256 over the file's manifest bytes
(`ContentKey` in `internal/transfer/manifest.go`). It's computed in
`Peer.Share` (`peer.go`) and printed by the console's `share` command,
on its own (`content key: …`) and as a magnet link
(`magnet link: magnet:?xt=urn:tp2p:<key>&dn=<name>`,
[10](10-magnet-links.md)). The manifest includes the file name, size, piece
size, and every piece hash. So the same bytes shared under a different
file name get a *different* key.

The DHT can answer "who stores values under key X?". It can't answer
"which keys exist?" or "which key is Ace Attorney?". Values live only on
the k nodes closest to their key, and nothing indexes keys. You get the
key out of band: a chat message, a web page, a QR code. Real BitTorrent
works the same way, and so do we: `fetch` accepts the key or the magnet
link carrying it. (The console's
`put`/`get` *is* a name → value store, with key = SHA-1(name) in
`consoleEnv.put`, `cmd/node/console.go`. But it stores text, not file
keys, and anyone can write to any name.)

**How real systems handle discovery:**

- **Magnet links (still out of band).**
  [BEP 9](https://www.bittorrent.org/beps/bep_0009.html):
  `magnet:?xt=urn:btih:<infohash>&dn=<display name>`, where `xt` is the
  only mandatory parameter. The link carries the key. The client finds
  peers through the DHT, fetches the metadata from them, and checks it
  hashes to the infohash. That's the same shape as our content key →
  `FIND_VALUE` → `GET_MANIFEST` → `DecodeManifest`. What makes the
  network trackerless is finding *peers*; finding *what to download*
  still happens outside it, usually on websites that list magnet links.
  **This project does the same**, with its own `urn:tp2p:` namespace
  because our key isn't a BitTorrent info hash ([10](10-magnet-links.md)).
- **Keyword search inside a DHT.** eMule's Kad network publishes two kinds
  of keys: a **source key** (hash of the file) pointing to peers that have
  it, and a **keyword key** per word of the file name, pointing to source
  keys. A search hashes a word and asks the nodes nearest that key. "Index
  pollution" (bogus keyword entries) is a well-documented weakness.
  Sources: academic descriptions, e.g.
  [this Eurecom paper](https://eurecom.fr/publication/2309/download/ce-steinmo-070924.pdf)
  and [this NCSU report](https://techrep.csc.ncsu.edu/2009/TR-2009-2.pdf).
  **(Not verified against eMule's source code.)**
- **Indexing by crawling.** [BEP 51](https://www.bittorrent.org/beps/bep_0051.html)
  (Draft) adds `sample_infohashes`, so an indexer can list the infohashes
  a DHT node stores, fetch their metadata, and build a search engine
  outside the DHT.
- **Names owned by a key.** [BEP 44](https://www.bittorrent.org/beps/bep_0044.html)
  "mutable items" are stored under SHA-1(publisher's ed25519 public key +
  optional salt). Each value is signed and carries an increasing sequence
  number. That gives a stable, tamper-evident "name" whose content only
  the key owner can change. You still have to learn the publisher's
  public key out of band.

**What a name → key layer could look like here (not implemented):**

1. On `share`, also publish an index record for each normalized word of
   the name. For example `Store(SHA-1("kw:" + word), record)`, where the
   record is a JSON `{content_key, name, size, publisher}`. It must stay
   under 256 bytes (`maxValueSize`), and at most 16 records fit per key
   (`maxValuesPerKey`, `internal/kademlia/store.go`).
2. `search ace attorney`: `FindValue` each word's key, intersect the
   records by `content_key`, and show the candidates. The user picks one,
   then `fetch`es its content key.

Its trust problems, which are the hard part:

- **Anyone can STORE anything under any key.** `Node.replyStore`
  (`node.go`) checks only the size; it's labeled SIMPLIFICATION. Fake
  "Ace Attorney" records pointing at junk are trivial to publish. Hash
  checks don't help here: they prove you got *the file the key names*,
  not that the key is the file you *wanted*.
- **Flooding.** With 16 records per key and oldest-dropped, a spammer can
  push real entries out. Common words overflow on their own.
- **Sybil/eclipse.** Choose node IDs right next to `SHA-1("kw:ace")`, and
  you *are* the k nodes that answer that search, free to censor or rewrite
  it. That's exactly what BEP 42 makes harder for infohashes.
- **Staleness.** Values never expire here, so records for deleted files
  stay forever.

Mitigations real systems use: signed records (BEP 44 style), trusted
publisher lists or a web of trust, ranking by independent confirmations
(cheap to fake with Sybils), and expiry with republishing. Each would be
its own step.

### Why doesn't a joining node get a list of the files already shared?

**Because no node has that list, and building one would undo
"trackerless".** The tempting design is "when I join, the bootstrap node
tells me which files exist". Here's why it doesn't work:

- **A share isn't broadcast.** `share` runs `Node.Store(key, record)`,
  which sends `STORE` to the k nodes closest to the *file's key*, and to
  no one else ([06](06-store-and-find-value.md)). With k = 3:
  `store complete … stored_at=2 stored_locally=true` → `replicas=3`. In
  `demo-big-network.sh` (20 nodes, k = 4), 16 nodes never hear that a
  file exists. The bootstrap node knows only the files whose keys happen
  to land near its ID.
- **If it did know, it would be a tracker.** If every node reported its
  shares to the bootstrap node, killing that node would kill discovery,
  which is the single point of failure this project exists to avoid. And
  any node can be someone's bootstrap node, so there's no one place to
  report to.
- **A list copied at join time goes stale** the moment the next file is
  shared.
- **Broadcasting every share to every node** would keep lists current,
  but it costs N messages per share, needs someone who knows every node
  (nobody does: a bucket holds at most k contacts), and loses shares sent
  while a node is down or joining. Raft's leader does send every entry to
  every follower, but a Raft cluster has about 5 servers that all need
  the whole log. A DHT gives each of many nodes a small slice.
- **Real trackerless BitTorrent doesn't do it either.**
  [BEP 5](https://www.bittorrent.org/beps/bep_0005.html) has no catalog. People find magnet links on websites and in chats.

What we built instead is magnet links ([10](10-magnet-links.md)): the key
travels outside the network, as in BitTorrent.

The one design that *would* work is a catalog under a well-known key,
for example each `share` also stores `{key, name, size}` under
`SHA-1("catalog")` and a `catalog` command runs `FindValue` on it. It
stays current because every query pulls the latest records. It's an
extension beyond BitTorrent (closer to eMule's Kad), with this project's
limits:
- at most 16 records per key (`maxValuesPerKey`), so only the 16 newest
  shares would be listed;
- `FindValue` stops at the first round with values, and nothing
  republishes, so a query may see only one holder's copy;
- anyone can post fake entries;
- entries never expire.

Like the keyword index in the previous question, it would be its own
step.

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
3. **Connect.** For each provider record,
   `net.Dialer.DialContext(ctx, "tcp4", ip:tcp_port)` (`dialProvider` in
   `internal/transfer/client.go`). The operating system does the TCP
   three-way handshake, and on the provider `Accept()` returns
   (`server.go`). On that connection we send length-prefixed requests
   (`GET_MANIFEST`, then `GET_PIECE i`) and get length-prefixed replies.

So Kademlia answers *"who has the file?"*, and TCP carries the file.
That's how BitTorrent uses its DHT too. Step by step with real logs:
[07](07-file-transfer-over-tcp.md) and [DRY-RUN.md](DRY-RUN.md) part 2.

### What exact attributes do we receive, and which ones do we use to connect?

| Source | Attributes | Used for |
|---|---|---|
| `FIND_NODE` / lookup result (`Contact`) | node ID, IP, **UDP** port | routing: who to ask next |
| `FIND_VALUE` result (`ProviderRecord`, a stored value) | `node_id`, `ip`, **`tcp_port`** | connecting to download |

A real provider record from the demo (87 bytes of JSON, stored as an
opaque DHT value):

```json
{"node_id":"10f71961bd11dd33c1c95c771b98cf0e09d57b7c","ip":"127.0.0.1","tcp_port":9001}
```

The record carries its own TCP port, so a node can serve files on a
different port from its DHT socket. TCP and UDP port numbers are separate
spaces, so by default the CLI uses the same number for both. The `ip`
comes from the provider's `--advertise-ip`.

### Why not take the TCP address from the connection itself?

A provider sees a downloader's connection coming from an OS-chosen
temporary port (e.g. `127.0.0.1:57976`), not the downloader's own server
port. You can only learn where a peer *listens* by being told. That's
why the record states `tcp_port` explicitly.

### What happens if a provider is dead or lying?

- **Dead** (process gone): `connect: connection refused` (instant on
  localhost), or a dial timeout on a real network. The downloader marks
  it dead and retries the same piece on the next provider. The demo's
  part 3 shows exactly this after killing the seeder.
- **Lying** (wrong manifest or corrupted piece): the hash check fails, and
  the provider is treated as dead. `TestCorruptProviderIsSkipped`.

A dead provider's record stays in the DHT (no expiry yet), so every
downloader pays one failed connection for it.

### Why not send the file itself through STORE / FIND_VALUE?

DHT messages are single UDP datagrams (8 KB max here). UDP has no
retransmission or flow control. And storing whole files on k random
nodes would make them carry data they never asked for. So the DHT holds
only small pointers, and bulk data goes over TCP directly between the
peers that want it.

### How do we know a piece from a stranger isn't corrupted or fake?

The content key is a hash of the file's manifest, which includes a hash
of every piece. A downloader checks `hash(manifest) == key`
(`DecodeManifest`), then `SHA-256(piece) == manifest.piece_hashes[i]`
for each piece (`VerifyPiece`). A peer can't send a fake piece without
breaking a hash.

### How does a leecher know what order the pieces go in when it rebuilds the file?

From the **piece index**, never from arrival order. That matters on this
branch, where pieces routinely arrive out of order. The path:

- `PieceHashes[i]` in the manifest is the hash of the file's `i`-th chunk
  (`BuildManifest`).
- The picker chooses an index (`pickPiece`, `picker.go`), and
  `swarmDownload.startPiece` (`download.go`) asks a provider for that
  index (`GET_PIECE`, via `providerConn.fetch` in `client.go`).
- The provider reads bytes `[i·PieceSize, …)` (`Library.readPiece`), but
  only if piece `i`'s bit is set.
- The worker checks the bytes against `PieceHashes[i]`
  (`Manifest.VerifyPiece`). `swarmDownload.handle` writes them at
  `PieceOffset(i)` (`file.WriteAt`), then publishes the piece
  (`Library.markHave`).

The step-by-step path is in
[07, "How the downloader knows where each piece goes"](07-file-transfer-over-tcp.md#how-the-downloader-knows-where-each-piece-goes).

### Can this network handle pieces arriving out of order?

**Yes, and on this branch it does so routinely.** The downloader picks
pieces rarest-first with random tie-breaks and keeps up to
`--max-inflight` (default 4) requests in flight on different
connections. Pieces arrive in whatever order providers answer, e.g.
`2, 11, 6, 15, 10, 8, …` in DRY-RUN part 5's parallel demo. Placement,
verification, and serving are all per index, so order doesn't matter.

What still constrains it: **one outstanding request per connection**,
because replies don't repeat the index, so there's no pipelining. On
`main` the downloader requested `0, 1, 2, …` strictly one at a time,
although the format already allowed any order. Details:
[07, "Can pieces arrive out of order?"](07-file-transfer-over-tcp.md#can-pieces-arrive-out-of-order).

### Why SHA-1 for node IDs but SHA-256 for files?

Kademlia IDs and keys are 160 bits, SHA-1's output size, so node IDs use
SHA-1 as the paper suggests. A node ID needs no collision resistance;
it just has to be spread out. File integrity does: the content key is
the root of the trust chain, and SHA-1 has practical collision attacks.
So file keys are **SHA-256 truncated to 160 bits** (BitTorrent v2 does
the same) and piece hashes are full SHA-256.

### Is our content key an info hash? Why do our magnet links say `urn:tp2p`, not `urn:btih`?

**It's an info hash, but not BitTorrent's.** BitTorrent's info hash isn't
a separate value. It's *defined* as the hash of the file's description,
the `info` dictionary (name, piece size, length, piece hashes). Our
`Manifest` holds the same four things, and the content key is its hash:

| | Description | Key |
|---|---|---|
| BitTorrent v1 | `info` dictionary, bencoded | SHA-1 → 160 bits |
| BitTorrent v2 ([BEP 52](https://www.bittorrent.org/beps/bep_0052.html)) | `info` dictionary, bencoded | SHA-256, cut to 160 bits for the DHT |
| Ours | `Manifest`, JSON | SHA-256, cut to 160 bits (`ContentKey`) |

Hashing the description, not the whole file, is what lets you check each
piece as it arrives: key → manifest → piece hashes
([07](07-file-transfer-over-tcp.md#the-integrity-chain)).

`btih` means "BitTorrent info hash", so a link labelled `btih` would send
a real BitTorrent client searching for a torrent that doesn't exist.
Computing a real BitTorrent info hash (bencode + SHA-1) wouldn't make us
compatible either, because every message on our wire differs (next
section). So our links use their own namespace, `urn:tp2p` (Trackerless
P2P), and `fetch` refuses `btih` links. [10](10-magnet-links.md) works
through a real manifest and its key byte by byte.

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

## Compared with BitTorrent / libtorrent

### Is our wire protocol the same as libtorrent's? Could it be made the same easily?

**No, it isn't the same.** The concepts line up closely, but every byte
on the wire differs. libtorrent implements the BitTorrent BEPs: BEP 3
(peer protocol), BEP 5 (DHT), BEP 9/10 (magnet metadata), BEP 44, BEP 51,
BEP 52, and others (per
[Wikipedia's libtorrent BEP table](https://en.wikipedia.org/wiki/Libtorrent)).
The two layers compare separately.

**DHT over UDP** — ours (`internal/kademlia`) vs.
[BEP 5](https://www.bittorrent.org/beps/bep_0005.html):

| | Ours | BEP 5 (Mainline DHT) |
|---|---|---|
| Encoding | JSON envelope inside a `[4-byte length]` frame | bencoded dictionary as the raw UDP payload, no length prefix |
| Request/reply matching | `rpc_id`: 160-bit random | `t`: short transaction string (typically 2 bytes) |
| Message kinds | `PING`, `FIND_NODE`, `STORE`, `FIND_VALUE` and `_RESP` types | `y` = `q`/`r`/`e`; queries `ping`, `find_node`, `get_peers`, `announce_peer` |
| Contacts in replies | JSON `{id, ip, port}` | "compact node info": 26 bytes (20-byte ID + 4-byte IPv4 + 2-byte port) |
| `k` | 20 | 8 |
| Storing "I have X" | `STORE(key, any value ≤ 256 B)`; the IP comes from `--advertise-ip` | `announce_peer(info_hash, port, token)`: the storing node records the sender's **source IP**; it needs a `token` from an earlier `get_peers` |
| Finding | `FIND_VALUE` → values or contacts | `get_peers` → `values` (6-byte compact peers) or `nodes`, plus a `token` |
| Errors | `STORE_RESP {stored:false, reason}` only | `e` = `[code, message]`, codes 201–204 |
| Routing table | flat 160 buckets, no refresh | splitting buckets, refresh after 15 min idle, good/questionable/bad nodes |

Conceptually `FIND_VALUE` ≈ `get_peers`, and `STORE` of a provider
record ≈ `announce_peer`. The routing table, bucket rule, and iterative
lookup carry over almost unchanged.

**Piece transfer over TCP** — ours (`internal/transfer`) vs.
[BEP 3](https://www.bittorrent.org/beps/bep_0003.html):

| | Ours | BEP 3 peer wire protocol |
|---|---|---|
| Start of a connection | no handshake | 68-byte handshake: `19`, `"BitTorrent protocol"`, 8 reserved bytes, 20-byte infohash, 20-byte peer ID |
| Framing | `[4-byte length][JSON request]`; reply = header frame + raw data frame | `[4-byte length][1-byte message ID][payload]`; length 0 = keep-alive |
| Unit of transfer | whole 64 KiB pieces | 16 KiB **blocks** (`request index, begin, length`) inside pieces (256 KiB typical) |
| Flow control | none | `choke`/`unchoke`, `interested`/`not interested` |
| Who has what | nothing on `main` (whole files only) | `bitfield` (high bit of byte 0 = piece 0) + `have` messages |
| Pipelining | one request outstanding per connection | many outstanding requests per connection |
| File description | manifest = JSON; key = SHA-256(manifest)[:20] | `info` dictionary, bencoded; v1 infohash = SHA-1(info); piece hashes = 20-byte SHA-1s concatenated |
| Getting the description from peers | `GET_MANIFEST` | `ut_metadata` ([BEP 9](https://www.bittorrent.org/beps/bep_0009.html)) over the BEP 10 extension protocol, in 16 KiB blocks |
| Handing someone a file | `magnet:?xt=urn:tp2p:<key>&dn=<name>` ([10](10-magnet-links.md)) | `magnet:?xt=urn:btih:<infohash>&dn=<name>` (BEP 9), optionally `tr=` trackers and `x.pe=` peers |

One genuine match: both frame messages with a 4-byte big-endian length
prefix. After BitTorrent's fixed-size handshake (which isn't framed),
`framing.Read` would split a BitTorrent byte stream into the right
messages. Only what's inside them differs.

**Rough effort to become compatible.** This is our own estimate, not
measured, for one student who already knows this codebase:

- **DHT (to join the real Mainline DHT):** a bencode encoder/decoder (a
  few hundred lines plus tests), the KRPC message mapping, compact
  node/peer encodings, token generation and checking, `get_peers` /
  `announce_peer` in place of `STORE`/`FIND_VALUE` for peer lists, error
  replies, `k = 8`. Then testing against real nodes. Routing table and
  lookup stay. **About 1–2 weeks.**
- **Peer wire protocol (to swap pieces with real clients):** handshake,
  the message codec, a minimal choke/interest state machine, bitfield and
  `have`, block-level requests reassembled into pieces, pipelining,
  bencoded `info` dictionaries with SHA-1 piece hashes, and BEP 9/10 for
  magnet metadata. **About 2–4 weeks.**

The protocols are well documented. Most of the time goes to edge cases
real clients exercise. Not implemented, and out of scope for now.
