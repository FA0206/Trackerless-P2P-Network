#!/usr/bin/env bash
#
# "Before" demo: the seeder dies mid-download, and the file is LOST.
#
#   node1 shares a 1 MiB file (16 pieces). Three leechers (node2, node3,
#   node4) start downloading it one after another. Then node1 is killed
#   (SIGKILL) while every leecher is still mid-download.
#
# On this branch (main) a downloader only announces itself once it has the
# whole file, and a failed download deletes its partial output. So when the
# only complete copy disappears, nobody can finish, and the pieces that were
# already sent are thrown away. This demo PASSES when exactly that happens:
# every leecher reports "fetch failed" and no output file is left behind.
#
# Branch feature/parallel-partial-seeding changes the downloader so that the
# same scenario succeeds; there, this script's expectation is flipped.
#
# Timing: localhost transfers take milliseconds, so every node runs with
# --debug-serve-delay (a DEBUG ONLY flag, off by default; docs/DEBUGGING.md)
# and the script decides when to kill by watching the logs, never by
# sleeping. Kill rule: as soon as the leechers TOGETHER hold every piece, or
# the leading leecher has 3/4 of them, whichever comes first -- and never
# after any leecher has finished.
#
# Usage: scripts/demo-seeder-death.sh   (env: BASE_PORT=9200 DELAY=150ms)
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/nodes.sh

BASE_PORT="${BASE_PORT:-9200}"
DELAY="${DELAY:-150ms}"
K=3
PIECES=16
LEECHERS="2 3 4"
SEED_PREFIX=demo

nodes_init
echo "Starting 5 nodes on ports $BASE_PORT-$((BASE_PORT + 4)) (k=$K, --debug-serve-delay $DELAY)..."
start_node 0 "$BASE_PORT" - --k "$K" --debug-serve-delay "$DELAY"
for i in 1 2 3 4; do
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k "$K" --debug-serve-delay "$DELAY"
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
leading_count() {
	local best=0 n
	for i in $LEECHERS; do
		n="$(count "$i")"
		[ "$n" -le "$best" ] || best="$n"
	done
	echo "$best"
}
any_finished() {
	for i in $LEECHERS; do
		if grep -q '^fetched ' "$WORK/node$i.log"; then return 0; fi
	done
	return 1
}

echo
echo "=== Waiting for the kill moment ==="
REASON=""
waited=0
while [ -z "$REASON" ]; do
	if any_finished; then
		echo "FAIL: a leecher finished before the kill moment; the timing assumption broke (try a larger DELAY)."
		exit 1
	fi
	if [ "$(union_count)" -ge "$PIECES" ]; then
		REASON="the leechers together hold all $PIECES pieces"
	elif [ "$(leading_count)" -ge $((PIECES * 3 / 4)) ]; then
		REASON="the leading leecher has $((PIECES * 3 / 4)) of $PIECES pieces"
	fi
	sleep 0.05
	waited=$((waited + 1))
	[ "$waited" -lt 1200 ] || { echo "FAIL: kill moment never came" >&2; exit 1; }
done
echo "  kill moment: $REASON"

echo
echo "=== Killing the seeder (node1, SIGKILL) ==="
kill_node 1
for i in $LEECHERS; do
	wait_for_log "$WORK/node$i.log" '^fetch' 60
done
for i in $LEECHERS; do
	echo "  node$i: $(grep '^fetch' "$WORK/node$i.log" | head -1)"
done

# ranges: "0 1 2 5" on stdin -> "0-2,5"
ranges() {
	awk 'NR==1{s=$1;p=$1;next} $1==p+1{p=$1;next} {printf "%s%s,",s,(s==p?"":"-"p);s=$1;p=$1}
	     END{if(NR) printf "%s%s",s,(s==p?"":"-"p); else printf "none"}'
}
echo
echo "=== What had spread when the seeder died (from the leechers' logs) ==="
for i in $LEECHERS; do
	echo "  node$i: $(count "$i")/$PIECES pieces  [$(pieces_received "$i" | ranges)]"
done
echo "  together: $(union_count)/$PIECES pieces  [$(for i in $LEECHERS; do pieces_received "$i"; done | sort -n -u | ranges)]"

echo
echo "=== Verdict (main: the file is expected to be lost) ==="
STATUS=0
for i in $LEECHERS; do
	if grep -q '^fetch failed' "$WORK/node$i.log" && [ ! -e "$WORK/node$i.bin" ]; then
		echo "  node$i: download failed and left no partial file  (expected)"
	else
		echo "  node$i: UNEXPECTED: $(grep '^fetch' "$WORK/node$i.log" | head -1); file exists: $([ -e "$WORK/node$i.bin" ] && echo yes || echo no)"
		STATUS=1
	fi
done
echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: the seeder died before any leecher finished, and the file was lost -- the pieces already sent were thrown away."
else
	echo "FAIL"
fi
exit "$STATUS"
