# Shared helpers for the edge-case scripts in scripts/edge/.
# Source it AFTER nodes.sh:
#
#   . scripts/lib/nodes.sh
#   . scripts/lib/edge.sh
#
# Like nodes.sh, written for macOS's bash 3.2 (no associative arrays).
# Per-node IDs live in variables ID0, ID1, ... (read with `eval`).

# xor_hex <a> <b>: XOR of two 40-hex-digit IDs, as 40 hex digits. Done 8
# digits at a time because bash integers are 64-bit. Equal-length hex
# strings sort in numeric order, so plain `sort` ranks distances. (Same
# function as in scripts/demo-big-network.sh, which is left unchanged.)
xor_hex() {
	local a="$1" b="$2" out="" j
	for j in 0 8 16 24 32; do
		out="$out$(printf '%08x' $((0x${a:j:8} ^ 0x${b:j:8})))"
	done
	echo "$out"
}

# node_ids_init <n>: set ID<i> = SHA-1("<SEED_PREFIX><i>") for i < n, the
# ID `start_node i` gives node i. Computed once: shasum is a process per
# call, which is slow (especially under WSL).
node_ids_init() {
	local i
	for ((i = 0; i < $1; i++)); do
		eval "ID$i=$(node_id "${SEED_PREFIX:-demo}$i")"
	done
}

# closest_nodes <target-hex> <count> <index...>: of the given node indexes,
# the <count> whose IDs are XOR-closest to target, nearest first. This is
# the brute-force ground truth the DHT's behavior is compared with.
closest_nodes() {
	local target="$1" count="$2" i id
	shift 2
	for i in "$@"; do
		eval "id=\$ID$i"
		echo "$(xor_hex "$id" "$target") $i"
	done | sort | awk -v c="$count" 'NR <= c {print $2}'
}

# in_list <x> <item...>: true if x is one of the items.
in_list() {
	local x="$1"
	shift
	case " $* " in *" $x "*) return 0 ;; esac
	return 1
}

# count_lines <file> <regex>: how many lines of file match (0 if none).
count_lines() {
	local n
	n="$(grep -c -- "$2" "$1" 2>/dev/null)" || true
	echo "${n:-0}"
}

# wait_for_count <file> <regex> <n> <timeout_s>: block until at least n
# lines match. Use it instead of wait_for_log when the same console
# command runs more than once on one node (the first match is old).
wait_for_count() {
	local file="$1" pattern="$2" want="$3" timeout_s="$4" waited=0
	while [ "$(count_lines "$file" "$pattern")" -lt "$want" ]; do
		sleep 0.1
		waited=$((waited + 1))
		if [ "$waited" -gt $((timeout_s * 10)) ]; then
			echo "Timed out after ${timeout_s}s waiting for $want x '$pattern' in $file" >&2
			tail -n 20 "$file" >&2 || true
			exit 1
		fi
	done
}

# now_ms: milliseconds on the monotonic clock, for timing things. Not the
# wall clock: that can be stepped while a script runs (WSL re-syncs it
# with the host, which once made a 2s lookup measure 1.34s). perl because
# macOS's date has no %N; shasum, which nodes.sh already needs, is perl too.
now_ms() {
	perl -MTime::HiRes=clock_gettime,CLOCK_MONOTONIC -e 'printf "%d\n", clock_gettime(CLOCK_MONOTONIC) * 1000'
}

# fmt_ms <ms>: "12ms" or "6.04s".
fmt_ms() {
	if [ "$1" -lt 1000 ]; then echo "${1}ms"; else printf '%d.%02ds\n' $(($1 / 1000)) $(($1 % 1000 / 10)); fi
}

# index_of_id <hex-id> <n>: the node index (< n) with that ID, or "?".
index_of_id() {
	local i id
	for ((i = 0; i < $2; i++)); do
		eval "id=\$ID$i"
		if [ "$id" = "$1" ]; then echo "$i"; return; fi
	done
	echo "?"
}

# lookup_answer <i> <target> <nth>: the contact IDs node i's console printed
# for its nth `lookup <target>` (the numbered "  N. <id>@ip:port" lines),
# nearest first. Same format scripts/demo-big-network.sh parses.
lookup_answer() {
	awk -v hdr="lookup $2:" -v nth="$3" '
		index($0, hdr) == 1 { seen++; on = (seen == nth); next }
		on && /^time=/ { next }                       # log lines can interleave
		on && /^ +[0-9]+\. / { split($2, a, "@"); print a[1]; next }
		on { exit }' "$WORK/node$1.log"
}

# start_node_nowait <i> <udp-port> <bootstrap-port|-> [extra flags...]:
# like nodes.sh's start_node, but returns immediately, so many nodes can be
# started at the same moment. Wait for them with wait_for_log afterwards.
start_node_nowait() {
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
}

# holders_of <key> <n>: indexes of nodes (< n) that logged storing a value
# under key, i.e. the nodes a STORE RPC for that key reached.
holders_of() {
	local key="$1" n="$2" i out=""
	for ((i = 0; i < n; i++)); do
		if grep -q "msg=\"value stored\".*key=$key" "$WORK/node$i.log" 2>/dev/null; then
			out="$out $i"
		fi
	done
	echo $out
}
