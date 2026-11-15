#!/usr/bin/env bash
#
# Edge case A3: two islands that know each other only through one bridge
# node, and the bridge dies.  (docs/EDGE-CASES.md, case A3)
#
# Two separate networks run side by side, built the same way:
#
#   island A: a0 starts alone; a1..a4 join via a0
#   island B: b0 starts alone; b1..b4 join via b0      (never told about A)
#   bridge X: joins island A via a0, then PINGs b0. Now X and b0 know each
#             other, and every A node knows X. Nobody else crosses over.
#
#   "quiet"     network: nobody looks anything up across the bridge. X dies.
#   "used"      network: before X dies, a3 and b3 each do ONE lookup for a
#                        node on the other island, through the bridge.
#   "refreshed" network: like quiet, but every node refreshes its buckets
#                        every REFRESH (default 3s; paper Sec. 2.3,
#                        refresh.go). X dies after every node has refreshed
#                        twice. (Built after the quiet network is gone, on
#                        its ports.)
#
# After X is killed, a3 looks up b3's ID and b3 looks up a3's ID, twice.
# k is the default 20, larger than the network (11 nodes), so a correct
# lookup returns EVERY live node: the brute-force answer is "all of them".
#
# Prediction:
#   - quiet: before the kill, `table` shows no A node knows any B node, and
#     only b0 (of B) knows X. After the kill the islands are cut apart for
#     good: each lookup finds 0 of the 5 nodes on the other island, pays a
#     2s timeout on the dead bridge, and the repeat is the same. The
#     periodic refresh is hourly by default, so none runs in time, and
#     once X is dead no refresh could help: there's no path left.
#   - used: the islands survive the kill. A lookup contacts nodes directly,
#     and every node it contacts records the querier (touchContact), so the
#     one lookup through the bridge already connected the islands.
#   - refreshed: the islands survive the kill, like "used", with no
#     lookup from anyone. A refresh is a lookup for a random ID in each
#     bucket; X's refresh asks b0 and so learns island B, and A nodes'
#     refreshes then ask X and reach B. Refresh makes every bridge "used".
# The script PASSes when all three hold.
#
# Usage: scripts/edge/a3-bridge-between-islands.sh   (env: BASE_PORT=9450 REFRESH=3s)
set -euo pipefail
cd "$(dirname "$0")/../.."
. scripts/lib/nodes.sh
. scripts/lib/edge.sh

BASE_PORT="${BASE_PORT:-9450}"
SEED_PREFIX=edgea3-
SIZE=11 # nodes per network: offset+0..4 = island A, +5..9 = island B, +10 = bridge X
QUIET=0
USED=11
REFRESHED=22 # reuses the quiet network's ports once it's gone
N=33
REFRESH="${REFRESH:-3s}"
nodes_init
node_ids_init "$N"

port() { # the refreshed network (22..32) reuses ports 0..10
	if [ "$1" -ge "$REFRESHED" ]; then echo $((BASE_PORT + $1 - REFRESHED)); else echo $((BASE_PORT + $1)); fi
}
name() { # name <i>: "a3", "b0", "X" (which network is clear from context)
	local r=$(($1 % SIZE))
	if [ "$r" -lt 5 ]; then echo "a$r"; elif [ "$r" -lt 10 ]; then echo "b$((r - 5))"; else echo "X"; fi
}

# build <offset> <label> [node flags...]: start one network: two
# islands and a bridge.
build() {
	local o="$1" label="$2" i
	shift 2
	echo "  [$label] island A: ports $(port "$o")-$(port $((o + 4))), island B: $(port $((o + 5)))-$(port $((o + 9))), bridge X: $(port $((o + 10)))"
	start_node "$o" "$(port "$o")" - "$@"
	for i in 1 2 3 4; do start_node $((o + i)) "$(port $((o + i)))" "$(port "$o")" "$@"; done
	start_node $((o + 5)) "$(port $((o + 5)))" - "$@"
	for i in 6 7 8 9; do start_node $((o + i)) "$(port $((o + i)))" "$(port $((o + 5)))" "$@"; done
	start_node $((o + 10)) "$(port $((o + 10)))" "$(port "$o")" "$@" # X joins A
	send $((o + 10)) "ping 127.0.0.1:$(port $((o + 5)))" >/dev/null # ...and greets b0
	wait_for_log "$WORK/node$((o + 10)).log" '^PONG from\|^no reply from' 10
}

# knows <i> <j>: node i's (single) `table` output lists node j.
knows() {
	local id
	eval "id=\$ID$2"
	grep -q "^    $id@" "$WORK/node$1.log"
}

# cross_lookup <querier> <target-node> <offset>: node querier looks up the
# target node's ID. Prints a row and sets FOUND_OTHER (nodes of the other
# island in the answer) and FOUND_ALL (all nodes in the answer).
ISSUED_ON=""
cross_lookup() {
	local q="$1" t="$2" o="$3" id nth issued start ms summary got i r mine
	eval "id=\$ID$t"
	nth=$(($(count_lines "$WORK/node$q.log" "^lookup $id:") + 1))
	issued=$(($(count_lines "$WORK/node$q.log" '^lookup [0-9a-f]\{40\}:\|^lookup failed') + 1))
	start="$(now_ms)"
	send "$q" "lookup $id" >/dev/null
	wait_for_count "$WORK/node$q.log" '^lookup [0-9a-f]\{40\}:\|^lookup failed' "$issued" 30
	ms=$(($(now_ms) - start))
	summary="$(grep "^lookup $id:" "$WORK/node$q.log" | sed -n "${nth}p" | cut -d: -f2-)"
	got="$(lookup_answer "$q" "$id" "$nth" | while read -r x; do index_of_id "$x" "$N"; done)"
	mine=$(((q - o) / 5)) # 0 = island A, 1 = island B
	FOUND_OTHER=0
	FOUND_ALL=0
	for i in $got; do
		FOUND_ALL=$((FOUND_ALL + 1))
		r=$((i - o))
		if [ "$r" -lt 10 ] && [ $((r / 5)) -ne "$mine" ]; then FOUND_OTHER=$((FOUND_OTHER + 1)); fi
	done
	printf '    %s looks up %s:%s %7s  found %d of 5 on the other island, %d nodes in all\n' \
		"$(name "$q")" "$(name "$t")" "$summary" "$(fmt_ms "$ms")" "$FOUND_OTHER" "$FOUND_ALL"
}

echo "Starting two networks of $SIZE nodes, k=20 (the default); a third comes later..."
build "$QUIET" quiet
build "$USED" used
STATUS=0
check() { # check <ok?> <text>
	if [ "$1" = yes ]; then echo "  as predicted: $2"; else echo "  UNEXPECTED: $2"; STATUS=1; fi
}

echo
echo "=== quiet: who knows whom before the bridge dies (from \`table\`) ==="
CROSS=0
for i in 1 2 3 4 6 7 8 9 5; do # every island node; b0 last
	send $((QUIET + i)) "table" >/dev/null
	wait_for_log "$WORK/node$((QUIET + i)).log" "contacts in .* non-empty buckets" 10
	n=0
	for j in 0 1 2 3 4 5 6 7 8 9 10; do
		if [ $((i / 5)) -ne $((j / 5)) ] && knows $((QUIET + i)) $((QUIET + j)); then n=$((n + 1)); fi
	done
	[ "$i" -eq 5 ] || CROSS=$((CROSS + n))
	echo "    $(name "$i") knows $n node(s) outside its island"
done
check "$([ "$CROSS" -eq 4 ] && knows $((QUIET + 5)) $((QUIET + 10)) && echo yes)" \
	"each A node knows only X across the gap ($CROSS in total from a1-a4, b1-b4); among B only b0 knows X"

echo
echo "=== used: one lookup across the bridge from each side, before it dies ==="
cross_lookup $((USED + 8)) $((USED + 3)) "$USED"
U_B=$FOUND_OTHER
cross_lookup $((USED + 3)) $((USED + 8)) "$USED"
U_A=$FOUND_OTHER
check "$([ "$U_B" -eq 5 ] && [ "$U_A" -eq 5 ] && echo yes)" "the bridge works: both lookups found all 5 nodes on the other island"

echo
echo "=== Killing the bridge X in both networks (SIGKILL) ==="
kill_node $((QUIET + 10))
kill_node $((USED + 10))

echo
echo "=== quiet: lookups across the gap, twice ==="
Q_TOTAL=0
for pass in 1 2; do
	cross_lookup $((QUIET + 8)) $((QUIET + 3)) "$QUIET"
	Q_TOTAL=$((Q_TOTAL + FOUND_OTHER))
	cross_lookup $((QUIET + 3)) $((QUIET + 8)) "$QUIET"
	Q_TOTAL=$((Q_TOTAL + FOUND_OTHER))
done
check "$([ "$Q_TOTAL" -eq 0 ] && echo yes)" "4 lookups found $Q_TOTAL nodes across the gap: the islands are cut apart, and stay that way"

echo
echo "=== used: the same lookups ==="
U_TOTAL=0
for pass in 1 2; do
	cross_lookup $((USED + 8)) $((USED + 3)) "$USED"
	U_TOTAL=$((U_TOTAL + FOUND_OTHER))
	cross_lookup $((USED + 3)) $((USED + 8)) "$USED"
	U_TOTAL=$((U_TOTAL + FOUND_OTHER))
done
check "$([ "$U_TOTAL" -eq 20 ] && echo yes)" "4 lookups found $U_TOTAL of 20 possible across the gap: one lookup through the bridge had already connected the islands"

echo
echo "=== refreshed: the quiet setup again, with --refresh-interval $REFRESH, and nobody looking anything up ==="
for i in 0 1 2 3 4 5 6 7 8 9; do kill_node $((QUIET + i)); done # free the quiet network's ports
build "$REFRESHED" refreshed --refresh-interval "$REFRESH"
for i in 0 1 2 3 4 5 6 7 8 9 10; do # two periodic refreshes everywhere, after X greeted b0
	wait_for_count "$WORK/node$((REFRESHED + i)).log" 'msg="buckets refreshed" .*reason=periodic' 2 60
done
echo "    every node has refreshed twice:" \
	"$(grep -h 'msg="buckets refreshed" .*reason=periodic' "$WORK/node$((REFRESHED + 3)).log" | tail -1 | grep -o 'buckets=.*') (a3's latest)"
R_CROSS=0
for i in 1 2 3 4; do
	send $((REFRESHED + i)) "table" >/dev/null
	wait_for_log "$WORK/node$((REFRESHED + i)).log" "contacts in .* non-empty buckets" 10
	for j in 5 6 7 8 9; do
		if knows $((REFRESHED + i)) $((REFRESHED + j)); then R_CROSS=$((R_CROSS + 1)); fi
	done
done
echo "    a1-a4 now know $R_CROSS of the 20 (a, b) pairs across the gap (quiet: 0)"
kill_node $((REFRESHED + 10))
echo "    killed the bridge X"
R_TOTAL=0
for pass in 1 2; do
	cross_lookup $((REFRESHED + 8)) $((REFRESHED + 3)) "$REFRESHED"
	R_TOTAL=$((R_TOTAL + FOUND_OTHER))
	cross_lookup $((REFRESHED + 3)) $((REFRESHED + 8)) "$REFRESHED"
	R_TOTAL=$((R_TOTAL + FOUND_OTHER))
done
check "$([ "$R_TOTAL" -eq 20 ] && echo yes)" "4 lookups found $R_TOTAL of 20 possible across the gap: refresh had connected the islands before the bridge died"

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: an unused bridge's death split the network for good, unless one lookup had crossed it or bucket refresh had run; then it could die harmlessly."
else
	echo "FAIL"
fi
exit "$STATUS"
