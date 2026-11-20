// One node's live connection: its snapshots and its /api/events stream
// (docs/NODE-API.md). Every later increment builds on this; a page with
// 20 nodes simply calls Viz.connectNode 20 times (decision 4: one
// EventSource per node, straight to the node).
"use strict";

window.Viz = window.Viz || {};

(function (Viz) {
  // getJSON fetches one snapshot endpoint, e.g. getJSON(api, "/api/node").
  async function getJSON(api, path) {
    const res = await fetch(`http://${api}${path}`, { cache: "no-store" });
    if (!res.ok) throw new Error(`${path}: HTTP ${res.status}`);
    return res.json();
  }

  // connectNode opens the event stream of the node whose API is `api`
  // ("host:port"). Callbacks:
  //   onOpen()        the stream is connected; events from now on arrive.
  //                   Streams replay nothing, so act only after this.
  //   onEvent(ev)     one event {seq, time, level, msg, attrs, dropped}.
  //   onClose(reason) the stream ended: the node died, or never answered.
  // Returns {api, close()}.
  function connectNode(api, { level = "debug", onOpen, onEvent, onClose }) {
    const src = new EventSource(`http://${api}/api/events?level=${level}`);
    let closed = false;

    src.onopen = () => onOpen && onOpen();

    // The node writes only `id:` and `data:` lines (no `event:` field),
    // so every event arrives here; dispatch on ev.msg. The 15 s
    // `: heartbeat` comments never reach JavaScript.
    src.onmessage = (m) => {
      let ev;
      try {
        ev = JSON.parse(m.data);
      } catch (err) {
        console.warn(`${api}: unparsable event`, m.data);
        return;
      }
      onEvent && onEvent(ev);
    };

    // EventSource reconnects by itself after an error. We don't want that:
    // a killed node must stay dead on screen, and a reconnected stream
    // would silently miss whatever happened in between. So close it.
    src.onerror = () => {
      if (closed) return;
      closed = true;
      src.close();
      onClose && onClose("stream closed");
    };

    return {
      api,
      close() {
        closed = true;
        src.close();
      },
    };
  }

  Viz.getJSON = getJSON;
  Viz.connectNode = connectNode;
})(window.Viz);
