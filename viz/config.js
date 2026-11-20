// Which nodes to watch (docs/11-visualizer.md, "Nodes come from a config
// list"). Never hard-coded to localhost: each entry is a name and the
// node's API address, so the same page can later watch nodes on other
// laptops just by getting a different list.
"use strict";

window.Viz = window.Viz || {};

(function (Viz) {
  // parseNodesParam reads "?nodes=viz0@127.0.0.1:8700,viz1@127.0.0.1:8701".
  // A bare "host:port" is fine too; its name is then the address.
  function parseNodesParam(text) {
    return text
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean)
      .map((s) => {
        const at = s.indexOf("@");
        return at < 0 ? { name: s, api: s } : { name: s.slice(0, at), api: s.slice(at + 1) };
      });
  }

  // loadConfig returns {nodes: [{name, api}], …}. It asks the runner's
  // /viz/config first (increment 5; a plain static server answers 404),
  // then falls back to the page URL's ?nodes= (or the older ?node=).
  async function loadConfig() {
    try {
      const res = await fetch("/viz/config", { cache: "no-store" });
      if (res.ok) return await res.json();
    } catch (err) {
      // no runner: fall through to the URL
    }
    const q = new URLSearchParams(location.search);
    const list = q.get("nodes") || q.get("node") || "127.0.0.1:8700";
    return { nodes: parseNodesParam(list) };
  }

  Viz.loadConfig = loadConfig;
  Viz.parseNodesParam = parseNodesParam;
})(window.Viz);
