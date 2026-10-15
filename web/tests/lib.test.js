// Tests for web/lib.js — the client logic, without a browser.
//
// Run with: node --test web/tests/
//
// This is deliberately not part of the Go test suite: the Go tests check that
// the assets are embedded and served; these check that the client's decisions
// (the router, the query builder, the chart aggregation, the projection and the
// tree) are correct.
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const S = require("../lib.js");

test("routeOf reads the view and the parameters out of the hash", () => {
  assert.deepEqual(S.routeOf(""), { view: "dashboard", params: {} });
  assert.deepEqual(S.routeOf("#/"), { view: "dashboard", params: {} });
  assert.deepEqual(S.routeOf("#/feed"), { view: "feed", params: {} });
  assert.deepEqual(S.routeOf("#/feed?collection=news"), {
    view: "feed",
    params: { collection: "news" },
  });
  assert.deepEqual(S.routeOf("#/tree?item=item-1&depth=2"), {
    view: "tree",
    params: { item: "item-1", depth: "2" },
  });
  // percent-encoding and + survive a round trip
  assert.deepEqual(S.routeOf("#/search?q=tag%3A%22supply+chain%22").params, {
    q: 'tag:"supply chain"',
  });
});

test("hrefFor and buildQuery produce a stable, encoded link", () => {
  assert.equal(S.hrefFor("feed"), "#/feed");
  assert.equal(S.hrefFor("feed", { collection: "news" }), "#/feed?collection=news");
  assert.equal(S.buildQuery({ b: "2", a: "1", empty: "" }), "a=1&b=2");
  assert.equal(S.buildQuery({ q: 'tag:"x y"' }), "q=tag%3A%22x%20y%22");
});

test("queryFromFilters turns the filter bar into the server query language", () => {
  assert.equal(S.queryFromFilters({}), "");
  assert.equal(
    S.queryFromFilters({ category: "supply chain", source: "CBC" }),
    'tag:"supply chain" source:CBC'
  );
  assert.equal(
    S.queryFromFilters({ from: "2026-01-01", to: "2026-02-01", text: "ransomware" }),
    "from:2026-01-01 to:2026-02-01 ransomware"
  );
  // a value with a quote is escaped, so it cannot break out of the group
  assert.equal(S.queryFromFilters({ category: 'a"b' }), 'tag:"a\\"b"');
});

test("aggregate is what the three charts read", () => {
  const items = [
    { source: "CBC", categories: ["Cyber", "cyber"], published: "2026-09-20T10:00:00Z" },
    { source: "ANSA", categories: ["Economy"], published: "2026-09-20T12:00:00Z" },
    { source: "CBC", categories: [], fetched: "2026-09-22T09:00:00Z" },
  ];
  const agg = S.aggregate(items);
  assert.deepEqual(agg.bySource, { CBC: 2, ANSA: 1 });
  // categories fold to lower case, so "Cyber" and "cyber" are one slice
  assert.deepEqual(agg.byCategory, { cyber: 2, economy: 1 });
  assert.deepEqual(agg.byDay, { "2026-09-20": 2, "2026-09-22": 1 });
  assert.deepEqual(S.topEntries(agg.bySource), [["CBC", 2], ["ANSA", 1]]);
  assert.deepEqual(S.topEntries(agg.bySource, 1), [["CBC", 2]]);
});

test("seriesOfDay fills the gaps so the line chart has an even axis", () => {
  assert.deepEqual(S.seriesOfDay({}), []);
  assert.deepEqual(S.seriesOfDay({ "2026-09-20": 2, "2026-09-22": 3 }), [
    ["2026-09-20", 2],
    ["2026-09-21", 0],
    ["2026-09-22", 3],
  ]);
});

test("distinctSources and newestItem", () => {
  const items = [
    { source: "CBC", published: "2026-09-20T10:00:00Z" },
    { source: "ANSA", fetched: "2026-09-25T10:00:00Z" },
    { source: "CBC", published: "2026-09-21T10:00:00Z" },
    { source: "" },
  ];
  assert.deepEqual(S.distinctSources(items), ["ANSA", "CBC"]);
  assert.equal(S.newestItem(items).source, "ANSA");
  assert.equal(S.newestItem([]), null);
});

test("project is the equirectangular projection onto the canvas", () => {
  assert.deepEqual(S.project(0, 0, 360, 180), { x: 180, y: 90 });
  assert.deepEqual(S.project(90, -180, 360, 180), { x: 0, y: 0 });
  assert.deepEqual(S.project(-90, 180, 360, 180), { x: 360, y: 180 });
  // Ottawa lands in the northern hemisphere, west of Greenwich
  const ottawa = S.project(45.42, -75.7, 960, 480);
  assert.ok(ottawa.x > 200 && ottawa.x < 300, "x=" + ottawa.x);
  assert.ok(ottawa.y > 100 && ottawa.y < 200, "y=" + ottawa.y);
});

test("geoPoints keeps only items that can actually be placed", () => {
  const items = [
    { id: "a", hasGeo: true, lat: 45.4, lon: -75.7 },
    { id: "b", hasGeo: false },
    { id: "c", hasGeo: true, lat: "not a number", lon: 3 },
  ];
  const pts = S.geoPoints(items, 360, 180);
  assert.equal(pts.length, 1);
  assert.equal(pts[0].item.id, "a");
});

test("cluster groups nearby markers and averages their position", () => {
  const pts = [
    { x: 10, y: 10, item: { id: "a" } },
    { x: 12, y: 11, item: { id: "b" } },
    { x: 200, y: 200, item: { id: "c" } },
  ];
  const cl = S.cluster(pts, 26);
  assert.equal(cl.length, 2);
  const big = cl.find((c) => c.count === 2);
  assert.ok(big, "two adjacent markers must share a cluster");
  assert.deepEqual(big.items.map((i) => i.id), ["a", "b"]);
  assert.equal(big.x, 11);
  assert.equal(big.y, 10.5);
  assert.equal(cl.find((c) => c.count === 1).items[0].id, "c");
});

test("treeOf builds sources → groups → items → entities → links/insights", () => {
  const tree = S.treeOf({
    groups: [{ name: "Cyber watch", tags: ["cyber"] }],
    items: [
      { id: "i1", source: "CBC", title: "First", entities: [{ id: "e1", kind: "place", name: "Ottawa" }] },
      { id: "i2", source: "ANSA", title: "Second", entities: [] },
    ],
    links: [{ from: "i1", to: "i2", relation: "supports" }],
    insights: [{ id: "x1", title: "Corroborated", items: ["i1"] }],
  });

  // the group is first: it is what the analyst is tracking
  assert.equal(tree[0].kind, "group");
  assert.equal(tree[0].label, "Cyber watch");
  assert.deepEqual(tree[0].children.map((c) => c.label), ["cyber"]);

  const sources = tree.filter((n) => n.kind === "source");
  assert.deepEqual(sources.map((n) => n.label), ["ANSA", "CBC"]);

  const cbc = sources.find((n) => n.label === "CBC");
  const item = cbc.children[0];
  assert.equal(item.label, "First");
  const kinds = item.children.map((c) => c.kind).sort();
  assert.deepEqual(kinds, ["entity", "insight", "link"]);
  assert.ok(item.children.find((c) => c.label === "Ottawa"));
  assert.ok(item.children.find((c) => c.label.startsWith("i2 (supports)")));
  assert.ok(item.children.find((c) => c.label === "Corroborated"));
});

test("outlineOf renders a citation-ready indented outline", () => {
  const tree = [
    { label: "Source", kind: "source", children: [
      { label: "Item", kind: "item", children: [{ label: "place:Ottawa", kind: "entity", children: [] }] },
    ] },
  ];
  assert.equal(S.outlineOf(tree), "- Source\n  - Item\n    - place:Ottawa\n");
  assert.equal(S.outlineOf([]), "");
});

test("triage puts the most severe insight first, then the most recent", () => {
  const list = [
    { id: "a", severity: "low", updated: "2026-09-20T00:00:00Z" },
    { id: "b", severity: "critical", updated: "2026-09-19T00:00:00Z" },
    { id: "c", severity: "high", updated: "2026-09-21T00:00:00Z" },
    { id: "d", severity: "high", updated: "2026-09-22T00:00:00Z" },
  ];
  assert.deepEqual(S.triage(list).map((i) => i.id), ["b", "d", "c", "a"]);
  assert.ok(S.severityRank("critical") > S.severityRank("info"));
  assert.equal(S.severityRank("nonsense"), 0);
});

test("annotations counts the analyst's marks per item", () => {
  const items = [{ id: "i1" }, { id: "i2" }];
  const a = S.annotations(
    items,
    [{ from: "i1", to: "i2", relation: "supports" }],
    [{ item: "i1", body: "x" }, { item: "i1", body: "y" }],
    [{ subject: "i1", title: "n" }]
  );
  assert.deepEqual(a.get("i1"), { links: 1, comments: 2, notes: 1 });
  assert.deepEqual(a.get("i2"), { links: 1, comments: 0, notes: 0 });
});

test("escapeHTML neutralises text that goes into a document", () => {
  assert.equal(S.escapeHTML('<b>a&"b"</b>'), "&lt;b&gt;a&amp;&quot;b&quot;&lt;/b&gt;");
  assert.equal(S.escapeHTML(null), "");
  assert.equal(S.escapeHTML("it's"), "it&#39;s");
});

test("formatCount and timeAgo render the KPI values", () => {
  assert.equal(S.formatCount(0), "0");
  assert.equal(S.formatCount(999), "999");
  assert.equal(S.formatCount(1200), "1.2k");
  assert.equal(S.formatCount(2000000), "2M");
  const now = new Date("2026-09-20T12:00:00Z");
  assert.equal(S.timeAgo("2026-09-20T11:59:40Z", now), "just now");
  assert.equal(S.timeAgo("2026-09-20T11:30:00Z", now), "30m ago");
  assert.equal(S.timeAgo("2026-09-20T09:00:00Z", now), "3h ago");
  assert.equal(S.timeAgo("2026-09-17T12:00:00Z", now), "3d ago");
  assert.equal(S.timeAgo("", now), "—");
  assert.equal(S.timeAgo("not a date", now), "—");
  // a future stamp (clock skew) reads as "just now", never as a negative age
  assert.equal(S.timeAgo("2026-09-21T00:00:00Z", now), "just now");
});
