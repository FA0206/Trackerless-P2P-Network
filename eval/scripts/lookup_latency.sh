#!/usr/bin/env bash
#
# Graph 1: lookup latency vs. network size.
#
#   One network grows on localhost, each node joining through a randomly
#   chosen running node. Each time it reaches a size in NS, LOOKUPS lookups
#   run, each for a random 160-bit target from a random node, through
#   POST /api/lookup. Latency is curl's time for that HTTP request; rounds,
#   rpcs_sent and rpcs_failed come from the response. Growing one network
#   gives the same network a fresh build to that size would.
#
# Output: eval/results/lookup_latency.csv
#   n,trial,latency_ms,rounds,rpcs_sent,rpcs_failed
#
# Usage: eval/scripts/lookup_latency.sh
#   (env: NS="5 10 20 40 80 160" LOOKUPS=20 BASE_PORT=9500 SEED=<number>)
set -euo pipefail
cd "$(dirname "$0")/../.."
. eval/scripts/lib.sh

NS="${NS:-5 10 20 40 80 160}"
LOOKUPS="${LOOKUPS:-20}"
BASE_PORT="${BASE_PORT:-9500}"
SEED="${SEED:-$$}"
RANDOM="$SEED"
SEED_PREFIX=lat
OUT="$RESULTS/lookup_latency.csv"

eval_init
new_run
csv_reset "$OUT" "n,trial,latency_ms,rounds,rpcs_sent,rpcs_failed"
echo "Graph 1: lookups at network sizes $NS, $LOOKUPS each (SEED=$SEED)"
for n in $NS; do
	grow_to "$n"
	for ((t = 1; t <= LOOKUPS; t++)); do
		q=$((RANDOM % n))
		ms="$(api_post "$q" lookup "{\"target\":\"$(random_id)\"}" "$WORK/lookup.json")"
		echo "$n,$t,$ms,$(json_num "$WORK/lookup.json" rounds),$(json_num "$WORK/lookup.json" rpcs_sent),$(json_num "$WORK/lookup.json" rpcs_failed)" >>"$OUT"
	done
	awk -F, -v n="$n" '$1 == n { s += $3; r += $4; c++ } END { printf "  N=%-4d mean latency %.1f ms, mean rounds %.2f\n", n, s / c, r / c }' "$OUT"
done
echo "Wrote $OUT"
