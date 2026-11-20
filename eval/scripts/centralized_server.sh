#!/usr/bin/env bash
#
# Graph 2's centralized baseline: serve one directory over plain HTTP from a
# single server (eval/fileserver), its total upload capped at <rate> bytes
# per second (0 = uncapped). Runs in the foreground until killed;
# download_time.sh starts it in the background and stops it when done.
#
# Usage: eval/scripts/centralized_server.sh <dir> <port> [rate]
set -euo pipefail
cd "$(dirname "$0")/../.."
BIN="${FILESERVER_BIN:-$(mktemp -d)/fileserver}"
[ -x "$BIN" ] || go build -o "$BIN" ./eval/fileserver
exec "$BIN" -dir "$1" -addr "127.0.0.1:$2" -rate "${3:-0}"
