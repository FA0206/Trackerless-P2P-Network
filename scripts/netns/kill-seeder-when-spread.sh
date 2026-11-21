#!/usr/bin/env bash
# Kills the seeder at the one moment that proves a file survives its
# seeder: the leechers *together* hold every piece, yet none of them holds
# them all. Kill earlier, and some piece may exist only on the seeder, so
# losing the file would be the correct outcome. Kill later, and a complete
# copy already existed, so "the leechers finished" proves nothing.
#
# A stand-alone version of the "Waiting for the kill moment" loop in
# scripts/demo-seeder-death.sh, for nodes started by hand (the network-
# namespace lab, or real devices). That script started its nodes itself,
# so it knows their PIDs and log names; here we only know the seeder's
# --id-seed and the leechers' log files.
#
# Usage: bash scripts/netns/kill-seeder-when-spread.sh <seeder-id-seed> <leecher-log>...
#   e.g. bash scripts/netns/kill-seeder-when-spread.sh seeder lab/4-death/L*.log
#   env: TIMEOUT=300 (seconds to wait for the moment before giving up)
#
# Start it before (or right after) the first leecher's fetch. Every 50 ms
# it takes one snapshot of the logs and then:
#   - if a leecher holds every piece, or has finished: prints VOID, kills
#     nothing, exits 1. Redo the run (slower delay, or start the leechers
#     closer together);
#   - else, once the leechers together hold every piece: SIGKILLs the
#     seeder (like the demo) and prints each leecher's count, exits 0.
# The piece total comes from the logs' "of=" field, so any file size works.
#
# Run it as yourself, never with sudo. Under sudo, $USER is root, and the
# pkill below would match the root-owned `sudo -u ... --id-seed seeder`
# wrapper instead of the node. sudo can't pass SIGKILL on to its child, so
# the wrapper would die and the seeder would keep running.
set -euo pipefail

if [ "$(id -u)" -eq 0 ]; then
	echo "run as yourself, not with sudo (see the comment at the top)" >&2
	exit 1
fi
if [ $# -lt 2 ]; then
	echo "usage: $0 <seeder-id-seed> <leecher-log>..." >&2
	exit 2
fi
SEED="$1"
shift
TIMEOUT="${TIMEOUT:-300}"

# pieces <log>: the distinct piece indexes this leecher has received, one per line.
pieces() {
	{ grep 'msg="piece received"' "$1" 2>/dev/null || true; } | grep -o 'index=[0-9]*' | sort -u || true
}

# total <log>...: the file's piece count, from the first "of=" any leecher
# logged. Empty until the first piece arrives.
total() {
	{ cat "$@" 2>/dev/null || true; } | grep 'msg="piece received"' | grep -o 'of=[0-9]*' | head -n1 | cut -d= -f2 || true
}

polls=0
while :; do
	n="$(total "$@")"
	if [ -n "$n" ]; then
		# One snapshot per check: each leecher's pieces, read once.
		all=""
		report=""
		for f in "$@"; do
			p="$(pieces "$f")"
			c="$(printf '%s' "$p" | grep -c . || true)"
			if [ "$c" -ge "$n" ] || grep -qE '^fetched |msg="download complete"' "$f" 2>/dev/null; then
				echo "VOID: $f finished before the pieces spread ($c/$n); nothing killed -- redo the run"
				exit 1
			fi
			all="$all$p"$'\n'
			report="$report  $f: $c/$n at the kill"$'\n'
		done
		union="$(printf '%s' "$all" | grep . | sort -u | wc -l)"
		if [ "$union" -ge "$n" ]; then
			# ( |$): seed "seeder" must not also match "seeder2".
			if ! pkill -KILL -u "$USER" -f -- "--id-seed ${SEED}( |\$)"; then
				echo "the moment came, but no process of yours has --id-seed $SEED; nothing killed" >&2
				exit 1
			fi
			echo "Killed the seeder ($SEED): the leechers together hold all $n pieces, and none has finished."
			printf '%s' "$report"
			exit 0
		fi
	fi
	polls=$((polls + 1))
	if [ "$polls" -ge $((TIMEOUT * 20)) ]; then
		echo "gave up after ${TIMEOUT}s: the leechers never together held every piece" >&2
		exit 1
	fi
	sleep 0.05
done
