#!/usr/bin/env python3
"""04: Join the real Mainline DHT and watch your k-buckets fill.

The Mainline DHT (BEP 5) is the Kademlia network every BitTorrent client
shares: millions of nodes, no owner. This joins it as a plain DHT node (no
torrents, nothing downloaded, nothing shared), then:

  1. draws your routing table as it grows, bucket by bucket
  2. estimates the network's size from how deep the table got
  3. times real get_peers lookups (the "who has this infohash?" query) and
     counts their RPCs, to compare with log2(N)

    .venv/bin/python 04_dht_internet.py
    .venv/bin/python 04_dht_internet.py --warmup 120 --lookup <40-hex infohash>

Needs the DHT's UDP traffic to get out. Many campus networks drop it (DNS still
works, the DHT routers just never answer); a phone hotspot or home Wi-Fi works.
"""

import argparse
import math
import os
import time

import libtorrent as lt

import lab

# Legal, Creative Commons films that WebTorrent uses as demos.
KNOWN = {
    "Sintel (Blender, CC-BY)": "08ada5a7a6183aae1e09d831df6748d566095a10",
    "Big Buck Bunny (Blender, CC-BY)": "dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c",
}
K = 8  # libtorrent's bucket size (the Kademlia paper's k; the Go project uses 20)


def draw_table(buckets):
    lines = ["bucket = how many leading bits a node's ID shares with yours"]
    for i, b in enumerate(buckets):
        n, r = b["num_nodes"], b["num_replacements"]
        lines.append(f"  {i:3d}  {'#' * n:{K}}  {n:2d} live  {r:2d} spare")
    return lines


def lookup(ses, name, ih_hex, timeout=30):
    target = lt.sha1_hash(bytes.fromhex(ih_hex))
    ses.dht_get_peers(target)
    t0, rpcs, nodes = time.time(), 0, set()
    while time.time() - t0 < timeout:
        ses.wait_for_alert(250)
        for a in ses.pop_alerts():
            if isinstance(a, lt.dht_outgoing_get_peers_alert) and a.info_hash == target:
                rpcs += 1
                nodes.add(a.endpoint)
            elif isinstance(a, lt.dht_get_peers_reply_alert) and a.info_hash == target:
                dt = time.time() - t0
                print(f"  {name:32} {dt:6.2f}s  {rpcs:4d} get_peers RPCs to {len(nodes):4d} nodes"
                      f"  ->  {a.num_peers} peers have it")
                return dt, rpcs
    print(f"  {name:32} timed out after {timeout}s ({rpcs} RPCs sent)")
    return None, rpcs


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--warmup", type=int, default=45, help="seconds to let the routing table grow")
    ap.add_argument("--port", type=int, default=0, help="UDP/TCP port (0 = pick one)")
    ap.add_argument("--lookup", action="append", default=[], help="extra infohash(es) to look up")
    args = ap.parse_args()

    ses = lt.session({
        "listen_interfaces": f"0.0.0.0:{args.port}",
        "enable_dht": True,
        "dht_bootstrap_nodes": "dht.libtorrent.org:25401,router.bittorrent.com:6881,"
                               "dht.transmissionbt.com:6881,router.utorrent.com:6881",
        # leave the home router alone, and don't shout on the LAN
        "enable_upnp": False, "enable_natpmp": False, "enable_lsd": False,
        "alert_mask": lt.alert_category.dht | lt.alert_category.dht_operation
                      | lt.alert_category.status | lt.alert_category.error,
    })

    lab.banner("1. Joining the Mainline DHT")
    scr = lab.Screen()
    t0 = time.time()
    buckets, my_ip = [], None
    while time.time() - t0 < args.warmup:
        ses.post_dht_stats()
        time.sleep(1)
        for a in ses.pop_alerts():
            if isinstance(a, lt.dht_stats_alert):
                buckets = a.routing_table
            elif isinstance(a, lt.external_ip_alert):
                my_ip = str(a.external_address)
            elif isinstance(a, lt.dht_bootstrap_alert):
                scr.note(f"  [{time.time() - t0:4.1f}s] bootstrap finished")
        n = ses.status().dht_nodes
        scr.draw([f"t={time.time() - t0:4.0f}s  nodes in routing table: {n}   "
                  f"(other nodes told us our public IP: {'yes' if my_ip else 'not yet'})",
                  *draw_table(buckets)])
        if n == 0 and time.time() - t0 > 25:
            break

    total = sum(b["num_nodes"] for b in buckets)
    if total == 0:
        print("\nNo DHT node answered in 25 s. This network is dropping BitTorrent DHT\n"
              "traffic (common on campus Wi-Fi: plain UDP such as DNS works, but the DHT\n"
              "routers never reply). Re-run on a phone hotspot or home network.\n"
              "Meanwhile, 03_dht_scaling.py and 05_dht_postit.py run a private DHT on\n"
              "localhost, which no firewall can block.")
        return

    lab.banner("2. How big is the network?")
    depth = len(buckets)
    est = K * 2 ** (depth - 1)
    print(f"Your table is {depth} buckets deep. Each bucket halves the ID space, and\n"
          f"splitting stops when ~k={K} nodes are left near your ID, so N ~ k * 2^(depth-1)\n"
          f"  = {K} * 2^{depth - 1} ~ {est:,.0f} nodes  (very rough; it grows the longer you stay)\n"
          f"Published measurements put the Mainline DHT at roughly 10-25 million nodes.\n"
          f"log2(N) ~ {math.log2(est):.0f}: that's the number of ID bits a lookup has to 'fix'.")
    print("Note: with BEP 42 your node ID is derived from your public IP, which other\n"
          "nodes reported back to you (the 'ip' key in every KRPC reply).")

    lab.banner("3. Lookups: who has this infohash?")
    targets = dict(KNOWN)
    for i, h in enumerate(args.lookup):
        targets[f"--lookup #{i + 1}"] = h
    targets["random ID (nobody has it)"] = os.urandom(20).hex()
    for name, ih in targets.items():
        lookup(ses, name, ih)
    print("\nEach lookup asks ~alpha nodes at a time, always the closest ones it knows,\n"
          "until the k closest to the target have answered: O(log N) rounds.\n"
          "The RPC count is what your Go node's `--lookup` reports too.")


if __name__ == "__main__":
    main()
