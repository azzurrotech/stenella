// stenella — the views.
//
// One baseline script: no framework, no bundler. It owns the DOM; every
// decision it makes comes from lib.js (window.Stenella), which the tests cover
// without a browser. Data comes from the platform's JSON endpoints.
(function () {
  "use strict";

  const S = window.Stenella;
  const W = window.StenellaWorld || { land: [], graticule: { lat: [], lon: [] } };

  // --- tiny DOM helpers ----------------------------------------------------

  function $(id) { return document.getElementById(id); }

  function el(tag, attrs, ...kids) {
    const n = document.createElement(tag);
    for (const k of Object.keys(attrs || {})) {
      const v = attrs[k];
      if (v == null || v === false) continue;
      if (k === "class") n.className = v;
      else if (k === "text") n.textContent = v;
      else if (k === "html") n.innerHTML = v; // only ever fed by escapeHTML()
      else if (k.startsWith("on") && typeof v === "function") n.addEventListener(k.slice(2), v);
      else if (k === "dataset") for (const d of Object.keys(v)) n.dataset[d] = v[d];
      else n.setAttribute(k, v);
    }
    for (const kid of kids.flat()) {
      if (kid == null || kid === false) continue;
      n.appendChild(typeof kid === "string" ? document.createTextNode(kid) : kid);
    }
    return n;
  }

  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

  function badge(text, cls) { return el("span", { class: "badge " + (cls || ""), text: text }); }

  // --- the API -------------------------------------------------------------

  let offline = false;

  async function api(path, opts) {
    const res = await fetch(path, Object.assign({ headers: { Accept: "application/json" } }, opts || {}));
    if (!res.ok) {
      let detail = res.status + " " + res.statusText;
      try {
        const body = await res.json();
        if (body && body.error) detail = body.error;
      } catch (e) { /* not JSON */ }
      throw new Error(path + " → " + detail);
    }
    offline = false;
    if (res.status === 204) return null;
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  }

  function post(path, body) {
    return api(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    });
  }

  function showError(target, err) {
    clear(target);
    target.appendChild(el("p", { class: "err", text: String(err && err.message ? err.message : err) }));
  }

  // The server returns "notes" for operators it understands but cannot answer
  // (author:, host:). Show them instead of pretending the query was complete.
  function showNotes(list) {
    const n = $("notes");
    if (!list || list.length === 0) { n.hidden = true; n.textContent = ""; return; }
    n.hidden = false;
    n.textContent = "notes: " + list.join(" · ");
  }

  // --- charts (canvas 2D, no library) --------------------------------------

  const PALETTE = ["#0a84ff", "#12b76a", "#f79009", "#f04438", "#8b5cf6", "#14b8a6", "#eab308", "#ec4899"];

  function fitCanvas(cv) {
    const ctx = cv.getContext("2d");
    const w = cv.width, h = cv.height;
    ctx.clearRect(0, 0, w, h);
    ctx.font = "11px system-ui, sans-serif";
    ctx.textBaseline = "middle";
    return { ctx, w, h };
  }

  function axis(ctx, w, h, pad) {
    ctx.strokeStyle = "rgba(127,127,127,.35)";
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(pad.l, pad.t);
    ctx.lineTo(pad.l, h - pad.b);
    ctx.lineTo(w - pad.r, h - pad.b);
    ctx.stroke();
    ctx.fillStyle = "rgba(127,127,127,.9)";
  }

  // barChart redraws in place: the canvas element is reused, never replaced.
  function barChart(cv, entries) {
    const { ctx, w, h } = fitCanvas(cv);
    const pad = { l: 34, r: 10, t: 12, b: 46 };
    axis(ctx, w, h, pad);
    if (!entries.length) { ctx.fillText("no data", pad.l + 6, h / 2); return; }
    const max = Math.max(...entries.map((e) => e[1]), 1);
    const bw = (w - pad.l - pad.r) / entries.length;
    entries.forEach((e, i) => {
      const bh = ((h - pad.t - pad.b) * e[1]) / max;
      const x = pad.l + i * bw + bw * 0.15;
      const y = h - pad.b - bh;
      ctx.fillStyle = PALETTE[i % PALETTE.length];
      ctx.fillRect(x, y, bw * 0.7, bh);
      ctx.fillStyle = "rgba(127,127,127,.9)";
      const label = e[0].length > 9 ? e[0].slice(0, 8) + "…" : e[0];
      ctx.save();
      ctx.translate(x + bw * 0.35, h - pad.b + 6);
      ctx.rotate(-Math.PI / 4);
      ctx.fillText(label, 0, 0);
      ctx.restore();
      if (e[1] > 0) {
        ctx.fillStyle = "rgba(127,127,127,.9)";
        ctx.fillText(String(e[1]), x + bw * 0.35 - 3, y - 6);
      }
    });
  }

  function lineChart(cv, series) {
    const { ctx, w, h } = fitCanvas(cv);
    const pad = { l: 34, r: 10, t: 12, b: 26 };
    axis(ctx, w, h, pad);
    if (series.length === 0) { ctx.fillText("no data", pad.l + 6, h / 2); return; }
    const max = Math.max(...series.map((p) => p[1]), 1);
    const stepX = series.length > 1 ? (w - pad.l - pad.r) / (series.length - 1) : 0;
    const yOf = (v) => h - pad.b - ((h - pad.t - pad.b) * v) / max;
    ctx.strokeStyle = PALETTE[0];
    ctx.lineWidth = 2;
    ctx.beginPath();
    series.forEach((p, i) => {
      const x = pad.l + i * stepX;
      if (i === 0) ctx.moveTo(x, yOf(p[1])); else ctx.lineTo(x, yOf(p[1]));
    });
    ctx.stroke();
    ctx.fillStyle = PALETTE[0];
    series.forEach((p, i) => {
      const x = pad.l + i * stepX;
      ctx.beginPath();
      ctx.arc(x, yOf(p[1]), 2.5, 0, Math.PI * 2);
      ctx.fill();
    });
    ctx.fillStyle = "rgba(127,127,127,.9)";
    ctx.fillText(series[0][0], pad.l, h - pad.b + 10);
    const last = series[series.length - 1][0];
    ctx.fillText(last, w - pad.r - ctx.measureText(last).width, h - pad.b + 10);
  }

  function pieChart(cv, entries) {
    const { ctx, w, h } = fitCanvas(cv);
    if (!entries.length) { ctx.fillText("no data", 40, h / 2); return; }
    const total = entries.reduce((a, e) => a + e[1], 0) || 1;
    const cx = h / 2 + 6, cy = h / 2, r = Math.min(h, w) / 2 - 14;
    let a0 = -Math.PI / 2;
    entries.forEach((e, i) => {
      const a1 = a0 + (Math.PI * 2 * e[1]) / total;
      ctx.beginPath();
      ctx.moveTo(cx, cy);
      ctx.arc(cx, cy, r, a0, a1);
      ctx.closePath();
      ctx.fillStyle = PALETTE[i % PALETTE.length];
      ctx.fill();
      a0 = a1;
    });
    ctx.fillStyle = "rgba(127,127,127,.9)";
    let y = 14;
    entries.slice(0, 10).forEach((e, i) => {
      ctx.fillStyle = PALETTE[i % PALETTE.length];
      ctx.fillRect(cx + r + 8, y - 5, 9, 9);
      ctx.fillStyle = "rgba(127,127,127,.95)";
      const label = e[0].length > 14 ? e[0].slice(0, 13) + "…" : e[0];
      ctx.fillText(label + " (" + e[1] + ")", cx + r + 22, y);
      y += 14;
    });
  }

  // --- dashboard -----------------------------------------------------------

  const dashboardState = { items: [], stats: null, filters: {} };
  let chartsDrawn = false;

  function currentFilters() {
    return {
      collection: $("f-collection").value,
      source: $("f-source").value,
      category: $("f-category").value.trim(),
      from: $("f-from").value,
      to: $("f-to").value,
      text: $("f-text").value.trim(),
    };
  }

  function queryOf(f) {
    // The source filter is its own parameter when it comes from the dropdown;
    // a category/date/text filter travels in the metadata query.
    return S.queryFromFilters({ category: f.category, from: f.from, to: f.to, text: f.text });
  }

  function renderKpis() {
    const items = dashboardState.items;
    const stats = dashboardState.stats || {};
    const newest = S.newestItem(items);
    const kpis = [
      ["Items", S.formatCount(items.length)],
      ["Distinct sources", S.formatCount(S.distinctSources(items).length)],
      ["Open insights", S.formatCount(stats.openInsights || 0)],
      ["Since newest item", newest ? S.timeAgo(newest.published || newest.fetched) : "—"],
    ];
    const box = $("kpis");
    clear(box);
    for (const [label, value] of kpis) {
      box.appendChild(el("div", { class: "kpi" },
        el("span", { class: "kpi-value", text: value }),
        el("span", { class: "kpi-label", text: label })));
    }
  }

  function redrawCharts() {
    const agg = S.aggregate(dashboardState.items);
    barChart($("chart-source"), S.topEntries(agg.bySource, 8));
    lineChart($("chart-time"), S.seriesOfDay(agg.byDay));
    pieChart($("chart-category"), S.topEntries(agg.byCategory, 8));
    chartsDrawn = true;
  }

  async function loadDashboard() {
    const f = currentFilters();
    dashboardState.filters = f;
    const qs = S.buildQuery({ collection: f.collection, source: f.source, q: queryOf(f), limit: 500 });
    try {
      const [stats, items] = await Promise.all([
        api("/stenella/api/stats"),
        api("/stenella/api/items?" + qs),
      ]);
      dashboardState.stats = stats;
      dashboardState.items = items.items || [];
      showNotes(items.notes);
    } catch (err) {
      showError($("kpis"), err);
      return;
    }
    renderKpis();
    redrawCharts(); // updates the existing canvases in place
  }

  function fillSelect(sel, values, keep) {
    const current = keep !== undefined ? keep : sel.value;
    clear(sel);
    for (const v of values) sel.appendChild(el("option", { value: v, text: v || "all" }));
    if (values.indexOf(current) >= 0) sel.value = current;
  }

  async function loadFilterOptions() {
    try {
      const [colls, sources] = await Promise.all([
        api("/stenella/api/collections"),
        api("/stenella/api/sources"),
      ]);
      const names = (colls.collections || []).map((c) => (typeof c === "string" ? c : c.name || c.id));
      const srcNames = (sources.sources || []).map((s) => s.name);
      fillSelect($("f-collection"), [""].concat(names));
      fillSelect($("feed-collection"), [""].concat(names));
      fillSelect($("brief-collection"), [""].concat(names));
      fillSelect($("f-source"), [""].concat(srcNames));
    } catch (err) { /* the dashboard still works with "all" */ }
  }

  // --- the timeline --------------------------------------------------------

  function itemBadges(it) {
    const out = [el("span", { class: "badge source", text: it.source || "(unknown)" })];
    if (it.collection && it.collection !== "all") out.push(badge(it.collection));
    if (it.acl && it.acl !== "public") out.push(badge(it.acl, "acl-" + it.acl));
    for (const c of it.categories || []) out.push(badge(c));
    for (const e of it.entities || []) out.push(badge(e.kind + ":" + e.name));
    for (const a of it.attributions || []) out.push(badge("also: " + a));
    if (it.hasGeo) out.push(badge("geo"));
    return out;
  }

  function itemActions(it, rerender) {
    const acts = el("div", { class: "acts" });
    acts.appendChild(el("button", { type: "button", text: "Comment", onClick: async () => {
      const body = window.prompt("Comment on this item:");
      if (!body) return;
      try { await post("/stenella/api/comments", { item: it.id, body, author: "analyst" }); rerender(); }
      catch (e) { window.alert(e.message); }
    } }));
    acts.appendChild(el("button", { type: "button", text: "Note", onClick: async () => {
      const title = window.prompt("Note title:");
      if (!title) return;
      const body = window.prompt("Note body:") || "";
      try { await post("/stenella/api/notes", { subject: it.id, title, body }); rerender(); }
      catch (e) { window.alert(e.message); }
    } }));
    acts.appendChild(el("button", { type: "button", text: "Link", onClick: async () => {
      const to = window.prompt("Link to which item id?");
      if (!to) return;
      const relation = window.prompt("Relation (supports | contradicts | duplicates | references | related):", "related") || "related";
      try { await post("/stenella/api/links", { from: it.id, to, relation }); rerender(); }
      catch (e) { window.alert(e.message); }
    } }));
    acts.appendChild(el("button", { type: "button", text: "Track", title: "Save a group that tracks this item's source", onClick: async () => {
      const name = window.prompt("Group name:", (it.source || "source") + " watch");
      if (!name) return;
      const q = S.queryFromFilters({ source: it.source, category: (it.categories || [])[0] || "" });
      try { await post("/stenella/api/groups", { name, query: q }); rerender(); }
      catch (e) { window.alert(e.message); }
    } }));
    acts.appendChild(el("a", { href: "#/map?item=" + encodeURIComponent(it.id), text: "Map" }));
    acts.appendChild(el("a", { href: "#/tree?item=" + encodeURIComponent(it.id), text: "Tree" }));
    return acts;
  }

  // timelineInto renders items into a list. It is shared by the feed, the
  // search results and the briefing, so every view shows the same record.
  function timelineInto(list, items, rerender) {
    clear(list);
    if (!items.length) {
      list.appendChild(el("li", { class: "muted", text: "nothing collected yet — run a collection from Sources" }));
      return;
    }
    for (const it of items) {
      list.appendChild(el("li", {},
        el("div", { class: "head" },
          el("span", { class: "title", text: it.title || "(untitled)" }),
          el("span", { class: "muted", text: S.timeAgo(it.published || it.fetched) })),
        el("div", { class: "badges" }, itemBadges(it)),
        it.summary ? el("p", { text: it.summary }) : null,
        itemActions(it, rerender)));
    }
  }

  async function loadFeed() {
    const collection = $("feed-collection").value;
    try {
      const out = await api("/stenella/api/items?" + S.buildQuery({ collection, limit: 100 }));
      showNotes(out.notes);
      $("feed-count").textContent = (out.count || 0) + " items";
      timelineInto($("timeline"), out.items || [], loadFeed);
    } catch (err) {
      showError($("timeline"), err);
    }
  }

  // --- the map -------------------------------------------------------------

  let mapItems = [];

  function drawMap() {
    const cv = $("map");
    const ctx = cv.getContext("2d");
    const w = cv.width, h = cv.height;
    ctx.clearRect(0, 0, w, h);

    // ocean
    ctx.fillStyle = "rgba(10,132,255,.06)";
    ctx.fillRect(0, 0, w, h);

    // graticule
    ctx.strokeStyle = "rgba(127,127,127,.25)";
    ctx.lineWidth = 1;
    for (const lat of W.graticule.lat) {
      const p1 = S.project(lat, -180, w, h), p2 = S.project(lat, 180, w, h);
      ctx.beginPath(); ctx.moveTo(p1.x, p1.y); ctx.lineTo(p2.x, p2.y); ctx.stroke();
    }
    for (const lon of W.graticule.lon) {
      const p1 = S.project(90, lon, w, h), p2 = S.project(-90, lon, w, h);
      ctx.beginPath(); ctx.moveTo(p1.x, p1.y); ctx.lineTo(p2.x, p2.y); ctx.stroke();
    }

    // the bundled land outline
    ctx.fillStyle = "rgba(127,127,127,.30)";
    ctx.strokeStyle = "rgba(127,127,127,.65)";
    ctx.lineWidth = 1;
    for (const poly of W.land) {
      ctx.beginPath();
      poly.forEach((pt, i) => {
        const p = S.project(pt[1], pt[0], w, h);
        if (i === 0) ctx.moveTo(p.x, p.y); else ctx.lineTo(p.x, p.y);
      });
      ctx.closePath();
      ctx.fill();
      ctx.stroke();
    }

    // markers, clustered so a dense map stays readable
    const clusters = S.cluster(S.geoPoints(mapItems, w, h), 26);
    mapClusters = clusters;
    for (const c of clusters) {
      const r = Math.min(6 + Math.sqrt(c.count) * 2.2, 18);
      ctx.beginPath();
      ctx.arc(c.x, c.y, r, 0, Math.PI * 2);
      ctx.fillStyle = "rgba(240,68,56,.65)";
      ctx.fill();
      ctx.strokeStyle = "#f04438";
      ctx.stroke();
      if (c.count > 1) {
        ctx.fillStyle = "#fff";
        ctx.font = "bold 11px system-ui, sans-serif";
        ctx.textAlign = "center";
        ctx.fillText(String(c.count), c.x, c.y + 1);
        ctx.textAlign = "left";
      }
    }
    ctx.fillStyle = "rgba(127,127,127,.9)";
    ctx.font = "11px system-ui, sans-serif";
    ctx.fillText(clusters.length + " markers from " + mapItems.filter((i) => i.hasGeo).length + " placed items", 8, h - 8);
  }

  let mapClusters = [];

  function selectCluster(c) {
    const box = $("map-info");
    clear(box);
    box.appendChild(el("h3", { text: c.count === 1 ? (c.items[0].title || "(untitled)") : c.count + " items here" }));
    if (c.count === 1) {
      const it = c.items[0];
      box.appendChild(el("div", { class: "badges" }, itemBadges(it)));
      if (it.summary) box.appendChild(el("p", { text: it.summary }));
    } else {
      const ul = el("ul", { class: "cites" });
      for (const it of c.items) {
        ul.appendChild(el("li", {},
          el("a", { href: "#/feed?item=" + encodeURIComponent(it.id), text: it.title || "(untitled)" })));
      }
      box.appendChild(ul);
    }
  }

  async function loadMap() {
    try {
      const out = await api("/stenella/api/items?" + S.buildQuery({ limit: 1000 }));
      mapItems = out.items || [];
      drawMap();
    } catch (err) {
      showError($("map-info"), err);
    }
  }

  // --- the information tree ------------------------------------------------

  let treeData = null;

  function treeNodeEl(node) {
    const kids = node.children || [];
    if (kids.length === 0) {
      return el("li", {},
        el("span", { text: node.label }),
        el("span", { class: "kind kind-" + node.kind, text: node.kind }));
    }
    const det = el("details", { open: node.kind === "group" || node.kind === "source" },
      el("summary", {},
        el("span", { text: node.label }),
        el("span", { class: "kind kind-" + node.kind, text: node.kind + " · " + kids.length })));
    const ul = el("ul");
    for (const k of kids) ul.appendChild(treeNodeEl(k));
    det.appendChild(ul);
    return el("li", {}, det);
  }

  function renderTree() {
    const box = $("tree");
    clear(box);
    if (!treeData) { box.appendChild(el("p", { class: "muted", text: "loading…" })); return; }
    const nodes = S.treeOf(treeData);
    if (nodes.length === 0) { box.appendChild(el("p", { class: "muted", text: "nothing to show yet" })); return; }
    const ul = el("ul");
    for (const n of nodes) ul.appendChild(treeNodeEl(n));
    box.appendChild(ul);
  }

  async function loadTree() {
    try {
      const [sources, groups, items, links, insights] = await Promise.all([
        api("/stenella/api/sources"),
        api("/stenella/api/groups"),
        api("/stenella/api/items?" + S.buildQuery({ limit: 1000 })),
        api("/stenella/api/links"),
        api("/stenella/api/insights"),
      ]);
      treeData = {
        sources: sources.sources || [],
        groups: groups.groups || [],
        items: items.items || [],
        links: links.links || [],
        insights: insights.insights || [],
      };
      renderTree();
    } catch (err) {
      showError($("tree"), err);
    }
  }

  // --- the insights inbox --------------------------------------------------

  async function loadInbox() {
    const status = $("inbox-status").value;
    try {
      const out = await api("/stenella/api/insights?" + S.buildQuery({ status }));
      const list = S.triage(out.insights || []);
      const box = $("inbox");
      clear(box);
      if (list.length === 0) {
        box.appendChild(el("p", { class: "muted", text: "no insights in this state" }));
        return;
      }
      for (const inx of list) {
        const card = el("div", { class: "insight sev-" + (inx.severity || "info") },
          el("div", { class: "head" },
            el("span", { class: "title", text: inx.title || "(untitled insight)" }),
            badge(inx.severity || "info", "sev-" + (inx.severity || "info")),
            badge(inx.status || "new"),
            el("span", { class: "muted", text: S.timeAgo(inx.updated || inx.created) })),
          el("p", { text: inx.text || "" }),
          el("p", { class: "muted", text: (inx.count || 0) + " items · " + (inx.sources || []).join(", ") }),
        );
        if (inx.reason) card.appendChild(el("p", { class: "muted", text: "dismissed: " + inx.reason }));
        if (inx.items && inx.items.length) {
          const ul = el("ul", { class: "cites" });
          for (const id of inx.items) ul.appendChild(el("li", { text: id }));
          card.appendChild(ul);
        }
        const acts = el("div", { class: "acts" });
        if (inx.status !== "dismissed") {
          acts.appendChild(el("button", { type: "button", text: "Dismiss", onClick: async () => {
            const reason = window.prompt("Reason for dismissing (required):");
            if (!reason) return;
            try { await post("/stenella/api/insights/" + encodeURIComponent(inx.id), { action: "dismiss", reason }); loadInbox(); }
            catch (e) { window.alert(e.message); }
          } }));
        }
        if (inx.status !== "promoted") {
          acts.appendChild(el("button", { type: "button", text: "Promote to note", onClick: async () => {
            try { await post("/stenella/api/insights/" + encodeURIComponent(inx.id), { action: "promote" }); loadInbox(); }
            catch (e) { window.alert(e.message); }
          } }));
        }
        if (inx.status !== "new") {
          acts.appendChild(el("button", { type: "button", text: "Reopen", onClick: async () => {
            try { await post("/stenella/api/insights/" + encodeURIComponent(inx.id), { action: "reopen" }); loadInbox(); }
            catch (e) { window.alert(e.message); }
          } }));
        }
        card.appendChild(acts);
        box.appendChild(card);
      }
    } catch (err) {
      showError($("inbox"), err);
    }
  }

  // --- search --------------------------------------------------------------

  async function runSearch(query) {
    const q = query != null ? query : $("q").value;
    $("search-input").value = q;
    try {
      const out = await api("/stenella/api/search?" + S.buildQuery({ q, limit: 200 }));
      showNotes(out.notes);
      timelineInto($("hits"), out.hits || [], () => runSearch(q));
    } catch (err) {
      showError($("hits"), err);
    }
  }

  async function loadSaved() {
    const box = $("saved");
    try {
      const out = await api("/stenella/api/groups");
      clear(box);
      const groups = out.groups || [];
      if (groups.length === 0) { box.appendChild(el("span", { class: "muted", text: "no saved queries yet" })); return; }
      box.appendChild(el("span", { class: "muted", text: "saved:" }));
      for (const g of groups) {
        box.appendChild(el("button", { type: "button", text: g.name, title: g.query || "", onClick: () => runSearch(g.query || "") }));
        box.appendChild(el("button", { type: "button", class: "danger", text: "×", title: "Delete " + g.name, onClick: async () => {
          try { await api("/stenella/api/groups/" + encodeURIComponent(g.id), { method: "DELETE" }); loadSaved(); }
          catch (e) { window.alert(e.message); }
        } }));
      }
    } catch (err) {
      showError(box, err);
    }
  }

  // --- sources, exports, OPML ---------------------------------------------

  async function loadSources() {
    try {
      const out = await api("/stenella/api/sources");
      const body = $("sources-body");
      clear(body);
      for (const s of out.sources || []) {
        const tr = el("tr", {},
          el("td", { text: s.name }),
          el("td", { text: s.kind || "feed" }),
          el("td", { text: s.collection || "all" }),
          el("td", { text: s.reliability || "" }),
          el("td", { text: s.lastStatus || "never" }),
          el("td", { text: String(s.lastCount || 0) }),
          el("td", {},
            el("button", { type: "button", text: s.enabled ? "Disable" : "Enable", onClick: async () => {
              try { await post("/stenella/api/sources/" + encodeURIComponent(s.id), { action: s.enabled ? "disable" : "enable" }); loadSources(); }
              catch (e) { window.alert(e.message); }
            } }),
            " ",
            el("button", { type: "button", text: "Fetch", onClick: async () => {
              try { await post("/stenella/api/sources/" + encodeURIComponent(s.id), { action: "fetch" }); loadSources(); }
              catch (e) { window.alert(e.message); }
            } })));
        body.appendChild(tr);
      }
    } catch (err) {
      showError($("sources-body"), err);
    }
  }

  async function runCollection() {
    const out = $("run-report");
    out.textContent = "collecting…";
    try {
      const rep = await post("/stenella/api/run", {});
      out.textContent = "run: " + JSON.stringify(rep);
      loadSources();
    } catch (err) {
      out.textContent = err.message;
    }
  }

  function printBriefing() {
    const collection = $("brief-collection").value;
    api("/stenella/api/items?" + S.buildQuery({ collection, limit: 500 })).then((out) => {
      const items = out.items || [];
      let html = "<!DOCTYPE html><html><head><meta charset='utf-8'><title>stenella briefing</title>" +
        "<style>body{font:14px/1.5 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem}" +
        "h1{font-size:1.3rem}h2{font-size:1rem;margin-top:1.6rem;border-bottom:1px solid #ccc}" +
        ".meta{color:#666;font-size:.85rem}ul{padding-left:1.2rem}</style></head><body>" +
        "<h1>stenella briefing — " + S.escapeHTML(collection || "all collections") + "</h1>" +
        "<p class='meta'>generated " + S.escapeHTML(new Date().toISOString()) + " · " + items.length + " items · no links, by design</p>";
      for (const it of items) {
        html += "<h2>" + S.escapeHTML(it.title || "(untitled)") + "</h2>" +
          "<p class='meta'>" + S.escapeHTML(it.source || "") + " · " + S.escapeHTML(it.collection || "") +
          " · " + S.escapeHTML(it.acl || "public") + " · " + S.escapeHTML(S.niceDate(it.published || it.fetched)) + "</p>" +
          "<p>" + S.escapeHTML(it.summary || "") + "</p>";
        const tags = (it.categories || []).map((c) => S.escapeHTML(c));
        const ents = (it.entities || []).map((e) => S.escapeHTML(e.kind + ":" + e.name));
        if (tags.length || ents.length) {
          html += "<p class='meta'>" + tags.join(" · ") + (ents.length ? " — " + ents.join(" · ") : "") + "</p>";
        }
      }
      html += "</body></html>";
      const win = window.open("", "_blank");
      if (!win) { window.alert("Allow pop-ups to print the briefing."); return; }
      win.document.write(html);
      win.document.close();
      win.focus();
      win.print();
    }).catch((err) => window.alert(err.message));
  }

  // --- the console ---------------------------------------------------------

  const PANELS = [
    "platform", "subprojects", "apps", "functions", "secrets", "head", "headers",
    "song", "pod", "shepherd", "firewall", "static", "counts",
  ];

  async function loadConsole() {
    const box = $("console");
    clear(box);
    try {
      const doc = await api("/stenella/api/console");
      for (const name of PANELS) {
        const body = el("pre", { text: JSON.stringify(doc[name], null, 2) });
        box.appendChild(el("details", { class: "panel card", open: name === "platform" },
          el("summary", { text: name }), body));
      }
    } catch (err) {
      showError(box, err);
    }
  }

  // --- the router ----------------------------------------------------------

  const VIEWS = {
    dashboard: { show: "#view-dashboard", load: loadDashboard },
    feed: { show: "#view-feed", load: loadFeed },
    map: { show: "#view-map", load: loadMap },
    tree: { show: "#view-tree", load: loadTree },
    inbox: { show: "#view-inbox", load: loadInbox },
    search: { show: "#view-search", load: () => { loadSaved(); } },
    sources: { show: "#view-sources", load: () => { loadSources(); } },
    console: { show: "#view-console", load: loadConsole },
  };

  let currentView = "";

  function route() {
    const { view } = S.routeOf(window.location.hash);
    const chosen = VIEWS[view] ? view : "dashboard";
    for (const name of Object.keys(VIEWS)) {
      const sec = $(VIEWS[name].show);
      if (sec) sec.hidden = name !== chosen;
    }
    for (const a of document.querySelectorAll("nav#views a")) {
      if (a.dataset.view === chosen) a.setAttribute("aria-current", "page");
      else a.removeAttribute("aria-current");
    }
    if (chosen !== currentView) {
      currentView = chosen;
      VIEWS[chosen].load();
    }
    window.scrollTo(0, 0);
  }

  // --- wiring --------------------------------------------------------------

  function init() {
    loadFilterOptions().then(loadDashboard);

    $("f-apply").addEventListener("click", loadDashboard);
    $("filters").addEventListener("submit", (e) => { e.preventDefault(); loadDashboard(); });
    $("filters").addEventListener("reset", () => setTimeout(loadDashboard, 0));
    for (const id of ["f-collection", "f-source", "f-from", "f-to"]) {
      $(id).addEventListener("change", loadDashboard);
    }

    $("feed-collection").addEventListener("change", loadFeed);
    $("feed-refresh").addEventListener("click", loadFeed);

    $("map").addEventListener("click", (ev) => {
      const cv = $("map");
      const rect = cv.getBoundingClientRect();
      const x = ((ev.clientX - rect.left) / rect.width) * cv.width;
      const y = ((ev.clientY - rect.top) / rect.height) * cv.height;
      let best = null, bestD = 1e9;
      for (const c of mapClusters) {
        const d = Math.hypot(c.x - x, c.y - y);
        if (d < bestD) { bestD = d; best = c; }
      }
      if (best && bestD < 30) selectCluster(best);
    });

    $("tree-expand").addEventListener("click", () => {
      for (const d of $("tree").querySelectorAll("details")) d.open = true;
    });
    $("tree-collapse").addEventListener("click", () => {
      for (const d of $("tree").querySelectorAll("details")) d.open = false;
    });
    $("tree-export").addEventListener("click", () => {
      const text = S.outlineOf(treeData ? S.treeOf(treeData) : []);
      const win = window.open("", "_blank");
      if (!win) { window.alert("Allow pop-ups to export the outline."); return; }
      win.document.write("<pre>" + S.escapeHTML(text) + "</pre>");
      win.document.close();
    });

    $("inbox-status").addEventListener("change", loadInbox);
    $("inbox-run").addEventListener("click", async () => {
      try { await post("/stenella/api/run", {}); loadInbox(); }
      catch (e) { window.alert(e.message); }
    });

    $("searchbar").addEventListener("submit", (e) => {
      e.preventDefault();
      window.location.hash = S.hrefFor("search", { q: $("q").value });
      runSearch($("q").value);
    });
    $("save-query").addEventListener("click", async () => {
      const name = window.prompt("Name this saved query:");
      if (!name) return;
      try { await post("/stenella/api/groups", { name, query: $("q").value }); loadSaved(); }
      catch (e) { window.alert(e.message); }
    });
    $("search-go").addEventListener("click", () => runSearch($("search-input").value));
    $("search-save").addEventListener("click", async () => {
      const name = window.prompt("Name this saved query:");
      if (!name) return;
      try { await post("/stenella/api/groups", { name, query: $("search-input").value }); loadSaved(); }
      catch (e) { window.alert(e.message); }
    });

    $("src-run").addEventListener("click", runCollection);
    $("src-add").addEventListener("submit", async (e) => {
      e.preventDefault();
      const f = new FormData(e.target);
      try {
        await post("/stenella/api/sources", {
          name: f.get("name"), url: f.get("url"), kind: f.get("kind"),
          collection: f.get("collection") || "all", reliability: f.get("reliability") || "C",
          acl: "public", enabled: true,
        });
        e.target.reset();
        loadSources();
      } catch (err) { window.alert(err.message); }
    });
    $("opml-import").addEventListener("click", async () => {
      const doc = $("opml").value.trim();
      if (!doc) { $("opml-result").textContent = "paste an OPML document first"; return; }
      try {
        const res = await api("/stenella/api/opml", {
          method: "POST",
          headers: { "Content-Type": "application/xml" },
          body: doc,
        });
        $("opml-result").textContent = "added " + res.added + ", skipped " + res.skipped;
        loadSources();
      } catch (err) { $("opml-result").textContent = err.message; }
    });
    $("brief-print").addEventListener("click", printBriefing);

    window.addEventListener("hashchange", route);
    window.addEventListener("offline", () => { offline = true; });
    window.addEventListener("online", () => { offline = false; });
    route();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
