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
# Step 4 works: the seeder's re-announce lookup meets the dead holders,
# skips past them to the k closest LIVE nodes (lookup.go, paper Sec. 2.3)
# and stores the record there, and the third leecher's FIND_VALUE finds
# it. Re-sharing by hand is what republishing would do automatically.
# (Before the lookup fix, failed candidates kept their places among the k
# closest, so step 4 failed too; see docs/EDGE-CASES.md.)
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
echo "=== Step 4: node$SEEDER shares the file again (predicted: this brings it back) ==="
SHARES="$(count_lines "$WORK/node$SEEDER.log" "content key:")"
send "$SEEDER" "share $FILE"
wait_for_count "$WORK/node$SEEDER.log" "content key:" $((SHARES + 1)) 60
LOOKUP="$(grep "msg=\"lookup converged\".*target=$KEY" "$WORK/node$SEEDER.log" | tail -1 | grep -o 'rounds=.*' || true)"
STORE="$(grep "msg=\"store complete\".*key=$KEY" "$WORK/node$SEEDER.log" | tail -1 | grep -o 'stored_at=.*' || true)"
echo "  node$SEEDER's re-announce lookup: $LOOKUP"
echo "  node$SEEDER's store:              $STORE"
case "$LOOKUP $STORE" in
*"rpcs_failed=0"*)
	echo "  UNEXPECTED: the re-announce lookup never met a dead holder"
	STATUS=1 ;;
*"closest_found=$K"*"failed=0")
	echo "  as predicted: the lookup skipped the dead holders and the record went to the $K closest live nodes" ;;
*)
	echo "  UNEXPECTED: the re-announce didn't reach $K live nodes"
	STATUS=1 ;;
esac
send "$L3" "fetch $KEY $WORK/node$L3.bin"
wait_for_log "$WORK/node$L3.log" '^fetch' 60
echo "  node$L3: $(grep '^fetch' "$WORK/node$L3.log" | head -1 | sed -E 's/ into [^ ]+//')"
if grep -q '^fetched ' "$WORK/node$L3.log" && cmp -s "$FILE" "$WORK/node$L3.bin"; then
	echo "  as predicted: node$L3 found the new record and its copy is identical"
else
	echo "  UNEXPECTED: node$L3's download failed"
	STATUS=1
fi

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: with every record holder dead the file could not be found, though 2 providers were online, until the seeder shared it again."
else
	echo "FAIL (repeat this exact run with SEED=$SEED)"
fi
exit "$STATUS"
