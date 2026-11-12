#!/usr/bin/env python3
"""00: Anatomy of a .torrent file (v1 vs v2 vs hybrid).

Creates one small folder, hashes it three ways, and shows what's inside each
.torrent: bencoding, the info-dict, SHA-1 piece hashes (v1) vs per-file SHA-256
merkle roots (v2, BEP 52), and the infohash(es) that the DHT is keyed on.

    .venv/bin/python 00_anatomy.py
"""

import argparse

import libtorrent as lt

import lab


def short(v, n=60):
    s = repr(v)
    return s if len(s) <= n else s[:n] + f"... ({len(v)} bytes)"


def show(entry, indent=0):
    """Pretty-print a decoded bencoded structure, truncating binary blobs."""
    pad = "  " * indent
    if isinstance(entry, dict):
        for k, v in entry.items():
            if isinstance(k, bytes):
                try:
                    k = k.decode() or '""  (v2 leaf marker: this node is a file)'
                except UnicodeDecodeError:  # 'piece layers' is keyed by binary merkle roots
                    k = f"<root {k.hex()[:16]}...>"
            if isinstance(v, (dict, list)) and v:
                print(f"{pad}{k}:")
                show(v, indent + 1)
            else:
                print(f"{pad}{k}: {short(v)}")
    elif isinstance(entry, list):
        for i, v in enumerate(entry[:4]):
            print(f"{pad}[{i}]")
            show(v, indent + 1)
        if len(entry) > 4:
            print(f"{pad}... {len(entry) - 4} more")
    else:
        print(f"{pad}{short(entry)}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--piece-kib", type=int, default=64)
    args = ap.parse_args()

    print(f"libtorrent {lt.__version__}")
    w = lab.workdir("00-anatomy")
    content = w / "meme-folder"
    (content / "readme.txt").parent.mkdir(parents=True)
    (content / "readme.txt").write_text("if you can read this, the swarm worked\n")
    lab.make_file(content / "cat.gif", 300 * lab.KiB, seed=7)
    lab.make_file(content / "cat-copy.gif", 300 * lab.KiB, seed=7)  # identical content!

    raw_torrent = None
    for flavor in ("v1", "v2", "hybrid"):
        path = w / f"meme-folder.{flavor}.torrent"
        ti = lab.make_torrent(content, args.piece_kib * lab.KiB, flavor, out=path)
        raw = path.read_bytes()
        ih = ti.info_hashes()
        lab.banner(f"{flavor}: {path.name} ({len(raw)} bytes on disk)")
        print("first 80 bytes, raw bencoding:", raw[:80])
        print(f"pieces: {ti.num_pieces()} x {ti.piece_length() // lab.KiB} KiB")
        print(f"v1 infohash (SHA-1 of info dict)   : {ih.v1 if ih.has_v1() else '-'}")
        print(f"v2 infohash (SHA-256 of info dict) : {ih.v2 if ih.has_v2() else '-'}")
        print(f"magnet: {lt.make_magnet_uri(ti)}")
        print("\ndecoded structure:")
        show(lt.bdecode(raw))
        if flavor == "v2":
            raw_torrent = lt.bdecode(raw)

    lab.banner("Things to notice")
    print("""\
* There is no 'announce' key: no tracker. Peers will be found through the DHT,
  which is keyed by the infohash printed above.
* v1 stores one 20-byte SHA-1 per piece in info.pieces, and pieces span file
  boundaries.
* v2 stores a 'file tree' with one 32-byte 'pieces root' (merkle root) per file,
  plus 'piece layers' outside the info dict.""")
    if raw_torrent:
        tree = raw_torrent[b"info"][b"file tree"]
        roots = {name.decode(): node[b""].get(b"pieces root", b"").hex()[:16]
                 for name, node in tree.items()}
        print("* v2 merkle roots per file:")
        for name, root in roots.items():
            print(f"    {name:14} {root}...")
        print("  cat.gif and cat-copy.gif have the SAME root: identical files are\n"
              "  recognisable across torrents (only possible in v2). That's also why\n"
              "  'piece layers' has a single entry: one layer serves both files.\n"
              "* readme.txt is smaller than one piece, so its root alone is enough.\n"
              "* hybrid adds '.pad' files (attr 'p', BEP 47) so every file starts on a\n"
              "  piece boundary; that keeps the v1 and v2 piece grids identical.")
    print(f"\nFiles are in {w}; open a .torrent in a hex viewer: xxd {w}/meme-folder.v1.torrent | head")


if __name__ == "__main__":
    main()
