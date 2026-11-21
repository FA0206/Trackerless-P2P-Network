#!/usr/bin/env bash
# Tears down the lab built by up.sh: stops every process inside each p2p*
# namespace, waits until they're gone, then deletes the namespaces and the
# bridge. Safe to run when the lab is half-built or already gone.
#
# Usage: sudo bash scripts/netns/down.sh
#
# Why kill first: `ip netns del` only removes the namespace's name. While
# a process still runs inside, the namespace (and its veth cable) lives on,
# unnamed and hard to clean up. Network namespaces don't separate process
# IDs, so `ip netns pids` is the reliable way to find exactly the processes
# inside one -- never `pkill node`, which would hit nodes everywhere.
set -euo pipefail

BRIDGE=p2pbr

if [ "$(id -u)" -ne 0 ]; then
	echo "needs root to delete namespaces: sudo bash $0" >&2
	exit 1
fi

# stop_namespace <ns>: SIGTERM everything inside, wait up to 5 s, then
# SIGKILL whatever is left and wait until the namespace is empty.
stop_namespace() {
	local ns="$1" waited=0
	ip netns pids "$ns" | xargs -r kill 2>/dev/null || true
	while [ -n "$(ip netns pids "$ns")" ]; do
		if [ "$waited" -eq 50 ]; then
			ip netns pids "$ns" | xargs -r kill -KILL 2>/dev/null || true
		fi
		if [ "$waited" -ge 100 ]; then
			echo "  $ns: processes still running after 10 s: $(ip netns pids "$ns" | tr '\n' ' ')" >&2
			return 1
		fi
		sleep 0.1
		waited=$((waited + 1))
	done
}

for ns in $(ip netns list | awk '{print $1}' | grep -E '^p2p[0-9]+$' || true); do
	stop_namespace "$ns"
	ip netns del "$ns"
	echo "  deleted $ns"
done

# Deleting a namespace destroys the veth end inside it and, with it, the
# host end. Remove any host end left over from a half-built lab anyway.
for link in $(ip -o link show | grep -oE 'veth[0-9]+-br' | sort -u || true); do
	ip link del "$link" 2>/dev/null || true
done

if ip link show "$BRIDGE" >/dev/null 2>&1; then
	ip link del "$BRIDGE"
	echo "  deleted $BRIDGE"
fi

echo "Lab down."
