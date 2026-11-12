#!/usr/bin/env bash
#
# A bigger network that doesn't join as a chain.
#
#   N nodes (default 20) on ports BASE_PORT.. (default 9300..). Node 0 starts
#   alone; every later node bootstraps through a RANDOMLY chosen node that is
#   already running. A small k (default 4) means "the k closest" is a real
#   subset, so lookups take more than one round.
#
#   1. Lookups: several nodes look up several targets. Each answer is checked
#      against a brute-force answer this script computes itself: every
#      node ID is SHA-1("big<i>") (same as --id-seed), XOR-ed with the
#      target, sorted, and the k smallest kept (excluding the querying
#      node, which never returns itself).
#   2. A file transfer: one node shares a 1 MiB file and three others
#      download it at the same time; every copy is compared byte for byte.
#
# Usage: scripts/demo-big-network.sh   (env: N=20 K=4 BASE_PORT=9300 SEED=<number>)
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/nodes.sh

N="${N:-20}"
K="${K:-4}"
BASE_PORT="${BASE_PORT:-9300}"
SEED="${SEED:-$$}"
RANDOM="$SEED" # seed bash's RANDOM so a run can be repeated with SEED=...
SEED_PREFIX=big
LOOKUPS_FROM=4
TARGETS=3

nodes_init
echo "Starting $N nodes on ports $BASE_PORT-$((BASE_PORT + N - 1)), k=$K, random bootstrap (SEED=$SEED)..."
start_node 0 "$BASE_PORT" - --k "$K"
JOINS="node0: first node"
for ((i = 1; i < N; i++)); do
	via=$((RANDOM % i))
	start_node "$i" $((BASE_PORT + i)) $((BASE_PORT + via)) --k "$K"
	JOINS="$JOINS, node$i<-node$via"
done
echo "  joins: $JOINS"

# xor_hex <a> <b>: XOR of two 40-hex-digit IDs, as 40 hex digits (8 at a
# time, since bash integers are 64-bit). Equal-length hex strings sort in
# numeric order, so plain `sort` ranks distances correctly.
xor_hex() {
	local a="$1" b="$2" out="" j
	for j in 0 8 16 24 32; do
		out="$out$(printf '%08x' $((0x${a:j:8} ^ 0x${b:j:8})))"
	done
	echo "$out"
}

# expected_closest <querier> <target>: brute-force k closest node IDs.
expected_closest() {
	local q="$1" target="$2" i id
	for ((i = 0; i < N; i++)); do
		[ "$i" -eq "$q" ] && continue
		id="$(node_id "big$i")"
		echo "$(xor_hex "$id" "$target") $id"
	done | sort | head -n "$K" | awk '{print $2}' | sort
}

# lookup_result <i> <target>: IDs node i's console printed for that lookup.
lookup_result() {
	awk -v hdr="lookup $2:" -v k="$K" '
		index($0, hdr) == 1 { on = 1; n = 0; next }
		on && /^time=/ { next }                       # log lines can interleave
		on && /^ +[0-9]+\. / { split($2, a, "@"); print a[1]; if (++n == k) exit; next }
		on { exit }' "$WORK/node$1.log" | sort
}

echo
echo "=== Lookups checked against a brute-force answer ==="
STATUS=0
MULTI_ROUND=0
QUERIERS=""
while [ "$(echo $QUERIERS | wc -w)" -lt "$LOOKUPS_FROM" ]; do
	q=$((RANDOM % N))
	case " $QUERIERS " in *" $q "*) ;; *) QUERIERS="$QUERIERS $q" ;; esac
done
for q in $QUERIERS; do
	for ((t = 1; t <= TARGETS; t++)); do
		target="$(node_id "target-$SEED-$q-$t")"
		send "$q" "lookup $target" >/dev/null
		wait_for_log "$WORK/node$q.log" "^lookup $target:" 20
		summary="$(grep "^lookup $target:" "$WORK/node$q.log" | head -1 | cut -d: -f2-)"
		rounds="$(echo "$summary" | grep -o 'rounds=[0-9]*' | cut -d= -f2)"
		[ "$rounds" -le 1 ] || MULTI_ROUND=$((MULTI_ROUND + 1))
		if [ "$(lookup_result "$q" "$target")" = "$(expected_closest "$q" "$target")" ]; then
			verdict="ok"
		else
			verdict="MISMATCH"
			STATUS=1
		fi
		printf '  node%-2s -> %s…:%s  %s\n' "$q" "${target:0:8}" "$summary" "$verdict"
		if [ "$verdict" = MISMATCH ]; then
			echo "    expected: $(expected_closest "$q" "$target" | cut -c1-8 | tr '\n' ' ')"
			echo "    got:      $(lookup_result "$q" "$target" | cut -c1-8 | tr '\n' ' ')"
		fi
	done
done
echo "  $MULTI_ROUND of $((LOOKUPS_FROM * TARGETS)) lookups needed more than one round"

echo
echo "=== A file transfer across the network ==="
PICK=""
while [ "$(echo $PICK | wc -w)" -lt 4 ]; do
	n=$((RANDOM % N))
	case " $PICK " in *" $n "*) ;; *) PICK="$PICK $n" ;; esac
done
set -- $PICK
SEEDER="$1"
shift
LEECHERS="$*"
head -c 1048576 /dev/urandom >"$WORK/original.bin"
send "$SEEDER" "share $WORK/original.bin"
wait_for_log "$WORK/node$SEEDER.log" "content key:" 20
KEY="$(grep "content key:" "$WORK/node$SEEDER.log" | awk '{print $3}')"
for i in $LEECHERS; do
	send "$i" "fetch $KEY $WORK/node$i.bin"
done
for i in $LEECHERS; do
	wait_for_log "$WORK/node$i.log" '^fetch' 60
	if cmp -s "$WORK/original.bin" "$WORK/node$i.bin"; then
		echo "  node$i: $(grep '^fetched' "$WORK/node$i.log" | head -1 | sed -E 's/ into [^ ]+//')  identical"
	else
		echo "  node$i: FAILED: $(grep '^fetch' "$WORK/node$i.log" | head -1)"
		STATUS=1
	fi
done

echo
if [ "$STATUS" -eq 0 ] && [ "$MULTI_ROUND" -gt 0 ]; then
	echo "PASS: $N nodes, random joins, every lookup returned the true $K closest nodes, and the file reached all leechers intact."
elif [ "$STATUS" -eq 0 ]; then
	echo "FAIL: lookups were correct, but none needed more than one round; use a smaller K or a larger N."
	STATUS=1
else
	echo "FAIL (repeat this exact run with SEED=$SEED)"
fi
exit "$STATUS"
