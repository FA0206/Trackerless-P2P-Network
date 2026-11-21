#!/usr/bin/env bash
#
# "After" demo: the seeder dies mid-download, and the file SURVIVES.
#
#   node1 shares a 1 MiB file (16 pieces). Three leechers (node2, node3,
#   node4) start downloading it one after another. Then node1 is killed
#   (SIGKILL) while every leecher is still mid-download -- at the first moment
#   the leechers TOGETHER hold every piece, though none of them holds all.
#
# THE "BEFORE" VERSION LIVES ON main: there, the same scenario loses the file
# (downloaders only announce complete files and fetch pieces in order, so
# the leechers' pieces overlap and can never cover the file before one of
# them finishes). The script keeps its name on this branch so that
#   git diff main feature/parallel-partial-seeding -- scripts/demo-seeder-death.sh
# shows exactly what changed: the kill rule and the verdict.
#
# On this branch downloaders fetch rarest-first and serve pieces as soon as
# they have them, so after the seeder dies the leechers
# finish from each other. This demo PASSES when every leecher ends up with a
# byte-identical copy although no node held the whole file when the seeder
# died.
#
# Timing: every node runs with the DEBUG ONLY --debug-serve-delay (off by
# default), and the script watches logs instead of
# sleeping. Kill rule: as soon as the leechers together hold every piece,
# never after any leecher has finished. (main's version has a fallback --
# "or the leading leecher has 3/4 of the pieces" -- because there the
# leechers can never together hold every piece before one finishes. Here the
# fallback would only kill the seeder before the pieces had spread.)
#
# Each leecher fetches ONE piece at a time (--max-inflight 1). Rarest-first
# then sends that one request to a piece only the seeder has, so the swarm
# as a whole runs ahead of every individual leecher and there is a clear
# moment when the pieces have spread but nobody has them all. With more
# parallelism the leechers copy each other so fast that "together they hold
# everything" and "someone holds everything" arrive almost at once
# (scripts/demo-parallel.sh shows the parallelism itself).
#
# Usage: scripts/demo-seeder-death.sh   (env: BASE_PORT=9200 DELAY=150ms MAXINFLIGHT=1)
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/nodes.sh

BASE_PORT="${BASE_PORT:-9200}"
DELAY="${DELAY:-150ms}"
MAXINFLIGHT="${MAXINFLIGHT:-1}"
K=3
PIECES=16
LEECHERS="2 3 4"
SEED_PREFIX=demo

nodes_init
echo "Starting 5 nodes on ports $BASE_PORT-$((BASE_PORT + 4)) (k=$K, --debug-serve-delay $DELAY, --max-inflight $MAXINFLIGHT)..."
start_node 0 "$BASE_PORT" - --k "$K" --debug-serve-delay "$DELAY" --max-inflight "$MAXINFLIGHT"
for i in 1 2 3 4; do
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k "$K" --debug-serve-delay "$DELAY" --max-inflight "$MAXINFLIGHT"
done

head -c $((PIECES * 65536)) /dev/urandom >"$WORK/original.bin"
echo
echo "=== node1 (the seeder) shares a $PIECES-piece file ==="
send 1 "share $WORK/original.bin"
wait_for_log "$WORK/node1.log" "content key:" 10
KEY="$(grep "content key:" "$WORK/node1.log" | awk '{print $3}')"
echo "  content key: $KEY"

count() { pieces_received "$1" | wc -l | tr -d ' '; }
wait_pieces() { # wait_pieces <i> <n>: until node i has received n pieces
	local waited=0
	while [ "$(count "$1")" -lt "$2" ]; do
		sleep 0.1
		waited=$((waited + 1))
		[ "$waited" -lt 300 ] || { echo "node$1 never reached $2 pieces" >&2; exit 1; }
	done
}

echo
echo "=== Three leechers start, staggered so they're at different points ==="
send 2 "fetch $KEY $WORK/node2.bin"
wait_pieces 2 4
send 3 "fetch $KEY $WORK/node3.bin"
wait_pieces 3 2
send 4 "fetch $KEY $WORK/node4.bin"
wait_pieces 4 1

union_count() { for i in $LEECHERS; do pieces_received "$i"; done | sort -n -u | wc -l | tr -d ' '; }
echo
echo "=== Waiting for the kill moment ==="
REASON=""
waited=0
while [ -z "$REASON" ]; do
	# One snapshot per check: a leecher with all pieces counts as finished
	# even before its console prints "fetched".
	FINISHED=""
	AT_KILL=""
	for i in $LEECHERS; do
		c="$(count "$i")"
		AT_KILL="$AT_KILL node$i:$c/$PIECES"
		if [ "$c" -ge "$PIECES" ] || grep -q '^fetched ' "$WORK/node$i.log"; then FINISHED="node$i"; fi
	done
	if [ -n "$FINISHED" ]; then
		echo "FAIL: $FINISHED finished before the leechers together held every piece; the timing assumption broke."
		exit 1
	fi
	if [ "$(union_count)" -ge "$PIECES" ]; then
		REASON="the leechers together hold all $PIECES pieces"
	fi
	sleep 0.05
	waited=$((waited + 1))
	[ "$waited" -lt 1200 ] || { echo "FAIL: kill moment never came" >&2; exit 1; }
done
echo "  kill moment: $REASON; none has finished ($AT_KILL )"

echo
echo "=== Killing the seeder (node1, SIGKILL); the leechers carry on from each other ==="
kill_node 1
for i in $LEECHERS; do
	wait_for_log "$WORK/node$i.log" '^fetch' 90
done
for i in $LEECHERS; do
	echo "  node$i: $(grep '^fetch' "$WORK/node$i.log" | head -1 | sed -E 's/ into [^ ]+//')"
	awk '/^fetch/{on=1;next} on && /^    /{print "  " $0; next} on{exit}' "$WORK/node$i.log"
done

# ranges: "0 1 2 5" on stdin -> "0-2,5"
ranges() {
	awk 'NR==1{s=$1;p=$1;next} $1==p+1{p=$1;next} {printf "%s%s,",s,(s==p?"":"-"p);s=$1;p=$1}
	     END{if(NR) printf "%s%s",s,(s==p?"":"-"p); else printf "none"}'
}
echo
echo "=== Pieces each leecher received in total, from anyone (from the logs) ==="
for i in $LEECHERS; do
	echo "  node$i: $(count "$i")/$PIECES pieces  [$(pieces_received "$i" | ranges)]"
done
echo "  together: $(union_count)/$PIECES pieces  [$(for i in $LEECHERS; do pieces_received "$i"; done | sort -n -u | ranges)]"

echo
echo "=== Verdict (this branch: the file is expected to survive) ==="
STATUS=0
for i in $LEECHERS; do
	if grep -q '^fetched' "$WORK/node$i.log" && cmp -s "$WORK/original.bin" "$WORK/node$i.bin"; then
		echo "  node$i: complete, byte-identical to the original"
	else
		echo "  node$i: NOT complete: $(grep '^fetch' "$WORK/node$i.log" | head -1)"
		STATUS=1
	fi
done
echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: the seeder died before any leecher finished and no node had the whole file, yet every leecher completed it from the others."
else
	echo "FAIL"
fi
exit "$STATUS"
