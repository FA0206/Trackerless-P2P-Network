// The page's half of each scene: what to draw.
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

  // Every node in a grid gets a colour: the seeder amber (file payload),
  // the others three calm tones. A received piece is painted in the
  // colour of the node that sent it (the event's `from`), so the grids
  // show who fed whom. Four is enough: scenes 7 and 8 have four nodes.
  const SOURCE_COLORS = ["#ffb547", "#b39dff", "#f39ac7", "#e8e2d0"];

  function colorFor(app, name) {
    if (!app.colors) app.colors = new Map();
    if (!app.colors.has(name)) app.colors.set(name, SOURCE_COLORS[app.colors.size % SOURCE_COLORS.length]);
    return app.colors.get(name);
  }

  // makeGrid draws one node's pieces as cells; set() fills one from a
  // "piece received" event, in the sender's colour. The seeder's grid
  // starts full (it shared), in its own colour.
  function makeGrid(app, host, name, pieces, role, full) {
    const me = colorFor(app, name);
    const row = document.createElement("div");
    row.className = `grid-row ${role}`;
    row.innerHTML = `<div class="grid-head"><span class="grid-name"></span><span class="grid-role"></span><span class="grid-count"></span></div><div class="grid-cells"></div><div class="grid-from"></div><div class="grid-note"></div>`;
    row.querySelector(".grid-name").textContent = name;
    row.querySelector(".grid-name").style.color = me;
    row.querySelector(".grid-role").textContent = role;
    const cells = row.querySelector(".grid-cells");
    cells.style.gridTemplateColumns = `repeat(${Math.min(pieces, 16)}, 1fr)`;
    for (let i = 0; i < pieces; i++) {
      const c = document.createElement("span");
      if (full) {
        c.className = "have";
        c.style.background = me;
      }
      cells.appendChild(c);
    }
    const fromLine = row.querySelector(".grid-from");
    const tally = new Map(); // sender name -> pieces received from it
    const renderTally = () => {
      fromLine.replaceChildren();
      for (const [who, n] of tally) {
        const chip = document.createElement("span");
        chip.className = "from-chip";
        chip.innerHTML = `<i></i><b></b> ${n}`;
        chip.querySelector("i").style.background = colorFor(app, who);
        chip.querySelector("b").textContent = who;
        fromLine.appendChild(chip);
      }
    };
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
      tally,
      set(index, of, haveN, from) {
        const c = cells.children[index];
        if (c && !c.classList.contains("have")) {
          c.classList.add("have");
          if (from) c.style.background = colorFor(app, from);
          if (window.gsap) gsap.fromTo(c, { scale: 1.6 }, { scale: 1, duration: 0.4, ease: "back.out(3)" });
        }
        if (from) {
          tally.set(from, (tally.get(from) || 0) + 1);
          renderTally();
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
      // addNote appends to the note line (one line per grid saves room).
      addNote(text, cls = "") {
        const span = document.createElement("span");
        span.className = cls;
        span.textContent = ` · ${text}`;
        note.appendChild(span);
      },
      // dead collapses the grid to its header: a killed node serves nothing.
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
      <div class="legend">each piece is painted in the colour of the node that sent it</div>
      <div class="grids" id="grids"></div>
      <div class="spread" id="spread"></div>
      <div class="banner" id="banner"></div>
      <div class="hashes" id="hashes"></div>`);
    app.grids.clear();
    app.colors = new Map(); // colours restart with each panel
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
      renderSpread(app, swarmLeechers);
    },
    done(app) {
      // No key press here: the runner goes straight on to scene 7 and
      // kills the seeder itself, the moment every piece is in the swarm.
      app.setHint("the seeder dies the moment every piece is somewhere in the swarm");
    },
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
          // The colours came from the live events; the /api/fetch answer
          // counts the same thing independently (pieces_from). Say whether
          // they agree.
          const from = msg.result && msg.result.pieces_from ? Object.entries(msg.result.pieces_from) : [];
          const agree = from.length > 0 && from.every(([addr, k]) => g.tally.get(app.resolve(addr)) === k);
          g.note(agree ? "✓ done · tally = /api/fetch" : `complete · /api/fetch says ${from.map(([a, k]) => `${app.resolve(a) || a}: ${k}`).join(" · ")}`, "ok");
        }
      } else if (msg.type === "sha256") {
        // The runner hashed the original and this copy on disk.
        const g = app.grids.get(msg.node);
        const text = msg.match ? "✓ SHA-256 = original" : `✗ SHA-256 ${String(msg.got).slice(0, 8)}… differs`;
        if (g) {
          g.addNote(text, `hash-line ${msg.match ? "ok" : "bad"}`);
        } else {
          const row = document.createElement("div");
          row.className = `hash-line ${msg.match ? "ok" : "bad"}`;
          row.textContent = `${msg.node} ${text}`;
          app.$("#hashes").appendChild(row);
        }
      }
    },
    onLive(app, n, ev) {
      if (ev.msg === "provider failed, trying the next one") {
        const g = app.grids.get(n.name);
        if (g) g.note(`its provider ${app.resolve(ev.attrs.provider) || ev.attrs.provider} died: trying the next one`, "fail");
      }
    },
  };

  // --- 8. give up / resume --------------------------------------------------

  const resume = {
    tallPanel: true,
    caption: "If the seeder dies too early, the download gives up after 15 s, but keeps serving its pieces. Later, the same fetch resumes.",
    enter(app) {
      app.ring.dim(true);
      swarmLeechers = [];
      swarmPanel(app, `give up, keep seeding, resume · ${app.config.serve_delay} per piece`);
      app.setBadgeMode(`real time · serve delay ${app.config.serve_delay}`);
      resume.timers = new Map();
    },
    onStatus(app, msg) {
      if (msg.type === "magnet" && !app.grids.has(msg.node)) {
        const g = makeGrid(app, app.$("#grids"), msg.node, msg.pieces, swarmLeechers.length ? "new seeder" : "seeder", true);
        app.ring.light(msg.node, true);
        if (swarmLeechers.length) g.note("joined just now, shares the same bytes (same key)", "file");
      } else if (msg.type === "fetch started" && !msg.again) {
        swarmLeechers.push(msg.node);
        makeGrid(app, app.$("#grids"), msg.node, msg.pieces, "leecher", false);
        app.ring.light(msg.node);
      } else if (msg.type === "fetch started" && msg.again) {
        app.grids.get(msg.node) && app.grids.get(msg.node).note("fetch again, into the same file…", "file");
      } else if (msg.type === "node up") {
        app.ring.light(msg.name);
      } else if (msg.type === "fetch done" && msg.ok) {
        const g = app.grids.get(msg.node);
        const had = msg.result ? msg.result.already_had : "?";
        if (g) {
          g.done();
          g.note(`complete · ${had} pieces were already on disk (resumed)`, "ok");
        }
      }
      if (msg.type === "killed" || msg.type === "sha256") kill.onStatus(app, msg); // banner, hash line
    },
    onLive(app, n, ev) {
      const g = app.grids.get(n.name);
      if (!g) return;
      if (ev.msg.startsWith("download stalled")) {
        // A real countdown from the event's own time: stallTimeout is 15 s.
        const since = Date.parse(ev.time);
        clearInterval(resume.timers.get(n.name));
        const tick = () => g.note(`stalled: nobody alive has the missing pieces · ${Math.min(15, Math.floor((Date.now() - since) / 1000))} s / 15 s`, "fail");
        tick();
        resume.timers.set(n.name, setInterval(tick, 250));
      } else if (ev.msg.startsWith("download gave up")) {
        clearInterval(resume.timers.get(n.name));
        resume.timers.delete(n.name);
        g.note(`gave up with ${ev.attrs.have}/${ev.attrs.of} · still serving them`, "file");
      } else if (ev.msg === "download resumed") {
        g.note(`resumed: ${ev.attrs.have}/${ev.attrs.of} on disk, re-hashed`, "file");
      } else if (ev.msg === "piece received" && resume.timers.has(n.name)) {
        // A piece arrived, so the node could progress and reset its stall
        // clock (download.go run(): it logs "download stalled" again if it
        // stalls again). Stop ours rather than show a timer it isn't running.
        clearInterval(resume.timers.get(n.name));
        resume.timers.delete(n.name);
        g.note(`got piece ${ev.attrs.index} from ${app.resolve(ev.attrs.from) || ev.attrs.from}`, "file");
      }
    },
  };

  // --- 9. outro: the finale ----------------------------------------------------

  // Every number here is real: from the runner's summary (counted from the
  // recorded events), or from the status messages this page received.
  // Nothing counts up through made-up intermediate values; each tile just
  // appears with its final number.
  const outro = {
    tallPanel: true,
    caption: "Every dot, arrow and number you saw came from these nodes' own log events and API answers.",
    enter(app) {
      app.ring.clearMarkers();
      app.ring.clearEdges();
      app.ring.dim(false);
      app.setPanel(`
        <div class="finale-head">No tracker.<br>No server.<br>Just peers.</div>
        <div class="finale-sub" id="finale-sub">the network these nodes built for themselves</div>
        <div class="tiles" id="tiles"></div>
        <div class="finale-foot" id="finale-foot"></div>`);
      outro.drawMesh(app);
    },

    // drawMesh asks every live node for its routing table (/api/table)
    // and draws each contact as a line: the real network, all at once.
    async drawMesh(app) {
      const live = [...app.nodes.values()].filter((n) => n.onRing && !n.dead);
      const pairs = new Set();
      for (const n of live) {
        let table;
        try {
          table = await Viz.getJSON(n.api, "/api/table");
        } catch (err) {
          continue;
        }
        for (const b of table.buckets) {
          for (const c of b.contacts) {
            const other = app.resolve(`${c.id}@`);
            const other_ = app.nodes.get(other);
            if (!other_ || other_.dead) continue;
            const key = [n.name, other].sort().join("|");
            if (pairs.has(key)) continue;
            pairs.add(key);
            const line = app.ring.edge(n.name, other, "mesh");
            if (line && window.gsap) gsap.fromTo(line, { opacity: 0 }, { opacity: 0.32, duration: 1.2, delay: pairs.size * 0.025 });
          }
        }
      }
      const sub = app.$("#finale-sub");
      if (sub) sub.textContent = `${live.length} live nodes · ${pairs.size} links, each one a contact some node learned on its own`;
      // A slow heartbeat across every node: the network is alive.
      if (window.gsap) {
        const halos = [...app.ring.nodes.values()].filter((n) => !n.g.classList.contains("dead")).map((n) => n.halo);
        halos.forEach((h) => h.setAttribute("class", "node-halo"));
        gsap.fromTo(halos, { attr: { r: 18 }, opacity: 0.7 }, { attr: { r: 34 }, opacity: 0, duration: 1.8, ease: "power2.out", stagger: { each: 0.08, repeat: -1, repeatDelay: 2.4 } });
      }
    },

    onStatus(app, msg) {
      if (msg.type !== "summary") return;
      const c = msg.counts || {};
      const verified = app.status.filter((m) => m.type === "sha256" && m.match).length;
      const killed = app.status.filter((m) => m.type === "killed").length;
      const tiles = [
        { n: msg.nodes, label: "nodes started, none in charge", cls: "dht" },
        { n: c["rpc sent"] || 0, label: "DHT messages", cls: "dht" },
        { n: c["piece received"] || 0, label: "pieces moved peer to peer", cls: "file" },
        { n: killed, label: "seeders killed mid-download", cls: "file" },
        { n: verified, label: "copies verified, SHA-256", cls: "ok" },
        { n: msg.events, label: "log events behind every frame", cls: "ink" },
      ];
      const host = app.$("#tiles");
      host.innerHTML = tiles
        .map((t) => `<div class="tile ${t.cls}"><div class="tile-n">${Number(t.n).toLocaleString("en-US")}</div><div class="tile-l">${esc(t.label)}</div></div>`)
        .join("");
      app.$("#finale-foot").textContent =
        `${msg.nodes} live processes on one laptop · k = ${msg.k}, α = ${msg.alpha} · DHT scenes replayed in their real order · ` +
        `pieces served at ${msg.serve_delay} each (--debug-serve-delay) so you could watch · seeds ${msg.seeds} · ${msg.date}`;
      if (window.gsap) {
        gsap.from(".finale-head", { opacity: 0, y: 30, duration: 0.9, ease: "power3.out" });
        gsap.from("#tiles .tile", { opacity: 0, y: 26, scale: 0.94, duration: 0.7, ease: "back.out(1.6)", stagger: 0.16, delay: 0.5 });
        gsap.from("#finale-foot", { opacity: 0, duration: 1, delay: 1.7 });
      }
    },
  };

  Viz.scenes = { join, buckets, lookup, store, share, swarm, kill, resume, outro };
  Viz.scenesUtil = { makeGrid, swarmSpread };
})(window.Viz);
