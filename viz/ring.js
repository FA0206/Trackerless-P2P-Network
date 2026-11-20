// The ring: every node drawn at the angle its ID predicts
// (docs/11-visualizer.md). Kademlia IDs are points in [0, 2^160); going
// once around the circle covers that whole space, starting at 12 o'clock
// (ID 0x000…) and running clockwise. Deterministic: the same seeds give
// the same picture on every run, and nodes never jitter.
//
// Position on the ring is just a way to *see* IDs. Kademlia's distance is
// XOR, not the angle between two dots (docs/01): two neighbours on the
// ring are close in XOR only when their IDs share a long prefix.
//
// Also drawn here: comets (one message travelling between two nodes, on
// an arc bending toward the centre), lines between nodes (a routing
// table), and key markers (where a key falls on the same ring).
"use strict";

window.Viz = window.Viz || {};

(function (Viz) {
  const SVG_NS = "http://www.w3.org/2000/svg";
  const TWO_POW_32 = 4294967296;
  const MAX_TRAILS = 12; // at most ~12 moving things at once

  // fractionOf maps an ID to [0, 1): ID / 2^160. Only the top 32 bits
  // change the angle visibly (2^-32 of a turn is far below a pixel), so
  // no BigInt is needed.
  function fractionOf(id) {
    return parseInt(id.slice(0, 8), 16) / TWO_POW_32;
  }

  // pointAt returns the stage coordinates of fraction f on a circle.
  function pointAt(f, cx, cy, r) {
    const a = f * 2 * Math.PI - Math.PI / 2; // 0 at 12 o'clock, clockwise
    return { x: cx + r * Math.cos(a), y: cy + r * Math.sin(a) };
  }

  function el(name, attrs, parent) {
    const e = document.createElementNS(SVG_NS, name);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    if (parent) parent.appendChild(e);
    return e;
  }

  // createRing draws the empty ring into `svg` and returns the functions
  // that draw on it.
  function createRing(svg, { cx, cy, r, onSelect }) {
    const defs = el("defs", {}, svg);
    const glow = el("filter", { id: "glow", x: "-50%", y: "-50%", width: "200%", height: "200%" }, defs);
    el("feGaussianBlur", { stdDeviation: "5", result: "b" }, glow);
    const merge = el("feMerge", {}, glow);
    el("feMergeNode", { in: "b" }, merge);
    el("feMergeNode", { in: "SourceGraphic" }, merge);

    el("circle", { cx, cy, r, class: "ring-track" }, svg);

    // Quarter marks, so anyone can check a node's place against its ID:
    // a node whose ID starts with 0x4… sits a quarter of the way round.
    const marks = [
      [0, "0x0…"],
      [0.25, "0x4…"],
      [0.5, "0x8…"],
      [0.75, "0xc…"],
    ];
    for (const [f, label] of marks) {
      const inner = pointAt(f, cx, cy, r - 14);
      const outer = pointAt(f, cx, cy, r + 14);
      el("line", { x1: inner.x, y1: inner.y, x2: outer.x, y2: outer.y, class: "ring-tick" }, svg);
      const t = pointAt(f, cx, cy, r - 50);
      el("text", { x: t.x, y: t.y, class: "ring-mark" }, svg).textContent = label;
    }

    const edgeLayer = el("g", { class: "edges" }, svg);
    const markerLayer = el("g", {}, svg);
    const nodeLayer = el("g", {}, svg);
    const cometLayer = el("g", {}, svg);
    const nodes = new Map(); // name -> {g, dot, halo, f, id, x, y}
    let trails = 0;

    function addNode(name, id) {
      if (nodes.has(name)) return nodes.get(name);
      const f = fractionOf(id);
      const p = pointAt(f, cx, cy, r);
      const g = el("g", { class: "node", "data-name": name, "data-id": id }, nodeLayer);
      const halo = el("circle", { cx: p.x, cy: p.y, r: 18, class: "node-halo" }, g);
      const dot = el("circle", { cx: p.x, cy: p.y, r: 15, class: "node-dot" }, g);
      // The label sits outside the ring, along the same radius: its name
      // and the first 8 hex digits of its ID (shown when it's in focus).
      const leader = el("line", { class: "node-leader" }, g);
      const label = el("text", { class: "node-label" }, g);
      label.textContent = name;
      const idText = el("text", { class: "node-idtext" }, g);
      idText.textContent = id.slice(0, 8);
      g.addEventListener("click", () => onSelect && onSelect(name));
      const n = { g, dot, halo, f, id, x: p.x, y: p.y, label, idText, leader };
      nodes.set(name, n);
      layoutLabels();
      if (window.gsap) {
        // clearProps: no inline opacity left behind, or the CSS dimming would lose.
        gsap.from(g, { opacity: 0, scale: 0.3, svgOrigin: `${p.x} ${p.y}`, duration: 0.7, ease: "back.out(2)", clearProps: "opacity,transform" });
      }
      return n;
    }

    // layoutLabels keeps labels readable where IDs cluster (they really
    // do: positions are the IDs, never nudged). A label too close to its
    // neighbour's moves out to the next lane, joined by a thin leader.
    const LANE0 = 44;
    const LANE_STEP = 46;
    const MIN_GAP_PX = 74;
    function layoutLabels() {
      const sorted = [...nodes.values()].sort((a, b) => a.f - b.f);
      const lastAngle = [-1e9, -1e9, -1e9];
      for (const n of sorted) {
        let lane = 0;
        for (; lane < 2; lane++) {
          const arc = (n.f - lastAngle[lane]) * 2 * Math.PI * (r + LANE0 + lane * LANE_STEP);
          if (arc >= MIN_GAP_PX) break;
        }
        lastAngle[lane] = n.f;
        const lr = r + LANE0 + lane * LANE_STEP;
        const lp = pointAt(n.f, cx, cy, lr);
        const lpIn = pointAt(n.f, cx, cy, r + 18);
        const lpOut = pointAt(n.f, cx, cy, lr - 16);
        n.label.setAttribute("x", lp.x);
        n.label.setAttribute("y", lp.y);
        n.idText.setAttribute("x", lp.x);
        n.idText.setAttribute("y", lp.y + 24);
        n.leader.setAttribute("x1", lpIn.x);
        n.leader.setAttribute("y1", lpIn.y);
        n.leader.setAttribute("x2", lpOut.x);
        n.leader.setAttribute("y2", lpOut.y);
        n.leader.style.display = lane > 0 ? "" : "none";
      }
    }

    // pulse flashes a node's halo in its event's colour family: the node
    // just logged something (one real event = one pulse).
    function pulse(name, family, strong) {
      const n = nodes.get(name);
      if (!n || !window.gsap) return;
      n.halo.setAttribute("class", `node-halo ${family}`);
      gsap.fromTo(
        n.halo,
        { attr: { r: 18 }, opacity: strong ? 1 : 0.7 },
        { attr: { r: strong ? 48 : 36 }, opacity: 0, duration: strong ? 0.9 : 0.6, ease: "power2.out", overwrite: true }
      );
    }

    // comet animates one message from node a to node b along an arc and
    // resolves when it arrives. family: "dht" (cyan) | "file" (amber) |
    // "reply" (pale cyan, travelling back along the request's arc).
    // color, if given, overrides the family's colour (a piece in its
    // sender's colour).
    function comet(a, b, family, duration = 0.7, color) {
      const A = nodes.get(a);
      const B = nodes.get(b);
      if (!A || !B || A === B || !window.gsap) return Promise.resolve();
      // Control point: pulled from the chord's middle toward the centre,
      // so arcs between neighbours stay short and cross-ring arcs bend.
      const mx = (A.x + B.x) / 2;
      const my = (A.y + B.y) / 2;
      const c = { x: mx + (cx - mx) * 0.45, y: my + (cy - my) * 0.45 };
      const d = `M${A.x},${A.y} Q${c.x},${c.y} ${B.x},${B.y}`;
      const cls = family === "reply" ? "dht reply" : family;
      const showTrail = trails < MAX_TRAILS;
      let trail = null;
      if (showTrail) {
        trails++;
        trail = el("path", { d, class: `comet-trail ${cls}` }, cometLayer);
        if (color) trail.style.stroke = color;
        const len = trail.getTotalLength();
        trail.style.strokeDasharray = `${len}`;
        trail.style.strokeDashoffset = `${len}`;
        gsap.to(trail.style, { strokeDashoffset: 0, duration, ease: "power2.inOut" });
      }
      const head = el("circle", { cx: A.x, cy: A.y, r: family === "file" ? 9 : 7, class: `comet-head ${cls}`, filter: "url(#glow)" }, cometLayer);
      if (color) head.style.fill = color;
      const t = { v: 0 };
      return new Promise((resolve) => {
        gsap.to(t, {
          v: 1,
          duration,
          ease: "power2.inOut",
          onUpdate() {
            const u = t.v;
            const x = (1 - u) * (1 - u) * A.x + 2 * (1 - u) * u * c.x + u * u * B.x;
            const y = (1 - u) * (1 - u) * A.y + 2 * (1 - u) * u * c.y + u * u * B.y;
            head.setAttribute("cx", x);
            head.setAttribute("cy", y);
          },
          onComplete() {
            pulse(b, family === "reply" ? "dht" : family);
            gsap.to(head, { opacity: 0, duration: 0.25, onComplete: () => head.remove() });
            if (trail) {
              gsap.to(trail, {
                opacity: 0,
                duration: 0.9,
                delay: 0.2,
                onComplete: () => {
                  trail.remove();
                  trails--;
                },
              });
            }
            resolve();
          },
        });
      });
    }

    // edge draws a line between two nodes (a routing-table entry).
    function edge(a, b, cls = "") {
      const A = nodes.get(a);
      const B = nodes.get(b);
      if (!A || !B) return null;
      const line = el("line", { x1: A.x, y1: A.y, x2: B.x, y2: B.y, class: `edge ${cls}` }, edgeLayer);
      if (window.gsap) gsap.from(line, { opacity: 0, duration: 0.5 });
      return line;
    }

    function clearEdges() {
      edgeLayer.replaceChildren();
    }

    // marker shows where a key falls on the ring (keys and node IDs live
    // in the same 160-bit space): a diamond on the track, label inside.
    function marker(key, label, family = "dht") {
      const f = fractionOf(key);
      const p = pointAt(f, cx, cy, r);
      const g = el("g", { class: `key-marker ${family}` }, markerLayer);
      const s = 13;
      el("path", { d: `M${p.x},${p.y - s} L${p.x + s},${p.y} L${p.x},${p.y + s} L${p.x - s},${p.y} Z` }, g);
      const lp = pointAt(f, cx, cy, r - 70);
      el("text", { x: lp.x, y: lp.y }, g).textContent = label;
      if (window.gsap) gsap.from(g, { opacity: 0, scale: 0.2, svgOrigin: `${p.x} ${p.y}`, duration: 0.6, ease: "back.out(2)" });
      return g;
    }

    function clearMarkers() {
      markerLayer.replaceChildren();
    }

    function setDead(name) {
      const n = nodes.get(name);
      if (n) n.g.classList.add("dead");
    }

    // dim(true) fades every node to the background; light(name) brings
    // one back. One focus at a time; everything else ~25%.
    function dim(on) {
      svg.classList.toggle("dimmed", on);
      for (const n of nodes.values()) n.g.classList.remove("lit", "focus");
    }
    function light(name, focus) {
      const n = nodes.get(name);
      if (!n) return;
      n.g.classList.add("lit");
      if (focus) n.g.classList.add("focus");
    }

    return { addNode, pulse, comet, edge, clearEdges, marker, clearMarkers, setDead, dim, light, nodes, center: { cx, cy, r } };
  }

  Viz.ring = { fractionOf, pointAt, createRing };
})(window.Viz);
