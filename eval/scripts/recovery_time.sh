#!/usr/bin/env bash
#
# Graph 3, localhost half: does a file survive its seeder dying, and how
# fast do the leechers recover? (docs/10-performance-graphs.md)
#
#   The defaults are exactly eval-field-tests.html's test 1, so the
#   localhost and real-laptop series differ only by the network: a seeder
#   (node 0) and LEECHER_COUNT leechers, all with the DEBUG ONLY
#   --debug-serve-delay DELAY and --max-inflight 1, as in field test 4 and
#   scripts/demo-seeder-death.sh: without a delay a download is over before
#   anyone can react, and one request per leecher makes the swarm spread
#   pieces before anyone finishes. The seeder shares a FILE_SIZE file;
#   leecher 1 starts, each next one when the previous has STAGGER pieces.
#   When leecher 1 (the furthest along) has KILL_PCT percent of the pieces,
#   the seeder gets SIGTERM, which like the laptops' Ctrl+C logs
#   "shutting down" and closes its sockets.
#
#   Default kill points (40/60/80%) are calibrated, not guessed: with the
#   default LEECHER_COUNT/FILE_SIZE/STAGGER/DELAY, the two leechers'
#   combined pieces naturally cover the whole file by the time leecher 1
#   itself is ~60% done (measured directly: run leechers to completion with
#   no kill at all, and note leecher 1's piece count the moment the union
#   first covers every piece). So 40% reliably loses the file, 60% sits
#   right at that boundary, and 80% reliably survives it — a deliberately
#   mixed, presentation-worthy spread, not three points that happen to land
#   on the same side of the line (25/50/75% of a smaller file all survived
#   or all failed, depending on leecher count, which doesn't show much).
#
#   survived:   1 if every leecher then finished with a byte-identical copy,
#               0 if the file was lost (some piece existed only on the
#               seeder; the leechers give up after 15 s of no progress).
#   recovery_s: (survived trials only) for each leecher, the time from its
#               own "provider failed" line for the seeder (the moment it
#               noticed the seeder was gone) to its next "piece received";
#               the slowest leecher's value, i.e. until the whole swarm is
#               moving again. Both timestamps come from the leecher's own
#               log, never two machines' clocks (docs/09-multi-device.md),
#               so the real-host checklist computes it the same way. A
#               leecher that never noticed (no request to the seeder after
#               the kill) wasn't held up: 0. SIGTERM, like Ctrl+C, closes
#               the seeder's sockets at once, so noticing is immediate.
#
# Output: eval/results/recovery_time.csv (its real_hosts rows are kept)
#   kill_point_pct,trial,environment,survived,recovery_s
#
# Usage: eval/scripts/recovery_time.sh
#   (env: KILL_PCTS="40 60 80" TRIALS=5 LEECHER_COUNT=2 FILE_SIZE=4M DELAY=1s
#    STAGGER=8 BASE_PORT=10300 SEED=<number>)
set -euo pipefail
cd "$(dirname "$0")/../.."
. eval/scripts/lib.sh

KILL_PCTS="${KILL_PCTS:-40 60 80}"
TRIALS="${TRIALS:-5}"
LEECHER_COUNT="${LEECHER_COUNT:-2}"
FILE_SIZE="${FILE_SIZE:-4M}"
DELAY="${DELAY:-1s}"
STAGGER="${STAGGER:-8}"
BASE_PORT="${BASE_PORT:-10300}"
SEED="${SEED:-$$}"
RANDOM="$SEED"
SEED_PREFIX=rec
OUT="$RESULTS/recovery_time.csv"
HEADER="kill_point_pct,trial,environment,survived,recovery_s"
LEECHERS="$(seq 1 "$LEECHER_COUNT" | tr '\n' ' ')"

eval_init
FILE="$ROOT_WORK/test.bin"
make_file "$FILE" "$FILE_SIZE"
PIECES=$((($(wc -c <"$FILE") + 65535) / 65536))

received() { count_lines "$WORK/node$1.log" 'msg="piece received"'; }

# leecher_recovery_ms <log> <seeder-addr>: ms from the leecher noticing the
# seeder failed to its next piece, both from its own log; 0 if it never
# noticed. Same awk as eval-field-tests.html's test 1: keep them identical.
leecher_recovery_ms() {
	awk -v seeder="$2" 'function ms(t){sub(/^time=/,"",t);split(t,a,"T");split(substr(a[2],1,12),h,":");return int((h[1]*3600+h[2]*60+h[3])*1000+0.5)} /msg="provider failed, trying the next one"/ && index($0, " provider=" seeder " ") && !k {k=ms($1)} /msg="piece received"/ && k && !f {f=1; r=ms($1)-k} END{print (k && f) ? r : 0}' "$1"
}
wait_received() { # wait_received <i> <n>: until node i has received n pieces
	local waited=0
	while [ "$(received "$1")" -lt "$2" ]; do
		sleep 0.05
		waited=$((waited + 1))
		[ "$waited" -lt 2400 ] || { echo "node$1 never reached $2 pieces" >&2; exit 1; }
	done
}

csv_keep_other_env "$OUT" "$HEADER" localhost
echo "Graph 3 (localhost): $FILE_SIZE file ($PIECES pieces), kill at $KILL_PCTS% of leecher 1, $TRIALS trials (SEED=$SEED)"
for pct in $KILL_PCTS; do
	kill_at=$(((PIECES * pct + 99) / 100))
	for ((t = 1; t <= TRIALS; t++)); do
		new_run
		grow_to $((LEECHER_COUNT + 1)) --debug-serve-delay "$DELAY" --max-inflight 1
		key="$(share_file 0 "$FILE")"
		prev=""
		for i in $LEECHERS; do
			[ -z "$prev" ] || wait_received "$prev" "$STAGGER"
			bg api_post "$i" fetch "{\"key\":\"$key\",\"out\":\"$WORK/got$i.bin\"}" "$WORK/fetch$i.json" >/dev/null 2>&1
			prev="$i"
		done
		wait_received 1 "$kill_at"
		kill_node 0 TERM
		wait_jobs || true # each fetch returns: done, or given up (a lost file is an expected outcome here)

		survived=1
		worst=0
		for i in $LEECHERS; do
			if ! cmp -s "$FILE" "$WORK/got$i.bin" 2>/dev/null; then survived=0; continue; fi
			rm -f "$WORK/got$i.bin"
			ms="$(leecher_recovery_ms "$WORK/node$i.log" "127.0.0.1:$BASE_PORT")"
			[ "$ms" -le "$worst" ] || worst="$ms"
		done
		if [ "$survived" -eq 1 ]; then
			rec="$(awk -v ms="$worst" 'BEGIN { printf "%.3f", ms / 1000 }')"
		else
			rec=""
		fi
		echo "$pct,$t,localhost,$survived,$rec" >>"$OUT"
		echo "  kill at $pct% (leecher 1 had $(received 1)/$PIECES at the end): survived=$survived recovery=${rec:-n/a}s"
	done
done
echo "Wrote $OUT"
