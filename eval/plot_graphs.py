"""Draw the six performance graphs from eval/results/*.csv into eval/results/*.png.

Usage (from the repo root):  eval/.venv/bin/python eval/plot_graphs.py

Every graph is drawn from whatever rows its CSV has, so it works before all
experiments have run: a missing CSV is skipped with a note, and graphs 3 and
4 show only the localhost series until real-host rows are recorded.
What each graph measures and why: docs/10-performance-graphs.md.
"""

import csv
import math
import os
import statistics
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402
from matplotlib.ticker import FuncFormatter, MaxNLocator, NullLocator  # noqa: E402

RESULTS = os.environ.get("EVAL_RESULTS") or os.path.join(os.path.dirname(os.path.abspath(__file__)), "results")

# Reference data-viz palette, light mode: two categorical slots (validated
# together: CVD dE 24.7, normal-vision dE 33.6, both >= 3:1 on the surface).
SURFACE = "#fcfcfb"
INK = "#0b0b0b"
INK_2 = "#52514e"
MUTED = "#898781"
GRID = "#e1e0d9"
AXIS = "#c3c2b7"
BLUE = "#2a78d6"  # slot 1
ORANGE = "#eb6834"  # slot 2

# Neither eval script overrides --k/--alpha, so every lookup (graphs 1 & 6)
# ran with these defaults (kademlia.DefaultK, kademlia.DefaultAlpha, node.go).
K, ALPHA = 20, 3


def expected_rpcs(n):
    """A lookup stops once it's heard from the k closest nodes, so it costs
    about min(n-1, k) RPCs, whatever the network's size (docs/10, graph 1)."""
    return min(n - 1, K)


def expected_rounds(n):
    """RPCs sent alpha at a time."""
    return math.ceil(expected_rpcs(n) / ALPHA)


def ref_line(ax, xs, ys, label):
    """A derived (not fitted) reference curve: dashed, axis-gray, no marker,
    so it reads as 'the formula', not another measured series."""
    ax.plot(xs, ys, color=AXIS, linestyle="--", linewidth=1.5, zorder=1, label=label)

plt.rcParams.update({
    "font.family": "sans-serif",
    "font.sans-serif": ["DejaVu Sans", "Arial", "Helvetica"],
    "font.size": 11,
    "figure.facecolor": SURFACE,
    "axes.facecolor": SURFACE,
    "axes.edgecolor": AXIS,
    "axes.labelcolor": INK_2,
    "axes.titlecolor": INK,
    "xtick.color": MUTED,
    "ytick.color": MUTED,
    "xtick.labelcolor": INK_2,
    "ytick.labelcolor": INK_2,
    "axes.spines.top": False,
    "axes.spines.right": False,
    "axes.grid": True,
    "axes.grid.axis": "y",
    "grid.color": GRID,
    "grid.linewidth": 1,
    "grid.linestyle": "-",
    "lines.linewidth": 2,
    "lines.solid_capstyle": "round",
    "lines.solid_joinstyle": "round",
    "legend.frameon": False,
    "legend.labelcolor": INK_2,
})


def read(name):
    path = os.path.join(RESULTS, name)
    if not os.path.exists(path):
        return None
    with open(path, newline="") as f:
        return list(csv.DictReader(f))


def p95(xs):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, math.ceil(0.95 * len(xs)) - 1)]


def by(rows, key, value, cast=float):
    """{key: [values]}, keys sorted numerically."""
    out = {}
    for r in rows:
        if r[value] == "":
            continue
        out.setdefault(float(r[key]), []).append(cast(r[value]))
    return dict(sorted(out.items()))


def line(ax, xs, ys, color, label=None, end_label=None):
    ax.plot(xs, ys, color=color, label=label, zorder=3,
            marker="o", markersize=8, markeredgecolor=SURFACE, markeredgewidth=2)
    if end_label:
        ax.annotate(end_label, (xs[-1], ys[-1]), xytext=(10, 0), textcoords="offset points",
                    va="center", color=INK_2, fontsize=10)


def band(ax, groups, color):
    xs = list(groups)
    ax.fill_between(xs, [min(v) for v in groups.values()], [max(v) for v in groups.values()],
                    color=color, alpha=0.15, linewidth=0, zorder=2)


def titles(fig, title, question):
    fig.suptitle(title, x=0.06, y=0.97, ha="left", fontsize=16, fontweight="bold", color=INK)
    fig.text(0.06, 0.905, question, ha="left", fontsize=11, color=INK_2, style="italic")


def log2_axis(ax, ticks, fmt=lambda v: f"{v:g}"):
    ax.set_xscale("log", base=2)
    ax.set_xticks(ticks)
    ax.xaxis.set_major_formatter(FuncFormatter(lambda v, _: fmt(v)))
    ax.xaxis.set_minor_locator(NullLocator())


def save(fig, name, note=None):
    if note:
        fig.text(0.06, 0.015, note, ha="left", fontsize=9, color=MUTED)
    path = os.path.join(RESULTS, name)
    fig.savefig(path, dpi=200)
    plt.close(fig)
    print("wrote", os.path.relpath(path))


def graph1(rows):
    lat = by(rows, "n", "latency_ms")
    rounds = by(rows, "n", "rounds")
    rpcs = by(rows, "n", "rpcs_sent")
    ns = list(lat)
    fig, (a, b, c) = plt.subplots(3, 1, figsize=(10, 8.5), sharex=True,
                                  gridspec_kw={"height_ratios": [2.2, 1, 1]})
    fig.subplots_adjust(left=0.1, right=0.86, top=0.85, bottom=0.11, hspace=0.25)
    titles(fig, "1 · Lookup latency vs. network size",
           "Without a tracker, can a fully decentralized lookup still find peers fast as the network grows?")
    mean = [statistics.mean(v) for v in lat.values()]
    exp_rounds = [expected_rounds(n) for n in ns]
    per_round_ms = statistics.mean(mean[i] / exp_rounds[i] for i in range(len(ns)))
    ref_line(a, ns, [r * per_round_ms for r in exp_rounds], "O(log n)")
    line(a, ns, mean, BLUE, "mean", "mean")
    line(a, ns, [p95(v) for v in lat.values()], ORANGE, "95th percentile", "p95")
    a.set_ylabel("lookup latency (ms)")
    a.set_ylim(bottom=0)
    a.legend(loc="upper left")
    ref_line(b, ns, [expected_rounds(n) for n in ns], "O(log n)")
    line(b, ns, [statistics.mean(v) for v in rounds.values()], BLUE, "measured")
    b.set_ylabel("rounds")
    b.set_ylim(bottom=0)
    b.yaxis.set_major_locator(MaxNLocator(integer=True, nbins=4))
    b.legend(loc="upper left", fontsize=9)
    ref_line(c, ns, [expected_rpcs(n) for n in ns], "O(log n)")
    line(c, ns, [statistics.mean(v) for v in rpcs.values()], BLUE, "measured")
    c.set_ylabel("RPCs sent")
    c.set_ylim(bottom=0)
    c.yaxis.set_major_locator(MaxNLocator(integer=True, nbins=4))
    c.legend(loc="upper left", fontsize=9)
    log2_axis(c, ns, lambda v: f"{v:g}")
    c.set_xlabel("network size N (nodes)")
    trials = len(next(iter(lat.values())))
    save(fig, "graph1_lookup_latency.png",
         f"localhost · {trials} lookups per N, random targets from random nodes · lower panels: mean rounds and RPCs"
         f" per lookup, vs. Kademlia's O(log n) (k={K}, α={ALPHA} at a time)")


def graph2(rows):
    fig, ax = plt.subplots(figsize=(10, 6))
    fig.subplots_adjust(left=0.1, right=0.84, top=0.82, bottom=0.14)
    titles(fig, "2 · Download time vs. number of leechers",
           "Does spreading the file across peers help as demand grows, vs. everyone hitting one server?")
    ls = None
    base = {}
    for mode, color, label in (("centralized", ORANGE, "centralized server"), ("p2p", BLUE, "P2P swarm")):
        g = by([r for r in rows if r["mode"] == mode], "leechers", "duration_s")
        if not g:
            continue
        ls = list(g)
        base[mode] = statistics.mean(g[ls[0]])
        band(ax, g, color)
        line(ax, ls, [statistics.mean(v) for v in g.values()], color, label, label)
    if "centralized" in base:
        # Derived from the run's own parameters, not fitted from a data point:
        # one connection's share of the server's uplink is rate/L, so its
        # transfer alone takes file_size/(rate/L) = (file_size/rate) * L. A
        # per-leecher anchor (e.g. the L=1 time) would also bake in L=1's own
        # one-time connection overhead and over-project it at higher L.
        per_leecher_s = float(rows[0]["file_size_bytes"]) / float(rows[0]["server_rate_bps"])
        ref_line(ax, ls, [per_leecher_s * l for l in ls], "ideal: file size ÷ server rate, × L")
    if "p2p" in base:
        ref_line(ax, [ls[0], ls[-1]], [base["p2p"], base["p2p"]], "ideal: no slowdown with more leechers")
    log2_axis(ax, ls)
    ax.set_xlabel("leechers downloading at once")
    ax.set_ylabel("mean download time (s)")
    ax.set_ylim(bottom=0)
    ax.legend(loc="upper left", fontsize=9)
    save(fig, "graph2_download_time.png",
         "localhost · every machine's upload capped at the same rate (--debug-upload-rate) · line: mean of trials, band: min–max")


def graph3(rows):
    fig, ax = plt.subplots(figsize=(10, 6))
    fig.subplots_adjust(left=0.1, right=0.97, top=0.82, bottom=0.16)
    titles(fig, "3 · Recovery after the seeder dies",
           "If the peer who had the file disappears, does the file stay available, and how fast does the swarm recover?")
    envs = [("localhost", BLUE, "localhost"), ("real_hosts", ORANGE, "3 laptops on Wi-Fi")]
    present = [e for e in envs if any(r["environment"] == e[0] for r in rows)]
    pcts = sorted({int(r["kill_point_pct"]) for r in rows})
    width = 0.38  # distance between paired bars: room for their labels
    top = max([float(r["recovery_s"]) for r in rows if r["recovery_s"]] or [1])

    def secs(s):
        return f"{s * 1000:.0f} ms" if s < 1 else f"{s:.2f} s"
    for j, (env, color, label) in enumerate(present):
        for i, pct in enumerate(pcts):
            trials = [r for r in rows if r["environment"] == env and int(r["kill_point_pct"]) == pct]
            if not trials:
                continue
            ok = [float(r["recovery_s"]) for r in trials if r["survived"] == "1"]
            x = i + (j - (len(present) - 1) / 2) * width
            y = statistics.mean(ok) if ok else 0
            if ok:
                ax.bar(x, y, 0.14, color=color, edgecolor=SURFACE, linewidth=2,
                       label=label if label not in ax.get_legend_handles_labels()[1] else None, zorder=3)
            ax.annotate(f"{len(ok)}/{len(trials)} survived", (x, y), xytext=(0, 4), textcoords="offset points",
                        ha="center", va="bottom", color=INK_2, fontsize=8.5)
            ax.annotate(secs(y) if ok else "file lost", (x, y), xytext=(0, 17), textcoords="offset points",
                        ha="center", va="bottom", color=INK if ok else INK_2, fontsize=10)
    ax.set_xlim(-0.6, len(pcts) - 0.4)
    ax.set_xticks(range(len(pcts)))
    ax.set_xticklabels([f"killed at {p}%" for p in pcts])
    ax.tick_params(axis="x", length=0, pad=4)
    ax.set_ylabel("time until every leecher receives pieces again (s)")
    ax.set_ylim(0, top * 1.35)
    if len(present) > 1:
        ax.legend(loc="upper right", fontsize=9)
    note = ("same setup in both: 2 leechers, 4 MiB, every piece served 1 s late · kill point: share the leading"
            " leecher had · bar: mean over survived trials")
    if len(present) < 2:
        note += " · real-host trials not recorded yet"
    save(fig, "graph3_recovery_time.png", note)


def graph4(rows):
    fig, ax = plt.subplots(figsize=(10, 6))
    fig.subplots_adjust(left=0.1, right=0.84, top=0.82, bottom=0.14)
    titles(fig, "4 · DHT control traffic vs. file payload",
           "Does replacing the tracker with a chattier DHT cost meaningful bandwidth next to the file itself?")
    sizes = None
    n = 0
    for env, color, label in (("real_hosts", ORANGE, "2 laptops on Wi-Fi"), ("localhost", BLUE, "localhost")):
        sub = [r for r in rows if r["environment"] == env]
        if not sub:
            continue
        n += 1
        pct = {}
        for r in sub:
            c, p = float(r["control_bytes"]), float(r["payload_bytes"])
            pct.setdefault(float(r["file_size_bytes"]), []).append(100 * c / (c + p))
        pct = dict(sorted(pct.items()))
        sizes = list(pct)
        means = [statistics.mean(v) for v in pct.values()]
        line(ax, sizes, means, color, label, label)
        for x, y, dx, ha in ((sizes[0], means[0], 0, "center"), (sizes[-1], means[-1], -12, "right")):
            ax.annotate(f"{y:.2g}%", (x, y), xytext=(dx, 10), textcoords="offset points",
                        ha=ha, va="bottom", color=INK, fontsize=10)
        lo = min(means) if n == 1 else min(lo, min(means))
        hi = max(means) if n == 1 else max(hi, max(means))
    ax.set_xscale("log", base=2)
    ax.set_yscale("log")
    ax.set_ylim(lo / 3, hi * 3)
    ax.set_xticks(sizes)
    ax.xaxis.set_major_formatter(FuncFormatter(
        lambda v, _: f"{v / 2**20:g} MiB" if v >= 2**20 else f"{v / 2**10:g} KiB"))
    ax.xaxis.set_minor_locator(NullLocator())
    ax.yaxis.set_major_formatter(FuncFormatter(lambda v, _: f"{v:g}%"))
    ax.set_xlabel("shared file size")
    ax.set_ylabel("DHT share of all bytes (log scale)")
    if n > 1:
        ax.legend(loc="upper right")
    note = "DHT bytes: every UDP message of the share and the download, each counted once · payload: the file"
    if n < 2:
        note += " · real-host trials not recorded yet"
    save(fig, "graph4_bytes_overhead.png", note)


def graph5(rows):
    tput = {}
    for r in rows:
        tput.setdefault(float(r["providers"]), []).append(float(r["throughput_bps"]) / 1e6)
    tput = dict(sorted(tput.items()))
    ps = list(tput)
    fig, ax = plt.subplots(figsize=(10, 6))
    fig.subplots_adjust(left=0.1, right=0.95, top=0.82, bottom=0.14)
    titles(fig, "5 · Download throughput vs. number of providers",
           "Does pulling from more providers at once actually speed up a single download?")
    band(ax, tput, BLUE)
    means = [statistics.mean(v) for v in tput.values()]
    line(ax, ps, means, BLUE)
    for x, y in ((ps[0], means[0]), (ps[-1], means[-1])):
        ax.annotate(f"{y:.0f} MB/s", (x, y), xytext=(0, 10), textcoords="offset points",
                    ha="center", va="bottom", color=INK, fontsize=10)
    log2_axis(ax, ps)
    ax.set_xlabel("providers holding the complete file")
    ax.set_ylabel("throughput (MB/s)")
    ax.set_ylim(0, max(max(v) for v in tput.values()) * 1.2)
    save(fig, "graph5_throughput_providers.png",
         "localhost · one leecher, uncapped · shows the scheduler using providers in parallel, not real-world bandwidth"
         " · line: mean, band: min–max")


def graph6(rows):
    ms = by(rows, "n", "bootstrap_ms")
    rounds = by(rows, "n", "self_lookup_rounds")
    ns = list(ms)
    fig, (a, b) = plt.subplots(2, 1, figsize=(10, 7.5), sharex=True, gridspec_kw={"height_ratios": [2.2, 1]})
    fig.subplots_adjust(left=0.1, right=0.95, top=0.84, bottom=0.12, hspace=0.25)
    titles(fig, "6 · Join time vs. network size",
           "As the network grows, how long does it take a new node to join it, with no tracker to ask?")
    band(a, ms, BLUE)
    line(a, ns, [statistics.mean(v) for v in ms.values()], BLUE)
    a.set_ylabel("join time (ms)")
    a.set_ylim(0, max(max(v) for v in ms.values()) * 1.15)
    line(b, ns, [statistics.mean(v) for v in rounds.values()], BLUE)
    b.set_ylabel("self-lookup rounds")
    b.set_ylim(bottom=0)
    b.yaxis.set_major_locator(MaxNLocator(integer=True, nbins=4))
    log2_axis(b, ns)
    b.set_xlabel("nodes already in the network")
    trials = len(next(iter(ms.values())))
    save(fig, "graph6_bootstrap_time.png",
         f"localhost · {trials} fresh networks per N · join: 'bootstrapping' to 'bootstrap complete' in the new node's log"
         " · line: mean, band: min–max · a self-lookup runs consistently ~1 round above graph 1's steady-network formula,"
         " starting from a single contact instead of an already-populated table")


GRAPHS = [
    ("lookup_latency.csv", graph1),
    ("download_time.csv", graph2),
    ("recovery_time.csv", graph3),
    ("bytes_overhead.csv", graph4),
    ("throughput_providers.csv", graph5),
    ("bootstrap_time.csv", graph6),
]


def main():
    missing = 0
    for name, draw in GRAPHS:
        rows = read(name)
        if not rows:
            print(f"skipped {name}: no data yet (run its eval/scripts/*.sh)", file=sys.stderr)
            missing += 1
            continue
        draw(rows)
    return 1 if missing == len(GRAPHS) else 0


if __name__ == "__main__":
    sys.exit(main())
