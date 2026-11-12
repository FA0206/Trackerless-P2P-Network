#!/usr/bin/env python3
"""02: Chaos swarm. The project's demo, on real BitTorrent.

One seed and N leechers on localhost. Nobody is told where the others are:
every node joins a PRIVATE DHT through node 0 (like Kademlia's bootstrap),
announces the infohash, and asks "who has this?" (BEP 5 announce_peer /
get_peers). Then CHAOS: the only seed is killed mid-download.

    .venv/bin/python 02_chaos_swarm.py                        # kill the seed at 40%
    .venv/bin/python 02_chaos_swarm.py --kill-at 0.1          # too early: does it stall?
    .venv/bin/python 02_chaos_swarm.py --kill-when-complete   # earliest safe moment
    .venv/bin/python 02_chaos_swarm.py --kill-when-complete --super-seed
    .venv/bin/python 02_chaos_swarm.py --sweep 1,2,4,8        # P2P vs client-server vs theory

Measures what the project brief asks for: time to recovery after the seed
dies, control bytes vs payload bytes, and download time vs leecher count.
"""

import argparse
import statistics
import time

import libtorrent as lt

import lab

TICK = 0.2


# --------------------------------------------------------------------------
# building and running a swarm
# --------------------------------------------------------------------------

def build_swarm(w, ti, n, *, seed_up, peer_up, dht=True, share=True, super_seed=False,
                base_port=7000, log=print):
    seed = lab.Node("seed", base_port, dht=dht, up_limit=seed_up)
    # share=False makes leechers refuse to upload (0 unchoke slots): that is
    # exactly a client-server download where only the server sends data.
    extra = {} if share else {"unchoke_slots_limit": 0}
    peers = [lab.Node(f"peer{i}", base_port + i, dht=dht, up_limit=peer_up, extra=extra)
             for i in range(1, n + 1)]

    if dht:
        # Step 1: build the DHT first. Everyone joins through node 0, the same
        # way the Go demo bootstraps (an announce into an empty routing table
        # would reach nobody).
        for p in peers:
            p.bootstrap_dht(seed)
        t0 = time.time()
        while time.time() - t0 < 5 and min(x.ses.status().dht_nodes for x in [seed, *peers]) < 1:
            time.sleep(0.05)
        log(f"DHT formed in {time.time() - t0:.2f}s: routing-table sizes "
            + ", ".join(f"{x.name}={x.ses.status().dht_nodes}" for x in [seed, *peers]))

    # Step 2: the seed publishes the file.
    seed.add(ti, w / "seed", flags=lt.torrent_flags.super_seeding if super_seed else 0)
    while seed.status().state != lt.torrent_status.seeding:
        time.sleep(0.05)

    # Step 3: leechers want it. With the DHT they only know the infohash.
    for i, p in enumerate(peers):
        p.add(ti, w / p.name)
        if not dht:                        # --direct: wire everyone up by hand
            for other in [seed, *peers[:i]]:
                p.connect(other)
    return seed, peers


def swarm_availability(statuses):
    """Fraction of pieces that exist on at least one leecher."""
    fields = [s.pieces for s in statuses]
    n = len(fields[0])
    return sum(1 for i in range(n) if any(f[i] for f in fields)) / n


def run_swarm(seed, peers, *, kill_at=None, kill_when_complete=False, live=True, stall_after=10.0):
    scr = lab.Screen()
    t0 = time.time()
    finished, timeline = {}, []          # timeline: (t, swarm download B/s)
    killed = None                        # dict describing the kill moment
    recovered_at = None
    last_done, last_change = -1, t0
    last_announce = 0.0
    found = {}                           # most peers each node has heard of via the DHT
    first_byte = None                    # when the first payload byte arrived anywhere

    while True:
        time.sleep(TICK)
        now = time.time() - t0
        sts = [p.status() for p in peers]

        for p in [seed, *peers]:
            for a in p.pop_alerts():
                if live and isinstance(a, lt.dht_reply_alert) and a.num_peers > found.get(p.name, 0):
                    found[p.name] = a.num_peers
                    scr.note(f"  [{now:5.1f}s] {p.name}: DHT get_peers says {a.num_peers} other peer(s) have this infohash")
                elif live and isinstance(a, lt.peer_ban_alert):
                    scr.note(f"  [{now:5.1f}s] {p.name}: banned {a.endpoint}")

        # A node that still knows no peers re-announces and asks the DHT again
        # (libtorrent would do this by itself, but only every few minutes).
        if now - last_announce > 2:
            last_announce = now
            for p in [seed, *peers]:
                s = p.status()
                if s and s.num_peers == 0 and not (p is not seed and s.is_seeding):
                    p.handle.force_dht_announce()

        avg = statistics.mean(s.progress for s in sts)
        avail = swarm_availability(sts)
        done = sum(s.total_wanted_done for s in sts)
        if first_byte is None and done > 0:
            first_byte = now
        timeline.append((now, sum(s.download_payload_rate for s in sts)))

        for p, s in zip(peers, sts):
            if s.is_seeding and p.name not in finished:
                finished[p.name] = now

        if seed.alive and ((kill_at is not None and avg >= kill_at) or (kill_when_complete and avail >= 1)):
            killed = {"t": now, "avg": avg, "avail": avail, "done": done,
                      "rate_before": statistics.mean(r for _, r in timeline[-int(3 / TICK):])}
            seed.kill()
            if live:
                scr.note(f"\n  [{now:5.1f}s] *** SEED KILLED *** average progress {avg:.0%}, "
                         f"pieces still alive among leechers: {avail:.1%}\n")

        if killed and recovered_at is None and done > killed["done"]:
            recovered_at = now

        if done != last_done:
            last_done, last_change = done, now

        if live:
            lines = [f"t={now:5.1f}s  avg progress {avg:6.1%}   pieces that exist on some leecher: {avail:6.1%}"]
            if seed.alive:
                ss = seed.status()
                lines.append(f"seed   {lab.bar(1)} 100.0%  up   {lab.rate(ss.upload_payload_rate)}  peers {ss.num_peers}")
            else:
                lines.append(f"seed   {'X' * 28}  DEAD   (killed at t={killed['t']:.1f}s)")
            for p, s in zip(peers, sts):
                tag = f"done @ {finished[p.name]:.1f}s" if p.name in finished else ""
                lines.append(f"{p.name:6} {lab.bar(s.progress)} {s.progress:6.1%}  down {lab.rate(s.download_payload_rate)}"
                             f"  up {lab.rate(s.upload_payload_rate)}  peers {s.num_peers}  {tag}")
            lines.append(f"swarm  {lab.sparkline([r for _, r in timeline[-60:]])}")
            scr.draw(lines)

        if len(finished) == len(peers):
            return {"t": now, "finished": finished, "killed": killed, "recovered_at": recovered_at,
                    "timeline": timeline, "stalled": False, "avail": avail, "first_byte": first_byte}
        if killed and now - last_change > stall_after:
            return {"t": now, "finished": finished, "killed": killed, "recovered_at": None,
                    "timeline": timeline, "stalled": True, "avail": avail, "avg": avg}


def overhead_report(nodes):
    total = {"payload": 0, "bt": 0, "ip": 0, "dht": 0}
    for n in nodes:
        c = n.counters()
        total["payload"] += c.get("net.sent_payload_bytes", 0)
        total["bt"] += c.get("net.sent_bytes", 0) - c.get("net.sent_payload_bytes", 0)
        total["ip"] += c.get("net.sent_ip_overhead_bytes", 0)
        total["dht"] += c.get("dht.dht_bytes_out", 0)
    all_bytes = sum(total.values())
    lab.banner("Control bytes vs payload bytes (everything every node sent)")
    for key, label in [("payload", "payload (file data)"),
                       ("bt", "BitTorrent messages (handshake, have, request...)"),
                       ("ip", "TCP/UDP/IP headers"),
                       ("dht", "DHT control traffic (KRPC over UDP)")]:
        print(f"  {label:52} {lab.human(total[key])}  {100 * total[key] / all_bytes:6.2f}%")
    print(f"  {'control overhead':52} {'':10} {100 * (all_bytes - total['payload']) / all_bytes:6.2f}%")


def timeline_chart(timeline, killed):
    per_sec = {}
    for t, r in timeline:
        per_sec.setdefault(int(t), []).append(r)
    secs = sorted(per_sec)
    vals = [statistics.mean(per_sec[s]) for s in secs]
    peak = max(vals) or 1
    lab.banner(f"Swarm download rate, one column per second (peak {peak / lab.MiB:.1f} MB/s)")
    print("  " + lab.sparkline(vals, peak))
    if killed:
        k = int(killed["t"])
        print("  " + " " * k + "^ seed killed here")


# --------------------------------------------------------------------------
# modes
# --------------------------------------------------------------------------

def chaos(args):
    w = lab.workdir("02-chaos")
    data = lab.make_file(w / "seed" / "cat-video-8k.bin", args.size_mb * lab.MiB)
    ti = lab.make_torrent(data, args.piece_kib * lab.KiB, "hybrid")
    print(f"{args.size_mb} MiB, {ti.num_pieces()} pieces; seed uploads {args.seed_up} MB/s, "
          f"each peer uploads {args.peer_up} MB/s; discovery: {'direct' if args.direct else 'private DHT'}")

    seed, peers = build_swarm(w, ti, args.leechers, seed_up=args.seed_up * lab.MiB, peer_up=args.peer_up * lab.MiB,
                              dht=not args.direct, super_seed=args.super_seed)
    kill_at = None if args.kill_at < 0 else args.kill_at
    res = run_swarm(seed, peers, kill_at=kill_at, kill_when_complete=args.kill_when_complete)

    timeline_chart(res["timeline"], res["killed"])
    lab.banner("Result")
    k = res["killed"]
    if k:
        print(f"seed killed at t={k['t']:.1f}s, average progress {k['avg']:.0%}; "
              f"{k['avail']:.1%} of pieces existed on at least one leecher")
    if res["stalled"]:
        missing = round((1 - res["avail"]) * ti.num_pieces())
        print(f"\nSTALLED at {res['avg']:.1%} average progress. {missing} piece(s) existed ONLY on the seed,\n"
              "and they died with it. No amount of DHT lookups can find data nobody has.\n"
              "-> The seed is needed until every piece exists at least once in the swarm\n"
              "   (re-run with --kill-when-complete, and try --super-seed to get there sooner).")
    else:
        for name, t in res["finished"].items():
            print(f"  {name} finished at {t:6.1f}s")
        if k:
            after = [r for t, r in res["timeline"] if t > k["t"] + 1]
            print(f"time to recovery (kill -> next byte of progress): {1000 * (res['recovered_at'] - k['t']):.0f} ms "
                  f"(sampling every {TICK * 1000:.0f} ms)")
            print(f"swarm rate: {lab.rate(k['rate_before'])} before the kill, "
                  f"{lab.rate(statistics.mean(after) if after else 0)} after")
        original = lab.md5(data)
        ok = all(lab.md5(p.save_path / data.name) == original for p in peers)
        print(f"md5 of every leecher's copy == seed's original ({original}): {ok}")

    overhead_report([seed, *peers])


def sweep(args):
    """Download time vs number of leechers, client-server vs P2P, vs Kurose 2.5."""
    ns = [int(x) for x in args.sweep.split(",")]
    F, us, ui = args.size_mb * lab.MiB, args.seed_up * lab.MiB, args.peer_up * lab.MiB
    print(f"F = {args.size_mb} MiB, server/seed upload u_s = {args.seed_up} MB/s, "
          f"peer upload u = {args.peer_up} MB/s, peer download unlimited")
    print("theory (Kurose & Ross, section 2.5):")
    print("  D_cs  = N*F / u_s")
    print("  D_p2p = max( F/u_s , N*F / (u_s + N*u) )\n")
    print(f"{'N':>3} | {'client-server':>13} {'theory':>7} | {'P2P':>7} {'theory':>7}")
    print("-" * 46)
    run = 0
    for n in ns:
        row = []
        for share in (False, True):
            run += 1
            w = lab.workdir(f"02-sweep/{n}-{'p2p' if share else 'cs'}")
            data = lab.make_file(w / "seed" / "file.bin", F)
            ti = lab.make_torrent(data, args.piece_kib * lab.KiB, "hybrid")
            seed, peers = build_swarm(w, ti, n, seed_up=us, peer_up=ui, dht=False, share=share,
                                      base_port=7000 + 20 * run)
            res = run_swarm(seed, peers, live=False)
            row.append(res["t"] - res["first_byte"])  # ignore connection setup
            for p in [seed, *peers]:
                p.kill()
        d_cs, d_p2p = n * F / us, max(F / us, n * F / (us + n * ui))
        print(f"{n:>3} | {row[0]:>12.1f}s {d_cs:>6.1f}s | {row[1]:>6.1f}s {d_p2p:>6.1f}s", flush=True)
    print("\nTimes are measured from the first payload byte to the last leecher finishing.\n"
          "Client-server grows linearly with N. P2P barely moves: every new leecher\n"
          "also brings upload capacity. That's the whole point of BitTorrent.\n"
          "Theory is a lower bound: it treats data as a fluid. Real peers must first\n"
          "receive a whole 64 KiB piece (and hash it) before they can pass it on, and\n"
          "the rate limits also pay for ~6% of protocol + TCP/IP headers.")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--leechers", type=int, default=5)
    ap.add_argument("--size-mb", type=int, default=None, help="file size (default 32, or 8 for --sweep)")
    ap.add_argument("--piece-kib", type=int, default=64)
    ap.add_argument("--seed-up", type=float, default=2.0, help="seed upload cap, MB/s")
    ap.add_argument("--peer-up", type=float, default=1.0, help="each leecher's upload cap, MB/s")
    ap.add_argument("--kill-at", type=float, default=0.4, help="kill the seed at this average progress (-1 = never)")
    ap.add_argument("--kill-when-complete", action="store_true",
                    help="kill the seed as soon as every piece exists on some leecher")
    ap.add_argument("--super-seed", action="store_true", help="BEP 16: seed hands each piece out only once")
    ap.add_argument("--direct", action="store_true", help="skip the DHT, connect peers by hand")
    ap.add_argument("--sweep", help="comma list of leecher counts, e.g. 1,2,4,8")
    args = ap.parse_args()
    if args.kill_when_complete:
        args.kill_at = -1
    if args.size_mb is None:
        args.size_mb = 8 if args.sweep else 32
    sweep(args) if args.sweep else chaos(args)


if __name__ == "__main__":
    main()
