// The visualizer's glue (docs/11-visualizer.md): load the node list,
// open one event stream per node, follow the runner's status stream,
// and hand everything to the current scene (scenes.js).
//
// Step mode (default): → / Space / PageDown asks the runner for the next
// scene, and the page holds on the scene's last frame until the next key.
// Continuous mode (?mode=auto): the page asks by itself after each hold.
"use strict";

(function (Viz) {
  const RING = { cx: 700, cy: 470, r: 320 };
  const AUTO_HOLD_S = 4;
  const $ = (id) => document.getElementById(id);
  const { shortHex, category, attrsLine, clock, contact, duration } = Viz.format;

  const params = new URLSearchParams(location.search);
  const mode = params.get("mode") === "auto" ? "auto" : "step";

  // app is what scenes see: the ring, the nodes, and a few drawing helpers.
  const app = {
    ring: null,
    replay: null,
    config: null,
    nodes: new Map(), // name -> {name, api, id, port, dead, live, onRing, events: [], dropped}
    counts: {}, // msg -> events of that kind the page received (checked against events.jsonl)
    scene: null, // current scene object
    sceneIndex: -1,
    status: [], // every runner status message so far
    grids: new Map(), // name -> grid (scenes 6-8)
    history: { viz0Tables: [] }, // viz0's /api/table after each join (scene 2)
    checks: [], // on-screen checks: {text, ok}
    holding: false,
    mode,
  };
  Viz.app = app;

  // --- nodes and their streams ---------------------------------------------

  // resolve maps "id@ip:port" or "ip:port" to a node name. Events carry
  // no node IDs of their own, so this is how an arrow finds its two ends.
  // RPC events name UDP addresses; a piece event's `from` is the
  // provider's TCP address (its file server), so both are matched.
  app.resolve = function (text) {
    const c = contact(text);
    for (const n of app.nodes.values()) {
      if (c.id && n.id === c.id) return n.name;
    }
    for (const n of app.nodes.values()) {
      if (c.addr && (n.addr === c.addr || n.tcpAddr === c.addr)) return n.name;
    }
    return "";
  };

  // watch registers a node, reads its ID, and opens its event stream.
  // It goes on the ring now, unless a replay window will place it later.
  async function watch(entry, { place = true } = {}) {
    if (app.nodes.has(entry.name)) return app.nodes.get(entry.name);
    const n = { ...entry, id: entry.id || "", addr: "", live: false, dead: !!entry.dead, onRing: false, events: [], dropped: 0 };
    app.nodes.set(n.name, n);
    try {
      const info = await Viz.getJSON(n.api, "/api/node");
      n.id = info.id;
      n.addr = `${info.ip}:${info.udp_port}`; // where its DHT messages come from
      n.tcpAddr = `${info.ip}:${info.tcp_port}`; // where its pieces come from
      n.info = info;
    } catch (err) {
      n.dead = true;
      renderBadge();
      return n;
    }
    if (place) placeNode(n.name);
    await new Promise((resolve) => {
      Viz.connectNode(n.api, {
        level: "debug",
        onOpen() {
          n.live = true;
          renderBadge();
          // Tell the runner, so it acts on this node only once we listen.
          fetch("/viz/subscribed", { method: "POST", body: JSON.stringify({ name: n.name }) }).catch(() => {});
          resolve();
        },
        onEvent: (ev) => onNodeEvent(n, ev),
        onClose() {
          n.dead = true;
          app.ring.setDead(n.name);
          renderBadge();
          app.scene && app.scene.onDead && app.scene.onDead(app, n.name);
          resolve();
        },
      });
    });
    return n;
  }

  function placeNode(name) {
    const n = app.nodes.get(name);
    if (!n || n.onRing || !n.id) return;
    n.onRing = true;
    app.ring.addNode(n.name, n.id);
    if (n.dead) app.ring.setDead(n.name);
    renderBadge();
  }
  app.placeNode = placeNode;

  function onNodeEvent(n, ev) {
    n.events.push(ev);
    app.counts[ev.msg] = (app.counts[ev.msg] || 0) + 1;
    // `dropped` is this viewer's running loss count for that node
    // (NODE-API.md); it must stay 0. A gap in `seq` is NOT a loss.
    if ((ev.dropped || 0) > n.dropped) {
      n.dropped = ev.dropped;
      renderBadge();
    }
    if (app.replay.add(n.name, ev)) return; // inside a replay window: played later
    onLiveEvent(n, ev);
  }

  // Events outside replay windows are drawn as they arrive: file
  // transfer (scenes 6-8) is slowed by --debug-serve-delay, not replayed.
  const LIVE_TICKER = new Set([
    "provider announced",
    "providers found",
    "new providers found",
    "provider failed, trying the next one",
    "download stalled: no live provider has the missing pieces; looking for more",
    "download gave up, still serving its pieces",
    "download resumed",
    "download complete",
    "share complete",
  ]);

  function onLiveEvent(n, ev) {
    const family = category(ev);
    const a = ev.attrs || {};
    if (ev.msg === "piece received") {
      // The comet and the grid cell both take the sender's colour, so
      // you can see who fed whom (scenes.js colorFor).
      const from = app.resolve(a.from);
      app.ring.comet(from, n.name, "file", 0.7, app.colors && app.colors.get(from));
      const g = app.grids.get(n.name);
      if (g) g.set(a.index, a.of, a.have, from);
    } else if (ev.msg === "provider failed, trying the next one") {
      app.ring.pulse(n.name, "fail", true);
    } else if (ev.msg === "provider announced" || ev.msg === "download complete" || ev.msg === "download resumed") {
      app.ring.pulse(n.name, "file", true);
    }
    if (LIVE_TICKER.has(ev.msg)) addTicker(n.name, ev, family);
    app.scene && app.scene.onLive && app.scene.onLive(app, n, ev);
  }

  // --- ticker, badge, panel ------------------------------------------------

  const TICKER_ROWS = 4; // a few real log lines, never a wall of logs

  function addTicker(name, ev, family) {
    const li = document.createElement("li");
    li.className = family;
    li.innerHTML = `<span class="t"></span><span class="who"></span><span class="msg"></span>`;
    li.children[0].textContent = clock(ev.time);
    li.children[1].textContent = name;
    li.children[2].textContent = `${shortHex(ev.msg)}  ${attrsLine(ev.attrs)}`;
    const list = $("ticker-list");
    list.prepend(li);
    while (list.children.length > TICKER_ROWS) list.lastElementChild.remove();
    if (window.gsap) gsap.from(li, { opacity: 0, x: -20, duration: 0.35, ease: "power2.out" });
  }
  app.addTicker = addTicker;

  // Replay ticker: the real log lines behind each step (not the noise).
  function replayTicker(items, win) {
    const sent = new Set(items.filter((it) => it.ev.msg === "rpc sent").map((it) => it.ev.attrs.rpc_id));
    for (const { node, ev } of items) {
      const k = Viz.rpc.kindOf(ev);
      if (k === "noise" || (win.pace === "fast" && k !== "label")) continue;
      // The receiver's line, when the sender's is there too, says the same thing.
      if (ev.msg === "rpc received" && k === "launch" && sent.has(ev.attrs.rpc_id)) continue;
      addTicker(node, ev, category(ev));
    }
  }

  let badgeMode = "";
  function renderBadge() {
    const all = [...app.nodes.values()];
    const live = all.filter((n) => n.live && !n.dead && n.onRing).length;
    const dropped = all.reduce((s, n) => s + n.dropped, 0);
    const parts = [live > 0 ? "LIVE" : "CONNECTING", `${live} nodes`];
    if (badgeMode) parts.push(badgeMode);
    if (dropped > 0) parts.push(`dropped ${dropped}`); // only worth showing if a viewer fell behind
    $("badge-text").textContent = parts.join(" · ");
    $("badge").classList.toggle("live", live > 0);
    $("badge").classList.toggle("replay", badgeMode.startsWith("REPLAY"));
    $("badge").classList.toggle("warn", dropped > 0);
  }
  app.setBadgeMode = (text) => {
    badgeMode = text || "";
    renderBadge();
  };

  // setPanel cross-fades the right column to new content.
  app.setPanel = function (html) {
    const panel = $("panel");
    panel.innerHTML = html;
    if (window.gsap) gsap.fromTo(panel, { opacity: 0, y: 16 }, { opacity: 1, y: 0, duration: 0.5, ease: "power2.out" });
    return panel;
  };
  app.$ = (sel) => $("panel").querySelector(sel);

  function setTitle(title, caption) {
    const box = $("caption");
    const swap = () => {
      $("scene-title").textContent = title;
      $("scene-caption").textContent = caption || "";
      // A long title gets a smaller font, so it stays on one line and
      // clear of the ring's lowest labels.
      box.classList.toggle("long", title.length > 36);
    };
    if (!window.gsap) return swap();
    gsap.to(box, {
      opacity: 0,
      duration: 0.3,
      onComplete() {
        swap();
        gsap.to(box, { opacity: 1, duration: 0.5 });
      },
    });
  }

  function setHint(text) {
    $("hint").textContent = text || "";
    $("hint").classList.toggle("on", !!text);
  }
  app.setHint = setHint;

  // --- the runner's status stream ------------------------------------------

  function onStatus(msg) {
    app.status.push(msg);
    switch (msg.type) {
      case "scene started":
        enterScene(msg);
        break;
      case "node up":
        // Inside a replay window (a join), the node goes on the ring when
        // its join replays; otherwise now.
        watch({ name: msg.name, api: msg.api, id: msg.id }, { place: !app.replay.recording });
        break;
      case "window open":
        app.replay.open(msg);
        break;
      case "window closed":
        closeWindow(msg);
        break;
      case "scene done":
        sceneDone(msg);
        break;
    }
    app.scene && app.scene.onStatus && app.scene.onStatus(app, msg);
  }

  // Snapshots that belong to a window are read when it closes (live
  // state at that moment). The window leaves the buffer at once, so the
  // next one can open; its replay waits for the snapshots.
  function closeWindow(msg) {
    const ready = (async () => {
      const extra = {};
      if (msg.window.startsWith("join-")) {
        try {
          extra.newTable = await Viz.getJSON(msg.api, "/api/table");
          const viz0 = app.nodes.get(app.config.nodes[0].name);
          const t = await Viz.getJSON(viz0.api, "/api/table");
          app.history.viz0Tables.push({ after: msg.newcomer, table: t });
        } catch (err) {
          // a missing snapshot only means less to draw
        }
      }
      return extra;
    })();
    app.replay.close(msg, ready);
  }

  function enterScene(msg) {
    app.sceneIndex = msg.index;
    app.scene = Viz.scenes[msg.scene] || {};
    app.holding = false;
    setHint("");
    app.setBadgeMode(app.scene.badge || "");
    // File scenes need the whole right column for their piece grids; their
    // events are on the grids themselves, so the log ticker steps aside.
    $("stage").classList.toggle("tall-panel", !!app.scene.tallPanel);
    setTitle(msg.title, app.scene.caption);
    // A scene's own animation (scene 2's table history) may outlast the
    // runner's part; sceneDone waits for it before showing "→ next".
    app.entering = Promise.resolve(app.scene.enter && app.scene.enter(app, msg));
  }

  async function sceneDone(msg) {
    await app.entering;
    await app.replay.idle();
    if (app.scene && app.scene.done) await app.scene.done(app, msg);
    if (msg.chained) return; // the runner starts the next scene by itself (scene 6 → 7)
    app.holding = true;
    const last = msg.index + 1 >= (app.config.scenes || 9);
    if (msg.error) setHint(`scene error: ${msg.error}`);
    else if (!last && mode === "step") setHint("→  next");
    if (mode === "auto" && !last) setTimeout(next, (app.scene.autoHold ?? AUTO_HOLD_S) * 1000);
  }

  async function next() {
    if (app.config.watch) return setHint("watch mode: no scenes");
    const res = await fetch("/viz/next", { method: "POST" }).catch(() => null);
    if (!res) return setHint("vizrun isn't answering");
    if (res.status === 409) {
      const body = await res.json().catch(() => ({}));
      flashHint(body.error || "busy");
    }
  }

  function flashHint(text) {
    const old = $("hint").textContent;
    setHint(text);
    setTimeout(() => setHint(app.holding ? old : ""), 1500);
  }

  function openStatus() {
    const src = new EventSource("/viz/status");
    src.onmessage = (m) => {
      try {
        onStatus(JSON.parse(m.data));
      } catch (err) {
        console.error("status", err, m.data);
      }
    };
    return new Promise((resolve) => (src.onopen = resolve));
  }

  // --- start ---------------------------------------------------------------

  function fitStage() {
    const s = Math.min(innerWidth / 1920, innerHeight / 1080);
    const stage = $("stage");
    stage.style.transform = `scale(${s})`;
    stage.style.left = `${(innerWidth - 1920 * s) / 2}px`;
    stage.style.top = `${(innerHeight - 1080 * s) / 2}px`;
  }

  async function start() {
    fitStage();
    addEventListener("resize", fitStage);
    app.ring = Viz.ring.createRing($("ring"), { ...RING });
    app.replay = Viz.createReplay(app.ring, app.resolve, {
      badge(win) {
        if (win) app.setBadgeMode(`REPLAY · in order · real ${duration(win.realNs)}`);
        else app.setBadgeMode((app.scene && app.scene.badge) || "");
      },
      start: (win) => app.scene && app.scene.onWindowStart && app.scene.onWindowStart(app, win),
      step: (step, win) => app.scene && app.scene.onStep && app.scene.onStep(app, step, win),
      end: (win) => app.scene && app.scene.onWindowEnd && app.scene.onWindowEnd(app, win),
      ticker: replayTicker,
    });

    app.config = await Viz.loadConfig();
    const hasRunner = !!app.config.scenes;
    // Streams first, then the status stream, then act: nothing is replayed.
    await Promise.all(app.config.nodes.map((e) => watch(e)));
    if (hasRunner && !app.config.watch) await openStatus();
    renderBadge();

    addEventListener("keydown", (e) => {
      if (["ArrowRight", " ", "PageDown", "Enter"].includes(e.key)) {
        e.preventDefault();
        next();
      }
    });
    if (!hasRunner || app.config.watch) {
      setTitle("Watching live nodes", "No runner: this page only watches. Start cmd/vizrun to run the scenes.");
    } else if ((app.config.next_scene || 0) === 0) {
      setTitle("Trackerless P2P, live", mode === "auto" ? "Real nodes of our code, one mechanism at a time." : `${app.config.nodes.length} node running. Press → to begin.`);
      setHint(mode === "auto" ? "" : "→  begin");
      if (mode === "auto") setTimeout(next, 2000);
    }
  }

  // Wait for the fonts so nothing reflows mid-scene (decision 2).
  document.fonts.ready.then(start);
})(window.Viz);
