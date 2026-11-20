# 9. Several machines on one subnet

Prerequisite: [07-file-transfer-over-tcp.md](07-file-transfer-over-tcp.md)
and [08-swarming.md](08-swarming.md).

Until now every node ran on one laptop and talked over `127.0.0.1`. This
doc covers running one node per device on a shared network: a home
router, a phone hotspot, or a switch. Separate networks (NAT) are out of
scope; see [the end](#whats-not-handled).

## The three rules (read this first)

1. **One node per device**, or every node on one machine. Never mix the
   two ([why](#f2-loopback-contacts-leak-to-other-machines)).
2. **Give the first node `--advertise-ip <its IPv4 address>`.** Nodes that
   join through another machine detect theirs; the first node has nothing
   to detect from. Find the address with `ip route get <another device's IP>`
   (the `src` field), not `hostname -I`.
3. **After a misconfigured run, restart every node** before trying again
   ([why](#f3-a-stale-record-beats-the-corrected-one)).

## Why so little had to change: two kinds of address

A node learns where another node lives in two ways.

| | Observed | Self-reported |
|---|---|---|
| What | the source address of a packet that arrived | an address a node states about itself |
| Used for | every routing-table contact, i.e. the whole DHT | provider records: "I have file X, connect to me at IP:port" |
| Where | `handleMessage` → `ContactFromUDPAddr` (`node.go`, `contact.go`) | `Node.Self()`, used by `Peer.announce` (`transfer/peer.go`) |
| Comes from | the operating system | `--advertise-ip`, default `127.0.0.1` |
| Correct on a LAN? | always | only if set (or detected) correctly |

**Analogy:** the post office stamps the true return address on every
envelope. That's the observed address, and it can't be wrong. Inside the
letter you write "come visit me at my house". That's the self-reported
address. If "my house" is `127.0.0.1`, the reader drives to *their own*
house.

So the DHT part works across machines unchanged. In the lab below, the
seeder in `10.10.0.1` records a joining node under the address its packet
came from:

```
[seeder] msg="rpc received" type=PING from=4bf85c99…@10.10.0.2:9000
```

Both sockets already listen on every interface: UDP on `0.0.0.0`
(`NewTransport`), TCP on `:port` (`transfer/server.go` `listen`). Compare
your Raft project, where every server's address sat in a config file:
Kademlia has no such list, and learns addresses from packets.

## How the advertise IP is chosen (`chooseAdvertiseIP`, `cmd/node/main.go`)

| You start a node with | Its advertise IP |
|---|---|
| `--advertise-ip X` | X, after validation |
| no `--advertise-ip`, `--bootstrap` on another machine | **detected**: the IP this machine uses to reach the bootstrap node |
| no `--advertise-ip`, no `--bootstrap` (the first node) | `127.0.0.1` (rule 2) |
| no `--advertise-ip`, `--bootstrap 127.0.0.1:…`, `:…` or `0.0.0.0:…` | `127.0.0.1`: everything is on this machine |

**Detection** (`routeSourceIP`) asks the operating system "which of my IPs
would I send from to reach the bootstrap node?". It opens a UDP socket
"connected" to that address, which sends no packet and only picks a
route, and reads the local address. That's the same question as
`ip route get <ip>`. `hostname -I` is the wrong tool: on a laptop with
virtual machines it prints extra addresses, such as libvirt's
`192.168.122.1`, which no other device can reach. A detected loopback or
IPv6 address is ignored. The joiner logs the result:

```
[leecher] msg="advertise ip auto-detected" ip=10.10.0.2 via=10.10.0.1:9000
```

**Validation** (`parseAdvertiseIP`) accepts only an IPv4 address another
machine can dial. Each rejected kind used to fail late and silently, on
the downloader's side (see F1):

```
$ node --advertise-ip laptop-a.local
error: -advertise-ip "laptop-a.local": must be an IPv4 address other nodes can reach, e.g. 192.168.0.101 (not a hostname, IPv6, or 0.0.0.0)
```

`::1` and `0.0.0.0` give the same error. `127.0.0.1` is allowed: it's the
right value when every node is on one machine.

**The warning** (`warnIfAdvertisingLoopback`, `node.go`) catches the
first node when rule 2 is forgotten. On the first message from another
machine while advertising a loopback IP, the node logs once:

```
[seeder] level=WARN msg="advertise ip is loopback but a remote peer contacted us" advertise_ip=127.0.0.1 from=4bf85c99…@10.10.0.2:9000
```

It's checked when a message arrives, not when a file is shared. A seeder
that shares before anyone has joined knows no other machine at that
moment, so a check at share time would stay silent. The fix is to restart
that node with `--advertise-ip`, and then every node (rule 3).

## What goes wrong, with real logs

All logs below are real, from the [lab](#the-lab-several-machines-on-one-laptop)
(timestamps and `node=` stripped, IDs shortened). "Before" means the code
before this step.

### F1: a node advertises an address others can't use

**Loopback.** The seeder in `10.10.0.1` was started without
`--advertise-ip`:

```
[seeder]  msg="provider announced" key=5813effb… tcp_addr=127.0.0.1:9000 replicas=2
[leecher] msg="providers found" key=5813effb… count=1
[leecher] level=WARN msg="tcp request refused" from=127.0.0.1:47718 op=GET_MANIFEST error="unknown content key 5813effb…"
[leecher] level=WARN msg="provider failed, trying the next one" provider=127.0.0.1:9000 error="provider refused: unknown content key 5813effb…"
fetch failed: transfer: fetch manifest: every provider failed
```

The leecher dialed `127.0.0.1:9000`, which is *its own* machine, and its
own file server refused it. It literally drove to its own house. Before
this step nothing said why. Now the seeder logs the warning above as soon
as the leecher joins.

**A hostname** (`--advertise-ip laptop-a.local`), before this step:

```
[seeder]  msg="provider announced" key=5813effb… tcp_addr=laptop-a.local:9000 replicas=2
[leecher] level=WARN msg="ignoring bad provider record" error="transfer: provider record has bad address \"laptop-a.local\":9000"
fetch failed: transfer: no providers found for 5813effb…
```

Downloaders require an IP (`decodeProviderRecord`), so they dropped the
record. Now the node refuses to start instead. IPv6 would fail on every
fetch, since providers are dialed over `tcp4`. On Linux, `0.0.0.0` means
"this machine" to whoever dials it.

### F2: loopback contacts leak to other machines

This needs two nodes on one machine that joined each other through
`127.0.0.1`, plus a node elsewhere. The first two record each other as
`127.0.0.1:port`: observed, and correct *for them*. Replies
(`replyFindNode`, `replyFindValue`, through `closestExcluding`) pass that
address on, so the remote node sends to its *own* loopback. It gets a
2-second timeout per lookup round, or a reply from a different node that
happens to use that port, which is accepted because replies are matched
by RPC ID only.

**Rule 1 rules it out.** With one node per device, no packet ever has a
loopback source, so no such contact exists. This is a labeled
`SIMPLIFICATION:` in `closestExcluding`, not a guard. **If you see it**
(2-second stalls, `rpcs_failed>0`, and `127.0.0.1` contacts in `table`
or `/api/table` on a multi-device network), someone broke rule 1.

### F3: a stale record beats the corrected one

Provider records never expire ([06](06-store-and-find-value.md)). A node
keeps a key's values oldest first, and a downloader keeps only the
*first* record per node ID (`decodeProviders`, and `addProvider` in
`download.go`). So restarting only the seeder, with the same `--id-seed`
and the right IP, doesn't fix a bad run:

```
[seeder, 1st run] msg="provider announced" key=5813effb… tcp_addr=127.0.0.1:9000 replicas=3
[seeder, restart] msg="provider announced" key=5813effb… tcp_addr=10.10.0.1:9000 replicas=3
[L2] msg="find_value answered from local store" key=5813effb… values_found=2     ← the old and the new record
[L2] msg="providers found" key=5813effb… count=1                                 ← one per node ID: the old one
[L2] msg="tcp connected to provider" provider=127.0.0.1:9000                     ← L2's own machine
[L2] level=WARN msg="provider failed, trying the next one" provider=127.0.0.1:9000 error="provider refused: unknown content key 5813effb…"
```

With a random ID instead, the stale record just fails first and the new
one works. The real fix (keep the newest record) belongs with record
expiry and republishing. Until then, rule 3.

### F4: big UDP messages are split on real networks

Localhost has a 64 KB MTU, so nothing is ever split there. Ethernet and
Wi-Fi carry at most 1472 bytes of UDP payload per packet. Anything bigger
travels as IP fragments, and losing any one fragment loses the whole
message, which counts as a failed RPC.

| Message (JSON wire format) | Size | Fragments |
|---|---|---|
| `FIND_NODE_RESP`, 20 contacts | about 1.8 KB | 2 |
| `FIND_NODE_RESP`, first over 1472 B | at 16 contacts (`192.168.0.1xx`), 17 (`10.10.0.x`) | 2 |
| `FIND_VALUE_RESP`, 16 values of 250 B | about 5.6 KB | 4 |

So with k = 20, lookup replies fragment once a node knows about 18 or
more others; the K=4 demos never fragment. Seen in the lab: three nodes
held 16 values of 250 bytes under one key, and a fourth, alone in its
namespace, ran `get big`. It asked α = 3 nodes, and its namespace's kernel
counters (`/proc/net/snmp`) show 12 fragments reassembled into 3
messages:

```
get big: rounds=1 rpcs_sent=3 rpcs_failed=0, 16 values
Ip: ReasmReqds +12  ReasmOKs +3  ReasmFails +0
```

Nothing is changed for this yet: it's measured, not avoided
(`SIMPLIFICATION:` in `transport.go`). Under packet loss it shows up as
`rpcs_failed`; measure that before adding retries.

## The lab: several machines on one laptop

`scripts/netns/` builds a fake subnet out of **Linux network
namespaces**, the kernel feature Docker and Mininet are built on. Think
of apartments in one building. Each namespace has its own IP, its own
`127.0.0.1` and its own ports, and they're joined by a virtual switch:

```
  p2p1: veth1 = 10.10.0.1 ──┐
  p2p2: veth2 = 10.10.0.2 ──┼── bridge p2pbr = 10.10.0.254 (this laptop)
  p2p3: veth3 = 10.10.0.3 ──┘
```

`127.0.0.1` inside `p2p1` is a different place from `127.0.0.1` inside
`p2p2`, exactly as on real devices, so F1–F3 reproduce exactly. The links
have the real 1500-byte MTU, so F4 shows too. Linux only; on macOS, use
Docker Compose or real devices.

```sh
sudo bash scripts/netns/up.sh 5      # namespaces p2p1..p2p5; safe to run twice
sudo bash scripts/netns/down.sh      # stops the nodes inside, then removes everything
```

Build into `lab/` at the repo root, which git ignores (`.gitignore`), and
keep the lab's test files and logs there too:

```sh
go build -o lab/node ./cmd/node
```

Run a node in namespace i as yourself, not root (from the repo root):

```sh
sudo ip netns exec p2p1 sudo -u "$USER" ./lab/node --port 9000 --id-seed A --advertise-ip 10.10.0.1
```

Every namespace can use port 9000: each has its own ports.

- **Killing one node:** namespaces don't separate process IDs, so
  `pkill node` kills every node on the laptop. Kill by seed instead:
  `pkill -KILL -u "$USER" -f -- '--id-seed A( |$)'`.
- **A Wi-Fi-like link** (inside the namespace, where the veth lives;
  `replace`, because a second `add` fails):
  `sudo ip netns exec p2p2 tc qdisc replace dev veth2 root netem delay 5ms 2ms loss 1%`.
- **Seeing fragments:**
  `sudo ip netns exec p2p4 tcpdump -ni veth4 'ip[6:2] & 0x3fff != 0'`, or
  compare `sudo ip netns exec p2p4 cat /proc/net/snmp` (the `Ip:`
  `ReasmReqds`/`ReasmOKs` columns) before and after. Use a namespace with
  no other node in it: traffic between nodes in one namespace goes over
  its `lo`, which never fragments.
- **Docker:** if its daemon is running, it may DROP forwarded traffic
  across the bridge. Fix: `sudo iptables -I FORWARD -i p2pbr -j ACCEPT`.
- **Without sudo:** unprivileged user namespaces give a throwaway copy of
  the lab: `unshare -rnm --propagation private bash`, then
  `mount -t tmpfs tmpfs /run` and `bash scripts/netns/up.sh 4` inside.
  Run nodes with `ip netns exec p2pN <node> …` (no inner `sudo -u`).
  Everything disappears when that shell exits. `tcpdump` can't drop its
  privileges in there; use the `/proc/net/snmp` counters. The logs in
  this doc came from such a sandbox.

### Driving the lab from one terminal

Instead of one console per node, start nodes in the background with the
HTTP API ([NODE-API.md](NODE-API.md)) bound to their lab address, and send
commands with curl. Binding the API to `10.10.0.N` is safe here: the
bridge exists only on this laptop. Paste once per terminal, after
`sudo -v` (it caches your password so background `sudo` doesn't prompt;
repeat it after about 15 minutes). There are no `#` comments in this
block, because zsh rejects them at the prompt by default.

```sh
LAB="$(git rev-parse --show-toplevel)/lab"
lab_node() { local i=$1 seed=$2; shift 2; sudo ip netns exec "p2p$i" sudo -u "$USER" "$LAB/node" --console=false --port 9000 --id-seed "$seed" --advertise-ip "10.10.0.$i" --api "10.10.0.$i:8000" --api-control "$@" >"$LAB/$CASE/$seed.log" 2>&1 & }
lab_api() { if [ $# -ge 3 ]; then curl -s -X POST -H 'Content-Type: application/json' -d "$3" "10.10.0.$1:8000/api/$2"; else curl -s "10.10.0.$1:8000/api/$2"; fi; echo; }
lab_stop() { pkill -u "$USER" -f -- "$LAB/node --console=false"; sleep 1; }
```

- `lab_node <i> <seed> [flags]` starts a node in `p2p<i>`, advertising
  `10.10.0.<i>`. A later `--advertise-ip` in the flags wins (Go's `flag`
  keeps the last value), which is how to test F1. Its log goes to
  `$LAB/$CASE/<seed>.log`.
- `lab_api <i> <command> [json]` sends a GET, or a POST with the JSON.
- `lab_stop` stops every lab node. Values live only in memory, so this
  also wipes the DHT (rule 3).

`$LAB` is the git-ignored `lab/` at the repo root, as an absolute path:
node commands such as `fetch` resolve relative paths from the node's own
working directory. Rebuild `lab/node` after every code change, since
nothing rebuilds it automatically. Set `CASE=<name>; mkdir -p "$LAB/$CASE"`
before each case.

## Test checklist

The existing tests (`go test`, the five demos, the six edge cases) still
cover the algorithms and the "everything on one machine" mode, but every
address in them is `127.0.0.1`. These checks cover what only multiple
machines can show. Run them in the lab, then on real devices.

**The existing demos, one node per namespace:**

| Demo | Lab version | Passes when |
|---|---|---|
| `demo.sh` | 5 nodes, each joining through the previous; node 5 looks up node 3 (`printf n3 \| sha1sum`) | the lookup returns exactly the other 4 nodes, every `table` holds 4 contacts, every IP is `10.10.0.x` (seeds `n1`–`n5`: 2 rounds, 4 RPCs, 0 failed) |
| `demo-transfer.sh` | `--k 3`; `put`/`get`; share 1 MiB; fetch from 1, then 2 providers; kill the seeder; fetch again | every copy is byte-identical (`cmp`) |
| `demo-parallel.sh` | 3 providers with `--debug-serve-delay 150ms`, 1 downloader | its log shows 3 `piece requested` in flight at once |
| `demo-seeder-death.sh` | see below | the seeder dies with every leecher below 16/16, and all leechers finish |

**The seeder dies mid-download.** The run only proves something if, at
the kill, the leechers *together* hold every piece and *none* holds them
all. Kill earlier, and losing the file is the correct outcome; kill
later, and a complete copy already existed. Copy the demo's settings:
share a 1 MiB file (16 pieces); every node runs `--debug-serve-delay 1s`
(starting things by hand is slow, and at the demo's 150 ms one leecher
could finish before the next starts); leechers run `--max-inflight 1` and
start a few pieces apart. Then let the script pick the moment:

```sh
bash scripts/netns/kill-seeder-when-spread.sh seeder "$LAB/$CASE"/L*.log
```

It prints `VOID … redo the run` if a leecher already has everything, and
otherwise SIGKILLs the seeder the moment the leechers together hold every
piece, printing each one's count. Run it as yourself, never with `sudo`.

**Cases with no localhost version:**

- **F1:** a seeder without `--advertise-ip`: the fetch fails, and the
  seeder logs the warning when the first remote peer joins. A hostname:
  the node refuses to start.
- **Auto-detect:** a joiner started without `--advertise-ip` logs
  `advertise ip auto-detected ip=10.10.0.N`, and a remote node can fetch
  from it.
- **F3:** restart only the seeder, same `--id-seed`, right IP: the fetch
  still fails, which is why rule 3 exists.
- **F4:** 16 × `put big <~250 chars>` from one node, then a *new* node in
  an empty namespace runs `get big`: 4 fragments per reply. It must join
  after the puts, or it already holds the value. With netem loss on its
  link, compare `rpcs_failed`.

## On real devices

**Step by step:** open
[`scripts/multi-device/field-tests.html`](../scripts/multi-device/field-tests.html)
in a browser (`xdg-open scripts/multi-device/field-tests.html`). It walks
two or three laptops through four tests that all passed on real hardware:
a forgotten `--advertise-ip`, a laptop vanishing mid-download, two
providers at once, and the seeder dying with the file surviving. Type
each laptop's IP into the page once and every command fills it in. One
result from those runs: with two providers, the split follows speed, not
who shared first. A provider that answered far slower (still running
`--debug-serve-delay`) sent 1 piece of 32, because each provider gets its
next piece only after answering the previous one, and a piece once
assigned is asked of that provider alone.

The page's sibling,
[`eval/scripts/eval-field-tests.html`](../eval/scripts/eval-field-tests.html),
uses the same setup to record numbers instead of pass/fail: the
real-laptop halves of performance graphs 3 (recovery after the seeder
dies) and 4 (DHT bytes vs. file bytes), [10](10-performance-graphs.md).

- Use a phone hotspot or a home router. Campus and office Wi-Fi often
  block traffic between devices ("client isolation").
- Build once (`go build -o lab/node ./cmd/node`), so macOS's firewall
  prompt doesn't come back on every `go run`.
- Open UDP **and** TCP on the port: `sudo ufw allow 9000/udp` and
  `sudo ufw allow 9000/tcp`. On macOS, allow the prompt. On macOS 15+, if
  you get "no route to host", allow the terminal app under Privacy &
  Security → Local Network.
- Find each device's address with `ip route get <another device's IP>`
  (Linux) or `ipconfig getifaddr en0` (macOS). Pass an IP, never a
  hostname.
- The seeder-death run needs at least 3 devices (a seeder and 2
  leechers), better 4.
- Clocks differ between machines. Compare each node's own `duration=`
  values, never timestamps from different machines.

## SIMPLIFICATIONS (and whether they block the end goal)

| Simplification | Blocks the end goal? | When to fix |
|---|---|---|
| One node per device, or all on one machine; replies pass contacts on as observed (`closestExcluding`) | No: the multi-device demo runs one node per device, and large networks run one per namespace | Only if several nodes per device across machines are ever needed: filter loopback contacts out of replies to remote requesters |
| The advertise IP is detected from one route, the one to the bootstrap node (`chooseAdvertiseIP`) | No, on one subnet | Behind NAT, or with several interfaces: learn how peers see us (an "observed address" reply, as libp2p's identify does) |
| One subnet; no NAT traversal | No for the demo | Across the internet: an overlay VPN (Tailscale, ZeroTier) needs no code change; real NAT traversal is its own project |
| UDP fragments are measured, not avoided (`transport.go`) | Not yet | If `rpcs_failed` under Wi-Fi-like loss hurts the graphs: RPC retries, or smaller replies. (Graph 4's byte counts can't show fragmentation itself: `bytes=` is the UDP payload, the same on any MTU; fragments only add IP headers.) |
| IPv4 only (`udp4`, `tcp4`) | No | If ever needed |

## Where this lives in the code

| Concept | Function | File |
|---|---|---|
| Observed address | `handleMessage` → `ContactFromUDPAddr` | `internal/kademlia/node.go`, `contact.go` |
| Self-reported address | `Node.Self`, `Peer.announce` | `internal/kademlia/node.go`, `internal/transfer/peer.go` |
| Choosing the advertise IP | `chooseAdvertiseIP`, `parseAdvertiseIP`, `routeSourceIP`, `flagWasSet` | `cmd/node/main.go` (tests: `main_test.go`) |
| The loopback warning | `warnIfAdvertisingLoopback` | `internal/kademlia/node.go` (test: `advertise_test.go`) |
| Contacts passed on as observed | `closestExcluding` | `internal/kademlia/node.go` |
| Fragmentation note | `maxDatagramSize` | `internal/kademlia/transport.go` |
| The lab | `up.sh`, `down.sh`, `kill-seeder-when-spread.sh` | `scripts/netns/` |

A traced run of a cross-namespace join and download, function by
function: [DRY-RUN.md](DRY-RUN.md) part 6.

## What's not handled

Each is its own later step:

- keeping the **newest** provider record per node ID (the code fix for
  F3), with record expiry and republishing;
- rejecting a reply that doesn't come from the node we asked
  (`sendAndWait` matches by RPC ID only);
- RPC retries for Wi-Fi packet loss (measure F4 first);
- automating the lab (a namespace mode for `scripts/lib/`), to run
  `demo-big-network.sh` and the edge cases across namespaces. (The
  evaluation graphs went another way: scalable runs on localhost, real
  laptops by hand for the two graphs that need a real network;
  [10](10-performance-graphs.md));
- one visualizer reading every node's `--api` across machines, which is a
  security question (NODE-API.md);
- the internet: NAT traversal, or an overlay VPN with no code change.
