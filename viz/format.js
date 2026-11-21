// Turning node events into short, readable text.
// Pure functions: no network, no DOM. Classic scripts sharing one
// `window.Viz` namespace, not ES modules: no build step, nothing to bundle.
"use strict";

window.Viz = window.Viz || {};

(function (Viz) {
  // IDs and keys are 40 hex characters; on screen they are 8 (the same
  // short form the node's own log uses for `node=`).
  const HEX40 = /\b[0-9a-f]{40}\b/g;

  function shortHex(text) {
    return String(text).replace(HEX40, (h) => h.slice(0, 8));
  }

  // Which colour family an event belongs to. Red is for failure only,
  // amber for file payload, cyan for DHT control traffic.
  const FILE_WORDS = ["piece", "download", "provider", "tcp ", "share", "manifest"];

  function category(ev) {
    if (ev.level === "WARN" || ev.level === "ERROR") return "fail";
    const m = ev.msg || "";
    if (FILE_WORDS.some((w) => m.includes(w))) return "file";
    return "dht";
  }

  // The attributes that say most go first; the rest keep their order.
  const FIRST = ["type", "to", "from", "round", "batch", "rounds", "rpcs_sent", "index", "of", "have", "replicas", "provider", "key"];

  // "type=FIND_NODE to=8d9525b0@127.0.0.1:9308 rpc_id=3c7afb69"
  function attrsLine(attrs) {
    if (!attrs) return "";
    const rank = (k) => (FIRST.includes(k) ? FIRST.indexOf(k) : FIRST.length);
    return Object.entries(attrs)
      .sort((a, b) => rank(a[0]) - rank(b[0]))
      .map(([k, v]) => `${k}=${shortHex(typeof v === "string" ? v : JSON.stringify(v))}`)
      .join("  ");
  }

  // "18:00:43.884" from RFC 3339 with nanoseconds, in the node's own
  // offset (the string already carries it; no Date parsing, which would
  // drop everything past milliseconds).
  function clock(time) {
    const m = /T(\d\d:\d\d:\d\d)(\.\d{1,3})?/.exec(time || "");
    return m ? m[1] + (m[2] || ".000").padEnd(4, "0") : "";
  }

  // timeNs turns the event's RFC 3339 time into nanoseconds since the
  // start of its day, for ordering events from different streams. Go
  // trims trailing zeros from the fraction, so the strings themselves
  // don't sort. A run crossing midnight would misorder; demos don't.
  function timeNs(time) {
    const m = /T(\d\d):(\d\d):(\d\d)(?:\.(\d+))?/.exec(time || "");
    if (!m) return 0;
    const frac = Number((m[4] || "").padEnd(9, "0").slice(0, 9));
    return ((Number(m[1]) * 60 + Number(m[2])) * 60 + Number(m[3])) * 1e9 + frac;
  }

  // contact splits "id@ip:port" (the `from` / `to` of rpc events) into
  // {id, addr}. A plain "ip:port" (piece events' `from`) has no id.
  function contact(text) {
    const s = String(text || "");
    const at = s.indexOf("@");
    return at < 0 ? { id: "", addr: s } : { id: s.slice(0, at), addr: s.slice(at + 1) };
  }

  // duration formats nanoseconds for the replay badge: "3.1 ms", "420 µs".
  function duration(ns) {
    if (ns >= 1e9) return `${(ns / 1e9).toFixed(2)} s`;
    if (ns >= 1e6) return `${(ns / 1e6).toFixed(1)} ms`;
    return `${Math.round(ns / 1e3)} µs`;
  }

  Viz.format = { shortHex, category, attrsLine, clock, timeNs, contact, duration };
})(window.Viz);
