#!/usr/bin/env bash
#
# Headless check of the visualizer's run: runs
# every scene with `vizrun --auto` (no browser), then checks the evidence
# the page would draw from:
#   - vizrun's own checks passed (lookup rounds, SHA-256 of every copy,
#     the kill moment, give up + resume);
#   - events.jsonl holds every event kind the scenes draw;
#   - no stream dropped an event (`dropped` is 0 on every line);
#   - afterwards, no node port, API port or page port is still bound.
#
# Usage: bash scripts/demo-viz.sh   (env: BASE_PORT=9740 API_BASE=8740 HTTP=127.0.0.1:8791)
# Takes about 90 s (scene 8 has two real 15 s stalls).
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_PORT="${BASE_PORT:-9740}"
API_BASE="${API_BASE:-8740}"
HTTP="${HTTP:-127.0.0.1:8791}"
N=20
WORK="$(mktemp -d)"

# port_busy <port>: is anything listening on it (UDP or TCP)?
port_busy() {
	if command -v lsof >/dev/null 2>&1; then
		lsof -nP -i ":$1" >/dev/null 2>&1
	else
		ss -lunt 2>/dev/null | grep -qE "[:.]$1\b"
	fi
}

ports() { # every port this run uses: N+1 nodes (scene 8 adds one), their APIs, the page
	for i in $(seq 0 "$N"); do echo $((BASE_PORT + i)) $((API_BASE + i)); done | tr ' ' '\n'
	echo "${HTTP##*:}"
}

for p in $(ports); do
	if port_busy "$p"; then
		echo "FAIL: port $p is already in use; stop whatever holds it (or set BASE_PORT / API_BASE / HTTP)"
		exit 1
	fi
done

echo "Running every scene headless (vizrun --auto), output in $WORK ..."
STATUS=0
go run ./cmd/vizrun --auto --n "$N" --base-port "$BASE_PORT" --api-base "$API_BASE" --http "$HTTP" --out "$WORK" | tee "$WORK/vizrun.out" || STATUS=$?

echo
echo "=== Checks ==="
fail() { echo "  FAIL: $*"; STATUS=1; }

grep -q '^vizrun: PASS' "$WORK/vizrun.out" && echo "  vizrun's checks: all ok" || fail "vizrun's own checks (see above)"

EVENTS="$WORK/events.jsonl"
[ -s "$EVENTS" ] || fail "no events.jsonl"
echo "  events recorded: $(wc -l <"$EVENTS" | tr -d ' ')"
for msg in "rpc sent" "rpc received" "lookup round starting" "lookup converged" "value stored" \
	"store complete" "find_value finished" "share complete" "provider announced" "piece received" \
	"tcp request served" "provider failed, trying the next one" "download complete" \
	"download stalled: no live provider has the missing pieces; looking for more" \
	"download gave up, still serving its pieces" "download resumed" "bucket updated"; do
	c="$(grep -c "\"msg\":\"$msg\"" "$EVENTS" || true)"
	if [ "$c" -gt 0 ]; then printf '  %5d  %s\n' "$c" "$msg"; else fail "no '$msg' event"; fi
done

lost="$(grep -c '"dropped":[1-9]' "$EVENTS" || true)"
[ "$lost" -eq 0 ] && echo "  dropped: 0 on every event" || fail "$lost event(s) report dropped > 0"

matches="$(grep -c 'check: ok sha256: .* matches the original' "$WORK/vizrun.out" || true)"
[ "$matches" -eq 5 ] && echo "  SHA-256: 5 of 5 downloaded copies match their originals" || fail "SHA-256 matches: $matches of 5"

left=""
for p in $(ports); do port_busy "$p" && left="$left $p"; done
[ -z "$left" ] && echo "  no port left bound after the run" || fail "still bound:$left"

echo
if [ "$STATUS" -eq 0 ]; then
	echo "PASS: every scene ran on live nodes, every event kind the page draws was recorded, nothing was dropped, and every copy matched."
else
	echo "FAIL (logs and events in $WORK)"
fi
exit "$STATUS"
