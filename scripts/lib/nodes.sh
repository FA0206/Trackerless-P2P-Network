# Shared helpers for demo scripts that run many node processes.
# Source it from a script in scripts/:   . "$(dirname "$0")/lib/nodes.sh"
#
# Written for macOS's bash 3.2: no associative arrays, no {fd} syntax.
# Each node i gets:
#   $WORK/node<i>.log   its stdout/stderr (structured logs + console output)
#   $WORK/node<i>.in    a FIFO wired to its stdin; the script holds the write
#                       end open on file descriptor 10+i so the console never
#                       sees end-of-file, and `send i ...` types a command.
#   PID<i>              its process ID (read with `eval echo \$PID$i`)

PIDS=""

# nodes_init: make a work dir, build the binary, and kill every node on exit.
nodes_init() {
	WORK="$(mktemp -d)"
	BIN="$WORK/node"
	trap nodes_cleanup EXIT
	echo "Building node binary..."
	go build -o "$BIN" ./cmd/node
}

nodes_cleanup() {
	echo
	echo "Stopping all demo nodes..."
	for pid in $PIDS; do
		kill "$pid" >/dev/null 2>&1 || true
	done
	wait >/dev/null 2>&1 || true
	echo "Logs, FIFOs and files were kept in: $WORK"
}

# wait_for_log <file> <pattern> <timeout_s>: block until a line matches,
# or exit the script with the file's tail. Never use sleeps for this: a
# freshly built binary can take a while to start on macOS.
wait_for_log() {
	local file="$1" pattern="$2" timeout_s="$3" waited=0
	while ! grep -q "$pattern" "$file" 2>/dev/null; do
		sleep 0.1
		waited=$((waited + 1))
		if [ "$waited" -gt $((timeout_s * 10)) ]; then
			echo "Timed out after ${timeout_s}s waiting for '$pattern' in $file" >&2
			tail -n 20 "$file" >&2 || true
			exit 1
		fi
	done
}

# start_node <i> <udp-port> <bootstrap-port|-> [extra node flags...]
# Starts node i with --id-seed "<SEED_PREFIX><i>" and waits until it has
# bound its sockets (and, if bootstrapping, finished joining).
start_node() {
	local i="$1" port="$2" boot="$3"
	shift 3
	local bootflag=""
	if [ "$boot" != "-" ]; then bootflag="--bootstrap 127.0.0.1:$boot"; fi
	mkfifo "$WORK/node$i.in"
	# shellcheck disable=SC2086 # $bootflag is intentionally word-split
	"$BIN" --port "$port" --id-seed "${SEED_PREFIX:-demo}$i" $bootflag "$@" \
		<"$WORK/node$i.in" >"$WORK/node$i.log" 2>&1 &
	PIDS="$PIDS $!"
	eval "PID$i=$!"
	eval "exec $((10 + i))>\"$WORK/node$i.in\""
	wait_for_log "$WORK/node$i.log" "node started" 15
	if [ "$boot" != "-" ]; then
		wait_for_log "$WORK/node$i.log" "bootstrap complete" 15
	fi
}

# send <i> <command...>: type a console command into node i.
send() {
	local i="$1"
	shift
	echo "  [node$i] > $*"
	eval "printf '%s\n' \"\$*\" >&$((10 + i))"
}

# kill_node <i> [signal]: stop node i (default SIGKILL: an abrupt death,
# no goodbye). Its FIFO's write end is closed too.
kill_node() {
	local i="$1" sig="${2:-KILL}" pid
	eval "pid=\$PID$i"
	kill -"$sig" "$pid"
	{ wait "$pid"; } 2>/dev/null || true # reap it quietly (no "Killed: 9" notice)
	eval "exec $((10 + i))>&-"
}

# node_id <seed>: the 160-bit hex ID `node --id-seed <seed>` would use.
node_id() {
	printf '%s' "$1" | shasum -a 1 | awk '{print $1}'
}

# pieces_received <i>: distinct piece indexes node i has logged as received.
pieces_received() {
	grep 'msg="piece received"' "$WORK/node$1.log" 2>/dev/null |
		grep -o 'index=[0-9]*' | cut -d= -f2 | sort -n -u
}
