#!/usr/bin/env bash
#
# Giving up keeps seeding; fetching again resumes.
#
#   node0 shares a 2 MiB file (32 pieces). node1 starts downloading it;
#   once node1 has 20 pieces, node2 starts too; once node2 has 2, node0 is
#   killed. The leechers together hold only ~23 pieces, so the file can't
#   be finished: both downloads must give up. The question is how much each
#   one keeps.
#
#   node1 needs only the piece or two node2 has, so it gets stuck first and
#   gives up ~15 s later. node2 needs ~20 pieces from node1, one a second
#   (node0-node2 serve with a 1 s delay). Before this change, giving up
#   deleted the file and stopped serving it, so node1 cut node2 off
#   mid-copy and the two ended with different counts (e.g. 23 vs 18). Now a
#   download that gives up keeps serving what it has, so node2 catches up
#   and both end with the same pieces: the ones that died with the seeder
#   are missing from both. See docs/08-swarming.md, "Why giving up mustn't
#   delete".
#
#   Then node3 joins (serving at full speed) and shares the same file (same
#   bytes, same key), and node1 runs the same fetch again: it resumes,
#   fetching only the missing pieces (docs/08-swarming.md, "Resuming").
#
# PASS: both leechers give up with the same count, `files` lists the
# partial file on both with that count, and node1's second fetch resumes
# from that count and ends byte-identical to the original.
#
# Takes about a minute (1 s per piece on purpose, as in field test 4).
# Usage: scripts/demo-give-up-resume.sh   (env: BASE_PORT=9650 DELAY=1s)
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/nodes.sh

BASE_PORT="${BASE_PORT:-9650}"
DELAY="${DELAY:-1s}"
PIECES=32
SEED_PREFIX=demo

count() { pieces_received "$1" | wc -l | tr -d ' '; }
# gave_up_with <i>: the N in node i's "fetch failed: … with N of 32 pieces".
# (`|| true`: no match must print nothing, not stop the script under set -e.)
gave_up_with() { grep '^fetch failed' "$WORK/node$1.log" | sed -n 's/.* with \([0-9]*\) of .*/\1/p' | head -1 || true; }
# files_has <i>: the N in node i's `files` line "has N/32 pieces".
files_has() { grep ' bytes, has ' "$WORK/node$1.log" | sed -n 's/.* has \([0-9]*\)\/.*/\1/p' | tail -1 || true; }
# fetch_results <i>: node i's console results so far (fetched / fetch failed).
fetch_results() { grep '^fetched \|^fetch failed' "$WORK/node$1.log" || true; }

# The waits below are bounded and stop with FAIL, so a run never hangs
# without a verdict.
# wait_pieces <i> <n>: until node i has received n pieces; FAIL if its
# fetch ends first (e.g. a long DELAY hits the console's transfer timeout).
wait_pieces() {
	local waited=0
	while [ "$(count "$1")" -lt "$2" ]; do
		if [ -n "$(fetch_results "$1")" ]; then
			echo "FAIL: node$1's fetch ended with $(count "$1") pieces, before $2: $(fetch_results "$1" | head -1)"
			exit 1
		fi
		sleep 0.2
		waited=$((waited + 1))
		if [ "$waited" -gt 900 ]; then
			echo "FAIL: node$1 never reached $2 pieces"
			exit 1
		fi
	done
}
# wait_results <i> <n>: until node i has printed n fetch results.
wait_results() {
	local waited=0
	while [ "$(fetch_results "$1" | wc -l | tr -d ' ')" -lt "$2" ]; do
		sleep 0.2
		waited=$((waited + 1))
		if [ "$waited" -gt 900 ]; then
			echo "FAIL: node$1 printed no result for fetch number $2"
			exit 1
		fi
	done
}

nodes_init
echo "Starting 3 nodes on ports $BASE_PORT-$((BASE_PORT + 2)) (--debug-serve-delay $DELAY; leechers --max-inflight 1)..."
start_node 0 "$BASE_PORT" - --k 3 --debug-serve-delay "$DELAY"
for i in 1 2; do
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k 3 --debug-serve-delay "$DELAY" --max-inflight 1
done

head -c $((PIECES * 65536)) /dev/urandom >"$WORK/original.bin"
echo
echo "=== node0 shares, node1 starts, node2 starts at 20 pieces, node0 dies at node2's 2nd ==="
send 0 "share $WORK/original.bin"
wait_for_log "$WORK/node0.log" "content key:" 10
KEY="$(grep "content key:" "$WORK/node0.log" | awk '{print $3}')"
send 1 "fetch $KEY $WORK/node1.bin"
wait_pieces 1 20
send 2 "fetch $KEY $WORK/node2.bin"
wait_pieces 2 2
kill_node 0
UNION="$( (pieces_received 1; pieces_received 2) | sort -n -u | wc -l | tr -d ' ')"
echo "  node0 killed: node1 has $(count 1), node2 has $(count 2), together $UNION of $PIECES"

echo
echo "=== Both downloads give up (node2 ~20 s after node1) ==="
wait_results 1 1
wait_results 2 1
for i in 1 2; do
	echo "  node$i: $(fetch_results "$i" | head -1)"
done
for i in 1 2; do send "$i" files; done
wait_for_log "$WORK/node1.log" " bytes, has \|^no files shared" 10
wait_for_log "$WORK/node2.log" " bytes, has \|^no files shared" 10
A="$(gave_up_with 1)"; B="$(gave_up_with 2)"; FA="$(files_has 1)"; FB="$(files_has 2)"

echo
echo "=== node3 joins and shares the same file; node1 runs the same fetch again ==="
start_node 3 $((BASE_PORT + 3)) $((BASE_PORT + 1)) --k 3
send 3 "share $WORK/original.bin"
wait_for_log "$WORK/node3.log" "content key:" 10
send 1 "fetch $KEY $WORK/node1.bin"
wait_results 1 2
SECOND="$(fetch_results 1 | sed -n 2p)"
if [ "${SECOND#fetched }" != "$SECOND" ]; then
	awk '/^fetched /{on=1; print "  " $0; next} on && /^    /{print "  " $0; next} on && /^time=/{next} on{exit}' "$WORK/node1.log"
else
	echo "  $SECOND"
fi
RESUMED="$(sed -n 's/^ *\([0-9]*\) piece(s) were already on disk (resumed)/\1/p' "$WORK/node1.log" | head -1)"

echo
echo "=== Verdict ==="
STATUS=0
if [ -n "$A" ] && [ "$A" = "$B" ]; then
	echo "  both leechers gave up with $A of $PIECES pieces"
else
	echo "  leechers gave up with different counts: node1 ${A:-?}, node2 ${B:-?}"
	STATUS=1
fi
if [ "$FA" = "$A" ] && [ "$FB" = "$A" ]; then
	echo "  'files' still lists the partial file on both, at $A/$PIECES: they keep serving it"
else
	echo "  'files' shows node1 ${FA:-nothing}, node2 ${FB:-nothing} (expected $A on both)"
	STATUS=1
fi
if [ "$RESUMED" = "$A" ] && cmp -s "$WORK/original.bin" "$WORK/node1.bin"; then
	echo "  node1's second fetch resumed from $RESUMED pieces and is byte-identical to the original"
else
	echo "  node1's second fetch: resumed from ${RESUMED:-nothing} (expected $A), identical: $(cmp -s "$WORK/original.bin" "$WORK/node1.bin" && echo yes || echo no)"
	STATUS=1
fi
echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: the leechers that lost their seeder ended with the same pieces and kept serving them, and fetching again resumed."
else
	echo "FAIL"
fi
exit "$STATUS"
