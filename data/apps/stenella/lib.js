// stenella — the client logic that is *stenella's*, deliberately free of the DOM.
//
// The generic half of a view (escaping, stable ids, relative time, the hash
// router and its canonical record link, dashboard aggregation, map projection
// and clustering, severity triage, reference counts) is shared with every other
// song/atp application and now lives in song:
//
//   web/index.html loads /_song/viewkit.js first, and this file re-exports it
//   under the Stenella namespace. Node tests require the same file directly,
//   from atp/song/pkg/viewkit/viewkit.js, so there is one implementation.
//
// What stays here is the part that is stenella's information model: the query
// language the server parses, and the tree built from sources → groups → items
// → entities → links/insights.
(function (root, factory) {
  const api = factory();
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  root.Stenella = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  const V = typeof module !== "undefined" && module.exports
    ? require("../atp/song/pkg/viewkit/viewkit.js")
    : root.SongView;

  if (!V) {
    throw new Error("stenella: /_song/viewkit.js must be loaded before lib.js");
  }

  // --- the server query language -------------------------------------------

  // queryFromFilters turns the dashboard's filter bar into one metadata query,
  // the same language the server parses (tag:/source:/from:/to: + free text).
  // The collection filter travels as its own parameter, so it is not here.
  function queryFromFilters(f) {
    const parts = [];
    if (f && f.category) parts.push("tag:" + V.quoteValue(f.category));
    if (f && f.source) parts.push("source:" + V.quoteValue(f.source));
    if (f && f.from) parts.push("from:" + V.quoteValue(f.from));
    if (f && f.to) parts.push("to:" + V.quoteValue(f.to));
    if (f && f.text) parts.push(String(f.text).trim());
    return parts.filter(Boolean).join(" ");
  }

  // --- the information tree ------------------------------------------------

  function treeNode(label, kind, children, id) {
    return { label: label, kind: kind, children: children || [], id: id || "" };
  }

  // treeOf builds sources → groups → items → entities → links/insights from the
  // endpoints the views already fetch. Groups (saved queries and watchlists)
  // come first because they are what the analyst is tracking.
  function treeOf(data) {
    data = data || {};
    const items = data.items || [];
    const links = data.links || [];
    const insights = data.insights || [];

    const groups = (data.groups || []).map((g) => {
      const tags = (g.tags || []).map((t) => treeNode(t, "tag"));
      return treeNode(g.name, "group", tags, g.id);
    });

    const bySource = new Map();
    for (const it of items) {
      const key = it.source || "(unknown)";
      if (!bySource.has(key)) bySource.set(key, []);
      bySource.get(key).push(it);
    }

    const sources = [];
    for (const [name, list] of bySource) {
      const nodes = list.map((it) => {
        const children = (it.entities || []).map((e) =>
          treeNode(e.name || e.id, "entity")
        );
        for (const inx of insights) {
          if ((inx.items || []).indexOf(it.id) >= 0) children.push(treeNode(inx.title, "insight"));
        }
        for (const l of links) {
          if (l.from === it.id || l.to === it.id) {
            const other = V.referenceEnds(l, it.id).other;
            children.push(treeNode(other + " (" + (l.relation || "related") + ")", "link", [], other));
          }
        }
        return treeNode(it.title, "item", children, it.id);
      });
      sources.push(treeNode(name, "source", nodes));
    }
    sources.sort((a, b) => a.label.localeCompare(b.label));
    return groups.concat(sources);
  }

  // outlineOf renders a tree as a citation-ready indented outline.
  function outlineOf(nodes, depth) {
    const d = depth || 0;
    let out = "";
    for (const n of nodes || []) {
      out += "  ".repeat(d) + "- " + n.label + "\n";
      if (n.children && n.children.length) out += outlineOf(n.children, d + 1);
    }
    return out;
  }

  return Object.assign({}, V, {
    queryFromFilters: queryFromFilters,
    treeNode: treeNode,
    treeOf: treeOf,
    outlineOf: outlineOf,
  });
});
