# Shared helpers for the eval scripts (docs/10-performance-graphs.md).
# Source it from a script in eval/scripts/:   . eval/scripts/lib.sh
# (each script first cds to the repo root).
#
# Builds on scripts/lib/nodes.sh (start_node, kill_node, wait_for_log) and
# scripts/lib/edge.sh (now_ms, wait_for_count). Like those, bash 3.2 safe.
# Nodes are driven through their HTTP API (--api, --api-control): node i
# listens on UDP/TCP port BASE_PORT+i and serves its API on
# BASE_PORT+API_OFFSET+i.

. scripts/lib/nodes.sh
. scripts/lib/edge.sh

API_OFFSET=10000
RESULTS=eval/results
NODE_COUNT=0 # nodes started in the current run (indexes 0..NODE_COUNT-1)

command -v go >/dev/null || { echo "go is not on PATH (see eval/README.md)" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
mkdir -p "$RESULTS"

# eval_init: build the node binary once (nodes_init) and keep it outside the
# per-run directories, so new_run can give every run fresh logs and FIFOs.
eval_init() {
	nodes_init
	ROOT_WORK="$WORK"
	RUN=0
	trap eval_cleanup EXIT
}

# eval_cleanup: like nodes_cleanup, but test files and downloaded copies
# go (a sweep would otherwise leave gigabytes in /tmp); logs stay.
eval_cleanup() {
	nodes_cleanup
	find "$ROOT_WORK" -name '*.bin' -delete 2>/dev/null || true
}

# new_run: stop every node of the previous run and point WORK at an empty
# directory for the next one. The previous run keeps its logs, not its files.
new_run() {
	stop_all_nodes
	[ "$RUN" -eq 0 ] || rm -f "$WORK"/*.bin
	RUN=$((RUN + 1))
	WORK="$ROOT_WORK/run$RUN"
	mkdir -p "$WORK"
}

# same_file <a> <b>: fail the script unless b is a byte-identical copy of a;
# then delete b, so copies never pile up.
same_file() {
	cmp -s "$1" "$2" || { echo "$2 is not an identical copy of $1" >&2; exit 1; }
	rm -f "$2"
}

stop_all_nodes() {
	local i
	for pid in $PIDS; do kill -KILL "$pid" >/dev/null 2>&1 || true; done
	for pid in $PIDS; do wait "$pid" >/dev/null 2>&1 || true; done # only nodes: other jobs may outlive a run
	for ((i = 0; i < NODE_COUNT; i++)); do eval "exec $((10 + i))>&-" 2>/dev/null || true; done
	PIDS=""
	NODE_COUNT=0
}

api_port() { echo $((BASE_PORT + API_OFFSET + $1)); }

# start_api_node <i> <bootstrap-index|-> [extra node flags...]: start node i
# with its HTTP API (control endpoints on), joining through another node.
start_api_node() {
	local i="$1" via="$2" boot="-"
	shift 2
	[ "$via" = "-" ] || boot=$((BASE_PORT + via))
	start_node "$i" $((BASE_PORT + i)) "$boot" --api "127.0.0.1:$(api_port "$i")" --api-control "$@"
	[ "$i" -lt "$NODE_COUNT" ] || NODE_COUNT=$((i + 1))
}

# grow_to <n> [extra node flags...]: add nodes until the run has n of them.
# Node 0 starts alone; every later node joins through a randomly chosen
# running node (scripts/demo-big-network.sh's topology), so the network
# isn't a chain. start_node returns once each join has finished.
grow_to() {
	local n="$1" i
	shift
	for ((i = NODE_COUNT; i < n; i++)); do
		if [ "$i" -eq 0 ]; then
			start_api_node 0 - "$@"
		else
			start_api_node "$i" $((RANDOM % i)) "$@"
		fi
	done
}

# api_post <i> <endpoint> <json> <out-file>: POST to node i's control API,
# write the response body to out-file and print the request's wall time in
# milliseconds (curl's own measurement of the HTTP round trip). Fails on a
# non-2xx answer, showing the body.
api_post() {
	local i="$1" path="$2" body="$3" out="$4" secs
	secs="$(curl -sS -o "$out" -w '%{time_total}' --fail-with-body \
		-H 'Content-Type: application/json' --data "$body" \
		"http://127.0.0.1:$(api_port "$i")/api/$path")" || {
		echo "POST /api/$path on node$i failed: $(cat "$out" 2>/dev/null)" >&2
		return 1
	}
	awk -v s="$secs" 'BEGIN { printf "%.3f\n", s * 1000 }'
}

# json_num <file> <field>: a top-level number field from an API response
# (the API indents its JSON, so one field per line).
json_num() {
	grep -o "\"$2\": *[0-9.]*" "$1" | head -n 1 | sed 's/.*: *//'
}

# json_str <file> <field>
json_str() {
	grep -o "\"$2\": *\"[^\"]*\"" "$1" | head -n 1 | sed 's/.*: *"//; s/"$//'
}

# random_id: 40 random hex digits, a lookup target.
random_id() {
	head -c 20 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

# log_ms <line>: a log line's time= field as milliseconds since midnight
# (slog prints RFC 3339 with milliseconds, e.g. time=2026-11-20T16:45:01.123+05:30).
log_ms() {
	printf '%s\n' "$1" | awk '{
		split($1, t, "T"); split(substr(t[2], 1, 12), h, ":")
		printf "%d\n", (h[1] * 3600 + h[2] * 60 + h[3]) * 1000 + 0.5
	}'
}

# share_file <i> <path>: node i shares a file; prints its content key.
share_file() {
	local out="$WORK/share$1.json"
	api_post "$1" share "{\"path\":\"$2\"}" "$out" >/dev/null
	json_str "$out" key
}

# csv_reset <file> <header>: start a CSV afresh.
csv_reset() {
	echo "$2" >"$1"
}

# csv_keep_other_env <file> <header> <env>: drop this env's rows from a CSV
# whose third column is the environment, keeping the other environment's
# rows (the hand-recorded real-host trials) intact.
csv_keep_other_env() {
	local file="$1" header="$2" env="$3"
	if [ ! -f "$file" ]; then echo "$header" >"$file"; return; fi
	awk -F, -v env="$env" -v hdr="$header" 'NR == 1 { print hdr; next } $3 != env' "$file" >"$file.tmp"
	mv "$file.tmp" "$file"
}

# bg <command...>: run a command in the background, remembering its PID in
# JOBS; wait_jobs waits for all of them and fails if any failed.
JOBS=""
bg() {
	"$@" &
	JOBS="$JOBS $!"
}
wait_jobs() {
	local pid status=0
	for pid in $JOBS; do wait "$pid" || status=1; done
	JOBS=""
	return "$status"
}

# make_file <path> <bytes>: random test data.
make_file() {
	head -c "$2" /dev/urandom >"$1"
}
