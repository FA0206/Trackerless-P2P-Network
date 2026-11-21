# Trackerless P2P Network

A peer-to-peer file sharing system that works without a central tracker.
Nodes find each other and find files using a Kademlia DHT, which we
implemented from scratch in Go following the original paper
(`maymounkov-kademlia-lncs.pdf`). Files are split into pieces and
downloaded from several nodes at once, so a file can still be downloaded
after the node that first shared it goes offline.

How the code is organised is described in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Requirements

- Go 1.26 or newer (no other Go libraries are needed)
- bash, for the demo scripts (Linux or macOS; on Windows use WSL)
- Python 3 with matplotlib, only to redraw the graphs
- A web browser, only for the visualizer

## Build and test

```sh
go build ./...
go test ./... -race -count=1
```

## Running nodes by hand

Open two terminals. Start one node, then a second one that joins through
the first:

```sh
go run ./cmd/node --port 9000 --id-seed nodeA
go run ./cmd/node --port 9001 --id-seed nodeB --bootstrap 127.0.0.1:9000
```

You can then type commands into either terminal:

| Command | What it does |
|---|---|
| `help` | list all commands |
| `id`, `table` | show the node's ID and the other nodes it knows |
| `lookup <id>` / `lookup self` | find the nodes closest to an ID |
| `ping <host:port>` | check whether a node is alive |
| `put <name> <text>` / `get <name>` | store a short text and read it back from any node |
| `share <file>` | share a file; prints its key and a magnet link |
| `fetch <key or magnet link> <output file>` | download a file |
| `files` | list shared and downloaded files |

For example, run `share README.md` on the first node, then
`fetch <key> copy.md` on the second. Ctrl-C stops a node.

## Demo scripts

Each script starts a few nodes on this machine, runs a scenario and
prints `PASS` or `FAIL` at the end.

| Script | What it shows |
|---|---|
| `bash scripts/demo.sh` | 5 nodes join and a lookup finds all of them |
| `bash scripts/demo-transfer.sh` | storing text, sharing and downloading a file, then downloading again after the sharer is killed |
| `bash scripts/demo-seeder-death.sh` | the sharer dies mid-download and the downloaders finish by getting pieces from each other |
| `bash scripts/demo-parallel.sh` | one download getting pieces from 3 nodes at the same time |
| `bash scripts/demo-big-network.sh` | 20 nodes join in random order and every lookup is checked |
| `bash scripts/demo-merkle.sh` | a node sending corrupted pieces gets caught |
| `bash scripts/demo-magnet.sh` | downloading using a magnet link |
| `bash scripts/demo-give-up-resume.sh` | a stuck download keeps its pieces and continues later (~1 min) |
| `bash scripts/edge/run-all.sh` | harder cases: half the nodes dying, network splits, late joiners (~4 min) |

If a script says a port is in use, a node from an earlier run is still
running. Stop it (e.g. `pkill -x node`) and run the script again.

## Visualizer

```sh
go run ./cmd/vizrun
```

Open http://127.0.0.1:8790/ and press the right arrow key (or Space) to
run each scene on 21 real nodes: joining, lookups, storing values,
sharing, downloading, and killing the sharer. Ctrl-C stops everything;
restart it and reload the page to run it again. Add `?mode=auto` to the
URL to play all scenes automatically. `bash scripts/demo-viz.sh` runs the
scenes without a browser as a quick check.

## Running on several computers

The computers need to be on the same network (same Wi-Fi or router).
Start the first node with its own IP address:

```sh
go run ./cmd/node --port 9000 --advertise-ip 192.168.1.10
```

and on every other computer join through it (their address is detected
automatically):

```sh
go run ./cmd/node --port 9000 --bootstrap 192.168.1.10:9000
```

Run one node per computer and allow the port (UDP and TCP) through the
firewall.

## Performance graphs

The graphs and their raw data are in `eval/results/`. To measure again:

```sh
bash eval/scripts/lookup_latency.sh        # graph 1: lookup time vs network size
bash eval/scripts/download_time.sh         # graph 2: download time, P2P vs one server (~15 min)
bash eval/scripts/recovery_time.sh         # graph 3: recovery after the sharer is killed (~15 min)
bash eval/scripts/bytes_overhead.sh        # graph 4: control traffic vs file data
bash eval/scripts/throughput_providers.sh  # graph 5: speed vs number of sharers
bash eval/scripts/bootstrap_time.sh        # graph 6: join time
```

To redraw the images:

```sh
python3 -m venv eval/.venv && eval/.venv/bin/pip install matplotlib
eval/.venv/bin/python eval/plot_graphs.py
```

## Main options

| Option | Meaning |
|---|---|
| `--port` | port to use (default 9000, for both UDP and TCP) |
| `--bootstrap host:port` | join the network through this node |
| `--advertise-ip` | address other computers use to reach this node |
| `--id-seed` | fixed node ID instead of a random one |
| `--console=false` | don't read commands from the terminal |
| `--verbose` / `--quiet` | more or less logging |
| `--api 127.0.0.1:8000` | serve the node's state as JSON (used by the visualizer) |
| `--k`, `--alpha` | bucket size and number of parallel requests (default 20 and 3) |

`go run ./cmd/node --help` lists all options.

## Folders

```
cmd/node/           the node program and its terminal commands
cmd/vizrun/         starts the visualizer and its nodes
viz/                the visualizer web page
internal/kademlia/  the DHT: IDs, routing table, lookups, storing values
internal/transfer/  file sharing: pieces, downloads, Merkle checks, magnet links
internal/framing/   packing messages for sending
internal/nodeapi/   optional HTTP interface for a node
scripts/            demos and test scenarios
eval/               performance experiments and graphs
```
