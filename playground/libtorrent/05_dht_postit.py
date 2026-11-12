#!/usr/bin/env python3
"""05: Post-it notes on the DHT (BEP 44: storing arbitrary data in the DHT).

The DHT is not only "infohash -> peers". BEP 44 turns it into a key-value
store, which is exactly the STORE / FIND_VALUE step the Go Kademlia project
lists as next:

  immutable item   key = SHA-1(value). Anyone can store, nobody can change it.
  mutable item     key = SHA-1(public key + salt). Only the private-key holder
                   can update it; every update carries a higher sequence number
                   and an ed25519 signature the storing nodes check.

Alice stores notes, Bob (another node, far away in ID space) reads them, and
Mallory tries to overwrite Alice's mutable note without her key.

    .venv/bin/python 05_dht_postit.py                    # private 30-node DHT on localhost
    .venv/bin/python 05_dht_postit.py --nodes 100
    .venv/bin/python 05_dht_postit.py --internet         # the real Mainline DHT (public!)
"""

import argparse
import hashlib
import os
import time

import libtorrent as lt
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

import lab


def keypair():
    """An ed25519 keypair in the format libtorrent wants (64-byte expanded secret)."""
    seed = os.urandom(32)
    pk = Ed25519PrivateKey.from_private_bytes(seed).public_key().public_bytes(
        serialization.Encoding.Raw, serialization.PublicFormat.Raw)
    sk = bytearray(hashlib.sha512(seed).digest())
    sk[0] &= 248
    sk[31] &= 63
    sk[31] |= 64
    return pk, bytes(sk)


def text(v):
    return v.decode(errors="replace") if isinstance(v, bytes) else v


def wait_for(ses, kind, match=lambda a: True, timeout=20.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        ses.wait_for_alert(200)
        for a in ses.pop_alerts():
            if isinstance(a, kind) and match(a):
                return a, time.time() - t0
    return None, timeout


def internet_sessions():
    sessions = []
    for name in ("alice", "bob", "mallory"):
        sessions.append(lt.session({
            "listen_interfaces": "0.0.0.0:0", "enable_dht": True,
            "enable_upnp": False, "enable_natpmp": False, "enable_lsd": False,
            "dht_bootstrap_nodes": "dht.libtorrent.org:25401,router.bittorrent.com:6881,"
                                   "dht.transmissionbt.com:6881",
            "alert_mask": lab.DHT_ALERTS,
        }))
    print("joining the Mainline DHT (needs ~30 s and an unblocked network)...")
    t0 = time.time()
    while time.time() - t0 < 60 and min(s.status().dht_nodes for s in sessions) < 30:
        time.sleep(1)
    if min(s.status().dht_nodes for s in sessions) < 30:
        raise SystemExit("DHT unreachable from this network: try a hotspot, or drop --internet.")
    return sessions


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--nodes", type=int, default=30, help="size of the private DHT")
    ap.add_argument("--internet", action="store_true", help="use the real DHT: what you post is PUBLIC")
    ap.add_argument("--message", default="hello from the Trackerless-P2P playground")
    args = ap.parse_args()

    lab.banner("0. A DHT to write on")
    if args.internet:
        alice, bob, mallory = internet_sessions()
    else:
        nodes = lab.private_dht(args.nodes, 9000)
        alice, bob, mallory = nodes[1].ses, nodes[args.nodes // 2].ses, nodes[-1].ses
        print(f"alice = dht1, bob = dht{args.nodes // 2}, mallory = dht{args.nodes - 1}")

    lab.banner("1. Immutable item: a message in a bottle")
    target = alice.dht_put_immutable_item(args.message)
    expected = hashlib.sha1(lt.bencode(args.message)).hexdigest()
    put, dt = wait_for(alice, lt.dht_put_alert)
    print(f"alice put {args.message!r}")
    print(f"  key = {target}  (SHA-1 of the bencoded value: {expected})")
    print(f"  stored on {put.num_success if put else 0} nodes in {dt:.2f}s")
    bob.dht_get_immutable_item(target)
    got, dt = wait_for(bob, lt.dht_immutable_item_alert, lambda a: a.target == target)
    print(f"bob asked the DHT for that key and got: {text(got.item) if got else None!r}  ({dt:.2f}s)")
    print("  Bob needed only the 20-byte key. Change one letter and the key changes:\n"
          "  content addressing, the same idea as a torrent's infohash.")

    lab.banner("2. Mutable item: Alice's status board")
    pk, sk = keypair()
    salt = b"status"
    print(f"alice's public key (her 'address'): {pk.hex()}")
    for status in ("learning BitTorrent", "learning BitTorrent: it works!!"):
        alice.dht_put_mutable_item(sk, pk, status.encode(), salt)
        put, dt = wait_for(alice, lt.dht_put_alert, lambda a: bytes(a.public_key) == pk)
        print(f"  alice put seq={put.seq if put else '?'}: {status!r} -> stored on "
              f"{put.num_success if put else 0} nodes ({dt:.2f}s)")
    time.sleep(1)
    bob.dht_get_mutable_item(pk, salt)
    got, dt = wait_for(bob, lt.dht_mutable_item_alert, lambda a: bytes(a.key) == pk)
    print(f"bob reads alice's board: {text(got.item) if got else None!r}  seq={got.seq if got else '?'}  ({dt:.2f}s)")

    lab.banner("3. Mallory tries to overwrite Alice's status")
    _, mallory_sk = keypair()
    mallory.dht_put_mutable_item(mallory_sk, pk, b"alice was hacked lol", salt)
    put, dt = wait_for(mallory, lt.dht_put_alert, lambda a: bytes(a.public_key) == pk)
    print(f"mallory signed with HER key but used ALICE's public key -> "
          f"{put.num_success if put else 0} nodes accepted it")
    time.sleep(1)
    bob.dht_get_mutable_item(pk, salt)
    got, dt = wait_for(bob, lt.dht_mutable_item_alert, lambda a: bytes(a.key) == pk)
    print(f"bob reads alice's board again: {text(got.item) if got else None!r}  seq={got.seq if got else '?'}")
    print("Every storing node verifies the ed25519 signature against the public key,\n"
          "so the forgery is dropped. That's integrity without any central server, the\n"
          "same goal as the project's Merkle-tree work (see 06_evil_seeder.py).")


if __name__ == "__main__":
    main()
