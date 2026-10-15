// stenella — client logic, deliberately free of the DOM.
//
// Everything here is a pure function: the router, the query builder, the
// aggregation the dashboard charts read, the map projection and clustering, and
// the tree builder. app.js does the DOM; lib.js decides. Keeping the two apart
// means the decisions can be tested without a browser (see web/tests/).
//
// It is a plain script (no modules, no bundler) so it works from a <script> tag
// and from Node's require() in the tests.
(function (root, factory) {
  const api = factory();
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  root.Stenella = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  // --- text ----------------------------------------------------------------

  const ESC = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };

  // escapeHTML is for the few places that build text rather than DOM nodes
  // (an exported outline). Rendered text goes through textContent instead.
  function escapeHTML(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ESC[c]);
  }

  function formatCount(n) {
    const v = Number(n) || 0;
    if (v >= 1e6) return (v / 1e6).toFixed(1).replace(/\.0$/, "") + "M";
    if (v >= 1e3) return (v / 1e3).toFixed(1).replace(/\.0$/, "") + "k";
    return String(v);
  }

  // timeAgo renders "just now", "12m ago", "3h ago", "5d ago" or a date.
  function timeAgo(iso, now) {
    if (!iso) return "—";
    const t = Date.parse(iso);
    if (isNaN(t)) return "—";
    const base = now ? (now instanceof Date ? now.getTime() : Date.parse(now)) : Date.now();
    let s = Math.floor((base - t) / 1000);
    if (isNaN(s)) return "—";
    if (s < 0) s = 0;
    if (s < 45) return "just now";
    if (s < 3600) return Math.max(1, Math.round(s / 60)) + "m ago";
    if (s < 86400) return Math.round(s / 3600) + "h ago";
    if (s < 30 * 86400) return Math.round(s / 86400) + "d ago";
    return new Date(t).toISOString().slice(0, 10);
  }

  function niceDate(iso) {
    if (!iso) return "—";
    const t = Date.parse(iso);
    if (isNaN(t)) return "—";
    return new Date(t).toISOString().replace("T", " ").slice(0, 16) + "Z";
  }

  // --- the router ----------------------------------------------------------

  // routeOf turns "#/feed?collection=news" into {view, params}. An empty hash
  // is the dashboard, so the app has a home without a redirect.
  function routeOf(hash) {
    const raw = String(hash == null ? "" : hash).replace(/^#\/?/, "");
    const cut = raw.indexOf("?");
    const path = cut < 0 ? raw : raw.slice(0, cut);
    const qs = cut < 0 ? "" : raw.slice(cut + 1);
    const params = {};
    if (qs) {
      for (const pair of qs.split("&")) {
        if (!pair) continue;
        const eq = pair.indexOf("=");
        const k = decodeURIComponent(eq < 0 ? pair : pair.slice(0, eq));
        const v = eq < 0 ? "" : decodeURIComponent(pair.slice(eq + 1).replace(/\+/g, " "));
        params[k] = v;
      }
    }
    const view = (path.split("/")[0] || "dashboard").trim() || "dashboard";
    return { view, params };
  }

  function hrefFor(view, params) {
    const qs = buildQuery(params || {});
    return "#/" + view + (qs ? "?" + qs : "");
  }

  function buildQuery(params) {
    const parts = [];
    for (const k of Object.keys(params || {}).sort()) {
      const v = params[k];
      if (v === undefined || v === null || v === "") continue;
      parts.push(encodeURIComponent(k) + "=" + encodeURIComponent(String(v)));
    }
    return parts.join("&");
  }

  // --- the server query language -------------------------------------------

  function quoteValue(v) {
    const s = String(v == null ? "" : v).trim();
    if (s === "") return "";
    return /[\s"]/.test(s) ? '"' + s.replace(/"/g, '\\"') + '"' : s;
  }

  // queryFromFilters turns the dashboard's filter bar into one metadata query,
  // the same language the server parses (tag:/source:/from:/to: + free text).
  // The collection filter travels as its own parameter, so it is not here.
  function queryFromFilters(f) {
    const parts = [];
    if (f && f.category) parts.push("tag:" + quoteValue(f.category));
    if (f && f.source) parts.push("source:" + quoteValue(f.source));
    if (f && f.from) parts.push("from:" + quoteValue(f.from));
    if (f && f.to) parts.push("to:" + quoteValue(f.to));
    if (f && f.text) parts.push(String(f.text).trim());
    return parts.filter(Boolean).join(" ");
  }

  // --- aggregation for the dashboard ---------------------------------------

  function dayOf(item) {
    const stamp = item.published || item.fetched || "";
    return String(stamp).slice(0, 10);
  }

  // aggregate is what the three charts read: items per source (bar), items per
  // day (line) and items per category (pie).
  function aggregate(items) {
    const bySource = {};
    const byCategory = {};
    const byDay = {};
    for (const it of items || []) {
      const src = it.source || "(unknown)";
      bySource[src] = (bySource[src] || 0) + 1;
      for (const c of it.categories || []) {
        const k = String(c || "").toLowerCase();
        if (k) byCategory[k] = (byCategory[k] || 0) + 1;
      }
      const d = dayOf(it);
      if (d) byDay[d] = (byDay[d] || 0) + 1;
    }
    return { bySource, byCategory, byDay };
  }

  function distinctSources(items) {
    const seen = new Set();
    for (const it of items || []) if (it.source) seen.add(it.source);
    return [...seen].sort();
  }

  function newestItem(items) {
    let best = null;
    for (const it of items || []) {
      const t = Date.parse(it.published || it.fetched || "");
      if (isNaN(t)) continue;
      if (!best || t > best.t) best = { t, item: it };
    }
    return best ? best.item : null;
  }

  // topEntries turns a {name: count} map into [[name, count]] sorted by count
  // (then name), which is what a chart axis wants.
  function topEntries(map, limit) {
    const out = Object.keys(map || {}).map((k) => [k, map[k]]);
    out.sort((a, b) => (b[1] - a[1]) || a[0].localeCompare(b[0]));
    return limit && limit > 0 ? out.slice(0, limit) : out;
  }

  // seriesOfDay fills the gaps so the line chart has an even x axis.
  function seriesOfDay(byDay) {
    const days = Object.keys(byDay || {}).sort();
    if (days.length === 0) return [];
    const out = [];
    const start = Date.parse(days[0] + "T00:00:00Z");
    const end = Date.parse(days[days.length - 1] + "T00:00:00Z");
    for (let t = start; t <= end; t += 86400000) {
      const d = new Date(t).toISOString().slice(0, 10);
      out.push([d, byDay[d] || 0]);
    }
    return out;
  }

  // --- the map -------------------------------------------------------------

  // project is the equirectangular projection onto a w×h canvas.
  function project(lat, lon, w, h) {
    return { x: ((Number(lon) + 180) / 360) * w, y: ((90 - Number(lat)) / 180) * h };
  }

  function geoPoints(items, w, h) {
    const out = [];
    for (const it of items || []) {
      if (!it.hasGeo) continue;
      const lat = Number(it.lat);
      const lon = Number(it.lon);
      if (isNaN(lat) || isNaN(lon)) continue;
      const p = project(lat, lon, w, h);
      out.push({ item: it, x: p.x, y: p.y });
    }
    return out;
  }

  // cluster groups points into grid cells of `radius` pixels and returns the
  // centroid of each cell. It is enough to keep a dense map readable and it is
  // deterministic, so it can be tested.
  function cluster(points, radius) {
    const r = radius > 0 ? radius : 1;
    const cells = new Map();
    const out = [];
    for (const p of points || []) {
      const key = Math.floor(p.x / r) + "," + Math.floor(p.y / r);
      let c = cells.get(key);
      if (!c) {
        c = { x: 0, y: 0, items: [] };
        cells.set(key, c);
        out.push(c);
      }
      c.items.push(p.item);
      c.x += p.x;
      c.y += p.y;
    }
    for (const c of out) {
      c.x /= c.items.length;
      c.y /= c.items.length;
      c.count = c.items.length;
    }
    return out;
  }

  // --- the information tree ------------------------------------------------

  function treeNode(label, kind, children) {
    return { label: label, kind: kind, children: children || [] };
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
      return treeNode(g.name, "group", tags);
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
            children.push(treeNode((l.from === it.id ? l.to : l.from) + " (" + l.relation + ")", "link"));
          }
        }
        return treeNode(it.title, "item", children);
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

  // --- the insights inbox --------------------------------------------------

  const SEVERITY = { critical: 4, high: 3, medium: 2, low: 1, info: 0 };

  function severityRank(s) {
    const k = String(s || "").toLowerCase();
    return Object.prototype.hasOwnProperty.call(SEVERITY, k) ? SEVERITY[k] : 0;
  }

  // triage sorts an inbox: most severe first, then the most recent.
  function triage(insights) {
    return (insights || []).slice().sort((a, b) => {
      const d = severityRank(b.severity) - severityRank(a.severity);
      if (d !== 0) return d;
      return Date.parse(b.updated || b.created || 0) - Date.parse(a.updated || a.created || 0);
    });
  }

  // --- graph helpers used by the tree and the timeline --------------------

  function indexByID(items) {
    const m = new Map();
    for (const it of items || []) m.set(it.id, it);
    return m;
  }

  // annotations counts, per item, the analyst's own marks so the timeline can
  // show them inline.
  function annotations(items, links, comments, notes) {
    const out = new Map();
    const bump = (id, key) => {
      if (!id) return;
      const cur = out.get(id) || { links: 0, comments: 0, notes: 0 };
      cur[key]++;
      out.set(id, cur);
    };
    for (const l of links || []) {
      bump(l.from, "links");
      bump(l.to, "links");
    }
    for (const c of comments || []) bump(c.item, "comments");
    for (const n of notes || []) bump(n.subject, "notes");
    for (const it of items || []) {
      if (!out.has(it.id)) out.set(it.id, { links: 0, comments: 0, notes: 0 });
    }
    return out;
  }

  return {
    escapeHTML: escapeHTML,
    formatCount: formatCount,
    timeAgo: timeAgo,
    niceDate: niceDate,
    routeOf: routeOf,
    hrefFor: hrefFor,
    buildQuery: buildQuery,
    quoteValue: quoteValue,
    queryFromFilters: queryFromFilters,
    dayOf: dayOf,
    aggregate: aggregate,
    distinctSources: distinctSources,
    newestItem: newestItem,
    topEntries: topEntries,
    seriesOfDay: seriesOfDay,
    project: project,
    geoPoints: geoPoints,
    cluster: cluster,
    treeNode: treeNode,
    treeOf: treeOf,
    outlineOf: outlineOf,
    severityRank: severityRank,
    triage: triage,
    indexByID: indexByID,
    annotations: annotations,
  };
});
