#!/usr/bin/env python3
"""03: Lookup cost vs network size, on a private Kademlia DHT.

The project brief's first graph: "the time it takes to find a peer in a network
of 5, 20 and 50 nodes. It should demonstrate a logarithmic curve, not a linear
one." This draws that graph with libtorrent's production DHT (BEP 5), so you
have a reference curve for the Go implementation.

For each size N it starts N DHT-only nodes on localhost (each joins through one
random existing node), publishes a few infohashes, then runs lookups for them
from random nodes and counts the get_peers RPCs each lookup needed.

    .venv/bin/python 03_dht_scaling.py                       # N = 5,10,20,50,100
    .venv/bin/python 03_dht_scaling.py --sizes 10,40,160 --lookups 20
"""

import argparse
import math
import os
import random
import statistics
import time

import libtorrent as lt

import lab


def one_lookup(node, target, timeout=10.0):
    node.ses.dht_get_peers(target)
    t0, rpcs = time.time(), set()
    while time.time() - t0 < timeout:
        node.ses.wait_for_alert(100)
        for a in node.ses.pop_alerts():
            if isinstance(a, lt.dht_outgoing_get_peers_alert) and a.info_hash == target:
                rpcs.add(a.endpoint)
            elif isinstance(a, lt.dht_get_peers_reply_alert) and a.info_hash == target:
                return len(rpcs), time.time() - t0, a.num_peers()
    return len(rpcs), None, 0


def measure(n, lookups, base_port, rng):
    nodes = lab.private_dht(n, base_port)
    # Some nodes announce "I have torrent X" so lookups have something to find.
    published = []
    for _ in range(max(3, lookups // 4)):
        ih = lt.sha1_hash(os.urandom(20))
        rng.choice(nodes).ses.dht_announce(ih, 6881, 0)
        published.append(ih)
    time.sleep(1.5)
    for x in nodes:
        x.pop_alerts()

    rpcs, times, found = [], [], 0
    for i in range(lookups):
        rpc, dt, peers = one_lookup(rng.choice(nodes), published[i % len(published)])
        rpcs.append(rpc)
        if dt is not None:
            times.append(dt)
        found += peers > 0
    for x in nodes:
        x.kill()
    return rpcs, times, found


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--sizes", default="5,10,20,50,100")
    ap.add_argument("--lookups", type=int, default=12, help="lookups per network size")
    args = ap.parse_args()
    sizes = [int(s) for s in args.sizes.split(",")]
    rng = random.Random(42)

    rows = []
    for i, n in enumerate(sizes):
        lab.banner(f"N = {n}")
        rpcs, times, found = measure(n, args.lookups, 8000 + 400 * i, rng)
        rows.append((n, statistics.mean(rpcs), max(rpcs),
                     1000 * statistics.median(times) if times else float("nan"), found))
        print(f"{found}/{args.lookups} lookups found the publisher; "
              f"RPCs per lookup: avg {rows[-1][1]:.1f}, max {rows[-1][2]}")

    lab.banner("Lookup cost vs network size")
    top = max(r[1] for r in rows)
    print(f"{'N':>5} {'log2 N':>7} {'RPCs/lookup':>12} {'median ms':>10}   (each # = 1 RPC)")
    for n, avg, mx, ms, _ in rows:
        print(f"{n:>5} {math.log2(n):>7.1f} {avg:>12.1f} {ms:>10.1f}   {'#' * round(avg)}")
    first, last = rows[0], rows[-1]
    print(f"\nN grew {last[0] / first[0]:.0f}x; RPCs per lookup grew {last[1] / first[1]:.1f}x.")
    print("RPCs = get_peers queries sent until the first reply that contained a peer\n"
          "(the brief's 'time to find a peer'). Asking nodes at random would need ~N/2.\n"
          "Kademlia's XOR routing gets about one bit closer to the target per hop, so\n"
          "the cost tracks log N instead.\n"
          "Localhost has ~0 ms latency: on a real network, multiply the hops by the RTT.")


if __name__ == "__main__":
    main()
