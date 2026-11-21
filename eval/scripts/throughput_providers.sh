#!/usr/bin/env bash
#
# Graph 5: download throughput vs. number of providers.
#
#   For each provider count P, TRIALS times: a fresh localhost network of P
#   providers + 1 leecher. Every provider shares the same FILE_SIZE file
#   (same content, so the same content key: P complete copies, no
#   partial-seeding effects). The leecher fetches it with --max-inflight P,
#   so the only limit on parallelism is one request per provider.
#   throughput = file size / the fetch response's duration_ms.
#   The script checks pieces really came from all P providers.
#
#   P stays below 16: a key (any key, including a file's provider-record
#   list) can hold at most maxValuesPerKey=16 values (internal/kademlia/
#   store.go) before the oldest are silently dropped, so a leecher can never
#   discover more than 16 providers for one file regardless of how many
#   exist. Sweeping past that would measure the cap, not the scheduler.
#
# Output: eval/results/throughput_providers.csv
#   providers,trial,duration_s,file_size_bytes,throughput_bps
#
# Usage: eval/scripts/throughput_providers.sh
#   (env: PS="1 2 4 8" TRIALS=5 FILE_SIZE=20M BASE_PORT=10100 SEED=<number>)
set -euo pipefail
cd "$(dirname "$0")/../.."
. eval/scripts/lib.sh

PS="${PS:-1 2 4 8}"
TRIALS="${TRIALS:-5}"
FILE_SIZE="${FILE_SIZE:-20M}"
BASE_PORT="${BASE_PORT:-10100}"
SEED="${SEED:-$$}"
RANDOM="$SEED"
SEED_PREFIX=prov
OUT="$RESULTS/throughput_providers.csv"

eval_init
FILE="$ROOT_WORK/test.bin"
make_file "$FILE" "$FILE_SIZE"
BYTES="$(wc -c <"$FILE" | tr -d ' ')"

csv_reset "$OUT" "providers,trial,duration_s,file_size_bytes,throughput_bps"
echo "Graph 5: $FILE_SIZE file, provider counts $PS, $TRIALS trials each (SEED=$SEED)"
for p in $PS; do
	inflight=$((p > 4 ? p : 4))
	for ((t = 1; t <= TRIALS; t++)); do
		new_run
		grow_to $((p + 1)) --max-inflight "$inflight" # nodes 0..p-1 provide, node p downloads
		for ((i = 0; i < p; i++)); do key="$(share_file "$i" "$FILE")"; done
		api_post "$p" fetch "{\"key\":\"$key\",\"out\":\"$WORK/got.bin\"}" "$WORK/fetch.json" >/dev/null
		same_file "$FILE" "$WORK/got.bin"
		used="$(grep -c '"127.0.0.1:[0-9]*": [1-9]' "$WORK/fetch.json" || true)"
		[ "$used" -eq "$p" ] || echo "  note: trial $t used $used of $p providers" >&2
		ms="$(json_num "$WORK/fetch.json" duration_ms)"
		awk -v p="$p" -v t="$t" -v ms="$ms" -v b="$BYTES" 'BEGIN { printf "%d,%d,%.3f,%d,%.0f\n", p, t, ms / 1000, b, b / (ms / 1000) }' >>"$OUT"
	done
	awk -F, -v p="$p" '$1 == p { s += $5; c++ } END { printf "  P=%-2d mean throughput %.1f MB/s\n", p, s / c / 1e6 }' "$OUT"
done
echo "Wrote $OUT"
