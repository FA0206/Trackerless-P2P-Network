#!/usr/bin/env bash
#
# Merkle tree demo: a dishonest provider is
# caught by the proof check, and the manifest no longer grows with the file.
#
#   Part 1, tamper: node1 shares a 1 MiB file (16 pieces). node2 downloads
#   it (a genuine copy) and becomes a provider too, but it runs with the
#   DEBUG ONLY --debug-corrupt-pieces: it flips one byte of every piece it
#   serves and sends the genuine Merkle proof. Then node3 downloads, with
#   both node1 and node2 available. node3 asks both at once, so node2 gets
#   requests too.
#
#   Part 2, manifest size: node1 shares files of 1, 16, 256 and 2048
#   pieces. For each, the script prints the real manifest size (from the
#   "share complete" log line) next to the size the old manifest format,
#   a flat list of every piece's SHA-256, would have had. That one is
#   computed exactly from the old format's JSON layout; the formula is
#   checked against the 1146 bytes a 16-piece "original.bin" really had.
#
# PASS: node3 rejected node2's pieces with "failed its Merkle proof check",
# took 0 pieces from it, and its copy is byte-identical; and the manifest
# size barely changes from 1 to 2048 pieces.
#
# Usage: scripts/demo-merkle.sh   (env: BASE_PORT=9600)
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/nodes.sh

BASE_PORT="${BASE_PORT:-9600}"
SEED_PREFIX=merkle
LIAR=2
LIAR_ADDR="127.0.0.1:$((BASE_PORT + LIAR))"

nodes_init
echo "Starting 4 nodes on ports $BASE_PORT-$((BASE_PORT + 3)) (k=3); node$LIAR corrupts every piece it serves..."
start_node 0 "$BASE_PORT" - --k 3
start_node 1 $((BASE_PORT + 1)) "$BASE_PORT" --k 3
start_node 2 $((BASE_PORT + 2)) "$BASE_PORT" --k 3 --debug-corrupt-pieces
start_node 3 $((BASE_PORT + 3)) "$BASE_PORT" --k 3

# share_line <node> <file name>: the node's "share complete" log line for that file.
share_line() {
	grep "msg=\"share complete\".* name=$2 " "$WORK/node$1.log" | tail -1
}
field() { # field <name> <log line>
	echo "$2" | grep -o "$1=[^ ]*" | cut -d= -f2
}

echo
echo "=== Part 1: a dishonest provider ==="
head -c 1048576 /dev/urandom >"$WORK/original.bin"
send 1 "share $WORK/original.bin" >/dev/null
wait_for_log "$WORK/node1.log" 'msg="share complete"' 20
LINE="$(share_line 1 original.bin)"
KEY="$(field key "$LINE")"
echo "  node1 shared original.bin: $(field pieces "$LINE") pieces, key ${KEY:0:8}…"
echo "    merkle_root=$(field merkle_root "$LINE")"
echo "    manifest_bytes=$(field manifest_bytes "$LINE")"

send 2 "fetch $KEY $WORK/node2.bin" >/dev/null
wait_for_log "$WORK/node2.log" '^fetch' 60
echo "  node2 downloaded a genuine copy: $(grep '^fetch' "$WORK/node2.log" | head -1 | sed -E 's/ into [^ ]+//')"
echo "  node2 is now a provider too, but sends every piece with one byte flipped (and the genuine proof)"

send 3 "fetch $KEY $WORK/node3.bin" >/dev/null
wait_for_log "$WORK/node3.log" '^fetch' 60
echo
echo "  node3 downloads, with node1 and node2 both available:"
echo "    $(grep '^fetch' "$WORK/node3.log" | head -1 | sed -E 's/ into [^ ]+//')"
awk '/^fetch/{on=1;next} on && /^time=/{next} on && /^    /{print "  " $0; next} on{exit}' "$WORK/node3.log"
echo
echo "  What node2 did, and what node3 saw (logs):"
grep -h 'msg="DEBUG: serving a corrupted piece"' "$WORK/node2.log" | sed -E 's/^time=[^ ]+ //; s/^/    [node2] /' | head -3
grep -h 'msg="provider failed, trying the next one"' "$WORK/node3.log" | sed -E 's/^time=[^ ]+ //; s/^/    [node3] /'
grep -h 'msg="piece received"' "$WORK/node3.log" | head -2 | sed -E 's/^time=[^ ]+ //; s/^/    [node3] /'
echo "    [node3] … $(grep -c 'msg="piece received"' "$WORK/node3.log") pieces received in all, every one with its proof checked"

STATUS=0
check() { # check <ok?> <text>
	if [ "$1" = yes ]; then echo "  as predicted: $2"; else echo "  UNEXPECTED: $2"; STATUS=1; fi
}
echo
check "$(grep 'msg="provider failed, trying the next one"' "$WORK/node3.log" | grep -q "provider=$LIAR_ADDR .*failed its Merkle proof check" && echo yes)" \
	"node3 rejected node2's piece: it failed its Merkle proof check"
check "$(grep -q "piece(s) from $LIAR_ADDR" "$WORK/node3.log" || echo yes)" \
	"node3 accepted 0 pieces from node2"
check "$(cmp -s "$WORK/original.bin" "$WORK/node3.bin" && echo yes)" \
	"node3's copy is byte-identical to the original"

echo
echo "=== Part 2: manifest size, Merkle root vs the old flat list of piece hashes ==="
# old_manifest_bytes <name> <size> <pieces>: the old format's exact JSON size,
#   {"name":"N","size":S,"piece_size":65536,"piece_hashes":["<64 hex>",...]}
old_manifest_bytes() {
	local name="$1" size="$2" pieces="$3"
	echo $((9 + ${#name} + 9 + ${#size} + 36 + pieces * 66 + (pieces - 1) + 2))
}
check "$([ "$(old_manifest_bytes original.bin 1048576 16)" -eq 1146 ] && echo yes)" \
	"the old-format formula gives 1146 bytes for a 16-piece original.bin, as the old code produced"
printf '\n  %-24s %7s %15s %26s\n' "file" "pieces" "manifest now" "old flat list (computed)"
SIZES=""
for pieces in 1 16 256 2048; do
	name="f$pieces.bin"
	head -c $((pieces * 65536)) /dev/zero >"$WORK/$name"
	send 1 "share $WORK/$name" >/dev/null
	wait_for_log "$WORK/node1.log" "msg=\"share complete\".* name=$name " 60
	LINE="$(share_line 1 "$name")"
	now="$(field manifest_bytes "$LINE")"
	old="$(old_manifest_bytes "$name" $((pieces * 65536)) "$pieces")"
	SIZES="$SIZES $now"
	printf '  %-24s %7s %13s B %24s B\n' "$name ($((pieces * 64)) KiB)" "$pieces" "$now" "$old"
done
set -- $SIZES
check "$([ $(($4 - $1)) -le 10 ] && echo yes)" \
	"from 1 to 2048 pieces the manifest grew by $(($4 - $1)) bytes (the longer name and size); the flat list would have grown by ~135 KB"

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: node3 caught node2's corrupted pieces with the Merkle proof check and finished from node1 with an identical file; the manifest stays ~140 bytes from 1 to 2048 pieces."
else
	echo "FAIL"
	exit 1
fi
