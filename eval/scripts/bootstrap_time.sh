#!/usr/bin/env bash
#
# Graph 6: join (bootstrap) time vs. network size.
#
#   Each trial grows a fresh localhost network the way lookup_latency.sh
#   does (random bootstrap node per join) to max(NS)+1 nodes. Node N joins
#   a network of exactly N nodes, so for every N in NS the trial records
#   node N's join: the time from its "bootstrapping" log line to its
#   "bootstrap complete" line (its own clock, so process and API startup
#   aren't counted), plus that line's self_lookup_rounds and
#   self_lookup_rpcs. A fresh network per trial means no trial joins
#   through a node an earlier trial left behind.
#
# Output: eval/results/bootstrap_time.csv
#   n,trial,bootstrap_ms,self_lookup_rounds,self_lookup_rpcs
#
# Usage: eval/scripts/bootstrap_time.sh
#   (env: NS="5 10 20 40 80 160" TRIALS=5 BASE_PORT=9700 SEED=<number>)
set -euo pipefail
cd "$(dirname "$0")/../.."
. eval/scripts/lib.sh

NS="${NS:-5 10 20 40 80 160}"
TRIALS="${TRIALS:-5}"
BASE_PORT="${BASE_PORT:-9700}"
SEED="${SEED:-$$}"
RANDOM="$SEED"
SEED_PREFIX=join
OUT="$RESULTS/bootstrap_time.csv"

MAX_N=0
for n in $NS; do [ "$n" -le "$MAX_N" ] || MAX_N="$n"; done

eval_init
csv_reset "$OUT" "n,trial,bootstrap_ms,self_lookup_rounds,self_lookup_rpcs"
echo "Graph 6: joins at network sizes $NS, $TRIALS trials (SEED=$SEED)"
for ((t = 1; t <= TRIALS; t++)); do
	new_run
	grow_to $((MAX_N + 1))
	for n in $NS; do
		log="$WORK/node$n.log"
		start="$(log_ms "$(grep -m1 'msg=bootstrapping' "$log")")"
		done_line="$(grep -m1 'msg="bootstrap complete"' "$log")"
		end="$(log_ms "$done_line")"
		rounds="$(printf '%s' "$done_line" | grep -o 'self_lookup_rounds=[0-9]*' | cut -d= -f2)"
		rpcs="$(printf '%s' "$done_line" | grep -o 'self_lookup_rpcs=[0-9]*' | cut -d= -f2)"
		echo "$n,$t,$((end - start)),$rounds,$rpcs" >>"$OUT"
	done
	echo "  trial $t done"
done
awk -F, 'NR > 1 { s[$1] += $3; r[$1] += $4; c[$1]++ }
	END { for (n in s) printf "  N=%-4d mean join %.1f ms, mean self-lookup rounds %.2f\n", n, s[n] / c[n], r[n] / c[n] }' "$OUT" | sort -t= -k2 -n
echo "Wrote $OUT"
