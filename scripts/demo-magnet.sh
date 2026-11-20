#!/usr/bin/env bash
#
# Magnet link demo: a file's key travels as a link instead of by hand.
#
#   node1 shares a 200 KB file and prints its magnet link. node2 fetches
#   the file with that link. node0 fetches it with a doctored copy of the
#   link whose name (dn) lies: the download still succeeds and is named by
#   the manifest, because only the content key (xt) is used. Last, node2 is
#   handed a BitTorrent link (urn:btih) with the same 40 hex characters and
#   refuses it: it names a torrent in BitTorrent's network, not a file in
#   ours. See docs/10-magnet-links.md.
#
# PASS: both copies are byte-identical, the lying link's download reports
# the manifest's real name, and the BitTorrent link is refused without
# creating a file.
#
# Usage: scripts/demo-magnet.sh   (env: BASE_PORT=9600)
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/nodes.sh

BASE_PORT="${BASE_PORT:-9600}"
SEED_PREFIX=demo

nodes_init
echo "Starting 3 nodes on ports $BASE_PORT-$((BASE_PORT + 2)) (k=3)..."
start_node 0 "$BASE_PORT" - --k 3
for i in 1 2; do
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k 3
done

head -c 200000 /dev/urandom >"$WORK/holiday.bin"
echo
echo "=== node1 shares a file ==="
send 1 "share $WORK/holiday.bin"
wait_for_log "$WORK/node1.log" "magnet link:" 10
grep -E '^  (content key|magnet link):' "$WORK/node1.log" | sed 's/^/  /'
KEY="$(grep "content key:" "$WORK/node1.log" | awk '{print $3}')"
MAGNET="$(grep "magnet link:" "$WORK/node1.log" | awk '{print $3}')"

echo
echo "=== node2 fetches with the link ==="
send 2 "fetch $MAGNET $WORK/node2.bin"
wait_for_log "$WORK/node2.log" '^fetch' 30
echo "    $(grep '^fetch' "$WORK/node2.log" | head -1 | sed -E 's/ into [^ ]+//')"

echo
echo "=== node0 fetches with a link whose name lies ==="
LIAR="$(printf '%s' "$MAGNET" | sed 's/&dn=.*/\&dn=lies.txt/')"
send 0 "fetch $LIAR $WORK/node0.bin"
wait_for_log "$WORK/node0.log" '^fetch' 30
echo "    $(grep '^fetch' "$WORK/node0.log" | head -1 | sed -E 's/ into [^ ]+//')"
NAME0="$(grep '^fetched' "$WORK/node0.log" | head -1 | sed -E 's/^fetched "([^"]*)".*/\1/')"

echo
echo "=== node2 is handed a BitTorrent link with the same 40 hex characters ==="
send 2 "fetch magnet:?xt=urn:btih:$KEY&dn=holiday.bin $WORK/node2-btih.bin"
wait_for_log "$WORK/node2.log" "BitTorrent magnet link" 10
echo "    $(grep "BitTorrent magnet link" "$WORK/node2.log" | head -1)"

echo
same2="$(cmp -s "$WORK/holiday.bin" "$WORK/node2.bin" && echo yes || echo no)"
same0="$(cmp -s "$WORK/holiday.bin" "$WORK/node0.bin" && echo yes || echo no)"
btih_file="$([ -e "$WORK/node2-btih.bin" ] && echo yes || echo no)"
if [ "$same2" = yes ] && [ "$same0" = yes ] && [ "$NAME0" = holiday.bin ] && [ "$btih_file" = no ]; then
	echo "PASS: both links delivered identical copies, the lying name was ignored (the manifest says \"$NAME0\"), and the BitTorrent link was refused."
else
	echo "FAIL: node2 identical=$same2 node0 identical=$same0 node0 name=\"$NAME0\" btih file created=$btih_file"
	exit 1
fi
