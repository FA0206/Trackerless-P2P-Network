# eval/: the performance graphs

How to reproduce the six performance graphs. What each one measures, why,
and what the results mean: [docs/10-performance-graphs.md](../docs/10-performance-graphs.md).

| # | Graph | Script | Where it runs |
|---|---|---|---|
| 1 | Lookup latency vs. network size | `scripts/lookup_latency.sh` | localhost |
| 2 | Download time vs. leechers, P2P vs. centralized | `scripts/download_time.sh` | localhost |
| 3 | Recovery after the seeder dies | `scripts/recovery_time.sh` + `scripts/eval-field-tests.html` test 1 | localhost **and** 3 laptops |
| 4 | DHT control bytes vs. file payload | `scripts/bytes_overhead.sh` + `scripts/eval-field-tests.html` test 2 | localhost **and** 2 laptops |
| 5 | Download throughput vs. providers | `scripts/throughput_providers.sh` | localhost |
| 6 | Join time vs. network size | `scripts/bootstrap_time.sh` | localhost |

## One-time setup

- **Go** (the version in `go.mod`) on `PATH`, plus `bash`, `curl` and
  `awk`. The scripts build the node binary themselves.
- **matplotlib** for the plots, in a virtualenv inside this folder
  (git-ignored):

  ```sh
  python3 -m venv eval/.venv && eval/.venv/bin/pip install matplotlib
  ```

## Running the localhost experiments

From the repo root, each script writes one CSV to `eval/results/`:

```sh
bash eval/scripts/lookup_latency.sh        # graph 1   ~30 s
bash eval/scripts/bootstrap_time.sh        # graph 6   ~2 min
bash eval/scripts/throughput_providers.sh  # graph 5   ~20 s
bash eval/scripts/bytes_overhead.sh        # graph 4   ~15 s  (localhost half)
bash eval/scripts/recovery_time.sh         # graph 3   ~15 min (localhost half)
bash eval/scripts/download_time.sh         # graph 2   ~15 min
```

Run them one at a time: they time things, and a second experiment
competing for the CPU would skew the first. Each one stops its nodes on
exit (logs stay in the temp dir it prints). The defaults are the fixed
parameters of docs/10; every script's header lists the environment
variables that change them, e.g. a quick smoke run:

```sh
NS="5 10" LOOKUPS=3 bash eval/scripts/lookup_latency.sh
```

| Script | Variables (defaults) |
|---|---|
| `lookup_latency.sh` | `NS="5 10 20 40 80 160"` `LOOKUPS=20` |
| `bootstrap_time.sh` | `NS="5 10 20 40 80 160"` `TRIALS=5` |
| `download_time.sh` | `LS="1 2 4 8 16 32"` `TRIALS=5` `FILE_SIZE=20M` `UPLOAD_RATE=10000000` `SERVER_RATE=$UPLOAD_RATE` |
| `throughput_providers.sh` | `PS="1 2 4 8"` `TRIALS=5` `FILE_SIZE=20M` (stays below 16: a key can hold at most 16 values, `maxValuesPerKey` in `internal/kademlia/store.go`, so a leecher can never discover more than 16 providers for one file) |
| `recovery_time.sh` | `KILL_PCTS="40 60 80"` `TRIALS=5` `LEECHER_COUNT=2` `FILE_SIZE=4M` `DELAY=1s` `STAGGER=8` (the checklist's test 1 exactly: change one, and the laptop series is no longer comparable; the kill points are calibrated to straddle the ~60% point where the leechers' pieces naturally cover the whole file, not an arbitrary split — see docs/10) |
| `bytes_overhead.sh` | `SIZES="64K 1M 10M 100M"` `TRIALS=5` |

All of them also take `BASE_PORT` (each script has its own default, so
none collide) and `SEED` (bash `RANDOM`'s seed, printed at the start, to
repeat a run's topology).

## Running the real-laptop experiments

Open the checklist and follow it on the laptops:

```sh
xdg-open eval/scripts/eval-field-tests.html
```

Same setup as `scripts/multi-device/field-tests.html`: build once on laptop
A, hand `node-linux` to the others over USB, Drive or Telegram, type each
command by hand. Each trial ends with a one-line `awk` that reads the
numbers from that laptop's saved log, and the page turns them into the CSV
row to append (`environment=real_hosts`). The localhost scripts keep those
rows when they rewrite their own.

## Drawing the graphs

```sh
eval/.venv/bin/python eval/plot_graphs.py
```

writes `eval/results/graph1_lookup_latency.png` … `graph6_bootstrap_time.png`.
It draws whatever rows exist: graphs 3 and 4 show only the localhost
series until real-host rows are recorded, and say so.

## CSV columns

| File | Columns |
|---|---|
| `lookup_latency.csv` | `n,trial,latency_ms,rounds,rpcs_sent,rpcs_failed` |
| `download_time.csv` | `leechers,trial,mode,duration_s,file_size_bytes,server_rate_bps` (mode: `p2p`, `centralized`; the last two columns, constant per row, are the run's actual file size and server cap, so `plot_graphs.py` can derive the ideal-growth line from them instead of a measured data point) |
| `recovery_time.csv` | `kill_point_pct,trial,environment,survived,recovery_s` (`recovery_s` empty when the file was lost) |
| `bytes_overhead.csv` | `file_size_bytes,trial,environment,control_bytes,payload_bytes` |
| `throughput_providers.csv` | `providers,trial,duration_s,file_size_bytes,throughput_bps` |
| `bootstrap_time.csv` | `n,trial,bootstrap_ms,self_lookup_rounds,self_lookup_rpcs` |

## Caveats to state with the graphs

- This branch has no churn handling yet (stale-contact retry, republish,
  bucket refresh). No experiment kills nodes except graph 3's seeder, so
  lookups rarely hit a dead contact, but a stray 2 s RPC timeout is
  possible.
- Graphs 1, 2, 5 and 6 run on localhost only: no real network latency or
  NIC contention. Graph 2 caps every node's upload (`--debug-upload-rate`)
  so the comparison has the bottleneck a real network has.
- Graph 3 slows every piece by 1 s (`--debug-serve-delay`) in both
  environments, so a person can stop the seeder at a chosen piece;
  recovery times include that second, and the gap between the two series
  is the network.
- Real-host trials are run by hand: fewer of them (3 per point), and
  slower to repeat.

## Files

- `scripts/lib.sh`: shared helpers (build, start nodes with their API on,
  POST to it, grow a network, CSV bookkeeping); builds on
  `scripts/lib/nodes.sh` and `scripts/lib/edge.sh`.
- `scripts/centralized_server.sh` and `fileserver/`: graph 2's centralized
  baseline, a Go `net/http` file server with the same upload cap.
- `plot_graphs.py`: CSVs to PNGs.
- `results/`: the CSVs and PNGs.
