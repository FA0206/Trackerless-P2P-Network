# 3. Messages and framing over UDP

Prerequisite: [02-routing-table-and-kbuckets.md](02-routing-table-and-kbuckets.md).

## What it is

Kademlia is defined in terms of four RPCs (paper Sec. 2.3): `PING`,
`STORE`, `FIND_NODE`, `FIND_VALUE`. This pass implements only `PING` and
`FIND_NODE` (plus their replies) — see the top-level README's status
table, and the [FAQ](FAQ.md) for how `STORE`/`FIND_VALUE` will be used.

Every message, in both directions, is wrapped in an **envelope**
(`Envelope` in
[`internal/kademlia/messages.go`](../internal/kademlia/messages.go)):

```go
type Envelope struct {
    Type     MessageType     // "PING", "PONG", "FIND_NODE", "FIND_NODE_RESP"
    RPCID    ID              // random per-call ID; echoed back in the reply
    SenderID ID              // the sender's own node ID
    Payload  json.RawMessage // nil for PING/PONG; a request/response struct otherwise
}
```

- **`SenderID`** is what lets every message — not just replies to our own
  queries — update the recipient's routing table (paper Sec. 2.2: "every
  message a node transmits includes its node ID"). See
  `Node.handleMessage` in [`node.go`](../internal/kademlia/node.go), which
  calls `touchContact` on *every* incoming envelope before even looking at
  its type.
- **`RPCID`** is how we match a reply to the request that caused it (paper
  Sec. 2.3: "the recipient must echo a 160-bit random RPC ID"). See
  `Node.sendAndWait`, which registers a channel keyed by the RPC ID before
  sending, and `Node.deliverResponse`, which looks up that channel when a
  `PONG`/`FIND_NODE_RESP` arrives. The paper also notes this "provides
  some resistance to address forgery" — an attacker who can't see the
  outgoing request can't guess its RPC ID and inject a fake reply.

The whole thing (envelope + payload) is JSON. JSON was chosen over a
binary format for this pass purely for *readability while debugging* — you
can read a captured packet or a decode error directly — at the cost of
being larger on the wire and slower to parse than a binary encoding would
be. At this project's scale, that trade is the right one; it's not a
trade you'd keep making for a protocol meant to handle high query volume.

## Framing: why a length prefix, over UDP specifically

Each packet we actually put on the wire looks like:

```
[4-byte big-endian length][JSON-encoded Envelope]
```

See `Transport.Send` / `decodeEnvelope` in
[`internal/kademlia/transport.go`](../internal/kademlia/transport.go).
The frame format itself lives in its own tiny package,
[`internal/framing`](../internal/framing/framing.go). The TCP file
transfer ([07](07-file-transfer-over-tcp.md)) uses the same package.

Here's the nuance worth understanding, rather than taking on faith: **a
single UDP datagram is already one atomic unit as far as the two ends are
concerned.** Unlike a TCP connection, which is just a continuous byte
stream with no message boundaries of its own (if you write "AB" then "CD",
the reader might see "AB", "CD", "ABCD", or even "A" then "BCD" — TCP makes
no promises), one `sendto()` on a UDP socket corresponds to exactly one
`recvfrom()` on the other end, containing exactly those bytes, never split
across reads and never glued to another datagram. So, strictly speaking,
**UDP does not require a length prefix to find message boundaries** the
way a TCP-based protocol would.

So why does this project use one anyway? Two concrete reasons, not "because
the paper says so" (it doesn't specify a wire format at all):

1. **A cheap sanity check against garbage.** If a packet arrives that
   isn't one of ours — truncated by some middlebox, corrupted, or just
   a stray UDP packet from something else entirely hitting this port —
   the length prefix lets us catch that *before* trying to run a JSON
   parser over arbitrary bytes. `decodeEnvelope` checks that the declared
   length matches what actually arrived before attempting to unmarshal,
   and drops (logs and ignores) anything that doesn't. Without this,
   `json.Unmarshal` on the raw datagram would do the same validation
   anyway via decode errors — the prefix doesn't add safety the JSON
   decoder doesn't already give us, honestly, but it does mean malformed
   data is rejected by a one-line integer comparison instead of partway
   through JSON parsing, which is a clearer failure mode to reason about
   and log.
2. **One format for both transports.** File pieces travel over TCP
   ([07](07-file-transfer-over-tcp.md)), and on TCP framing is not
   optional: TCP has no message boundaries, full stop. Both sides use the
   same `[length][body]` frames from `internal/framing`. `Encode`/`Decode`
   handle one frame per datagram (UDP); `Write`/`Read` handle frames on a
   stream (TCP), where `Read` reads exactly 4 bytes, then exactly that many
   more.

If you take one thing from this: don't walk away thinking "UDP has no
message boundaries" — it does, per-datagram. What UDP *doesn't* have is
reliability, ordering between datagrams, or a size limit high enough to
ignore (packets over the path MTU get fragmented or dropped) — none of
which a length prefix fixes, and none of which this project currently
handles beyond a generous `maxDatagramSize` constant and a bounded
in-memory read buffer in `Transport.Listen`.

## RPC request/reply shapes

```go
type FindNodeRequest struct {
    Target ID
}

type FindNodeResponse struct {
    Contacts []Contact // up to k contacts the responder knows of, closest to Target
}
```

`PING`/`PONG` carry no payload at all — the envelope's `Type` and `RPCID`
are the entire message. `STORE` and `FIND_VALUE` and their replies are
covered in [06](06-store-and-find-value.md).

## Where this lives in the code

| Concept | Function / Type | File |
|---|---|---|
| Message envelope, RPC payload shapes | `Envelope`, `FindNodeRequest`, `FindNodeResponse`, … | `messages.go` |
| Frame format, for datagrams and streams | `Encode`, `Decode`, `Write`, `Read` | `internal/framing/framing.go` |
| UDP send/receive | `Transport.Send`, `Transport.Listen`, `decodeEnvelope` | `transport.go` |
| Matching replies to requests, routing-table updates on receipt | `Node.sendAndWait`, `Node.deliverResponse`, `Node.handleMessage` | `node.go` |

Tests: `internal/framing/framing_test.go` (round trips, a mismatched
length prefix, frames back to back on a stream, oversize and truncated
frames). The UDP path is also exercised end to end by
`internal/kademlia/integration_test.go` over real loopback sockets.

## What's next

[04-bootstrap-and-join.md](04-bootstrap-and-join.md): how a brand-new node,
knowing only one existing contact, uses `FIND_NODE` to discover the rest
of the network and populate its own routing table.
