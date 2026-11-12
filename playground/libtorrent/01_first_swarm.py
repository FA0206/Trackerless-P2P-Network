#!/usr/bin/env python3
"""01: Your first swarm. One seeder, one leecher, both on this laptop.

    .venv/bin/python 01_first_swarm.py                 # fast
    .venv/bin/python 01_first_swarm.py --rate 0.5      # slow enough to watch / capture

TCP only and unencrypted, so Wireshark can decode every message. To capture:

    sudo tcpdump -i lo -w /tmp/bt.pcap 'tcp port 6881'    # terminal 1
    .venv/bin/python 01_first_swarm.py --rate 0.5         # terminal 2
    wireshark /tmp/bt.pcap   then filter:  bittorrent

Add --encrypt and capture again: Message Stream Encryption turns the same
conversation into noise.
"""

import argparse
import time

import libtorrent as lt

import lab

HELLO = r"""
   ____  _ _  _____                          _
  | __ )(_) ||_   _|__  _ __ _ __ ___ _ __ | |_
  |  _ \| | __|| |/ _ \| '__| '__/ _ \ '_ \| __|
  | |_) | | |_ | | (_) | |  | | |  __/ | | | |_
  |____/|_|\__||_|\___/|_|  |_|  \___|_| |_|\__|

  If you are reading this on the LEECHER's disk, it arrived in
  16 KiB blocks, was checked against SHA-1 + SHA-256 hashes,
  and no tracker was involved. Welcome to the swarm.
"""


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--size-mb", type=int, default=64, help="size of the random payload file")
    ap.add_argument("--rate", type=float, default=0, help="seeder upload cap in MB/s (0 = unlimited)")
    ap.add_argument("--encrypt", action="store_true", help="enable BitTorrent protocol encryption")
    args = ap.parse_args()

    w = lab.workdir("01-first-swarm")
    content = w / "seeder" / "bt-hello"
    (content / "hello.txt").parent.mkdir(parents=True)
    (content / "hello.txt").write_text(HELLO)
    lab.make_file(content / "payload.bin", args.size_mb * lab.MiB)

    lab.banner("1. Make the .torrent")
    ti = lab.make_torrent(content, 256 * lab.KiB, "hybrid", out=w / "bt-hello.torrent")
    print(f"{ti.num_files()} files, {ti.num_pieces()} pieces of {ti.piece_length() // lab.KiB} KiB")
    print(f"infohash v1: {ti.info_hashes().v1}")
    print(f"infohash v2: {ti.info_hashes().v2}")

    lab.banner("2. Start two BitTorrent clients")
    up = args.rate * lab.MiB
    seeder = lab.Node("seeder", 6881, tcp_only=True, encrypt=args.encrypt, up_limit=up)
    leecher = lab.Node("leecher", 6882, tcp_only=True, encrypt=args.encrypt)
    seeder.add(ti, w / "seeder")
    leecher.add(ti, w / "leecher")
    print("seeder  listening on 127.0.0.1:6881 (has the whole file)")
    print("leecher listening on 127.0.0.1:6882 (has nothing)")

    # wait for the seeder to finish checking its files
    while seeder.status().state != lt.torrent_status.seeding:
        time.sleep(0.1)

    lab.banner("3. Introduce them (the job a tracker or DHT normally does)")
    leecher.connect(seeder)
    print("leecher -> connect_peer(127.0.0.1:6881)\n")

    scr = lab.Screen()
    t0 = time.time()
    while True:
        time.sleep(0.25)
        for node in (seeder, leecher):
            for a in node.pop_alerts():
                if isinstance(a, (lt.peer_connect_alert, lt.peer_disconnected_alert,
                                  lt.hash_failed_alert)) or (
                        node is leecher and isinstance(a, lt.torrent_finished_alert)):
                    scr.note(f"  [{time.time() - t0:5.2f}s] {node.name}: {a.message()}")
        s, l = seeder.status(), leecher.status()
        scr.draw([
            f"seeder   {lab.bar(1)} 100.0%  up   {lab.rate(s.upload_payload_rate)}",
            f"leecher  {lab.bar(l.progress)} {l.progress * 100:5.1f}%  down {lab.rate(l.download_payload_rate)}",
            f"pieces   {lab.piece_map(l.pieces)}",
        ])
        if l.is_seeding:
            break
    elapsed = time.time() - t0

    lab.banner("4. Verify")
    a, b = lab.md5_tree(content), lab.md5_tree(w / "leecher" / "bt-hello")
    print(f"md5 seeder : {a}\nmd5 leecher: {b}\n{'IDENTICAL' if a == b else 'MISMATCH!'}"
          f"  ({args.size_mb} MiB in {elapsed:.1f}s = {args.size_mb / elapsed:.1f} MiB/s)")
    print((w / "leecher" / "bt-hello" / "hello.txt").read_text())

    lab.banner("5. What did that cost on the wire?")
    c = leecher.counters()
    total, payload = c["net.recv_bytes"] + c["net.recv_ip_overhead_bytes"], c["net.recv_payload_bytes"]
    print(f"leecher received {lab.human(total)} in total, of which payload {lab.human(payload)}")
    print(f"protocol + TCP/IP overhead: {100 * (total - payload) / total:.2f}%")
    print(f"\n.torrent and files: {w}")


if __name__ == "__main__":
    main()
