#!/usr/bin/env bash
#
# Demo of STORE/FIND_VALUE and TCP file transfer, on 5 local node processes
# (ports 9000-9004, each node's file server on the same TCP port number).
#
#   Part 1  generic DHT values: node0 `put`s a text value, node4 `get`s it.
#   Part 2  file sharing: node1 shares a 1 MiB file; node3 downloads it (one
#           provider), then node4 downloads it (two providers: node1, node3).
#   Part 3  seeder gone: node1 is killed; node2 still downloads the file from
#           the nodes that downloaded it earlier.
#
# k=3, so each value lives on only 3 of the 5 nodes and FIND_VALUE has to
# search for it. Each node reads console commands from a named pipe (FIFO)
# that this script writes to. See docs/DRY-RUN.md, part 2.
set -euo pipefail

cd "$(dirname "$0")/.."

WORK="$(mktemp -d)"
BIN="$WORK/node"
K=3
PIDS=""

cleanup() {
	echo
	echo "Stopping all demo nodes..."
	for pid in $PIDS; do
		kill "$pid" >/dev/null 2>&1 || true
	done
	wait >/dev/null 2>&1 || true
	echo "Logs, FIFOs and downloaded files were kept in: $WORK"
}
trap cleanup EXIT

# wait_for_log <file> <pattern> <timeout_s>
wait_for_log() {
	local file="$1" pattern="$2" timeout_s="$3" waited=0
	while ! grep -q "$pattern" "$file" 2>/dev/null; do
		sleep 0.2
		waited=$((waited + 1))
		if [ "$waited" -gt $((timeout_s * 5)) ]; then
			echo "Timed out after ${timeout_s}s waiting for '$pattern' in $file" >&2
			tail -n 20 "$file" >&2 || true
			exit 1
		fi
	done
}

# start_node <i> [bootstrap-port]: node i listens on 900i, reads commands
# from the FIFO node<i>.in, and logs to node<i>.log. The script keeps the
# FIFO open on file descriptor 3+i, so the node's stdin never hits EOF.
start_node() {
	local i="$1"
	local port=$((9000 + i))
	local bootstrap=""
	if [ $# -ge 2 ]; then bootstrap="--bootstrap 127.0.0.1:$2"; fi
	mkfifo "$WORK/node$i.in"
	# shellcheck disable=SC2086 # $bootstrap is intentionally word-split
	"$BIN" --port "$port" --id-seed "demo$i" --k "$K" $bootstrap \
		<"$WORK/node$i.in" >"$WORK/node$i.log" 2>&1 &
	PIDS="$PIDS $!"
	eval "PID$i=$!"
	eval "exec $((3 + i))>\"$WORK/node$i.in\""
}

# send <i> <command...>: type a console command into node i.
send() {
	local i="$1"
	shift
	echo "  [node$i] > $*"
	eval "printf '%s\n' \"\$*\" >&$((3 + i))"
}

# show <i> <pattern>: print node i's console lines from the first line
# matching pattern up to the next log line.
show() {
	awk -v pat="$2" 'index($0, pat) {on=1} on && /^time=/ {exit} on {print "    " $0}' "$WORK/node$1.log"
}

echo "Building node binary..."
go build -o "$BIN" ./cmd/node

echo "Starting 5 nodes (k=$K), each joining via the previous one..."
start_node 0
wait_for_log "$WORK/node0.log" "node started" 10 # a PING sent before node0 binds is lost
for i in 1 2 3 4; do
	start_node "$i" $((9000 + i - 1))
	wait_for_log "$WORK/node$i.log" "bootstrap complete" 10
done

echo
echo "=== Part 1: STORE / FIND_VALUE with a plain text value ==="
send 0 "put greeting hello from node0"
wait_for_log "$WORK/node0.log" "put greeting" 10
show 0 "put greeting"
send 4 "get greeting"
wait_for_log "$WORK/node4.log" "get greeting:" 10
show 4 "get greeting:"

echo
echo "=== Part 2: node1 shares a 1 MiB file; node3, then node4, download it ==="
head -c 1048576 /dev/urandom >"$WORK/original.bin"
send 1 "share $WORK/original.bin"
wait_for_log "$WORK/node1.log" "content key:" 10
KEY="$(grep "content key:" "$WORK/node1.log" | awk '{print $3}')"
show 1 "shared "

send 3 "fetch $KEY $WORK/node3.bin"
wait_for_log "$WORK/node3.log" "fetch" 30
show 3 "fetch"

send 4 "fetch $KEY $WORK/node4.bin"
wait_for_log "$WORK/node4.log" "fetch" 30
show 4 "fetch"
echo "  node4 piece by piece (round-robin over its providers):"
grep 'msg="piece received"' "$WORK/node4.log" | grep -o 'index=[0-9]* of=[0-9]* from=[^ ]*' | sed 's/^/    /'

echo
echo "=== Part 3: kill the original seeder (node1); node2 downloads anyway ==="
echo "  killing node1 (pid $PID1)"
kill "$PID1"
send 2 "fetch $KEY $WORK/node2.bin"
wait_for_log "$WORK/node2.log" "fetch" 40
show 2 "fetch"

echo
echo "=== Verification ==="
STATUS=0
for i in 3 4 2; do
	if cmp -s "$WORK/original.bin" "$WORK/node$i.bin"; then
		echo "  node$i.bin is byte-identical to the original"
	else
		echo "  node$i.bin is MISSING or DIFFERENT"
		STATUS=1
	fi
done
echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: three downloads completed and verified, the last one after the seeder was killed."
else
	echo "FAIL"
fi
exit "$STATUS"
