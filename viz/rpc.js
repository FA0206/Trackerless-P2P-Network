// Turning a burst of real events from several nodes into replay steps.
// Pure: no DOM, no network. `resolve(contactText)` maps "id@ip:port" or
// "ip:port" to a node name (or "" if unknown).
//
// One RPC shows up in up to three events, on two streams:
//   requester: rpc sent      {type: FIND_NODE, to, rpc_id}
//   receiver:  rpc received  {type: FIND_NODE, from, rpc_id}
//   requester: rpc received  {type: FIND_NODE_RESP, from, rpc_id}   (the replier logs nothing)
// The rpc_id (8 hex) ties them together. A request seen only on the
// receiver's side (a newcomer whose stream isn't open yet) still becomes
// a comet; its reply, logged only by that newcomer, is never seen.
"use strict";

window.Viz = window.Viz || {};

(function (Viz) {
  const REQUESTS = new Set(["PING", "FIND_NODE", "FIND_VALUE", "STORE"]);
  const LABELS = new Set([
    "lookup converged",
    "find_value finished",
    "find_value answered from local store",
    "value stored",
    "store complete",
    "provider announced",
    "share complete",
  ]);

  function kindOf(ev) {
    const type = ev.attrs && ev.attrs.type;
    if (ev.msg === "rpc sent") return "launch";
    if (ev.msg === "rpc received") return REQUESTS.has(type) ? "launch" : "reply";
    if (ev.msg === "lookup round starting") return "round";
    if (LABELS.has(ev.msg)) return "label";
    return "noise"; // bucket updates, eviction pings…: ticker only
  }

  // buildSteps sorts the window's events by time and groups them into
  // steps: a set of requests leaving together, a set of replies coming
  // back together, a round marker, or a result label. Order is kept;
  // spacing is not (SIMPLIFICATION: the real duration is shown on the badge).
  // Each event is {node, ev}. Returns {steps, realNs, rpcs}.
  function buildSteps(items, resolve) {
    const { timeNs } = Viz.format;
    const sorted = items.slice().sort((a, b) => timeNs(a.ev.time) - timeNs(b.ev.time));
    const rpcs = new Map(); // rpc_id -> {id, type, from, to, launched}
    const steps = [];
    let cur = null;

    function newStep(kind, extra) {
      cur = { kind, comets: [], events: [], ...extra };
      steps.push(cur);
      return cur;
    }

    for (const it of sorted) {
      const { node, ev } = it;
      const kind = kindOf(ev);
      const a = ev.attrs || {};
      if (kind === "launch") {
        let rpc = rpcs.get(a.rpc_id);
        const fromName = ev.msg === "rpc sent" ? node : resolve(a.from);
        const toName = ev.msg === "rpc sent" ? resolve(a.to) : node;
        if (!rpc) {
          rpc = { id: a.rpc_id, type: a.type, from: fromName, to: toName, launched: false };
          rpcs.set(a.rpc_id, rpc);
        }
        rpc.from = rpc.from || fromName;
        rpc.to = rpc.to || toName;
        if (!rpc.launched) {
          rpc.launched = true;
          // Requests of one round leave together: same requester, same type.
          if (!cur || cur.kind !== "launch" || cur.type !== rpc.type || cur.from !== rpc.from) {
            newStep("launch", { type: rpc.type, from: rpc.from });
          }
          cur.comets.push(rpc);
        }
        cur.events.push(it);
      } else if (kind === "reply") {
        const rpc = rpcs.get(a.rpc_id) || { id: a.rpc_id, type: a.type, from: node, to: resolve(a.from) };
        if (!cur || cur.kind !== "reply" || cur.to !== node) newStep("reply", { to: node });
        cur.comets.push({ ...rpc, replyType: a.type });
        cur.events.push(it);
      } else if (kind === "round") {
        newStep("round", { round: a.round, batch: a.batch, node }).events.push(it);
      } else if (kind === "label") {
        newStep("label", { msg: ev.msg, node, attrs: a }).events.push(it);
      } else {
        if (!cur) newStep("noise");
        cur.events.push(it);
      }
    }
    const realNs = sorted.length > 1 ? timeNs(sorted[sorted.length - 1].ev.time) - timeNs(sorted[0].ev.time) : 0;
    return { steps, realNs, rpcs };
  }

  Viz.rpc = { buildSteps, kindOf };
})(window.Viz);
