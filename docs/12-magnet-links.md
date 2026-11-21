# 12. Magnet links: handing someone a file

Prerequisite: [07-file-transfer-over-tcp.md](07-file-transfer-over-tcp.md)
(manifests and content keys). It helps to have read
[06-store-and-find-value.md](06-store-and-find-value.md) too: it explains
why the DHT can't list its files.

After `share`, anyone who wants the file needs its 40-hex content key.
The DHT answers "who has key X?", but it can't answer "which keys
exist?". So the key travels outside the network: in a chat message, on a
web page, as a QR code. A **magnet link** is that key packaged for
people, the same way real trackerless BitTorrent does it:

```
magnet:?xt=urn:tp2p:637e2199d6798bd6c983adb58129deb1dfb32242&dn=holiday.bin
```

`share` prints one, `files` lists one per file, and `fetch` accepts one
wherever it accepted a key.

## What real BitTorrent does (and doesn't)

- **The DHT finds peers, not files.** The Mainline DHT
  ([BEP 5](https://www.bittorrent.org/beps/bep_0005.html)) has
  `announce_peer` ("I have info hash X", our provider record) and
  `get_peers` ("who has X?", our `FindValue`). Nothing in it lists the
  files that exist.
- **People find files outside the network**, on websites, forums and
  chats that post magnet links
  ([BEP 9](https://www.bittorrent.org/beps/bep_0009.html)):
  `magnet:?xt=urn:btih:<info hash>&dn=<name>`.
- So "trackerless" means **no tracker for finding peers**. It doesn't
  mean "no website for finding files".

This project does the same. The other designs we considered (a list from
the bootstrap node, broadcasting every share, a catalog stored in the
DHT) are under [alternatives we rejected](#alternatives-we-rejected).

**Analogy:** a magnet link is a library call number with a sticky note
attached. The call number (`xt`) decides which book you get. The sticky
note (`dn`) is someone's handwriting: useful, but not checked.

## The link, field by field

```
magnet:?xt=urn:tp2p:637e2199d6798bd6c983adb58129deb1dfb32242&dn=holiday.bin
└─────┘ └┘ └──────┘ └──────────────────────────────────────┘ └┘ └─────────┘
scheme  │   namespace        the content key (160 bits)       │  file name, URL-escaped
        xt: "exact topic"                                     dn: "display name"
```

| Part | Meaning | Checked? |
|---|---|---|
| `magnet:?` | "the rest is a list of `name=value` parameters" | the scheme must be `magnet` (any case) |
| `xt=urn:tp2p:<40 hex>` | the content key: `ContentKey` of the file's manifest | yes: exactly one `xt`, our namespace, 40 hex characters. Then the download checks the manifest against it ([07](07-file-transfer-over-tcp.md#the-integrity-chain)) |
| `dn=<name>` | the manifest's file name, for people to read | **no**: optional, never trusted, never used as a path |

**Escaping.** `MagnetLink` escapes the name with `url.QueryEscape`: a
space becomes `+`, `&` becomes `%26`, `é` becomes `%C3%A9`. So a link
never contains whitespace, and the console, which splits a line on
whitespace, always sees it as one argument.

**Parsing** (`ParseMagnet`) follows BEP 9 practice:
- Parameters it doesn't know (`tr`, `x.pe`, …) are ignored, as real
  clients do.
- The order of the parameters doesn't matter.
- `dn` may be missing.
- Anything else is refused with a message naming the problem: a missing
  `xt`, two `xt`s, a different namespace, or a bad key.

What we leave out of BitTorrent's link: `tr` (a tracker URL: we have
none), `x.pe` (a peer address to try first: the DHT finds peers), and
`xl` (exact length: the manifest has the size).

## Why `tp2p` and not `btih`

`btih` means "BitTorrent info hash". Our content key *is* an info hash,
because it is a hash of the file's description. It just isn't
BitTorrent's:

| | The file's description | Key = hash of the description |
|---|---|---|
| BitTorrent v1 | the `info` dictionary, bencoded | SHA-1 → 160 bits |
| BitTorrent v2 ([BEP 52](https://www.bittorrent.org/beps/bep_0052.html)) | the `info` dictionary, bencoded | SHA-256, cut to 160 bits for the DHT |
| Ours | `Manifest`, as JSON (`EncodeManifest`) | SHA-256, cut to 160 bits (`ContentKey`) |

Here is a worked example. A 150 000-byte `notes.txt` (the lines
`line 1 of the notes`, `line 2 of the notes`, …, cut at 150 000 bytes)
gets 3 pieces (65 536 + 65 536 + 18 928 bytes). Its manifest, as the
exact 134 bytes that get hashed, carries the root of the Merkle tree over
those pieces ([11](11-merkle-trees.md)):

```
{"name":"notes.txt","size":150000,"piece_size":65536,"merkle_root":"1ceb2510a315aef1b466be4ae370846a445d45d374fe834b7ce738e4e8ad9694"}
```

```
SHA-256(manifest) = 2217015135a0bf9f2b8af56b5faa7608e5e515c78b7ef0d5a98513806c912634   (64 hex = 256 bits)
                    └──────────── kept: 160 bits ─────────────┘└─ dropped: 96 bits ──┘
content key       = 2217015135a0bf9f2b8af56b5faa7608e5e515c7
```

A real node sharing that file printed the same key, which confirms the
hand computation (`sha256sum` over the line above):

```
msg="share complete" key=2217015135a0bf9f2b8af56b5faa7608e5e515c7 name=notes.txt size=150000 pieces=3 merkle_root=1ceb2510a315aef1b466be4ae370846a445d45d374fe834b7ce738e4e8ad9694 manifest_bytes=134
shared …/notes.txt
  content key: 2217015135a0bf9f2b8af56b5faa7608e5e515c7
  magnet link: magnet:?xt=urn:tp2p:2217015135a0bf9f2b8af56b5faa7608e5e515c7&dn=notes.txt
```

(Before the Merkle tree, the manifest listed all three piece hashes, and
the same file had another key.)

A link labelled `btih` would tell a real BitTorrent client to search
BitTorrent's DHT for a torrent that doesn't exist. Computing a real
BitTorrent info hash (bencode + SHA-1) wouldn't help either: every
message on our wire differs from BitTorrent's
([FAQ](FAQ.md#is-our-wire-protocol-the-same-as-libtorrents-could-it-be-made-the-same-easily)),
and SHA-1 has practical collision attacks
([FAQ](FAQ.md#why-sha-1-for-node-ids-but-sha-256-for-files)). For the
same reason, a `btih` link given to `fetch` is refused, even when its 40
hex characters equal one of our keys.

## What `fetch` does with a link

Parsing only. A link adds **no network messages**: it's turned into the
key locally, and the download is exactly the one in
[07](07-file-transfer-over-tcp.md#downloading-a-file-fetch-keymagnet-out-path).

```
console.go   consoleEnv.fetch
magnet.go      ParseKeyOrMagnet    starts with "magnet:"? → ParseMagnet, else IDFromHex
magnet.go        ParseMagnet       url.Parse → scheme "magnet" → url.ParseQuery → exactly one xt
                                   → "urn:tp2p:" prefix → kademlia.IDFromHex(40 hex) → key (dn returned, unused)
peer.go        Peer.Download(key, out-path)    FindValue → providers → manifest → pieces (doc 07, 08)
```

The HTTP API does the same: `POST /api/fetch` passes its `key` field
through `ParseKeyOrMagnet` ([NODE-API.md](NODE-API.md#post-apishare-and-post-apifetch)).

## A link can lie about the name, never about the bytes

The trust chain from [07](07-file-transfer-over-tcp.md#the-integrity-chain)
starts at the key, and `dn` isn't part of it:

1. You trust the key because you chose the link.
2. A manifest from a stranger is accepted only if it hashes to the key.
3. A piece is accepted only if its SHA-256 matches the manifest.

So a doctored link can't deliver the wrong bytes for its key. It can
only mislabel them. For that reason:

- **`dn` is never a path.** A link saying `dn=../../.bashrc` must not
  decide where a file is written, so `fetch` still requires an out-path.
  The manifest's `Name` follows the same rule.
- **`dn` isn't compared with anything.** The file is named by its
  manifest. In the demo, a link saying `dn=lies.txt` downloads
  `holiday.bin`.
- What a link *can* do is point you at the wrong key, for example a
  "holiday photos" link that names someone else's file. Hashes can't help
  here: they prove you got the file the key names, not that the key is
  the file you wanted. Trusting the person who sent the link is outside
  the protocol, as it is in BitTorrent.

## Alternatives we rejected

These are the obvious ways to let a new node see which files exist,
and why none of them was built.

**1. The bootstrap node hands a joiner its list of files.**
- The bootstrap node becomes a tracker. Kill it, and discovery is gone.
- It doesn't even know most files. A `share` sends its provider record to
  the k nodes closest to the *file's key*, not to every node and not to
  the bootstrap node. Here, with k = 3, node1's share reached exactly 3
  nodes, itself included:

  ```
  [node1] msg="store complete" key=637e2199… stored_at=2 stored_locally=true failed=0
  [node1] msg="provider announced" key=637e2199… tcp_addr=127.0.0.1:9601 replicas=3
  ```

  In `demo-big-network.sh` (20 nodes, k = 4), each share reaches 4 nodes
  and the other 16 learn nothing.
- Any node can be a bootstrap node, so "the bootstrap node" isn't one
  place.
- A list copied at join time misses every file shared afterwards.

**2. Broadcast every share to every node.** That keeps a list current,
but it costs N messages per share. It also needs someone who knows every
node, and in Kademlia nobody does: a bucket holds at most k contacts. A
node that is down or still joining misses the broadcast. Compare Raft,
where the leader does send every entry to every follower: that works for
about 5 servers that all need the whole log. A DHT is built for the
opposite case, with many nodes each holding a small slice.

**3. A catalog under a well-known key**, say `SHA-1("catalog")`: each
`share` stores a small `{key, name, size}` record there, and a `catalog`
command runs `FindValue` on it. This one would work. It stays current
because each query pulls the latest records. But it isn't BitTorrent
(it's closer to eMule's Kad keyword index), and it has limits:
- A key holds at most 16 values (`maxValuesPerKey`), so only the 16
  most recent shares would be listed.
- `FindValue` stops at the first round that finds values, and values
  never move to closer nodes that join later. So a query may see one
  holder's copy of the list, not all of them.
- Anyone can post fake entries.
- Entries never expire.

[FAQ](FAQ.md#why-doesnt-a-joining-node-get-a-list-of-the-files-already-shared)
has the longer discussion, and
[FAQ](FAQ.md#how-do-i-know-which-key-to-ask-for-if-i-want-an-ace-attorney-file-what-do-i-query)
covers keyword search.

## Seen in the demo

`bash scripts/demo-magnet.sh`: 3 nodes, k = 3. node1 shares a random
200 000-byte file, node2 fetches it with the link, and node0 fetches it
with a copy of the link whose `dn` lies. Then node2 is handed a
BitTorrent link carrying the same 40 hex characters.

```
=== node1 shares a file ===
  [node1] > share /tmp/tmp.KwtOD0BkDi/holiday.bin
    content key: 637e2199d6798bd6c983adb58129deb1dfb32242
    magnet link: magnet:?xt=urn:tp2p:637e2199d6798bd6c983adb58129deb1dfb32242&dn=holiday.bin

=== node2 fetches with the link ===
  [node2] > fetch magnet:?xt=urn:tp2p:637e2199d6798bd6c983adb58129deb1dfb32242&dn=holiday.bin /tmp/tmp.KwtOD0BkDi/node2.bin
    fetched "holiday.bin" (200000 bytes, 4 pieces) in 5ms

=== node0 fetches with a link whose name lies ===
  [node0] > fetch magnet:?xt=urn:tp2p:637e2199d6798bd6c983adb58129deb1dfb32242&dn=lies.txt /tmp/tmp.KwtOD0BkDi/node0.bin
    fetched "holiday.bin" (200000 bytes, 4 pieces) in 3ms

=== node2 is handed a BitTorrent link with the same 40 hex characters ===
  [node2] > fetch magnet:?xt=urn:btih:637e2199d6798bd6c983adb58129deb1dfb32242&dn=holiday.bin /tmp/tmp.KwtOD0BkDi/node2-btih.bin
    transfer: this is a BitTorrent magnet link (xt=urn:btih:...): it names a torrent in the BitTorrent network, not a file in this one, whose links use xt=urn:tp2p:<key>

PASS: both links delivered identical copies, the lying name was ignored (the manifest says "holiday.bin"), and the BitTorrent link was refused.
```

In node2's log, nothing happens between the refusal and shutdown: no
RPC, no TCP connection. The link was refused before any network
traffic. [DRY-RUN.md](DRY-RUN.md#part-7--scriptsdemo-magnetsh-fetching-with-a-link)
part 7 traces this run function by function.

## SIMPLIFICATIONS

None new. Neither design choice is a shortcut to undo later:
- Trusting only `xt` and treating `dn` as a label is how BitTorrent
  handles magnet links too.
- Our own `urn:tp2p` namespace is the honest label for a key that isn't
  a BitTorrent info hash.
- The parameters we leave out (`tr`, `x.pe`, `xl`) aren't needed by a
  node that has joined the DHT.

So nothing here blocks the end goal.

## Where this lives in the code

| Concept | Function / type | File |
|---|---|---|
| Build a link | `MagnetLink`, `SharedFile.Magnet` | `internal/transfer/magnet.go` |
| Read a link | `ParseMagnet`, `ParseKeyOrMagnet` | `internal/transfer/magnet.go` |
| The key inside it | `ContentKey`, `EncodeManifest` | `internal/transfer/manifest.go` |
| Console `share` / `fetch` / `files` | `consoleEnv.share` (+ `magnetFor`), `.fetch`, `.files` | `cmd/node/console.go` |
| HTTP API: `magnet` field, `fetch` by link | `File`, `fileOf`, `handleFetch`; `writeJSON` writes `&` as-is | `internal/nodeapi/api.go`, `control.go` |
| The demo | | `scripts/demo-magnet.sh` |

`magnet.go` does no I/O. Like `manifest.go`, it's pure functions on
bytes and strings.

## Tests

- **Unit, `internal/transfer/magnet_test.go`:**
  - The exact link for `notes.txt`; no `dn` for an empty name; a space
    becomes `+`.
  - Round trips for names with spaces, `&=?#%+`, `é`, and
    `../../etc/passwd`, and no link contains whitespace.
  - Accepted: an uppercase scheme, namespace or hex; `dn` before `xt`;
    extra `tr` and `x.pe`; percent-escaped UTF-8.
  - Refused: `btih`, no `xt`, two `xt`s, another namespace, an `http:`
    URL, a short key, non-hex, a bad `%` escape.
  - `ParseKeyOrMagnet` accepts a key (with surrounding whitespace) or a
    link, and its error names both forms.
- **Integration, `internal/nodeapi` (real UDP + TCP):**
  - `TestFetchAcceptsMagnetLink`: the link from `/api/share` downloads
    the file on another node, and a link with a lying `dn` still
    downloads the real file, named by its manifest.
  - `TestFilesEndpointListsSharedFiles` checks the `magnet` field and that
    the raw body contains `&dn=` verbatim.
  - `TestControlRejectsBadRequests` refuses a `btih` link with 400.
- **Demo:** `scripts/demo-magnet.sh` must print PASS.

## What's next

- The visual demo can show each file's link with a copy button, or as a
  QR code that a phone on the same subnet scans.
- Searching by name would be an extension beyond BitTorrent:
  [FAQ](FAQ.md#how-do-i-know-which-key-to-ask-for-if-i-want-an-ace-attorney-file-what-do-i-query)
  sketches one and explains why it's hard to make trustworthy.
