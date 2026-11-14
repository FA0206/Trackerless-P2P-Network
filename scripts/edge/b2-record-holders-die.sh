#!/usr/bin/env bash
#
# Edge case B2: every node holding a file's provider record dies, while
# the seeder itself stays online.  (docs/EDGE-CASES.md, case B2)
#
#   N nodes (default 8), k=3, all join through node0. node1 shares a file.
#   Its provider record ("node1 has it, at 127.0.0.1:<port>") is STOREd on
#   the 3 nodes whose IDs are closest to the content key (paper Sec. 2.3),
#   and the seeder is chosen so it isn't one of them.
#
#   1. Control: a leecher downloads the file. It works.
#   2. The 3 record holders are killed (SIGKILL). The seeder and the first
#      leecher, who both have the file, stay up and answer PING.
#   3. A second leecher tries to download: FIND_VALUE finds no record.
#   4. The seeder shares the file again (a manual "republish"). A third
#      leecher tries to download.
#
# Prediction: step 3 fails with "no providers found", although two
# providers are online. Values are never republished (store.go,
# SIMPLIFICATION), so records live only on the nodes chosen at share time.
#
# Step 4 ALSO fails, which the first version of this script got wrong (it
# predicted success): the seeder's re-announce lookup starts from the k
# closest contacts in its own routing table, which are exactly the dead
# holders. Dead contacts are never removed from a routing table on a
# timeout, and failed candidates still fill the lookup's k-closest list
# (lookup.go). So the lookup finds no live node, Store keeps the record
# only on the seeder itself, and nobody else's lookup reaches it.
# The script PASSES when all four steps behave as predicted.
#
# Usage: scripts/edge/b2-record-holders-die.sh   (env: N=8 BASE_PORT=9525 SEED=1)
set -euo pipefail
cd "$(dirname "$0")/../.."
. scripts/lib/nodes.sh
. scripts/lib/edge.sh

N="${N:-8}"
K=3
BASE_PORT="${BASE_PORT:-9525}"
SEED="${SEED:-1}" # picks the file's bytes, and so its content key
SEED_PREFIX=edgeb2-
SEEDER=1

nodes_init
node_ids_init "$N"
ALL="$(seq 0 $((N - 1)) | tr '\n' ' ')"
echo "Starting $N nodes on ports $BASE_PORT-$((BASE_PORT + N - 1)), k=$K, all joining via node0 (SEED=$SEED)..."
start_node 0 "$BASE_PORT" - --k "$K"
for ((i = 1; i < N; i++)); do
	start_node "$i" $((BASE_PORT + i)) "$BASE_PORT" --k "$K"
done

echo
echo "=== node$SEEDER shares a file; its record goes to the $K nodes closest to the key ==="
# The content key depends on the file, not on who shares it, so it can't
# be picked in advance. If the seeder would itself be a record holder
# (killing the holders would then kill the seeder too), share a file with
# a different name, which changes the key. Deterministic for a given SEED.
KEY=""
for try in 1 2 3 4 5 6; do
	FILE="$WORK/shared-$SEED-$try.bin"
	(yes "edge case B2, seed $SEED" || true) | head -c 262144 >"$FILE" # 4 pieces; `|| true`: yes dies of SIGPIPE
	send "$SEEDER" "share $FILE"
	wait_for_count "$WORK/node$SEEDER.log" "content key:" "$try" 20
	KEY="$(grep "content key:" "$WORK/node$SEEDER.log" | tail -1 | awk '{print $3}')"
	HOLDERS="$(closest_nodes "$KEY" "$K" $ALL | tr '\n' ' ')"
	if ! in_list "$SEEDER" $HOLDERS; then break; fi
	echo "  (node$SEEDER is itself among the $K closest to ${KEY:0:8}…; sharing a renamed file instead)"
	KEY=""
done
[ -n "$KEY" ] || { echo "FAIL: could not find a key the seeder isn't close to" >&2; exit 1; }
HOLDERS="$(echo $HOLDERS)"
LOGGED="$(holders_of "$KEY" "$N")"
echo "  content key:            $KEY"
echo "  expected holders:       $(for i in $HOLDERS; do printf 'node%s ' "$i"; done) (brute force: the $K IDs closest to the key)"
echo "  holders (from logs):    $(for i in $LOGGED; do printf 'node%s ' "$i"; done) (nodes that logged \"value stored\")"
STATUS=0
if [ "$(echo "$LOGGED" | tr ' ' '\n' | sort)" != "$(echo "$HOLDERS" | tr ' ' '\n' | sort)" ]; then
	echo "  UNEXPECTED: the record did not land exactly on the $K closest nodes"
	STATUS=1
fi

# Leechers: everyone except the seeder and the holders, in index order.
LEECHERS=""
for i in $ALL; do
	if [ "$i" -ne "$SEEDER" ] && ! in_list "$i" $HOLDERS; then LEECHERS="$LEECHERS $i"; fi
done
set -- $LEECHERS
[ "$#" -ge 3 ] || { echo "FAIL: need 3 leechers, have $#; raise N" >&2; exit 1; }
L1="$1" L2="$2" L3="$3"

echo
echo "=== Step 1 (control): node$L1 downloads while the holders are alive ==="
send "$L1" "fetch $KEY $WORK/node$L1.bin"
wait_for_log "$WORK/node$L1.log" '^fetch' 30
echo "  node$L1: $(grep '^fetch' "$WORK/node$L1.log" | head -1 | sed -E 's/ into [^ ]+//')"
if cmp -s "$FILE" "$WORK/node$L1.bin"; then
	echo "  node$L1's copy is identical; node$L1 is now a provider too"
else
	echo "  UNEXPECTED: the control download failed"
	STATUS=1
fi

echo
echo "=== Step 2: kill the $K record holders (SIGKILL) ==="
for i in $HOLDERS; do
	kill_node "$i"
	echo "  killed node$i"
done
for i in "$SEEDER" "$L1"; do
	send "$L2" "ping 127.0.0.1:$((BASE_PORT + i))" >/dev/null
	wait_for_count "$WORK/node$L2.log" '^PONG from\|^no reply from' "$([ "$i" = "$SEEDER" ] && echo 1 || echo 2)" 10
done
echo "  providers still online, pinged from node$L2:"
grep '^PONG from\|^no reply from' "$WORK/node$L2.log" | sed 's/^/    /'
if [ "$(count_lines "$WORK/node$L2.log" '^PONG from')" -ne 2 ]; then
	echo "  UNEXPECTED: a provider did not answer"
	STATUS=1
fi

echo
echo "=== Step 3: node$L2 tries to download (predicted: no record found) ==="
send "$L2" "fetch $KEY $WORK/node$L2.bin"
wait_for_log "$WORK/node$L2.log" '^fetch' 60
echo "  node$L2: $(grep '^fetch' "$WORK/node$L2.log" | head -1)"
FV="$(grep "msg=\"find_value finished\".*key=$KEY" "$WORK/node$L2.log" | tail -1 | grep -o 'values_found=.*' || true)"
echo "  its FIND_VALUE: $FV"
if grep -q '^fetch failed: transfer: no providers found' "$WORK/node$L2.log" && [ ! -e "$WORK/node$L2.bin" ]; then
	echo "  as predicted: the file is online on 2 nodes, but nobody can find them"
else
	echo "  UNEXPECTED: the download did not fail with \"no providers found\""
	STATUS=1
fi

echo
echo "=== Step 4: node$SEEDER shares the file again (predicted: this doesn't help) ==="
SHARES="$(count_lines "$WORK/node$SEEDER.log" "content key:")"
send "$SEEDER" "share $FILE"
wait_for_count "$WORK/node$SEEDER.log" "content key:" $((SHARES + 1)) 60
LOOKUP="$(grep "msg=\"lookup converged\".*target=$KEY" "$WORK/node$SEEDER.log" | tail -1 | grep -o 'rounds=.*' || true)"
STORE="$(grep "msg=\"store complete\".*key=$KEY" "$WORK/node$SEEDER.log" | tail -1 | grep -o 'stored_at=.*' || true)"
echo "  node$SEEDER's re-announce lookup: $LOOKUP"
echo "  node$SEEDER's store:              $STORE"
case "$LOOKUP $STORE" in
*"closest_found=0"*"stored_at=0 stored_locally=true"*)
	echo "  as predicted: every contact it started from was a dead holder, so the record stayed on node$SEEDER alone" ;;
*)
	echo "  UNEXPECTED: the re-announce reached live nodes"
	STATUS=1 ;;
esac
send "$L3" "fetch $KEY $WORK/node$L3.bin"
wait_for_log "$WORK/node$L3.log" '^fetch' 60
echo "  node$L3: $(grep '^fetch' "$WORK/node$L3.log" | head -1)"
if grep -q '^fetch failed: transfer: no providers found' "$WORK/node$L3.log"; then
	echo "  as predicted: node$L3's lookup also stops at the dead holders and never reaches node$SEEDER"
else
	echo "  UNEXPECTED: node$L3's download did not fail with \"no providers found\""
	STATUS=1
fi

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: with every record holder dead the file could not be found, though 2 providers were online, and re-sharing did not bring it back."
else
	echo "FAIL (repeat this exact run with SEED=$SEED)"
fi
exit "$STATUS"
