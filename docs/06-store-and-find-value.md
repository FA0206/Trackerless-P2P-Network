# 6. STORE and FIND_VALUE: the DHT as a key-value store

Prerequisite: [05-iterative-parallel-lookup.md](05-iterative-parallel-lookup.md).
Everything here is "a lookup, plus one more step".

## What it is

So far the network can only answer *"which nodes are closest to this
ID?"*. `STORE` and `FIND_VALUE` (paper Sec. 2.3) turn that into a
distributed hash table: any node can put a `(key, value)` pair into the
network, and any other node can get it back by key, with no node in
charge.

The rule that makes it work: **a value lives on the k nodes whose IDs are
closest to its key.** Keys and node IDs are both 160-bit, so "closest"
is the same XOR distance as before
([01](01-ids-and-xor-distance.md)).

- **Store** = look up the k closest nodes to the key, then send each of
  them `STORE(key, value)`.
- **Find** = run the same lookup toward the key, asking `FIND_VALUE`
  instead of `FIND_NODE`, and stop as soon as someone has the value.

Publisher and searcher never coordinate. They run the same lookup toward
the same key, so they arrive at the same neighborhood of the ID space.

## Why it needs nothing more than the lookup

`FIND_VALUE` behaves exactly like `FIND_NODE` (paper Sec. 2.3), with one
exception: a node that holds values for the key returns **the values**
instead of contacts. So the searcher's lookup either:

- reaches a node holding the value → done, early; or
- converges on the k closest nodes without finding anything → the key
  isn't stored (the result has no values, but still lists the closest
  nodes).

That's why the code needs no new search algorithm. `Lookup` and
`FindValue` share one function, `iterativeFind(ctx, target, findValue
bool)` in [`lookup.go`](../internal/kademlia/lookup.go). The only
differences: which RPC each round sends, and "stop once values arrive".

## Seen in the demo

`scripts/demo-transfer.sh` part 1 runs with `k = 3`, so each value lives
on only 3 of the 5 nodes:

```
[node0] > put greeting hello from node0
    put greeting (key a0f7e779…): stored on 2 other node(s), locally=true, failed=0
        db612776…@127.0.0.1:9002
        94fab99d…@127.0.0.1:9003
[node4] > get greeting
    get greeting: 1 value(s) via network: rounds=1 rpcs_sent=3 rpcs_failed=0
        "hello from node0"
```

- `put` turned the name into a key (`SHA-1("greeting") = a0f7e779…`) and
  looked up its 3 closest nodes: node2, node3, and node0 itself.
  Hence "2 other nodes, locally=true".
- node4 isn't one of them. Its `get` sent `FIND_VALUE` to its 3 closest
  known nodes (node1, node3, node0). node0 and node3 answered with the
  value, node1 with contacts, and the lookup stopped after one round.
  Both holders returned the same bytes, so `1 value(s)` after
  de-duplication.

Full trace: [DRY-RUN.md](DRY-RUN.md), part 2.

## Design decisions in this implementation

### Store locally if we're one of the k closest

A lookup never returns the searching node itself (invariant 3 in
[ARCHITECTURE.md](ARCHITECTURE.md)). So `Store` checks separately whether
this node would rank among the k closest (`selfAmongClosest` in
[`values.go`](../internal/kademlia/values.go)). If so, it keeps a copy
and sends one fewer `STORE`, keeping the total at k. `FindValue` likewise
checks the local store before going to the network.

### A key holds several values

The paper describes one value per key. File sharing needs many: "node A
has file X", "node B has file X" … all under file X's key. So the local
store ([`store.go`](../internal/kademlia/store.go)) keeps a list of
distinct values per key. Storing a new value adds it, and storing an
identical value again just refreshes it. BitTorrent's DHT does the same
with its per-file peer lists.

### Values are small, on purpose

A `FIND_VALUE` reply must fit in one UDP datagram (8 KB here,
[03](03-message-framing.md)). Values are base64 inside JSON, which grows
them by 4/3. So a value is capped at **256 bytes**, with at most **16 per
key**: worst case 16 × 256 × 4/3 ≈ 5.5 KB.
`TestLargestFindValueReplyFitsInDatagram` checks that arithmetic against
the real encoder. Large data never travels through the DHT; it goes over
TCP ([07](07-file-transfer-over-tcp.md)).

### STORE gets a reply

The paper doesn't require one. Here `STORE_RESP {stored: true}` tells the
publisher how many nodes actually kept the value (`stored on 2 other
node(s)`). A node refuses oversize values with a reason.

### Values are opaque bytes

The DHT doesn't care what a value means. `put` stores text, and file
sharing stores small JSON provider records. They're `[]byte` in Go and
base64 on the wire, because JSON strings can't carry arbitrary bytes
intact.

## SIMPLIFICATIONS (and whether they block the end goal)

| Paper feature | Here | Effect |
|---|---|---|
| Republish every hour; expire after 24 h (Sec. 2.3, 2.5) | Values never expire, and nobody republishes | Records of dead providers linger (a downloader wastes one failed connection on them). Values don't move to closer nodes that join later. **Fix before churn experiments.** |
| Cache the value at the closest node that didn't have it (Sec. 2.3) | No caching | Popular keys put all load on their k holders. Fine at this scale. |
| Stop the instant one reply contains the value | Stops after the round that found it (rounds are synchronous, [05](05-iterative-parallel-lookup.md)) | A dead node in that round still costs a full RPC timeout. The demo shows this: a 2 s stall in part 3. |
| — | Anyone can STORE anything under any key | Fine for a course demo. Consumers must validate values (the file layer does). |

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| Wire messages | `StoreRequest/Response`, `FindValueRequest/Response` | `messages.go` |
| Answering STORE / FIND_VALUE | `Node.replyStore`, `Node.replyFindValue` | `node.go` |
| Sending one STORE / FIND_VALUE | `Node.storeRPC`, `Node.findValueRPC` | `node.go` |
| Publishing to the k closest | `Node.Store`, `selfAmongClosest` | `values.go` |
| Finding a value | `Node.FindValue` → `iterativeFind(…, true)` | `values.go`, `lookup.go` |
| Local storage, limits | `valueStore`, `maxValueSize`, `maxValuesPerKey` | `store.go` |

Tests: `store_test.go` (distinct values per key, cap, copies, datagram
fit) and `values_test.go`. The latter checks over real UDP, with 8 nodes
and `k = 3`, that a stored value lands on **exactly** the 3 nodes a
brute-force sort says are closest, and that every node can find it, some
via the network. It also covers several publishers under one key, a
missing key, and an oversize value.

## What's next

[07-file-transfer-over-tcp.md](07-file-transfer-over-tcp.md): using this
to find who has a file, then fetching it over TCP.
