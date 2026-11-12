"""Shared helpers for the libtorrent playground.

Every experiment builds a small BitTorrent swarm on this laptop.

    one Node  =  one libtorrent session  =  one BitTorrent client
                 listening on 127.0.0.1:<port>

Running several Nodes inside one Python process is cheap: each session has its
own sockets, its own threads and (optionally) its own DHT node, so to the
protocol they are completely separate machines that happen to share an IP.
"""

from __future__ import annotations

import hashlib
import os
import random
import shutil
import sys
import time
import warnings
from pathlib import Path

import libtorrent as lt

# session.status() is deprecated but still the simplest way to read DHT size
warnings.filterwarnings("ignore", category=DeprecationWarning)

KiB, MiB = 1024, 1024 * 1024

# Big scratch files live outside the repo (and outside your notes).
WORK_ROOT = Path(os.environ.get("LT_WORK", "/tmp/lt-playground"))

# Alerts are libtorrent's event stream. These categories are useful without
# drowning Python in one alert per 16 KiB block.
QUIET_ALERTS = (
    lt.alert_category.error
    | lt.alert_category.status
    | lt.alert_category.storage
    | lt.alert_category.peer
    | lt.alert_category.connect
    | lt.alert_category.ip_block
    | lt.alert_category.dht
)


# --------------------------------------------------------------------------
# files and torrents
# --------------------------------------------------------------------------

def workdir(name: str) -> Path:
    """A fresh, empty scratch directory for one experiment."""
    d = WORK_ROOT / name
    shutil.rmtree(d, ignore_errors=True)
    d.mkdir(parents=True)
    return d


def make_file(path: Path, size: int, seed: int = 1) -> Path:
    """Write `size` bytes of deterministic random data."""
    path.parent.mkdir(parents=True, exist_ok=True)
    rng = random.Random(seed)
    with open(path, "wb") as f:
        left = size
        while left:
            n = min(left, 4 * MiB)
            f.write(rng.randbytes(n))
            left -= n
    return path


FLAVORS = {
    "v1": lt.create_torrent.v1_only,  # classic: SHA-1 per piece
    "v2": lt.create_torrent.v2_only,  # BEP 52: SHA-256 merkle tree per file
    "hybrid": 0,                      # both, works in v1 and v2 swarms
}


def make_torrent(content: Path, piece_size: int = 256 * KiB, flavor: str = "hybrid",
                 out: Path | None = None) -> lt.torrent_info:
    """Hash a file or a directory into a .torrent (no trackers: trackerless!)."""
    fs = lt.file_storage()
    lt.add_files(fs, str(content))
    ct = lt.create_torrent(fs, piece_size, FLAVORS[flavor])
    ct.set_creator("Trackerless-P2P-Network libtorrent playground")
    lt.set_piece_hashes(ct, str(content.parent))
    entry = ct.generate()
    if out is not None:
        out.write_bytes(lt.bencode(entry))
    return lt.torrent_info(entry)


def md5(path: Path) -> str:
    h = hashlib.md5()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 * MiB), b""):
            h.update(chunk)
    return h.hexdigest()


def md5_tree(path: Path) -> str:
    """MD5 of a file, or of all files in a directory (sorted by name)."""
    if path.is_file():
        return md5(path)
    h = hashlib.md5()
    for p in sorted(path.rglob("*")):
        if p.is_file():
            h.update(p.relative_to(path).as_posix().encode())
            h.update(md5(p).encode())
    return h.hexdigest()


# --------------------------------------------------------------------------
# nodes
# --------------------------------------------------------------------------

class Node:
    """One BitTorrent client on 127.0.0.1:<port>.

    dht=True gives the node its own DHT node. With no bootstrap list it can
    only reach other playground nodes, which makes a *private* DHT: the real
    Internet DHT is never touched.
    """

    def __init__(self, name: str, port: int, *, dht: bool = False,
                 up_limit: float = 0, down_limit: float = 0,
                 tcp_only: bool = False, encrypt: bool = True, plugins: bool = True,
                 alert_mask: int = QUIET_ALERTS, extra: dict | None = None):
        self.name, self.port = name, port
        self.handle: lt.torrent_handle | None = None
        self.save_path: Path | None = None
        self.alive = True
        self.events: list[tuple[float, str]] = []

        enc = lt.enc_policy.enabled if encrypt else lt.enc_policy.disabled
        settings = {
            "listen_interfaces": f"127.0.0.1:{port}",
            "user_agent": f"playground/{name}",
            "alert_mask": alert_mask,
            # no LAN multicast, no router port-mapping, no Internet DHT
            "enable_lsd": False, "enable_upnp": False, "enable_natpmp": False,
            "enable_dht": dht, "dht_bootstrap_nodes": "",
            # every node shares 127.0.0.1, so switch off "one peer per IP" defences
            "allow_multiple_connections_per_ip": True,
            "dht_restrict_routing_ips": False, "dht_restrict_search_ips": False,
            "dht_enforce_node_id": False, "dht_ignore_dark_internet": False,
            "dht_prefer_verified_node_ids": False,
            # The DHT's DoS blocker bans any IP sending > 5 packets/s for 5 minutes.
            # 100 nodes on 127.0.0.1 look exactly like that attack, so relax it.
            "dht_block_ratelimit": 1_000_000, "dht_upload_rate_limit": 10_000_000,
            "in_enc_policy": enc, "out_enc_policy": enc,
        }
        if tcp_only:
            settings.update(enable_outgoing_utp=False, enable_incoming_utp=False)
        settings.update(extra or {})
        # plugins=False drops libtorrent's default extensions (ut_metadata,
        # ut_pex, smart_ban), leaving the bare protocol
        self.ses = lt.session(settings, 1 if plugins else 0)

        if up_limit or down_limit:
            # libtorrent never rate-limits "local" peers by default. Put every IP
            # (127.0.0.1 included) into the global peer class so limits apply.
            f = lt.ip_filter()
            f.add_rule("0.0.0.0", "255.255.255.255", 1 << self.ses.global_peer_class_id)
            self.ses.set_peer_class_filter(f)
            self.ses.apply_settings({
                "upload_rate_limit": int(up_limit),
                "download_rate_limit": int(down_limit),
            })

    # -- wiring -------------------------------------------------------------

    @property
    def endpoint(self) -> tuple[str, int]:
        return ("127.0.0.1", self.port)

    def add(self, ti: lt.torrent_info, save_path: Path, *, flags: int = 0) -> lt.torrent_handle:
        save_path.mkdir(parents=True, exist_ok=True)
        p = lt.add_torrent_params()
        p.ti = lt.torrent_info(ti)  # each session gets its own copy
        p.save_path = str(save_path)
        if flags:
            p.flags |= flags
        self.save_path = save_path
        self.handle = self.ses.add_torrent(p)
        return self.handle

    def connect(self, other: "Node") -> None:
        """Introduce two peers by hand (what a tracker or the DHT would do)."""
        self.handle.connect_peer(other.endpoint)

    def bootstrap_dht(self, other: "Node") -> None:
        """Tell our DHT node about one existing DHT node, like Kademlia's join."""
        self.ses.add_dht_node(other.endpoint)

    def kill(self) -> None:
        """Destroy the session: sockets close, the DHT node vanishes."""
        if self.alive:
            self.final_counters = self.counters()
            self.alive = False
            self.handle = None
            self.ses = None  # the destructor tears everything down

    # -- observing ------------------------------------------------------------

    def status(self) -> lt.torrent_status | None:
        return self.handle.status() if self.alive and self.handle else None

    def pop_alerts(self) -> list:
        if not self.alive:
            return []
        return self.ses.pop_alerts()

    def counters(self) -> dict[str, int]:
        """Session-wide counters: bytes sent, payload bytes, DHT bytes, ..."""
        if not self.alive:
            return getattr(self, "final_counters", {})
        self.ses.post_session_stats()
        deadline = time.time() + 3
        while time.time() < deadline:
            self.ses.wait_for_alert(500)
            for a in self.ses.pop_alerts():
                if isinstance(a, lt.session_stats_alert):
                    return dict(a.values)
        return {}


DHT_ALERTS = lt.alert_category.dht | lt.alert_category.dht_operation | lt.alert_category.error


def dht_size(node: Node) -> int:
    """How many nodes are in this node's DHT routing table."""
    return node.ses.status().dht_nodes


def private_dht(n: int, base_port: int, *, refresh_rounds: int = 4, log=print) -> list[Node]:
    """Start n DHT-only nodes on localhost and join them into one private DHT.

    Join, as in the Kademlia paper (section 2.3):
      1. node i learns ONE contact: a random node that already exists
      2. every node then does a few lookups for random IDs ("bucket refresh"),
         and every node that answers lands in its routing table
    Nodes carry no torrents, so they get one disk thread each (n=100 stays light).
    """
    rng = random.Random(n)
    light = {"aio_threads": 1, "hashing_threads": 1}
    nodes: list[Node] = []
    t0 = time.time()
    for i in range(n):
        node = Node(f"dht{i}", base_port + i, dht=True, alert_mask=DHT_ALERTS, extra=light)
        while not node.ses.is_dht_running():
            time.sleep(0.01)
        if nodes:
            node.bootstrap_dht(rng.choice(nodes))
        nodes.append(node)
    while time.time() - t0 < 15 and min(dht_size(x) for x in nodes[1:]) < 1:
        time.sleep(0.1)
    for _ in range(refresh_rounds):
        for x in nodes:
            x.ses.dht_get_peers(lt.sha1_hash(os.urandom(20)))
        time.sleep(1.0)
        for x in nodes:
            x.pop_alerts()
    sizes = [dht_size(x) for x in nodes]
    log(f"private DHT of {n} nodes up in {time.time() - t0:.1f}s; nodes each one knows: "
        f"min {min(sizes)} / avg {sum(sizes) / n:.1f} / max {max(sizes)}")
    return nodes


# --------------------------------------------------------------------------
# terminal drawing
# --------------------------------------------------------------------------

def bar(frac: float, width: int = 28) -> str:
    frac = max(0.0, min(1.0, frac))
    full = int(frac * width)
    return "█" * full + "░" * (width - full)


def piece_map(pieces, width: int = 64) -> str:
    """Squash a piece bitfield into `width` cells: █ all, ▓ most, ▒ some, ░ none."""
    n = len(pieces)
    if n == 0:
        return ""
    cells = []
    for i in range(width):
        lo, hi = i * n // width, max(i * n // width + 1, (i + 1) * n // width)
        got = sum(1 for p in pieces[lo:hi] if p) / (hi - lo)
        cells.append("█" if got == 1 else "▓" if got >= .5 else "▒" if got > 0 else "░")
    return "".join(cells)


SPARK = " ▁▂▃▄▅▆▇█"


def sparkline(values, top: float | None = None) -> str:
    top = top or max(values or [1]) or 1
    return "".join(SPARK[min(8, round(8 * v / top))] for v in values)


def human(n: float) -> str:
    for unit in ("B", "KiB", "MiB", "GiB"):
        if abs(n) < 1024:
            return f"{n:6.1f} {unit}"
        n /= 1024
    return f"{n:.1f} TiB"


def rate(n: float) -> str:
    return f"{n / MiB:5.2f} MB/s"


class Screen:
    """Redraw a block of lines in place (falls back to plain prints)."""

    def __init__(self):
        self.prev = 0

    def draw(self, lines: list[str]) -> None:
        if sys.stdout.isatty():
            if self.prev:
                sys.stdout.write(f"\x1b[{self.prev}F")
            for ln in lines:
                sys.stdout.write("\x1b[2K" + ln + "\n")
            self.prev = len(lines)
        else:
            print("\n".join(lines))
        sys.stdout.flush()

    def note(self, msg: str) -> None:
        """Print a permanent line above the live block."""
        if sys.stdout.isatty() and self.prev:
            sys.stdout.write(f"\x1b[{self.prev}F\x1b[J")
            self.prev = 0
        print(msg, flush=True)


def banner(title: str) -> None:
    print(f"\n\x1b[1m=== {title} ===\x1b[0m" if sys.stdout.isatty() else f"\n=== {title} ===")
