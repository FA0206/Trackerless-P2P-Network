#!/usr/bin/env python3
"""06: The evil seeder. Why v2's Merkle tree matters.

Two seeders offer the same file at the same speed:
  honest  serves correct data
  evil    passed its own hash check at startup, then had one byte flipped in
          every piece on disk, so it serves poisoned blocks

A leecher downloads from both, and because they're equally fast it builds
most pieces out of blocks from BOTH. When such a piece fails its hash check,
who lied?

  v1 torrent: one SHA-1 per piece. libtorrent only knows "someone in this
              piece". It penalises every contributor, honest included.
  v2 torrent: SHA-256 Merkle tree with a leaf per 16 KiB block (BEP 52).
              libtorrent fetches the block hashes, finds the exact bad block,
              and penalises only its sender.

Run twice: "naive" (bare protocol, so v1's blindness shows) and "libtorrent"
(default defences: parole mode + the smart-ban plugin, clever workarounds
built because v1 can't point at a block).

    .venv/bin/python 06_evil_seeder.py
    .venv/bin/python 06_evil_seeder.py --piece-kib 4096
"""

import argparse
import random
import time

import libtorrent as lt

import lab


def corrupt(path, piece_len, n_pieces, rng):
    """Flip one byte at a random offset inside every piece."""
    with open(path, "r+b") as f:
        for i in range(n_pieces):
            off = i * piece_len + rng.randrange(piece_len)
            f.seek(off)
            b = f.read(1)
            f.seek(off)
            f.write(bytes([b[0] ^ 0xFF]))


def attack(flavor, defences, args, base_port, verbose=True):
    w = lab.workdir(f"06-evil/{flavor}-{'defended' if defences else 'naive'}")
    data = lab.make_file(w / "honest" / "firmware-update.bin", args.size_mb * lab.MiB, seed=3)
    (w / "evil").mkdir()
    (w / "evil" / data.name).write_bytes(data.read_bytes())
    ti = lab.make_torrent(data, args.piece_kib * lab.KiB, flavor)

    rate = args.rate * lab.MiB
    honest = [lab.Node(f"honest{i + 1}", base_port + i, up_limit=rate) for i in range(args.honest)]
    evil = lab.Node("evil", base_port + args.honest, up_limit=rate)
    leecher = lab.Node("leecher", base_port + args.honest + 1, plugins=defences, extra={
        "use_parole_mode": defences,
        # never ask one peer for a whole piece: blocks of every piece come from
        # several seeders, which is what happens in big real swarms
        "whole_pieces_threshold": 0,
    })
    for h in honest:
        h.add(ti, w / "honest")
    evil.add(ti, w / "evil")
    for n in (*honest, evil):
        while n.status().state != lt.torrent_status.seeding:
            time.sleep(0.05)

    # evil passed its hash check; NOW poison its copy on disk
    corrupt(w / "evil" / data.name, ti.piece_length(), ti.num_pieces(), random.Random(9))

    leecher.add(ti, w / "leecher")
    for n in (evil, *honest):
        leecher.connect(n)
    who = {n.port: n.name for n in (*honest, evil)}

    t0, fails, bans, log, last_progress = time.time(), 0, {}, [], (0, time.time())
    while time.time() - t0 < 90:
        time.sleep(0.1)
        now = time.time() - t0
        for a in leecher.pop_alerts():
            if isinstance(a, lt.hash_failed_alert):
                fails += 1
                if fails <= 3:
                    log.append(f"  [{now:5.2f}s] piece {a.piece_index} failed its hash check")
            elif isinstance(a, lt.peer_disconnected_alert) and a.endpoint[1] in who:
                # (peer_ban_alert only carries the IP, and both seeders are 127.0.0.1)
                name, why = who[a.endpoint[1]], a.error.message()
                if "corrupt" in why or "banned" in why:
                    bans.setdefault(name, now)
                    log.append(f"  [{now:5.2f}s] BANNED the {name} seeder: {why}")
        st = leecher.status()
        if st.is_seeding:
            break
        if st.total_wanted_done != last_progress[0]:
            last_progress = (st.total_wanted_done, time.time())
        elif time.time() - last_progress[1] > 8:
            log.append(f"  [{now:5.2f}s] STUCK at {st.progress:.0%}: {len(bans)} of {len(who)} seeders "
                       "banned, the rest can't complete the file")
            break

    st = leecher.status()
    if verbose:
        print("\n".join(log))
        if fails > 3:
            print(f"  ... {fails - 3} more hash failures")
    res = {"name": f"{flavor} {'libtorrent' if defences else 'naive'}", "fails": fails,
           "failed_bytes": st.total_failed_bytes, "bans": bans, "time": time.time() - t0,
           "done": st.is_seeding,
           "ok": st.is_seeding and lab.md5(w / "leecher" / data.name) == lab.md5(data)}
    for n in (*honest, evil, leecher):
        n.kill()
    return res


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--size-mb", type=int, default=16)
    ap.add_argument("--piece-kib", type=int, default=1024)
    ap.add_argument("--rate", type=float, default=1.0, help="upload cap of each seeder, MB/s")
    ap.add_argument("--honest", type=int, default=3, help="number of honest seeders")
    ap.add_argument("--trials", type=int, default=3, help="runs per configuration (results are noisy!)")
    args = ap.parse_args()

    configs = [(f, d) for d in (False, True) for f in ("v1", "v2")]
    runs, port = {c: [] for c in configs}, 7400
    print(f"{args.honest} honest + 1 evil seeder, {args.size_mb} MiB, {args.piece_kib} KiB pieces, "
          f"{args.trials} trial(s) per configuration")
    for trial in range(args.trials):
        for flavor, defences in configs:
            if trial == 0:
                lab.banner(f"{flavor} torrent, {'libtorrent defences ON' if defences else 'naive (no parole, no smart-ban)'}")
            r = attack(flavor, defences, args, port, verbose=trial == 0)
            port += 20
            runs[(flavor, defences)].append(r)
            print(f"  trial {trial + 1} {r['name']:14}: {r['fails']:2} hash fails, evil "
                  f"{'banned at %.1fs' % r['bans']['evil'] if 'evil' in r['bans'] else 'NEVER banned'}, "
                  f"{sum(1 for n in r['bans'] if n != 'evil')} honest banned, "
                  f"{'file OK' if r['ok'] else 'FAILED'} in {r['time']:.0f}s", flush=True)

    lab.banner(f"Damage report (averages over {args.trials} trials)")
    print(f"{'':15} {'hash fails':>10} {'thrown away':>12} {'evil banned':>12} {'honest banned':>14} {'files OK':>9}")
    for (flavor, defences), rs in runs.items():
        n = len(rs)
        mean = lambda k: sum(r[k] for r in rs) / n
        print(f"{rs[0]['name']:15} {mean('fails'):>10.1f} {lab.human(mean('failed_bytes')):>12} "
              f"{sum('evil' in r['bans'] for r in rs):>6} of {n:<3} "
              f"{sum(sum(1 for b in r['bans'] if b != 'evil') for r in rs) / n:>9.1f} avg "
              f"{sum(r['ok'] for r in rs):>4} of {n}")
    print("""
How libtorrent decides who lied (torrent.cpp: piece_failed / penalize_peers):
  * a peer that sent EVERY block of a failed piece is banned on the spot
  * otherwise every contributor loses 2 trust points (honest ones too) and is
    banned at -7; good pieces earn trust back
  * v1 torrent: that's all it knows (one SHA-1 per piece)
  * v2 torrent: it also requests the block-level Merkle hashes for the failed
    piece; once they arrive it blames only the sender of the bad 16 KiB block
  * defences ON adds parole mode (a suspect must fetch whole pieces alone, so
    its next failure has one author) and the smart-ban plugin (CRC of every
    block + who sent it, compared after re-download)

The numbers swing a lot between runs: who wins depends on which peer happened
to send the one poisoned block of each piece. That's a real lesson for the
project's evaluation too: run every scenario several times.
Try: --trials 10, --piece-kib 256 vs 4096, --honest 1 vs 6.""")


if __name__ == "__main__":
    main()
