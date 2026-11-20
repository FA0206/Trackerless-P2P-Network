// The page's half of each scene: what to draw (docs/11-visualizer.md).
// The runner's half, what the nodes do, is the scene with the same id in
// cmd/vizrun/scenes.go, which also holds the title (sent with "scene
// started", so titles live in one place and match the slides).
//
// A scene object may have:
//   caption                      the one-line caption under the title
//   badge                        badge text while no replay plays
//   enter(app, msg)              the scene starts: set up the panel
//   onWindowStart(app, win)      a replay window is about to play
//   onStep(app, step, win)       one replay step (rpc.js)
//   onWindowEnd(app, win)        the window finished playing
//   onLive(app, node, ev)        a node event outside any window
//   onStatus(app, msg)           a runner status message
//   onDead(app, name)            a node's stream closed
//   done(app, msg)               the runner finished; replays drained
// Every number drawn comes from an event, a snapshot, or a runner fact.
"use strict";

window.Viz = window.Viz || {};

(function (Viz) {
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
  const sleep = (s) => new Promise((res) => setTimeout(res, s * 1000));
  const short = (id) => String(id || "").slice(0, 8);

  // --- piece grids (scenes 6-8) ---------------------------------------------

  // makeGrid draws one node's pieces as cells; set() fills one from a
  // "piece received" event. The seeder's grid starts full (it shared).
  function makeGrid(app, host, name, pieces, role, full) {
    const row = document.createElement("div");
    row.className = `grid-row ${role}`;
    row.innerHTML = `<div class="grid-head"><span class="grid-name"></span><span class="grid-role"></span><span class="grid-count"></span></div><div class="grid-cells"></div><div class="grid-note"></div>`;
    row.querySelector(".grid-name").textContent = name;
    row.querySelector(".grid-role").textContent = role;
    const cells = row.querySelector(".grid-cells");
    cells.style.gridTemplateColumns = `repeat(${Math.min(pieces, 16)}, 1fr)`;
    for (let i = 0; i < pieces; i++) {
      const c = document.createElement("span");
      if (full) c.className = "have";
      cells.appendChild(c);
    }
    host.appendChild(row);
    let have = full ? pieces : 0;
    const count = row.querySelector(".grid-count");
    const note = row.querySelector(".grid-note");
    const render = () => (count.textContent = `${have}/${pieces}`);
    render();
    const grid = {
      name,
      pieces,
      row,
      have: new Set(full ? [...Array(pieces).keys()] : []),
      set(index, of, haveN) {
        const c = cells.children[index];
        if (c && !c.classList.contains("have")) {
          c.classList.add("have");
          if (window.gsap) gsap.fromTo(c, { scale: 1.6 }, { scale: 1, duration: 0.4, ease: "back.out(3)" });
        }
        grid.have.add(index);
        have = haveN ?? grid.have.size;
        render();
        app.scene && app.scene.onPieces && app.scene.onPieces(app);
      },
      note(text, cls = "") {
        note.textContent = text;
        note.className = `grid-note ${cls}`;
      },
      dead() {
        row.classList.add("dead");
      },
      done() {
        row.classList.add("done");
      },
    };
    app.grids.set(name, grid);
    return grid;
  }

  // swarmSpread: how many distinct pieces the leechers hold together.
  function swarmSpread(app, leechers) {
    const all = new Set();
    let pieces = 0;
    let anyDone = false;
    for (const name of leechers) {
      const g = app.grids.get(name);
      if (!g) continue;
      pieces = g.pieces;
      g.have.forEach((i) => all.add(i));
      if (g.have.size >= g.pieces) anyDone = true;
    }
    return { union: all.size, pieces, anyDone };
  }

  // --- 1. join ------------------------------------------------------------

  const join = {
    caption: "Every node but the first joins through one it already knows: a PING, then a lookup of its own ID. No server keeps a list.",
    enter(app) {
      app.ring.dim(false);
      app.setPanel(`
        <div class="eyebrow">Nodes in the network</div>
        <div class="big-number" id="join-count">${[...app.nodes.values()].filter((n) => n.onRing).length}</div>
        <div class="panel-line" id="join-now">waiting for the first newcomer…</div>
        <div class="panel-sub" id="join-how"></div>`);
    },
    async onWindowStart(app, win) {
      app.placeNode(win.newcomer);
      app.ring.dim(true);
      app.ring.light(win.newcomer, true);
      app.ring.light(win.boot);
      app.$("#join-now").innerHTML = `<b>${esc(win.newcomer)}</b> joins through <b>${esc(win.boot)}</b>`;
      app.$("#join-count").textContent = [...app.nodes.values()].filter((n) => n.onRing).length;
      if (win.pace !== "slow") app.$("#join-how").textContent = "the same PING + self-lookup, replayed faster";
      await sleep(win.pace === "slow" ? 0.6 : 0.1);
    },
    onStep(app, step, win) {
      if (step.kind !== "launch") return;
      step.comets.forEach((c) => app.ring.light(c.to));
      if (win.pace === "slow") {
        const what = step.type === "PING" ? "PING: are you there? (and who are you?)" : `${step.type} for its own ID: who is near me?`;
        app.$("#join-how").textContent = what;
      }
    },
    async onWindowEnd(app, win) {
      // The newcomer's routing table right after joining (/api/table).
      if (win.pace === "slow" && win.newTable) {
        const contacts = win.newTable.buckets.flatMap((b) => b.contacts.map((c) => c.id));
        const names = contacts.map((id) => app.resolve(`${id}@`)).filter(Boolean);
        names.forEach((n) => app.ring.edge(win.newcomer, n, "fresh"));
        app.$("#join-how").textContent = `${win.newcomer} now knows ${contacts.length} node(s), learned from the replies (its /api/table).`;
        await sleep(1.6);
        app.ring.clearEdges();
      }
      app.ring.dim(false);
    },
    done(app) {
      const n = [...app.nodes.values()].filter((x) => x.onRing).length;
      app.$("#join-count").textContent = n;
      app.$("#join-now").textContent = `${n} nodes, and none of them is in charge.`;
      app.$("#join-how").textContent = "Each join was seen from the nodes it contacted: their streams were already open.";
    },
  };

  // --- 2. k-buckets -------------------------------------------------------

  // A bucket index i holds contacts at XOR distance [2^i, 2^(i+1)).
  // Bucket 159 is "the other half of the ID space", 158 a quarter, …
  function bandName(i) {
    const parts = { 159: "the far half", 158: "a quarter away", 157: "an eighth away", 156: "1/16 away" };
    return parts[i] || `2^${i}`;
  }

  const buckets = {
    caption: "A node keeps at most k = 4 contacts per distance band: its close neighbours well, the far half of the network only through a few.",
    badge: "REPLAY · viz0's /api/table after each join",
    async enter(app) {
      const focus = app.config.nodes[0].name;
      app.ring.dim(true);
      app.ring.light(focus, true);
      app.setPanel(`
        <div class="eyebrow">${esc(focus)}'s routing table · k = ${app.config.k}</div>
        <div class="buckets" id="bucket-rows"></div>
        <div class="panel-sub" id="bucket-note"></div>`);
      const rows = app.$("#bucket-rows");
      const seen = new Set();
      for (const snap of app.history.viz0Tables) {
        drawBuckets(app, rows, snap.table, focus, seen);
        await sleep(0.45);
      }
      // The live table now, as the final frame.
      try {
        const now = await Viz.getJSON(app.nodes.get(focus).api, "/api/table");
        drawBuckets(app, rows, now, focus, seen);
      } catch (err) {
        // keep the last snapshot
      }
      const evs = app.nodes.get(focus).events;
      const full = evs.filter((e) => e.msg.startsWith("bucket full")).length;
      const kept = evs.filter((e) => e.msg.startsWith("stale contact still alive")).length;
      const evicted = evs.filter((e) => e.msg.startsWith("stale contact unresponsive")).length;
      app.$("#bucket-note").innerHTML =
        `Bucket full, newcomer seen: ${esc(focus)} pinged its oldest contact <b>${full}</b>× ` +
        `· it answered, so it stayed <b>${kept}</b>× · evicted <b>${evicted}</b>×. Old, proven contacts win (paper §2.2).`;
    },
  };

  function drawBuckets(app, rows, table, focus, seen) {
    rows.replaceChildren();
    for (const b of table.buckets) {
      const row = document.createElement("div");
      row.className = "bucket-row";
      const full = b.contacts.length >= table.k;
      row.innerHTML = `<span class="b-index">${b.index}</span><span class="b-band"></span><span class="b-dots"></span><span class="b-count ${full ? "full" : ""}">${b.contacts.length}/${table.k}</span>`;
      row.querySelector(".b-band").textContent = bandName(b.index);
      const dots = row.querySelector(".b-dots");
      for (const c of b.contacts) {
        const d = document.createElement("i");
        dots.appendChild(d);
        const name = app.resolve(`${c.id}@`);
        if (name && !seen.has(name)) {
          seen.add(name);
          app.ring.edge(focus, name, `b${Math.max(0, 159 - b.index)}`);
          app.ring.light(name);
        }
      }
      rows.appendChild(row);
    }
  }

  // --- 3. lookup ----------------------------------------------------------

  const lookup = {
    caption: "viz7 has never heard of its target. Each round asks the closest nodes it knows so far; their replies point it closer.",
    enter(app) {
      app.ring.clearEdges();
      app.ring.dim(true);
      app.setPanel(`
        <div class="eyebrow">Iterative lookup · α = ${app.config.alpha}, k = ${app.config.k}</div>
        <div class="panel-line" id="lk-title">…</div>
        <ol class="rounds" id="lk-rounds"></ol>
        <div class="verdict" id="lk-verdict"></div>`);
      lookup.rounds = 0;
    },
    async onWindowStart(app, win) {
      app.ring.light(win.from, true);
      app.ring.light(win.target);
      app.ring.marker(win.target_id, `target ${short(win.target_id)}`);
      app.$("#lk-title").innerHTML = `<b>${esc(win.from)}</b> looks for <b>${esc(win.target)}</b> (${short(win.target_id)}), not in its table`;
      await sleep(1.0);
    },
    onStep(app, step) {
      if (step.kind === "round") {
        lookup.rounds += 1;
        const li = document.createElement("li");
        li.innerHTML = `<span class="r-n">round ${step.round}</span><span class="r-who">asking ${step.batch}…</span>`;
        app.$("#lk-rounds").appendChild(li);
        lookup.li = li;
      } else if (step.kind === "launch" && step.type === "FIND_NODE" && lookup.li) {
        step.comets.forEach((c) => app.ring.light(c.to));
        lookup.li.querySelector(".r-who").textContent = "asks " + step.comets.map((c) => c.to || "?").join(", ");
      } else if (step.kind === "label" && step.msg === "lookup converged") {
        const a = step.attrs;
        const ok = a.rounds === lookup.rounds;
        app.$("#lk-verdict").innerHTML =
          `converged: <b>${a.rounds}</b> rounds, <b>${a.rpcs_sent}</b> RPCs, ${a.closest_found} closest found ` +
          `<span class="${ok ? "ok" : "bad"}">${ok ? "✓ replayed rounds match 'lookup converged'" : "✗ replay shows " + lookup.rounds}</span>`;
        app.checks.push({ text: `lookup rounds replayed=${lookup.rounds} converged=${a.rounds}`, ok });
      }
    },
    onWindowEnd(app, win) {
      app.ring.light(win.target, true);
    },
  };

  // --- 4. put / get -------------------------------------------------------

  const store = {
    caption: "A value lives on the k nodes whose IDs are closest to its key, so anyone can find it with the same lookup.",
    enter(app) {
      app.ring.clearMarkers();
      app.ring.clearEdges();
      app.ring.dim(true);
      app.setPanel(`
        <div class="eyebrow">put / get · key = SHA-1(name)</div>
        <div class="panel-line" id="st-put">…</div>
        <div class="holders" id="st-holders"></div>
        <div class="panel-line" id="st-get"></div>
        <div class="value-box" id="st-value"></div>`);
    },
    async onWindowStart(app, win) {
      if (win.window === "put") {
        app.ring.marker(win.key, `key "${win.name}"`);
        app.ring.light(win.from, true);
        app.$("#st-put").innerHTML = `<b>${esc(win.from)}</b> stores "${esc(win.name)}" under ${short(win.key)}`;
      } else {
        app.ring.dim(true);
        app.ring.light(win.from, true);
        app.$("#st-get").innerHTML = `<b>${esc(win.from)}</b>, which doesn't hold it, asks for "${esc(win.name)}"`;
      }
      await sleep(0.9);
    },
    onStep(app, step, win) {
      if (step.kind === "label" && step.msg === "value stored") {
        app.ring.light(step.node);
        const chip = document.createElement("span");
        chip.className = "chip";
        chip.textContent = step.node;
        app.$("#st-holders").appendChild(chip);
      } else if (step.kind === "label" && step.msg === "store complete") {
        const a = step.attrs;
        const chip = document.createElement("span");
        chip.className = "chip muted";
        chip.textContent = `${a.stored_at} replicas${a.stored_locally ? " + itself" : ""}`;
        app.$("#st-holders").appendChild(chip);
      } else if (step.kind === "launch" && win.window === "get") {
        step.comets.forEach((c) => app.ring.light(c.to));
      } else if (step.kind === "label" && step.msg === "find_value finished") {
        const a = step.attrs;
        app.$("#st-get").innerHTML += ` · found in <b>${a.rounds}</b> round(s), ${a.rpcs_sent} RPCs`;
      }
    },
    onWindowEnd(app, win) {
      if (win.window === "get" && win.value) {
        app.$("#st-value").textContent = `"${win.value}"`;
        if (window.gsap) gsap.from(app.$("#st-value"), { opacity: 0, y: 12, duration: 0.5 });
      }
    },
  };

  // --- 5. share -----------------------------------------------------------

  const share = {
    caption: "Sharing stores a provider record (\"I have it\") on the k nodes closest to the file's key. The magnet link is that key.",
    enter(app) {
      app.ring.clearMarkers();
      app.ring.dim(true);
      app.setPanel(`
        <div class="eyebrow">share</div>
        <div class="panel-line" id="sh-line">…</div>
        <div class="holders" id="sh-holders"></div>
        <div class="magnet" id="sh-magnet"></div>`);
    },
    async onWindowStart(app, win) {
      app.ring.light(win.from, true);
      app.ring.marker(win.key, "file key");
      app.$("#sh-line").innerHTML = `<b>${esc(win.from)}</b> splits the file into pieces, hashes them, and announces itself`;
      await sleep(0.9);
    },
    onStep(app, step) {
      if (step.kind === "label" && step.msg === "value stored") {
        app.ring.light(step.node);
        const chip = document.createElement("span");
        chip.className = "chip";
        chip.textContent = `${step.node} holds the record`;
        app.$("#sh-holders").appendChild(chip);
      } else if (step.kind === "label" && step.msg === "provider announced") {
        app.$("#sh-line").innerHTML += ` · provider record on <b>${step.attrs.replicas}</b> nodes`;
      }
    },
    async onStatus(app, msg) {
      if (msg.type !== "magnet") return;
      await app.replay.idle(); // the link comes from the answer, after the replayed share
      app.$("#sh-magnet").innerHTML = `<div class="eyebrow">magnet link · ${msg.pieces} pieces</div><code></code>`;
      app.$("#sh-magnet code").textContent = msg.magnet;
      if (window.gsap) gsap.from(app.$("#sh-magnet"), { opacity: 0, scale: 0.96, duration: 0.6 });
    },
  };

  // --- 6. swarm, 7. kill: one shared panel ---------------------------------

  let swarmSeeder = "";
  let swarmLeechers = [];

  function swarmPanel(app, title) {
    app.setPanel(`
      <div class="eyebrow">${esc(title)}</div>
      <div class="grids" id="grids"></div>
      <div class="spread" id="spread"></div>
      <div class="banner" id="banner"></div>
      <div class="hashes" id="hashes"></div>`);
    app.grids.clear();
  }

  function renderSpread(app, leechers) {
    const { union, pieces, anyDone } = swarmSpread(app, leechers);
    if (!pieces) return { union, pieces, anyDone };
    const el = app.$("#spread");
    if (el) el.innerHTML = `the leechers together hold <b>${union}/${pieces}</b> pieces`;
    return { union, pieces, anyDone };
  }

  const swarm = {
    tallPanel: true,
    caption: "Leechers fetch the rarest pieces first and serve each piece as soon as they have it.",
    keepsHint: true,
    enter(app) {
      app.ring.clearMarkers();
      app.ring.clearEdges();
      app.ring.dim(true);
      const magnet = app.status.filter((m) => m.type === "magnet").pop();
      swarmSeeder = magnet ? magnet.node : "";
      swarmLeechers = [];
      swarmPanel(app, `swarming · ${app.config.serve_delay} per piece served`);
      if (magnet) {
        makeGrid(app, app.$("#grids"), swarmSeeder, magnet.pieces, "seeder", true);
        app.ring.light(swarmSeeder, true);
      }
      app.setBadgeMode(`real time · serve delay ${app.config.serve_delay}`);
    },
    onStatus(app, msg) {
      if (msg.type === "fetch started") {
        swarmLeechers.push(msg.node);
        makeGrid(app, app.$("#grids"), msg.node, msg.pieces, "leecher", false);
        app.ring.light(msg.node);
      }
    },
    onLive(app, n, ev) {
      if (ev.msg === "provider announced" && swarmLeechers.includes(n.name)) {
        app.grids.get(n.name) && app.grids.get(n.name).note("now also a provider (seeding what it has)", "file");
      }
    },
    onPieces(app) {
      const s = renderSpread(app, swarmLeechers);
      if (app.mode === "step" && app.holding && s.pieces && s.union >= s.pieces && !s.anyDone) {
        app.setHint("every piece is in the swarm  ·  → kills the seeder");
      } else if (app.holding && s.anyDone) {
        app.setHint("→  next");
      }
    },
    done(app) {
      swarm.onPieces(app);
    },
    autoHold: 0,
  };

  const kill = {
    tallPanel: true,
    caption: "Once the swarm holds every piece, the seeder isn't needed: kill it, and the leechers finish from each other.",
    onPieces(app) {
      renderSpread(app, swarmLeechers);
    },
    enter(app) {
      renderSpread(app, swarmLeechers);
      app.$("#banner").textContent = "waiting for the moment the leechers together hold every piece…";
      app.setBadgeMode(`real time · serve delay ${app.config.serve_delay}`);
    },
    onStatus(app, msg) {
      if (msg.type === "killed") {
        const banner = app.$("#banner");
        banner.className = `banner ${msg.void ? "bad" : "kill"}`;
        banner.innerHTML = `SIGKILL ${esc(msg.node)} · ${esc(msg.reason)}`;
        const g = app.grids.get(msg.node);
        if (g) {
          g.dead();
          g.note("killed", "fail");
        }
        app.ring.pulse(msg.node, "fail", true);
      } else if (msg.type === "fetch done") {
        const g = app.grids.get(msg.node);
        if (g && msg.ok) {
          g.done();
          const from = msg.result && msg.result.pieces_from ? Object.entries(msg.result.pieces_from) : [];
          const named = from.map(([addr, k]) => `${app.resolve(addr) || addr}: ${k}`).join(" · ");
          g.note(`complete · pieces from ${named}`, "ok");
        }
      } else if (msg.type === "sha256") {
        // The runner hashed the original and this copy on disk.
        const g = app.grids.get(msg.node);
        const row = document.createElement("div");
        row.className = `hash-line ${msg.match ? "ok" : "bad"}`;
        row.textContent = msg.match
          ? `✓ SHA-256 ${String(msg.got).slice(0, 12)}… matches the original`
          : `✗ SHA-256 ${String(msg.got).slice(0, 12)}… differs from the original`;
        (g ? g.row : app.$("#hashes")).appendChild(row);
        if (window.gsap) gsap.from(row, { opacity: 0, x: 20, duration: 0.4 });
      }
    },
    onLive(app, n, ev) {
      if (ev.msg === "provider failed, trying the next one") {
        const g = app.grids.get(n.name);
        if (g) g.note(`its provider ${app.resolve(ev.attrs.provider) || ev.attrs.provider} died: trying the next one`, "fail");
      }
    },
  };

  // --- 8. outro -----------------------------------------------------------

  const outro = {
    caption: "Every dot, arrow and number on screen came from these nodes' own log events and API answers.",
    enter(app) {
      app.ring.dim(false);
      app.ring.clearMarkers();
      app.setPanel(`<div class="eyebrow">this run</div><div id="outro"></div>`);
    },
    onStatus(app, msg) {
      if (msg.type !== "summary") return;
      const c = msg.counts || {};
      const dropped = [...app.nodes.values()].reduce((s, n) => s + n.dropped, 0);
      app.$("#outro").innerHTML = `
        <dl class="outro">
          <div><dt>events recorded</dt><dd>${msg.events}</dd></div>
          <div><dt>dropped</dt><dd class="${msg.dropped === 0 && dropped === 0 ? "ok" : "bad"}">${msg.dropped} · page ${dropped}</dd></div>
          <div><dt>RPCs sent</dt><dd>${c["rpc sent"] || 0}</dd></div>
          <div><dt>pieces received</dt><dd>${c["piece received"] || 0}</dd></div>
          <div><dt>downloads complete</dt><dd>${c["download complete"] || 0}</dd></div>
          <div><dt>nodes</dt><dd>${msg.nodes}</dd></div>
        </dl>
        <div class="panel-sub">k = ${msg.k} · α = ${msg.alpha} · --max-inflight ${msg.max_inflight}<br>
        --debug-serve-delay ${esc(msg.serve_delay)} on every node: pieces were slowed down to be watchable; DHT traffic was not<br>
        DHT scenes: replayed in their real order, with their real duration shown<br>
        seeds ${esc(msg.seeds)} · commit ${esc(msg.commit || "?")} · ${esc(msg.date)}</div>`;
    },
  };

  Viz.scenes = { join, buckets, lookup, store, share, swarm, kill, outro };
  Viz.scenesUtil = { makeGrid, swarmSpread };
})(window.Viz);
