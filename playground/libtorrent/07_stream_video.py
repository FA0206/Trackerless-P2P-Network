#!/usr/bin/env python3
"""07: Stream a video out of a swarm while it's still downloading.

The project brief's dream demo: "Node 5 playing a video and buffering while
still downloading separate pieces from Node 2, 3 and 4."

  * ffmpeg makes a 60 s test video with a big on-screen clock
  * 3 seeders share it, each capped so that NO single one is fast enough to
    keep up with playback: smooth video needs all three at once
  * the leecher downloads in streaming mode (sequential order plus piece
    deadlines just ahead of the playhead) and serves the file over HTTP,
    blocking each read until the pieces under it have arrived and passed their
    hash check
  * VLC (or ffplay) plays http://127.0.0.1:8888/video.mp4 while the map shows
    pieces arriving

    .venv/bin/python 07_stream_video.py                  # opens VLC
    .venv/bin/python 07_stream_video.py --chaos          # kill a seeder mid-movie
    .venv/bin/python 07_stream_video.py --player ffplay
    .venv/bin/python 07_stream_video.py --player none    # then open the URL yourself
"""

import argparse
import http.server
import re
import shutil
import subprocess
import threading
import time

import libtorrent as lt

import lab

PORT_HTTP = 8888
WINDOW = 12  # pieces ahead of the playhead that get a deadline


def make_video(path, seconds):
    if path.exists():
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run([
        "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
        "-f", "lavfi", "-i", f"testsrc2=size=1280x720:rate=30:duration={seconds}",
        "-f", "lavfi", "-i", f"sine=frequency=330:duration={seconds}",
        "-vf", "drawtext=text='P2P stream %{pts\\:hms}':fontsize=72:fontcolor=white:box=1:boxcolor=black@0.6:x=(w-tw)/2:y=h-140",
        "-c:v", "libx264", "-preset", "veryfast", "-b:v", "2M", "-maxrate", "2M", "-bufsize", "4M",
        "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", str(path),
    ], check=True)


class Streamer:
    """Turns byte reads into 'wait for these pieces, and hurry them up'."""

    def __init__(self, handle, ti, path):
        self.h, self.ti, self.path = handle, ti, path
        self.size = ti.total_size()
        self.plen = ti.piece_length()
        self.playhead = 0          # piece the player is reading now
        self.served = 0            # furthest byte handed to the player
        self.first_byte_at = None  # when the player got its first byte
        self.active = 0            # HTTP responses in flight
        self.last_active = time.time()

    def wait_for(self, offset, length):
        first, last = offset // self.plen, min(self.ti.num_pieces() - 1, (offset + length - 1) // self.plen)
        self.playhead = first
        for i in range(WINDOW):    # "I need piece p in i*200 ms": libtorrent races for it
            p = first + i
            if p < self.ti.num_pieces() and not self.h.have_piece(p):
                self.h.set_piece_deadline(p, 200 * i)
        while not all(self.h.have_piece(p) for p in range(first, last + 1)):
            time.sleep(0.02)
        if self.first_byte_at is None:
            self.first_byte_at = time.time()
        self.served = max(self.served, offset + length)

    def handler(self):
        streamer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_HEAD(self):
                self.send_response(200)
                self.send_header("Content-Type", "video/mp4")
                self.send_header("Content-Length", str(streamer.size))
                self.send_header("Accept-Ranges", "bytes")
                self.end_headers()

            def do_GET(self):
                start, end = 0, streamer.size - 1
                m = re.match(r"bytes=(\d*)-(\d*)", self.headers.get("Range", ""))
                if m:
                    if m.group(1):
                        start = int(m.group(1))
                    if m.group(2):
                        end = min(end, int(m.group(2)))
                    self.send_response(206)
                    self.send_header("Content-Range", f"bytes {start}-{end}/{streamer.size}")
                else:
                    self.send_response(200)
                self.send_header("Content-Type", "video/mp4")
                self.send_header("Content-Length", str(end - start + 1))
                self.send_header("Accept-Ranges", "bytes")
                self.end_headers()
                streamer.active += 1
                try:
                    # libtorrent creates the file when the first piece is written
                    streamer.wait_for(start, min(64 * lab.KiB, end - start + 1))
                    with open(streamer.path, "rb") as f:
                        pos = start
                        while pos <= end:
                            n = min(64 * lab.KiB, end - pos + 1)
                            streamer.wait_for(pos, n)
                            f.seek(pos)
                            self.wfile.write(f.read(n))
                            pos += n
                except (BrokenPipeError, ConnectionResetError):
                    pass  # the player seeked or closed
                finally:
                    streamer.active -= 1
                    streamer.last_active = time.time()

        return Handler


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--seconds", type=int, default=60)
    ap.add_argument("--seed-rate", type=float, default=0.15, help="each seeder's upload cap, MB/s")
    ap.add_argument("--chaos", action="store_true", help="kill seeder 2 a third of the way in")
    ap.add_argument("--player", default="vlc", choices=["vlc", "ffplay", "none"])
    args = ap.parse_args()

    w = lab.WORK_ROOT / "07-stream"
    video = w / "source" / "p2p-movie.mp4"
    lab.banner("1. Make a test movie")
    make_video(video, args.seconds)
    size = video.stat().st_size
    bitrate = size / args.seconds
    print(f"{video.name}: {lab.human(size)}, {args.seconds}s, needs {lab.rate(bitrate)} to play in real time")
    ti = lab.make_torrent(video, 256 * lab.KiB, "hybrid")

    lab.banner("2. Three seeders, none fast enough alone")
    for d in ("seed1", "seed2", "seed3", "leecher"):
        shutil.rmtree(w / d, ignore_errors=True)
    seeders = []
    for i in range(3):
        (w / f"seed{i + 1}").mkdir(parents=True)
        shutil.copy(video, w / f"seed{i + 1}" / video.name)
        s = lab.Node(f"seed{i + 1}", 7600 + i, up_limit=args.seed_rate * lab.MiB)
        s.add(ti, w / f"seed{i + 1}")
        seeders.append(s)
    for s in seeders:
        while s.status().state != lt.torrent_status.seeding:
            time.sleep(0.05)
    print(f"each uploads {lab.rate(args.seed_rate * lab.MiB)}; together {lab.rate(3 * args.seed_rate * lab.MiB)} "
          f"vs {lab.rate(bitrate)} needed")

    lab.banner("3. The leecher streams")
    leecher = lab.Node("leecher", 7610)
    h = leecher.add(ti, w / "leecher", flags=lt.torrent_flags.sequential_download)
    for s in seeders:
        leecher.connect(s)
    streamer = Streamer(h, ti, w / "leecher" / video.name)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", PORT_HTTP), streamer.handler())
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{PORT_HTTP}/video.mp4"
    print(f"streaming at {url}")

    player = None
    if args.player == "vlc" and shutil.which("vlc"):
        player = subprocess.Popen(["vlc", "--quiet", "--play-and-exit", url],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    elif args.player == "ffplay" and shutil.which("ffplay"):
        player = subprocess.Popen(["ffplay", "-autoexit", "-loglevel", "quiet", url])
    else:
        print("open that URL in VLC (Media > Open Network Stream) or: ffplay " + url)

    scr = lab.Screen()
    t0, killed = time.time(), False
    try:
        while True:
            time.sleep(0.25)
            st = h.status()
            if args.chaos and not killed and st.progress > 0.33:
                seeders[1].kill()
                killed = True
                scr.note(f"  [{time.time() - t0:5.1f}s] *** seed2 KILLED *** (the stream should keep going)")
            n = ti.num_pieces()
            cells = list(lab.piece_map(st.pieces, 64))
            cells[min(63, streamer.playhead * 64 // n)] = "▶"
            names = {s.port: s.name for s in seeders}
            per_peer = "  ".join(f"{names.get(p.ip[1], p.ip[1])}:{p.down_speed / lab.MiB:4.2f}"
                                 for p in h.get_peer_info())
            startup = (f"{streamer.first_byte_at - t0:.1f}s" if streamer.first_byte_at else "waiting...")
            scr.draw([
                f"downloaded {st.progress:6.1%} at {lab.rate(st.download_payload_rate)}   "
                f"player has read {streamer.served / size:6.1%}   first byte to player after {startup}",
                f"pieces  {''.join(cells)}   (▶ = what the player is reading)",
                f"from    {per_peer or '(connecting...)'}   MB/s",
            ])
            # done once everything is downloaded AND the player has stopped reading
            idle = streamer.active == 0 and time.time() - streamer.last_active > 3
            if st.is_seeding and streamer.first_byte_at and (
                    (player is not None and player.poll() is not None) or (player is None and idle)):
                break
    except KeyboardInterrupt:
        pass
    finally:
        server.shutdown()
    ok = lab.md5(w / "leecher" / video.name) == lab.md5(video)
    print(f"\nfull file downloaded and verified: {ok}")
    print("Try --seed-rate 0.08 (the swarm is slower than the video: watch it buffer),\n"
          "--chaos (2 seeders left = 0.30 MB/s, still above 0.25), or both together.")


if __name__ == "__main__":
    main()
