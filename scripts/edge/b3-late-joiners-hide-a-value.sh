#!/usr/bin/env bash
#
# Edge case B3: nodes that join late, with IDs closer to a key than the
# nodes holding its value, make the value hard to find, though nothing
# died.  (docs/EDGE-CASES.md, case B3)
#
#   N nodes (default 8), k=2, all joining via node0. node1 `put`s a short
#   text; it lands on the 2 nodes closest to the key SHA-1(name).
#
#   1. Control: every other node `get`s it.
#   2. LATE nodes (default 4) join, each with an ID closer to the key than
#      either holder. The IDs are found by brute force: the script tries
#      seeds until SHA-1(seed) is close enough, and starts the node with
#      that --id-seed. Now the 2 closest nodes to the key are newcomers
#      that have never heard of the value. The holders are still alive.
#   3. Every node `get`s it again.
#   4. node1 `put`s the same text again (a manual "republish"), and the
#      newcomers `get` it once more.
#
# Prediction:
#   - step 1: everyone finds it.
#   - step 3: newcomers MISS it, though both holders are alive. A get
#     starts from the k=2 closest contacts in the node's own table. When
#     those are other newcomers, they don't have the value and only point
#     at each other, so the FIND_VALUE lookup never reaches a holder.
#     Values never move to closer nodes that join later (no republish:
#     store.go, SIMPLIFICATION; paper Sec. 2.5).
#     Not EVERY newcomer misses: one whose table happens to lack the other
#     newcomers still has a holder among its 2 closest contacts. The first
#     run predicted "all miss" and was wrong (see docs/EDGE-CASES.md), so
#     the check is "at least one newcomer misses".
#   - step 4: the re-put's lookup finds the 2 closest nodes (newcomers),
#     the value lands on them, and every newcomer finds it.
# The script PASSes when all three hold.
#
# Usage: scripts/edge/b3-late-joiners-hide-a-value.sh
#        (env: N=8 LATE=4 BASE_PORT=9550 SEED=1)
set -euo pipefail
cd "$(dirname "$0")/../.."
. scripts/lib/nodes.sh
. scripts/lib/edge.sh

N="${N:-8}"
LATE="${LATE:-4}"
K=2
BASE_PORT="${BASE_PORT:-9550}"
SEED="${SEED:-1}" # picks the value's name (so its key) and the late joiners' seeds
SEED_PREFIX=edgeb3-
PUTTER=1
TOTAL=$((N + LATE))

nodes_init
node_ids_init "$N"
ORIGINAL="$(seq 0 $((N - 1)) | tr '\n' ' ')"

# The key is SHA-1(name), so it can be chosen before anything is stored.
# Pick a name whose key the putter isn't among the K closest to, so the
# value lives on two OTHER nodes (the putter's own copy wouldn't be logged).
for ((try = 1; ; try++)); do
	NAME="b3-$SEED-$try"
	KEY="$(node_id "$NAME")"
	HOLDERS="$(closest_nodes "$KEY" "$K" $ORIGINAL | tr '\n' ' ')"
	in_list "$PUTTER" $HOLDERS || break
done
HOLDERS="$(echo $HOLDERS)"

echo "Starting $N nodes on ports $BASE_PORT-$((BASE_PORT + N - 1)), k=$K, all joining via node0 (SEED=$SEED)..."
start_node 0 "$BASE_PORT" - --k "$K"
for ((i = 1; i < N; i++)); do
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k "$K"
done

# get_on <i>: node i runs `get $NAME`; sets GOT ("yes"/"no") and prints a row.
get_on() {
	local i="$1" n line
	n=$(($(count_lines "$WORK/node$i.log" "^get $NAME:") + 1))
	send "$i" "get $NAME" >/dev/null
	wait_for_count "$WORK/node$i.log" "^get $NAME:\|^get failed" "$n" 30
	line="$(grep "^get $NAME:\|^get failed" "$WORK/node$i.log" | sed -n "${n}p")"
	case "$line" in *": 0 value(s)"* | "get failed"*) GOT=no ;; *) GOT=yes ;; esac
	printf '    node%-2s %-5s %s\n' "$i" "$([ "$GOT" = yes ] && echo found || echo MISS)" "${line#get $NAME: }"
}

# put_contacts <nth>: the node IDs the putter's nth `put $NAME` printed
# under its header. Interleaved log lines (time=…) are skipped, as in
# edge.sh's lookup_answer.
put_contacts() {
	awk -v hdr="put $NAME " -v nth="$1" '
		index($0, hdr) == 1 { seen++; on = (seen == nth); next }
		on && /^time=/ { next }
		on && /^    [0-9a-f]+@/ { split($1, a, "@"); print a[1]; next }
		on { exit }' "$WORK/node$PUTTER.log"
}

echo
echo "=== node$PUTTER puts a value under SHA-1(\"$NAME\") ==="
send "$PUTTER" "put $NAME hello from edge case B3"
wait_for_log "$WORK/node$PUTTER.log" "^put $NAME " 20
echo "  key:                 $KEY"
echo "  expected holders:    $(for i in $HOLDERS; do printf 'node%s ' "$i"; done) (brute force: the $K closest IDs)"
echo "  holders (from logs): $(for i in $(holders_of "$KEY" "$N"); do printf 'node%s ' "$i"; done)"
STATUS=0
check() { # check <ok?> <text>
	if [ "$1" = yes ]; then echo "  as predicted: $2"; else echo "  UNEXPECTED: $2"; STATUS=1; fi
}
check "$([ "$(holders_of "$KEY" "$N")" = "$(echo $HOLDERS | tr ' ' '\n' | sort -n | tr '\n' ' ' | sed 's/ $//')" ] && echo yes)" \
	"the value landed exactly on the $K closest nodes"

echo
echo "=== Step 1 (control): every other node gets it ==="
FOUND=0 ASKED=0
for i in $ORIGINAL; do
	in_list "$i" $HOLDERS && continue
	get_on "$i"
	ASKED=$((ASKED + 1))
	[ "$GOT" = no ] || FOUND=$((FOUND + 1))
done
check "$([ "$FOUND" -eq "$ASKED" ] && echo yes)" "$FOUND of $ASKED found it"

echo
echo "=== Step 2: $LATE nodes join, each closer to the key than both holders ==="
# The closer of the two holders sets the bar: a newcomer must beat it.
eval "bar=\$ID$(echo $HOLDERS | awk '{print $1}')"
BAR="$(xor_hex "$bar" "$KEY")"
n=0
for ((i = N; i < TOTAL; i++)); do
	while :; do
		seed="edgeb3-late-$SEED-$n"
		n=$((n + 1))
		id="$(node_id "$seed")"
		[[ "$(xor_hex "$id" "$KEY")" < "$BAR" ]] && break
	done
	eval "ID$i=$id"
	# start_node passes --id-seed edgeb3-<i>; the second --id-seed wins
	# (Go's flag package keeps the last value).
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k "$K" --id-seed "$seed"
	echo "    node$i joined: id ${id:0:8}…  distance to key $(xor_hex "$id" "$KEY" | cut -c1-8)…  (seed \"$seed\")"
done
echo "    the bar: holder node$(echo $HOLDERS | awk '{print $1}') is at distance ${BAR:0:8}…  ($n seeds tried)"
NEWHOLDERS_EXPECTED="$(closest_nodes "$KEY" "$K" $(seq 0 $((TOTAL - 1))) | tr '\n' ' ')"
echo "    the $K closest nodes to the key are now: $(for i in $NEWHOLDERS_EXPECTED; do printf 'node%s ' "$i"; done)"

echo
echo "=== Step 3: everyone gets it again (the holders are still alive) ==="
OLD_FOUND=0 OLD_ASKED=0 NEW_FOUND=0
for ((i = 0; i < TOTAL; i++)); do
	in_list "$i" $HOLDERS && continue
	get_on "$i"
	if [ "$i" -lt "$N" ]; then
		OLD_ASKED=$((OLD_ASKED + 1))
		[ "$GOT" = no ] || OLD_FOUND=$((OLD_FOUND + 1))
	else
		[ "$GOT" = no ] || NEW_FOUND=$((NEW_FOUND + 1))
	fi
done
echo "  original nodes: $OLD_FOUND of $OLD_ASKED found it (reported, not asserted)"
check "$([ "$NEW_FOUND" -lt "$LATE" ] && echo yes)" "$((LATE - NEW_FOUND)) of $LATE newcomers missed it, though both holders are alive"

echo
echo "=== Step 4: node$PUTTER puts the same value again (a manual republish) ==="
PUTS="$(count_lines "$WORK/node$PUTTER.log" "^put $NAME ")"
send "$PUTTER" "put $NAME hello from edge case B3"
wait_for_count "$WORK/node$PUTTER.log" "^put $NAME " $((PUTS + 1)) 20
# The console prints the header, then one line per node it stored on, as
# separate writes, and log lines from other goroutines can land in between
# (e.g. an eviction PING). So: skip log lines, and wait until the block has
# as many contacts as the header announced ("stored on N other node(s)").
WANT="$(grep "^put $NAME " "$WORK/node$PUTTER.log" | sed -n "$((PUTS + 1))p" | sed 's/.*stored on \([0-9]*\) other.*/\1/')"
for ((w = 0; w < 50; w++)); do
	IDS="$(put_contacts "$((PUTS + 1))")"
	[ "$(echo "$IDS" | grep -c .)" -ge "$WANT" ] && break
	sleep 0.1
done
STORED_AT="$(for id in $IDS; do index_of_id "$id" "$TOTAL"; done | sort -n | tr '\n' ' ')"
echo "  stored on:  $(for i in $STORED_AT; do printf 'node%s ' "$i"; done)"
check "$([ "$(echo $STORED_AT)" = "$(echo $NEWHOLDERS_EXPECTED | tr ' ' '\n' | sort -n | tr '\n' ' ' | sed 's/ $//')" ] && echo yes)" \
	"the re-put reached the $K closest nodes, which are newcomers"
NEW_FOUND=0
for ((i = N; i < TOTAL; i++)); do
	in_list "$i" $NEWHOLDERS_EXPECTED && continue
	get_on "$i"
	[ "$GOT" = no ] || NEW_FOUND=$((NEW_FOUND + 1))
done
ASKED=$((LATE - $(echo $NEWHOLDERS_EXPECTED | wc -w)))
check "$([ "$NEW_FOUND" -eq "$ASKED" ] && echo yes)" "$NEW_FOUND of $ASKED other newcomers find it now"

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: newcomers closer to the key missed a value whose holders were alive; re-putting moved it to them."
else
	echo "FAIL (repeat this exact run with SEED=$SEED)"
fi
exit "$STATUS"
