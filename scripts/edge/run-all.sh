#!/usr/bin/env bash
#
# Run every edge-case script one after another and
# print one verdict line per case. Each script's full output is kept in a
# directory printed at the end. Exits non-zero if any case FAILed, i.e.
# behaved differently from its documented prediction. After a fix to the
# core code, this shows which predictions need flipping.
#
# Usage: scripts/edge/run-all.sh   (takes a few minutes; env vars such as
#        SEED are passed through to every script)
set -uo pipefail
cd "$(dirname "$0")/../.."

OUT="$(mktemp -d)"
STATUS=0
for script in scripts/edge/[a-z][0-9]-*.sh; do
	name="$(basename "$script" .sh)"
	start=$SECONDS
	bash "$script" >"$OUT/$name.log" 2>&1
	rc=$?
	verdict="$(grep -E '^(PASS|FAIL)' "$OUT/$name.log" | tail -1)"
	printf '%-32s %4ss  %s\n' "$name" $((SECONDS - start)) "${verdict:-no verdict (exit $rc)}"
	[ "$rc" -eq 0 ] || STATUS=1
done
echo
echo "Full output of each case: $OUT"
exit "$STATUS"
