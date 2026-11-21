#!/usr/bin/env bash
# Builds the multi-machine lab: N Linux network namespaces joined by one
# virtual switch (a bridge), so N nodes on this laptop behave like N
# separate machines on one subnet. Each namespace has its own IP, its own
# 127.0.0.1 and its own ports, so "127.0.0.1" inside p2p1 is a different
# place from "127.0.0.1" inside p2p2 -- exactly as on real devices. Its
# link has the real 1500-byte MTU, so large UDP replies get fragmented,
# which the loopback interface (64 KB MTU) hides.
#
# Usage: sudo bash scripts/netns/up.sh [N]      (default N=5; Linux only)
# Undo:  sudo bash scripts/netns/down.sh
#
#   namespace p2p<i>: veth<i> = 10.10.0.<i>/24 --- veth<i>-br --+
#                                                               +-- bridge p2pbr = 10.10.0.254/24 (host)
#
# One node per namespace: the multi-device setup assumes one node per
# device. Build into the git-ignored lab/
# (go build -o lab/node ./cmd/node), then run a node inside namespace i
# as yourself, not root, from the repo root:
#   sudo ip netns exec p2p1 sudo -u "$USER" ./lab/node --port 9000 --id-seed A --advertise-ip 10.10.0.1
#
# The host address on the bridge lets this laptop reach the nodes
# (`ping 10.10.0.1`, or a node's --api bound to 10.10.0.<i>). It exists
# only on this laptop, so nothing outside can reach the lab.
#
# Safe to run twice: anything that already exists is left as it is.
#
# If Docker's daemon is running, it may set the FORWARD policy to DROP and
# silently block traffic across the bridge. Fix:
#   sudo iptables -I FORWARD -i p2pbr -j ACCEPT
set -euo pipefail

N="${1:-5}"
BRIDGE=p2pbr
SUBNET=10.10.0
HOST_ADDR="$SUBNET.254/24"

if [ "$(id -u)" -ne 0 ]; then
	echo "needs root to create namespaces: sudo bash $0 [N]" >&2
	exit 1
fi
case "$N" in
'' | *[!0-9]*)
	echo "N must be a number from 1 to 253, got '$N'" >&2
	exit 1
	;;
esac
if [ "$N" -lt 1 ] || [ "$N" -gt 253 ]; then
	echo "N must be a number from 1 to 253, got '$N'" >&2
	exit 1
fi

# The virtual switch, with the host's own address on it.
if ! ip link show "$BRIDGE" >/dev/null 2>&1; then
	ip link add "$BRIDGE" type bridge
fi
ip link set "$BRIDGE" up
if ! ip -4 addr show dev "$BRIDGE" | grep -q "inet $HOST_ADDR"; then
	ip addr add "$HOST_ADDR" dev "$BRIDGE"
fi

for i in $(seq 1 "$N"); do
	ns="p2p$i"
	if [ ! -e "/var/run/netns/$ns" ]; then
		ip netns add "$ns"
	fi
	# A veth pair is a virtual cable: veth<i> goes inside the namespace,
	# veth<i>-br stays on the host and plugs into the bridge.
	if ! ip link show "veth$i-br" >/dev/null 2>&1; then
		ip link add "veth$i-br" type veth peer name "veth$i" netns "$ns"
	fi
	ip link set "veth$i-br" master "$BRIDGE" up
	if ! ip -n "$ns" -4 addr show dev "veth$i" | grep -q "inet $SUBNET.$i/24"; then
		ip -n "$ns" addr add "$SUBNET.$i/24" dev "veth$i"
	fi
	ip -n "$ns" link set "veth$i" up
	ip -n "$ns" link set lo up
	echo "  $ns: $SUBNET.$i"
done

echo "Lab up: $N namespace(s) on bridge $BRIDGE ($SUBNET.0/24); host is $SUBNET.254."
echo "Check: ping -c1 $SUBNET.1    Tear down: sudo bash scripts/netns/down.sh"
