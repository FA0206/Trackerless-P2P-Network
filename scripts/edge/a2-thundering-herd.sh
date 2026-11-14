#!/usr/bin/env bash
#
# Edge case A2: a thundering herd. Many nodes join through the same node
# at the same moment.  (docs/EDGE-CASES.md, case A2)
#
#   node0 starts alone. Then N-1 nodes (default 19) are launched at once,
#   all with --bootstrap node0, without waiting for one another. k=4, so
#   buckets fill up and the eviction check runs while joins overlap.
#   Every node binary is built with Go's race detector (RACE=1, the
#   default), so a data race in any of the 20 processes is reported in its
#   log. That's the unit tests' `go test -race`, but on 20 real processes
#   talking over real sockets at the same time.
#
#   Then QUERIERS random nodes look up TARGETS random IDs, and every answer
#   is checked against the brute-force k closest (as in
#   demo-big-network.sh).
#
# Prediction: it holds up. Every join completes, every lookup is correct,
# and no process reports a data race. Concurrent packets are handled on
# separate goroutines, with the shared state behind locks
# (docs/ARCHITECTURE.md, "Goroutines and shared state").
#
# Usage: scripts/edge/a2-thundering-herd.sh
#        (env: N=20 K=4 QUERIERS=4 TARGETS=3 BASE_PORT=9425 SEED=1 RACE=1)
set -euo pipefail
cd "$(dirname "$0")/../.."
. scripts/lib/nodes.sh
. scripts/lib/edge.sh

N="${N:-20}"
K="${K:-4}"
QUERIERS="${QUERIERS:-4}"
TARGETS="${TARGETS:-3}"
BASE_PORT="${BASE_PORT:-9425}"
SEED="${SEED:-1}"
RANDOM="$SEED"
SEED_PREFIX=edgea2-
if [ "${RACE:-1}" = 1 ]; then
	export GOFLAGS="${GOFLAGS:-} -race" # nodes_init's `go build` picks this up
fi

nodes_init
node_ids_init "$N"
echo "Starting node0 on port $BASE_PORT, k=$K$([ "${RACE:-1}" = 1 ] && echo ', race detector on')..."
start_node 0 "$BASE_PORT" - --k "$K"

echo
echo "=== $((N - 1)) nodes join through node0 at the same moment ==="
start="$(now_ms)"
for ((i = 1; i < N; i++)); do
	start_node_nowait "$i" $((BASE_PORT + i)) "$BASE_PORT" --k "$K"
done
JOINED=0
for ((i = 1; i < N; i++)); do
	wait_for_log "$WORK/node$i.log" 'bootstrap complete\|^error:' 60
	grep -q 'bootstrap complete' "$WORK/node$i.log" && JOINED=$((JOINED + 1))
done
ms=$(($(now_ms) - start))
echo "  $JOINED of $((N - 1)) joined, all within $(fmt_ms "$ms")"
grep -h 'bootstrap complete' "$WORK"/node*.log | grep -o 'routing_table_size=[0-9]*' | cut -d= -f2 | sort -n |
	awk '{a[NR]=$1} END {printf "  routing table size right after joining: min %d, median %d, max %d\n", a[1], a[int((NR+1)/2)], a[NR]}'
for f in $(grep -L 'bootstrap complete' "$WORK"/node*.log || true); do
	[ "$(basename "$f")" = node0.log ] && continue # node0 started alone
	echo "  $(basename "$f" .log) did not join: $(grep -m1 '^error:' "$f" || tail -1 "$f")"
done

echo
echo "=== Lookups checked against a brute-force answer ==="
QS=""
while [ "$(echo $QS | wc -w)" -lt "$QUERIERS" ]; do
	q=$((RANDOM % N))
	in_list "$q" $QS || QS="$QS $q"
done
OK=0 TOTAL=0
for q in $QS; do
	for ((t = 1; t <= TARGETS; t++)); do
		target="$(node_id "a2-target-$SEED-$q-$t")"
		issued=$(($(count_lines "$WORK/node$q.log" '^lookup [0-9a-f]\{40\}:\|^lookup failed') + 1))
		send "$q" "lookup $target" >/dev/null
		wait_for_count "$WORK/node$q.log" '^lookup [0-9a-f]\{40\}:\|^lookup failed' "$issued" 30
		summary="$(grep "^lookup $target:" "$WORK/node$q.log" | cut -d: -f2- || echo ' lookup failed')"
		got="$(lookup_answer "$q" "$target" 1 | while read -r x; do index_of_id "$x" "$N"; done | tr '\n' ' ')"
		want="$(closest_nodes "$target" "$K" $(seq 0 $((N - 1)) | grep -vx "$q") | tr '\n' ' ')"
		TOTAL=$((TOTAL + 1))
		if [ "$got" = "$want" ]; then OK=$((OK + 1)) v=ok; else v="WRONG: got [$(echo $got)] want [$(echo $want)]"; fi
		printf '  node%-2s -> %s…:%s  %s\n' "$q" "${target:0:8}" "$summary" "$v"
	done
done

echo
echo "=== Data races reported by any of the $N processes ==="
RACES="$({ grep -l 'WARNING: DATA RACE' "$WORK"/node*.log 2>/dev/null || true; } | wc -l | tr -d ' ')"
if [ "${RACE:-1}" != 1 ]; then
	echo "  (RACE=0: built without the race detector, nothing to check)"
elif [ "$RACES" -eq 0 ]; then
	echo "  none"
else
	echo "  $RACES process(es) reported a race; first report:"
	grep -h -m1 -A20 'WARNING: DATA RACE' "$WORK"/node*.log | head -25 | sed 's/^/    /'
fi

echo
echo "=== Verdict ==="
STATUS=0
check() { # check <ok?> <text>
	if [ "$1" = yes ]; then echo "  as predicted: $2"; else echo "  UNEXPECTED: $2"; STATUS=1; fi
}
check "$([ "$JOINED" -eq $((N - 1)) ] && echo yes)" "$JOINED of $((N - 1)) simultaneous joins completed"
check "$([ "$OK" -eq "$TOTAL" ] && echo yes)" "$OK of $TOTAL lookups returned the true $K closest nodes"
check "$([ "$RACES" -eq 0 ] && echo yes)" "$RACES of $N processes reported a data race"

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: $((N - 1)) nodes joined through one node at once; lookups were correct and no data race was reported."
else
	echo "FAIL (repeat this exact run with SEED=$SEED)"
fi
exit "$STATUS"
