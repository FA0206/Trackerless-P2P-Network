#!/usr/bin/env bash
#
# Edge case A4: does a small k alone, with no failures, break lookups?
#
#
#   The SAME 20-node network is built four times: k = 1 without the
#   refresh at join (--join-refresh=false, the original code's join), then
#   k = 1, 2 and 4 with it. Same node IDs, same random joins (each node
#   bootstraps through a randomly chosen running node), same lookups.
#   QUERIERS nodes each look up TARGETS random IDs, and every answer is
#   compared with the brute-force k closest nodes (as in
#   demo-big-network.sh). Nobody dies.
#
# Prediction:
#   - k = 4: every lookup is correct (demo-big-network.sh's setting).
#   - k = 1 without the join refresh: some lookups return the wrong node.
#     A k-bucket then holds a single contact (paper Sec. 2.2), a FIND_NODE
#     reply carries a single contact, and the lookup stops as soon as its
#     one best candidate has been asked. With nearly empty routing tables,
#     one unlucky reply and it settles on a node that isn't the closest.
#   - k = 1 with the join refresh (paper Sec. 2.3; refresh.go): at least
#     as many correct. Each newcomer looks up a random ID in every bucket
#     farther than its closest neighbor, so its table fills up, and every
#     node it asks learns of it. Not necessarily all correct: k = 1 is
#     still one contact per bucket and per reply.
#   - k = 2: reported, not asserted.
# The script PASSes when these hold.
#
# Usage: scripts/edge/a4-tiny-buckets.sh
#        (env: N=20 QUERIERS=5 TARGETS=4 BASE_PORT=9475 SEED=1)
set -euo pipefail
cd "$(dirname "$0")/../.."
. scripts/lib/nodes.sh
. scripts/lib/edge.sh

N="${N:-20}"
QUERIERS="${QUERIERS:-5}"
TARGETS="${TARGETS:-4}"
BASE_PORT="${BASE_PORT:-9475}"
SEED="${SEED:-1}"
SEED_PREFIX=edgea4-
# k:join-refresh. The first is the original code's join, for comparison.
RUNS="1:off 1:on 2:on 4:on"

nodes_init
node_ids_init "$N" # ID0..ID<N-1>; network g's node o+i reuses ID<i>

STATUS=0
check() { # check <ok?> <text>
	if [ "$1" = yes ]; then echo "  as predicted: $2"; else echo "  UNEXPECTED: $2"; STATUS=1; fi
}

g=0
for RUN in $RUNS; do
	K="${RUN%%:*}" JR="${RUN##*:}"
	JRFLAG=""
	[ "$JR" = on ] || JRFLAG="--join-refresh=false"
	o=$((g * N)) # node indexes o..o+N-1 (each needs its own FIFO); ports are reused
	RANDOM="$SEED" # identical joins, queriers and targets in every network
	echo
	echo "=== k=$K, join refresh $JR: $N nodes on ports $BASE_PORT-$((BASE_PORT + N - 1)), random joins (SEED=$SEED) ==="
	start_node "$o" "$BASE_PORT" - --k "$K" --id-seed "${SEED_PREFIX}0" $JRFLAG
	eval "ID$o=\$ID0"
	for ((i = 1; i < N; i++)); do
		via=$((RANDOM % i))
		start_node $((o + i)) $((BASE_PORT + i)) $((BASE_PORT + via)) --k "$K" --id-seed "$SEED_PREFIX$i" $JRFLAG
		eval "ID$((o + i))=\$ID$i"
	done
	MEMBERS="$(seq "$o" $((o + N - 1)) | tr '\n' ' ')"

	QS=""
	while [ "$(echo $QS | wc -w)" -lt "$QUERIERS" ]; do
		q=$((RANDOM % N))
		in_list "$q" $QS || QS="$QS $q"
	done
	OK=0 TOTAL=0 ROUNDS=0 WRONG_LINES=""
	for q in $QS; do
		for ((t = 1; t <= TARGETS; t++)); do
			target="$(node_id "a4-target-$SEED-$q-$t")"
			node=$((o + q))
			issued=$(($(count_lines "$WORK/node$node.log" '^lookup [0-9a-f]\{40\}:\|^lookup failed') + 1))
			send "$node" "lookup $target" >/dev/null
			wait_for_count "$WORK/node$node.log" '^lookup [0-9a-f]\{40\}:\|^lookup failed' "$issued" 30
			r="$(grep "^lookup $target:" "$WORK/node$node.log" | grep -o 'rounds=[0-9]*' | cut -d= -f2 || true)"
			ROUNDS=$((ROUNDS + ${r:-0}))
			# IDs repeat across networks, so look them up among ID0..ID<N-1>:
			# that gives the shared node number directly.
			got="$(lookup_answer "$node" "$target" 1 | while read -r x; do index_of_id "$x" "$N"; done | tr '\n' ' ')"
			want="$(closest_nodes "$target" "$K" $(for i in $MEMBERS; do [ "$i" -ne "$node" ] && echo "$i"; done) |
				while read -r x; do echo $((x - o)); done | tr '\n' ' ')"
			TOTAL=$((TOTAL + 1))
			if [ "$got" = "$want" ]; then OK=$((OK + 1)); else
				WRONG_LINES="$WRONG_LINES    node$q -> ${target:0:8}…  rounds=$r  got [$(echo $got)] want [$(echo $want)]\n"
			fi
		done
	done
	printf '  k=%s, join refresh %s: %d of %d lookups correct, %d.%d rounds on average\n' "$K" "$JR" "$OK" "$TOTAL" \
		$((ROUNDS / TOTAL)) $((ROUNDS * 10 / TOTAL % 10))
	printf "$WRONG_LINES"
	eval "OK_${K}_$JR=$OK"
	for i in $MEMBERS; do kill_node "$i"; done # free the ports for the next k
	g=$((g + 1))
done

echo
echo "=== Verdict ==="
ALL=$((QUERIERS * TARGETS))
check "$([ "$OK_4_on" -eq "$ALL" ] && echo yes)" "k=4: $OK_4_on of $ALL correct"
check "$([ "$OK_1_off" -lt "$ALL" ] && echo yes)" "k=1 without the join refresh: $OK_1_off of $ALL correct, so a small k alone can return the wrong node"
check "$([ "$OK_1_on" -ge "$OK_1_off" ] && echo yes)" "k=1 with the join refresh: $OK_1_on of $ALL correct (was $OK_1_off)"
echo "  k=2: $OK_2_on of $ALL correct (reported, not asserted)"

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: with nothing dead, k=4 lookups were all correct; k=1 got $OK_1_off of $ALL right without the join refresh and $OK_1_on with it."
else
	echo "FAIL (repeat this exact run with SEED=$SEED)"
fi
exit "$STATUS"
