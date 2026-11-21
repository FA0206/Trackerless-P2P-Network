#!/usr/bin/env bash
#
# Graph 2: download time vs. number of leechers, P2P vs. centralized.
#
#   For each leecher count L, TRIALS times each:
#   - p2p: a fresh localhost network of 1 seeder + L leechers. The seeder
#     shares a FILE_SIZE random file; all L leechers fetch it at once
#     (POST /api/fetch). Each leecher's time is the response's duration_ms.
#   - centralized: the same file on one plain HTTP server
#     (centralized_server.sh); L curl downloads at once, each timed by curl.
#   A trial's duration_s is the mean over its L leechers. A fresh network per
#   p2p trial, so no leecher starts out already holding the file.
#
#   Every machine gets the same uplink: each node runs with the DEBUG ONLY
#   --debug-upload-rate UPLOAD_RATE, and the central server with the same
#   cap (SERVER_RATE, default UPLOAD_RATE). Loopback has no bandwidth limit,
#   so uncapped the server's one uplink never becomes the bottleneck P2P
#   exists to remove. Downloads are not capped.
#
# Output: eval/results/download_time.csv
#   leechers,trial,mode,duration_s,file_size_bytes,server_rate_bps
#   (mode: p2p or centralized; the last two columns are the same on every
#   row, there so the ideal-growth reference line in plot_graphs.py can be
#   derived from the actual run (file_size_bytes / server_rate_bps per
#   leecher) instead of guessed or fitted from one noisy data point)
#
# Usage: eval/scripts/download_time.sh
#   (env: LS="1 2 4 8 16 32" TRIALS=5 FILE_SIZE=20M UPLOAD_RATE=10000000
#    SERVER_RATE=$UPLOAD_RATE BASE_PORT=9900 HTTP_PORT=8790 SEED=<number>)
set -euo pipefail
cd "$(dirname "$0")/../.."
. eval/scripts/lib.sh

LS="${LS:-1 2 4 8 16 32}"
TRIALS="${TRIALS:-5}"
FILE_SIZE="${FILE_SIZE:-20M}"
UPLOAD_RATE="${UPLOAD_RATE:-10000000}" # bytes/s, every node: 10 MB/s (80 Mbit/s)
SERVER_RATE="${SERVER_RATE:-$UPLOAD_RATE}"
BASE_PORT="${BASE_PORT:-9900}"
HTTP_PORT="${HTTP_PORT:-8790}"
SEED="${SEED:-$$}"
RANDOM="$SEED"
SEED_PREFIX=dl
OUT="$RESULTS/download_time.csv"

eval_init
SERVE="$ROOT_WORK/serve"
mkdir -p "$SERVE"
make_file "$SERVE/test.bin" "$FILE_SIZE"
BYTES="$(wc -c <"$SERVE/test.bin" | tr -d ' ')"
FILESERVER_BIN="$ROOT_WORK/fileserver"
go build -o "$FILESERVER_BIN" ./eval/fileserver
FILESERVER_BIN="$FILESERVER_BIN" eval/scripts/centralized_server.sh "$SERVE" "$HTTP_PORT" "$SERVER_RATE" >"$ROOT_WORK/fileserver.log" 2>&1 &
HTTP_PID=$!
trap 'kill "$HTTP_PID" 2>/dev/null || true; wait "$HTTP_PID" 2>/dev/null || true; eval_cleanup' EXIT
until curl -s -o /dev/null "http://127.0.0.1:$HTTP_PORT/"; do sleep 0.1; done

# mean_of <file>: the mean of one number per line.
mean_of() { awk '{ s += $1; c++ } END { printf "%.3f\n", s / c }' "$1"; }

csv_reset "$OUT" "leechers,trial,mode,duration_s,file_size_bytes,server_rate_bps"
echo "Graph 2: $FILE_SIZE file, leecher counts $LS, $TRIALS trials each, uplink $UPLOAD_RATE B/s per node, $SERVER_RATE B/s server (SEED=$SEED)"
for l in $LS; do
	inflight=$((l > 4 ? l : 4))
	for ((t = 1; t <= TRIALS; t++)); do
		# --- p2p ---
		new_run
		grow_to $((l + 1)) --max-inflight "$inflight" --debug-upload-rate "$UPLOAD_RATE"
		key="$(share_file 0 "$SERVE/test.bin")"
		: >"$WORK/times"
		for ((i = 1; i <= l; i++)); do
			bg api_post "$i" fetch "{\"key\":\"$key\",\"out\":\"$WORK/got$i.bin\"}" "$WORK/fetch$i.json" >/dev/null
		done
		wait_jobs
		for ((i = 1; i <= l; i++)); do
			same_file "$SERVE/test.bin" "$WORK/got$i.bin"
			awk -v ms="$(json_num "$WORK/fetch$i.json" duration_ms)" 'BEGIN { print ms / 1000 }' >>"$WORK/times"
		done
		echo "$l,$t,p2p,$(mean_of "$WORK/times"),$BYTES,$SERVER_RATE" >>"$OUT"

		# --- centralized ---
		# Bodies go to /dev/null (32 copies of the file would fill /tmp); curl
		# checks the whole file arrived, the p2p side checks contents above.
		: >"$WORK/ctimes"
		for ((i = 1; i <= l; i++)); do
			bg curl -sS --fail -o /dev/null -w '%{time_total} %{size_download}\n' "http://127.0.0.1:$HTTP_PORT/test.bin" >"$WORK/ctime$i"
		done
		wait_jobs
		for ((i = 1; i <= l; i++)); do
			read -r secs got <"$WORK/ctime$i"
			[ "$got" -eq "$BYTES" ] || { echo "centralized download $i got $got of $BYTES bytes" >&2; exit 1; }
			echo "$secs" >>"$WORK/ctimes"
		done
		echo "$l,$t,centralized,$(mean_of "$WORK/ctimes"),$BYTES,$SERVER_RATE" >>"$OUT"
	done
	awk -F, -v l="$l" '$1 == l { s[$3] += $4; c[$3]++ }
		END { printf "  L=%-3d p2p %.2fs  centralized %.2fs\n", l, s["p2p"] / c["p2p"], s["centralized"] / c["centralized"] }' "$OUT"
done
echo "Wrote $OUT"
