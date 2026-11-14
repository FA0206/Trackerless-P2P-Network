#!/usr/bin/env bash
#
# Edge case B1: half the network dies at once; the survivors keep looking
# things up.  (docs/EDGE-CASES.md, case B1)
#
#   N nodes (default 16), k=4, each joining through a randomly chosen
#   running node (as in demo-big-network.sh), so lookups take several
#   rounds. One surviving node (the querier) looks up the same TARGETS:
#
#     pass 1    everyone alive              (the baseline)
#     --- KILL nodes are killed at once (SIGKILL), default 8 of 16 ---
#     pass 2    right after the kill
#     pass 3..  the same lookups again, until a pass has no failed RPC
#               (at most MAX_PASSES passes after the kill)
#
#   Every answer is compared with the brute-force k closest LIVE nodes.
#
# Prediction (from lookup.go, node.go and stale.go):
#   - pass 1: correct answers, no failed RPCs, milliseconds.
#   - pass 2: failed RPCs, and each round that includes a dead contact
#     waits a full --rpc-timeout (2s): rounds are synchronous.
#   - the querier learns: a node that fails --stale-after RPCs in a row
#     (default 5, paper Sec. 4.1) is stale, and lookups stop asking it,
#     even when other nodes still mention it. So failed RPCs fall pass by
#     pass, and some pass after pass 2 has none: lookups are back to
#     milliseconds. With the paper's 5 that takes a few passes; with
#     NODE_FLAGS="--stale-after 1" it's pass 3. (Before that fix, pass 3
#     asked the same dead nodes again: see docs/EDGE-CASES.md.)
#   - no answer is SHORT: every lookup that finishes returns k live nodes,
#     because a failed candidate gives up its place to the next-closest
#     one (lookup.go, paper Sec. 2.3). Before that fix, answers came back
#     short (see docs/EDGE-CASES.md).
#   - reported, not asserted: an answer can still miss a live node that
#     no survivor mentions. Survivors' tables keep their dead contacts,
#     and a FIND_NODE reply carries only the k closest of them, so dead
#     ones can crowd a live one out (SEED=4 shows it). Only bucket
#     refresh fixes that: the survivors that hand out the dead contacts
#     never send them an RPC themselves. And because the lookup keeps going
#     past dead contacts, at 2s per round, some lookups can run out of the
#     console's 10s.
# The script PASSes when all of these hold.
#
# Usage: scripts/edge/b1-half-the-network-dies.sh
#        (env: N=16 K=4 KILL=8 TARGETS=4 MAX_PASSES=10 BASE_PORT=9500 SEED=1 NODE_FLAGS=)
#        NODE_FLAGS: extra flags for every node, e.g. "--stale-after 1"
set -euo pipefail
cd "$(dirname "$0")/../.."
. scripts/lib/nodes.sh
. scripts/lib/edge.sh

N="${N:-16}"
K="${K:-4}"
KILL="${KILL:-8}"
TARGETS="${TARGETS:-4}"
# Passes after the kill, at most. A dead node that only one lookup meets
# fails once per pass, so it takes --stale-after (5) passes from the one
# that first meets it; with SEED=1 that ends at pass 7 or 8.
MAX_PASSES="${MAX_PASSES:-10}"
BASE_PORT="${BASE_PORT:-9500}"
SEED="${SEED:-1}"
RANDOM="$SEED" # joins, the querier, the victims, and the targets all follow from SEED
SEED_PREFIX=edgeb1-
NODE_FLAGS="${NODE_FLAGS:-}" # word-split on purpose: several flags
RPC_TIMEOUT_MS=2000 # the node's default --rpc-timeout

nodes_init
node_ids_init "$N"
echo "Starting $N nodes on ports $BASE_PORT-$((BASE_PORT + N - 1)), k=$K, random joins (SEED=$SEED)..."
start_node 0 "$BASE_PORT" - --k "$K" $NODE_FLAGS
JOINS="node0: first"
for ((i = 1; i < N; i++)); do
	via=$((RANDOM % i))
	start_node "$i" $((BASE_PORT + i)) $((BASE_PORT + via)) --k "$K" $NODE_FLAGS
	JOINS="$JOINS, node$i<-node$via"
done
echo "  joins: $JOINS"

Q=$((RANDOM % N))
VICTIMS=""
while [ "$(echo $VICTIMS | wc -w)" -lt "$KILL" ]; do
	v=$((RANDOM % N))
	if [ "$v" -ne "$Q" ] && ! in_list "$v" $VICTIMS; then VICTIMS="$VICTIMS $v"; fi
done
VICTIMS="$(echo $VICTIMS | tr ' ' '\n' | sort -n | tr '\n' ' ')"
ALIVE=""
for ((i = 0; i < N; i++)); do in_list "$i" $VICTIMS || ALIVE="$ALIVE $i"; done
TARGET_IDS=""
for ((t = 1; t <= TARGETS; t++)); do TARGET_IDS="$TARGET_IDS $(node_id "b1-target-$SEED-$t")"; done
echo "  querier: node$Q;  to be killed: $(for i in $VICTIMS; do printf 'node%s ' "$i"; done)"

ISSUED=0
# run_pass <pass-number> <label> <live-node-indexes...>: one lookup per
# target from node Q; prints one row each and records totals in
# PASS_FAILED, PASS_MS, PASS_SLOW (lookups with a failed RPC), PASS_WRONG
# (answers that differ from the brute-force k closest live nodes),
# PASS_SHORT (wrong only by missing nodes: a prefix of the true answer),
# PASS_TIMEOUT (lookups that ran out of the console's time), PASS_FEW
# (answers with fewer nodes than the true answer has, short or not), and
# FAILED_<pass>_<t> per target.
run_pass() {
	local pass="$1" label="$2" t=0 target nth start ms summary rounds sent failed got want verdict
	shift 2
	PASS_FAILED=0 PASS_MS=0 PASS_SLOW=0 PASS_WRONG=0 PASS_SHORT=0 PASS_TIMEOUT=0 PASS_FEW=0
	for target in $TARGET_IDS; do
		t=$((t + 1))
		ISSUED=$((ISSUED + 1))
		nth=$(($(count_lines "$WORK/node$Q.log" "^lookup $target:") + 1)) # this answer's position in the log
		start="$(now_ms)"
		send "$Q" "lookup $target" >/dev/null
		wait_for_count "$WORK/node$Q.log" '^lookup [0-9a-f]\{40\}:\|^lookup failed' "$ISSUED" 60
		ms=$(($(now_ms) - start))
		summary="$(grep "^lookup $target:" "$WORK/node$Q.log" | sed -n "${nth}p" || true)" # empty if it failed
		if [ -z "$summary" ]; then
			echo "  $label  ${target:0:8}…  lookup failed: $(grep '^lookup failed' "$WORK/node$Q.log" | tail -1)"
			eval "FAILED_${pass}_$t=99"
			PASS_SLOW=$((PASS_SLOW + 1)) PASS_TIMEOUT=$((PASS_TIMEOUT + 1)) PASS_MS=$((PASS_MS + ms))
			continue
		fi
		rounds="$(echo "$summary" | grep -o 'rounds=[0-9]*' | cut -d= -f2)"
		sent="$(echo "$summary" | grep -o 'rpcs_sent=[0-9]*' | cut -d= -f2)"
		failed="$(echo "$summary" | grep -o 'rpcs_failed=[0-9]*' | cut -d= -f2)"
		eval "FAILED_${pass}_$t=$failed"
		got="$(lookup_answer "$Q" "$target" "$nth" | while read -r id; do index_of_id "$id" "$N"; done | tr '\n' ' ')"
		want="$(closest_nodes "$target" "$K" $(for i in "$@"; do [ "$i" -ne "$Q" ] && echo "$i"; done) | tr '\n' ' ')"
		[ "$(echo $got | wc -w)" -ge "$(echo $want | wc -w)" ] || PASS_FEW=$((PASS_FEW + 1)) # fewer nodes than exist
		if [ "$got" = "$want" ]; then verdict="ok"; else
			PASS_WRONG=$((PASS_WRONG + 1))
			case "$want" in
			"$got"*) # want starts with got: right nodes, some missing
				verdict="SHORT: got [$(echo $got)] want [$(echo $want)]"
				PASS_SHORT=$((PASS_SHORT + 1)) ;;
			*) verdict="WRONG: got [$(echo $got)] want [$(echo $want)]" ;;
			esac
		fi
		[ "$failed" -eq 0 ] || PASS_SLOW=$((PASS_SLOW + 1))
		PASS_FAILED=$((PASS_FAILED + failed)) PASS_MS=$((PASS_MS + ms))
		printf '  %s  %s…  rounds=%-2s rpcs=%-3s failed=%-2s %7s  %s\n' \
			"$label" "${target:0:8}" "$rounds" "$sent" "$failed" "$(fmt_ms "$ms")" "$verdict"
	done
	printf '  %s  total: %d failed RPCs, %s for %d lookups, %d wrong answer(s), %d of them short, %d ran out of time\n' \
		"$label" "$PASS_FAILED" "$(fmt_ms "$PASS_MS")" "$TARGETS" "$PASS_WRONG" "$PASS_SHORT" "$PASS_TIMEOUT"
}

echo
echo "=== Pass 1: node$Q looks up $TARGETS targets, everyone alive ==="
run_pass 1 "pass 1" $(seq 0 $((N - 1)))
P1_FAILED=$PASS_FAILED P1_WRONG=$PASS_WRONG P1_MS=$PASS_MS

echo
echo "=== Killing $KILL of $N nodes at once (SIGKILL) ==="
for i in $VICTIMS; do kill_node "$i"; done
echo "  killed: $(for i in $VICTIMS; do printf 'node%s ' "$i"; done)"
echo "  alive:  $(for i in $ALIVE; do printf 'node%s ' "$i"; done)"
DEAD_KNOWN=0
send "$Q" "table" >/dev/null
wait_for_log "$WORK/node$Q.log" "contacts in .* non-empty buckets" 10
for i in $VICTIMS; do
	eval "id=\$ID$i"
	if grep -q "^ *$id@" "$WORK/node$Q.log"; then DEAD_KNOWN=$((DEAD_KNOWN + 1)); fi
done
echo "  node$Q's routing table: $(grep 'contacts in .* non-empty buckets' "$WORK/node$Q.log" | tail -1 | cut -d';' -f1), $DEAD_KNOWN of them now dead"

# After the kill: repeat the same lookups until a pass has no failed RPCs
# (the querier has learned which nodes are dead), at most MAX_PASSES times.
P2_FAILED=0 P2_MS=0 P2_SLOW=0
FEW=0 TIMEOUTS=0 WRONG=0 LOOKUPS=0
RECOVERED_AT="" RECOVERY="" ELAPSED_MS=0
for ((p = 2; p <= MAX_PASSES + 1; p++)); do
	echo
	if [ "$p" -eq 2 ]; then
		echo "=== Pass 2: the same lookups, right after the kill ==="
	else
		echo "=== Pass $p: the same lookups again ==="
	fi
	run_pass "$p" "pass $p" $ALIVE
	if [ "$p" -eq 2 ]; then P2_FAILED=$PASS_FAILED P2_MS=$PASS_MS P2_SLOW=$PASS_SLOW; fi
	FEW=$((FEW + PASS_FEW)) TIMEOUTS=$((TIMEOUTS + PASS_TIMEOUT)) WRONG=$((WRONG + PASS_WRONG))
	LOOKUPS=$((LOOKUPS + TARGETS)) ELAPSED_MS=$((ELAPSED_MS + PASS_MS))
	RECOVERY="$RECOVERY  pass $p: $PASS_FAILED failed, $(fmt_ms "$PASS_MS");"
	if [ "$PASS_FAILED" -eq 0 ] && [ "$PASS_TIMEOUT" -eq 0 ]; then
		RECOVERED_AT="$p" RECOVERED_MS="$PASS_MS"
		break
	fi
done

echo
echo "=== Recovery: failed RPCs and time, pass by pass after the kill ==="
echo "$RECOVERY" | tr ';' '\n' | sed '/^ *$/d'

echo
echo "=== Verdict ==="
STATUS=0
check() { # check <ok?> <text>
	if [ "$1" = yes ]; then echo "  as predicted: $2"; else echo "  UNEXPECTED: $2"; STATUS=1; fi
}
check "$([ "$P1_FAILED" -eq 0 ] && [ "$P1_WRONG" -eq 0 ] && echo yes)" \
	"pass 1 had $P1_FAILED failed RPCs and $P1_WRONG wrong answers ($(fmt_ms "$P1_MS") in total)"
check "$([ "$P2_SLOW" -gt 0 ] && [ "$P2_MS" -ge $((P2_SLOW * RPC_TIMEOUT_MS)) ] && echo yes)" \
	"pass 2: $P2_SLOW of $TARGETS lookups hit dead nodes, $P2_FAILED failed RPCs, $(fmt_ms "$P2_MS") in total (at least one ${RPC_TIMEOUT_MS}ms timeout each)"
if [ -n "$RECOVERED_AT" ]; then
	check "$([ "$RECOVERED_AT" -gt 2 ] && echo yes)" \
		"the querier learned: by pass $RECOVERED_AT no lookup asked a dead node ($(fmt_ms "$RECOVERED_MS") for $TARGETS lookups, $(fmt_ms "$ELAPSED_MS") of lookups after the kill)"
else
	check no "the querier learned: still asking dead nodes after $MAX_PASSES passes"
fi
check "$([ "$FEW" -eq 0 ] && [ "$TIMEOUTS" -lt "$LOOKUPS" ] && echo yes)" \
	"no answer was short: every lookup that finished returned $K live nodes"
echo "  reported, not asserted: $WRONG answer(s) missed a live node no survivor mentioned;" \
	"$TIMEOUTS of $LOOKUPS lookups after the kill ran out of the console's time"

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: after half the network died, lookups paid a timeout per dead node, but no answer came back short, and by pass $RECOVERED_AT the querier had stopped asking dead nodes."
else
	echo "FAIL (repeat this exact run with SEED=$SEED)"
fi
exit "$STATUS"
