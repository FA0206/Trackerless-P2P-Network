#!/usr/bin/env bash
#
# Graph 4, localhost half: DHT control bytes vs. file payload bytes.
#
#   Each trial: a fresh localhost network of a seeder (node 0) and a leecher
#   (node 1), the same two-node setup the real-host checklist uses. The
#   seeder shares a random file of each size in SIZES; the leecher fetches it.
#   Only the share and the download are counted, not the leecher's join,
#   using landmarks a person can also find in a laptop's log (the real-host
#   checklist runs the very same awk):
#   - seeder: from its first "rpc sent" on. It sends no request before the
#     share; during the leecher's join it only answers.
#   - leecher: after its "bootstrap complete" line.
#
#   control_bytes: the bytes= of every "rpc received" line in those windows,
#                  both nodes. Every UDP message is received exactly once,
#                  so this is all DHT traffic, each message counted once.
#   payload_bytes: the bytes= of the leecher's "download complete" line,
#                  the file itself.
#
# Output: eval/results/bytes_overhead.csv (its real_hosts rows are kept)
#   file_size_bytes,trial,environment,control_bytes,payload_bytes
#
# Usage: eval/scripts/bytes_overhead.sh
#   (env: SIZES="64K 1M 10M 100M" TRIALS=5 BASE_PORT=10500 SEED=<number>)
set -euo pipefail
cd "$(dirname "$0")/../.."
. eval/scripts/lib.sh

SIZES="${SIZES:-64K 1M 10M 100M}"
TRIALS="${TRIALS:-5}"
BASE_PORT="${BASE_PORT:-10500}"
SEED="${SEED:-$$}"
RANDOM="$SEED"
SEED_PREFIX=bytes
OUT="$RESULTS/bytes_overhead.csv"
HEADER="file_size_bytes,trial,environment,control_bytes,payload_bytes"

# Keep these awk programs identical to eval-field-tests.html's.
seeder_control() {
	awk '/msg="rpc sent"/ { on = 1 } on && /msg="rpc received"/ { match($0, / bytes=[0-9]+/); s += substr($0, RSTART + 7, RLENGTH - 7) } END { print s + 0 }' "$1"
}
leecher_control() {
	awk 'on && /msg="rpc received"/ { match($0, / bytes=[0-9]+/); s += substr($0, RSTART + 7, RLENGTH - 7) } /msg="bootstrap complete"/ { on = 1 } END { print s + 0 }' "$1"
}
payload() {
	awk '/msg="download complete"/ { match($0, / bytes=[0-9]+/); print substr($0, RSTART + 7, RLENGTH - 7); exit }' "$1"
}

eval_init
csv_keep_other_env "$OUT" "$HEADER" localhost
echo "Graph 4 (localhost): file sizes $SIZES, $TRIALS trials each (SEED=$SEED)"
for size in $SIZES; do
	file="$ROOT_WORK/test-$size.bin"
	make_file "$file" "$size"
	bytes="$(wc -c <"$file" | tr -d ' ')"
	for ((t = 1; t <= TRIALS; t++)); do
		new_run
		grow_to 2
		key="$(share_file 0 "$file")"
		api_post 1 fetch "{\"key\":\"$key\",\"out\":\"$WORK/got.bin\"}" "$WORK/fetch.json" >/dev/null
		same_file "$file" "$WORK/got.bin"
		wait_for_log "$WORK/node1.log" 'msg="download complete"' 10
		control=$(($(seeder_control "$WORK/node0.log") + $(leecher_control "$WORK/node1.log")))
		echo "$bytes,$t,localhost,$control,$(payload "$WORK/node1.log")" >>"$OUT"
	done
	awk -F, -v b="$bytes" '$1 == b && $3 == "localhost" { c += $4; p += $5; n++ }
		END { printf "  %10d B file: control %.0f B, payload %.0f B, overhead %.4f%%\n", b, c / n, p / n, 100 * c / (c + p) }' "$OUT"
done
echo "Wrote $OUT"
