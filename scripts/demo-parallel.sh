#!/usr/bin/env bash
#
# Parallel download demo: pieces arrive concurrently from several providers.
#
#   node1 shares a 1 MiB file (16 pieces); node2 and node3 download it, so
#   three nodes hold the whole file. Then node4 downloads it and the script
#   prints node4's timeline: when each piece was requested, from whom, and
#   when it arrived. Requests to different providers overlap in time; each
#   provider handles one request at a time.
#
# Providers run with the DEBUG ONLY --debug-serve-delay (default 150ms per
# piece; off by default in the binary) so overlaps are visible in the
# timestamps -- on localhost a piece otherwise takes ~1 ms. Fetching the 16
# pieces one at a time would take at least 16 x DELAY.
#
# PASS: node4's copy is byte-identical, pieces came from all 3 providers,
# and at least 3 requests were in flight at once.
#
# Usage: scripts/demo-parallel.sh   (env: BASE_PORT=9400 DELAY=150ms)
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/nodes.sh

BASE_PORT="${BASE_PORT:-9400}"
DELAY="${DELAY:-150ms}"
SEED_PREFIX=demo

nodes_init
echo "Starting 5 nodes on ports $BASE_PORT-$((BASE_PORT + 4)) (k=3)..."
start_node 0 "$BASE_PORT" - --k 3
for i in 1 2 3 4; do
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k 3 --debug-serve-delay "$DELAY"
done

head -c 1048576 /dev/urandom >"$WORK/original.bin"
echo
echo "=== Three providers: node1 shares, node2 and node3 download ==="
send 1 "share $WORK/original.bin"
wait_for_log "$WORK/node1.log" "content key:" 10
KEY="$(grep "content key:" "$WORK/node1.log" | awk '{print $3}')"
for i in 2 3; do
	send "$i" "fetch $KEY $WORK/node$i.bin"
	wait_for_log "$WORK/node$i.log" '^fetch' 60
	echo "    $(grep '^fetch' "$WORK/node$i.log" | head -1 | sed -E 's/ into [^ ]+//')"
done

echo
echo "=== node4 downloads from all three at once ==="
send 4 "fetch $KEY $WORK/node4.bin"
wait_for_log "$WORK/node4.log" '^fetch' 60
echo "    $(grep '^fetch' "$WORK/node4.log" | head -1 | sed -E 's/ into [^ ]+//')"
awk '/^fetch/{on=1;next} on && /^    /{print "  " $0; next} on{exit}' "$WORK/node4.log"

echo
echo "=== node4's timeline (ms since its first piece request) ==="
grep -E 'msg="piece (requested|received)"' "$WORK/node4.log" | awk '
	{
		split($1, t, "T"); split(substr(t[2], 1, 12), h, ":")
		ms = (h[1] * 3600 + h[2] * 60 + h[3]) * 1000
		if (start == "") start = ms
		ev = ($0 ~ /msg="piece requested"/) ? "request " : "received"
		idx = ""; from = ""; inflight = ""
		for (i = 4; i <= NF; i++) {
			if ($i ~ /^index=/) idx = substr($i, 7)
			if ($i ~ /^from=/) from = substr($i, 6)
			if ($i ~ /^inflight=/) inflight = substr($i, 10)
		}
		arrow = (ev == "request ") ? "->" : "<-"
		printf "  +%5dms  %s piece %2s %s %s   in flight after: %s\n", ms - start, ev, idx, arrow, from, inflight
	}'

MAX="$(grep 'msg="download complete"' "$WORK/node4.log" | grep -o 'max_inflight=[0-9]*' | cut -d= -f2)"
# Log lines (time=…) can land between the console's fetch lines: skip them.
PROVIDERS="$(awk '/^fetch/{on=1;next} on && /^time=/{next} on && /piece\(s\) from/{n++; next} on{exit} END{print n+0}' "$WORK/node4.log")"
echo
echo "  most requests in flight at once: $MAX   providers used: $PROVIDERS"
echo "  (one at a time would take at least 16 x $DELAY)"
echo
if cmp -s "$WORK/original.bin" "$WORK/node4.bin" && [ "${MAX:-0}" -ge 3 ] && [ "$PROVIDERS" -ge 3 ]; then
	echo "PASS: node4's copy is identical, built from 3 providers with up to $MAX pieces in flight at once."
else
	echo "FAIL: identical=$(cmp -s "$WORK/original.bin" "$WORK/node4.bin" && echo yes || echo no) max_inflight=$MAX providers=$PROVIDERS"
	exit 1
fi
