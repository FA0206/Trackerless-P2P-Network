#!/usr/bin/env bash
#
# Starts 5 Kademlia nodes as separate local processes on ports 9000-9004,
# bootstraps them into a chain (1 via 0, 2 via 1, 3 via 2, 4 via 3),
# triggers one lookup from node4 for node2's ID, and checks that it found
# exactly the other 4 nodes. This is the project's entire "evaluation" for
# now: one scenario you can run and read, not a benchmark suite.
#
# Usage: scripts/demo.sh
set -euo pipefail

cd "$(dirname "$0")/.." # repo root, so this works no matter where it's run from

BIN_DIR="$(mktemp -d)"
LOG_DIR="$(mktemp -d)"
BIN="$BIN_DIR/node"

PIDS=""
cleanup() {
	echo
	echo "Stopping all demo nodes..."
	for pid in $PIDS; do
		kill "$pid" >/dev/null 2>&1 || true
	done
	wait >/dev/null 2>&1 || true
	echo "Full logs were kept at: $LOG_DIR"
}
trap cleanup EXIT

echo "Building node binary..."
go build -o "$BIN" ./cmd/node

# node_id prints the same 160-bit hex ID that `node --id-seed <s>` would
# use, since both compute sha1(s) over the raw bytes (no trailing
# newline). This lets the script independently predict each node's ID.
node_id() {
	printf '%s' "$1" | shasum -a 1 | awk '{print $1}'
}

# wait_for_log blocks until `pattern` appears in `file`, or exits the
# script with the file's tail if `timeout_s` seconds pass first.
wait_for_log() {
	local file="$1" pattern="$2" timeout_s="$3" waited=0
	while ! grep -q "$pattern" "$file" 2>/dev/null; do
		sleep 0.2
		waited=$((waited + 1))
		if [ "$waited" -gt $((timeout_s * 5)) ]; then
			echo "Timed out after ${timeout_s}s waiting for '$pattern' in $file" >&2
			echo "--- last 20 lines of $file ---" >&2
			tail -n 20 "$file" >&2 || true
			exit 1
		fi
	done
}

echo "Starting node0 on :9000 (first node, nothing to bootstrap against)..."
"$BIN" --console=false --port 9000 --id-seed demo0 >"$LOG_DIR/node0.log" 2>&1 &
PIDS="$PIDS $!"
# Wait until node0's socket is bound: a PING sent earlier is silently lost
# (UDP to an unbound port), and macOS can take a moment to launch a freshly
# built binary for the first time.
wait_for_log "$LOG_DIR/node0.log" "node started" 10

echo "Starting node1 on :9001, bootstrapping via node0..."
"$BIN" --console=false --port 9001 --id-seed demo1 --bootstrap 127.0.0.1:9000 >"$LOG_DIR/node1.log" 2>&1 &
PIDS="$PIDS $!"
wait_for_log "$LOG_DIR/node1.log" "bootstrap complete" 10

echo "Starting node2 on :9002, bootstrapping via node1..."
"$BIN" --console=false --port 9002 --id-seed demo2 --bootstrap 127.0.0.1:9001 >"$LOG_DIR/node2.log" 2>&1 &
PIDS="$PIDS $!"
wait_for_log "$LOG_DIR/node2.log" "bootstrap complete" 10

echo "Starting node3 on :9003, bootstrapping via node2..."
"$BIN" --console=false --port 9003 --id-seed demo3 --bootstrap 127.0.0.1:9002 >"$LOG_DIR/node3.log" 2>&1 &
PIDS="$PIDS $!"
wait_for_log "$LOG_DIR/node3.log" "bootstrap complete" 10

TARGET="$(node_id demo2)"
echo "Starting node4 on :9004, bootstrapping via node3, then looking up node2's ID ($TARGET)..."
"$BIN" --console=false --port 9004 --id-seed demo4 --bootstrap 127.0.0.1:9003 \
	--lookup "$TARGET" --lookup-after 300ms >"$LOG_DIR/node4.log" 2>&1 &
PIDS="$PIDS $!"
wait_for_log "$LOG_DIR/node4.log" "TRIGGERED LOOKUP RESULT" 15

echo
echo "=== Triggered lookup, as logged by node4 ==="
RESULT_LINE="$(grep "TRIGGERED LOOKUP RESULT" "$LOG_DIR/node4.log")"
echo "$RESULT_LINE"

extract() { echo "$RESULT_LINE" | grep -o "$1=[^ ]*" | cut -d= -f2; }

ROUNDS="$(extract rounds)"
RPCS_SENT="$(extract rpcs_sent)"
RPCS_FAILED="$(extract rpcs_failed)"
CLOSEST_IDS="$(extract closest_ids)"

EXPECTED="$(for s in demo0 demo1 demo2 demo3; do node_id "$s"; done | sort)"
ACTUAL="$(echo "$CLOSEST_IDS" | tr ',' '\n' | sort)"

echo
echo "Rounds:      $ROUNDS"
echo "RPCs sent:   $RPCS_SENT"
echo "RPCs failed: $RPCS_FAILED"
echo

# Every message a node receives carries the sender's ID, and the receiver
# records that sender in its routing table. Counting the distinct senders in
# each log shows that knowledge spreads as a side effect of joins and
# lookups: node0 learns about nodes that never bootstrapped through it.
echo "=== Who learned about whom (distinct peers each node received RPCs from) ==="
for i in 0 1 2 3 4; do
	PEERS="$(grep -o 'from=[0-9a-f]*@' "$LOG_DIR/node$i.log" | sort -u | wc -l | tr -d ' ')"
	echo "node$i heard directly from $PEERS of the other 4 nodes"
done
echo

if [ "$EXPECTED" = "$ACTUAL" ]; then
	echo "PASS: node4's lookup found exactly the other 4 nodes (demo0, demo1, demo2, demo3)."
	exit 0
else
	echo "FAIL: the set of closest nodes found did not match what was expected."
	echo "--- expected IDs ---"
	echo "$EXPECTED"
	echo "--- actual IDs ---"
	echo "$ACTUAL"
	exit 1
fi
