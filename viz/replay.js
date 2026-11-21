// The replay buffer. The runner
// brackets each fast action with "window open" / "window closed"; every
// node event that arrives in between is buffered, and when the window
// closes it is replayed: in its real order, at a pace people can follow,
// with its real duration on the badge. Windows queue up, so a burst of
// joins plays one after another.
//
// SIMPLIFICATION: the replay keeps the events' order, not their spacing
// (a lookup's 4 ms become a few seconds, each step the same length).
// What the DHT scenes teach is order; the real duration is on the badge.
"use strict";

window.Viz = window.Viz || {};

(function (Viz) {
  // Seconds per step. "fast" windows (joins 4..19) get a quick replay.
  const PACE = {
    slow: { launch: 0.95, reply: 0.75, round: 0.7, label: 1.0, noise: 0 },
    fast: { launch: 0.38, reply: 0.3, round: 0.15, label: 0.25, noise: 0 },
  };

  const sleep = (s) => new Promise((res) => setTimeout(res, s * 1000));

  // createReplay returns {open, add, close, idle, recording}. hooks:
  //   start(win)            before a window's steps play
  //   step(step, win)       each step, before its comets fly
  //   end(win)              after the last step
  //   ticker(items, win)    the events of a step, for the log ticker
  //   badge(win | null)     a window started playing / the queue emptied
  function createReplay(ring, resolve, hooks) {
    let buffer = null; // events of the open window
    const queue = [];
    let playing = false;
    let idleWaiters = [];

    function open(win) {
      buffer = { ...win, items: [] };
    }

    // add buffers an event if a window is open; returns whether it did.
    function add(node, ev) {
      if (!buffer) return false;
      buffer.items.push({ node, ev });
      return true;
    }

    // close ends the open window and queues it; `ready` (optional) is a
    // promise of extra fields (snapshots) the replay waits for.
    function close(fields, ready) {
      if (!buffer) return;
      const win = { ...buffer, ...fields, ready };
      buffer = null;
      queue.push(win);
      if (!playing) play();
    }

    async function play() {
      playing = true;
      while (queue.length) {
        const win = queue.shift();
        if (win.ready) Object.assign(win, await win.ready);
        const { steps, realNs } = Viz.rpc.buildSteps(win.items, resolve);
        win.realNs = realNs;
        win.steps = steps;
        const pace = PACE[win.pace] || PACE.slow;
        hooks.badge && hooks.badge(win);
        hooks.start && (await hooks.start(win));
        for (const step of steps) {
          hooks.step && hooks.step(step, win);
          hooks.ticker && hooks.ticker(step.events, win);
          const d = pace[step.kind] || 0;
          if (step.kind === "launch") {
            await Promise.all(step.comets.map((c) => ring.comet(c.from, c.to, "dht", d * 0.85)));
            await sleep(d * 0.15);
          } else if (step.kind === "reply") {
            // A reply travels back along its request's arc: replier → requester.
            await Promise.all(step.comets.map((c) => ring.comet(c.to, c.from, "reply", d * 0.85)));
            await sleep(d * 0.15);
          } else if (d > 0) {
            await sleep(d);
          }
        }
        hooks.end && (await hooks.end(win));
      }
      playing = false;
      hooks.badge && hooks.badge(null);
      const w = idleWaiters;
      idleWaiters = [];
      w.forEach((fn) => fn());
    }

    // idle resolves once nothing is buffered, queued or playing.
    function idle() {
      if (!playing && !queue.length && !buffer) return Promise.resolve();
      return new Promise((res) => idleWaiters.push(res));
    }

    return {
      open,
      add,
      close,
      idle,
      get recording() {
        return buffer !== null;
      },
      get busy() {
        return playing || queue.length > 0 || buffer !== null;
      },
    };
  }

  Viz.createReplay = createReplay;
})(window.Viz);
