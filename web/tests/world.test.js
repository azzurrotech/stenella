// Tests for web/world.js — the bundled outline the map view draws.
//
// The map must be usable and self-contained: every landmass is a closed polygon
// of plausible [longitude, latitude] pairs, and every one of them must land
// inside the canvas when projected.
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
require("../world.js");
const S = require("../lib.js");

const W = globalThis.StenellaWorld;
const WIDTH = 960;
const HEIGHT = 480;

test("the outline is bundled, not fetched", () => {
  assert.ok(W, "StenellaWorld must be defined by world.js alone");
  assert.ok(Array.isArray(W.land));
  assert.ok(W.land.length >= 15, "expected at least 15 landmasses, got " + W.land.length);
  assert.ok(Array.isArray(W.graticule.lat) && W.graticule.lat.length > 0);
  assert.ok(Array.isArray(W.graticule.lon) && W.graticule.lon.length > 0);
  // nothing in the file may describe a URL: the base map makes no request
  const raw = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "world.js"), "utf8");
  assert.ok(!/https?:\/\//.test(raw), "world.js must not reference a URL");
});

test("every polygon is closed, well formed and on the globe", () => {
  for (const [i, poly] of W.land.entries()) {
    assert.ok(Array.isArray(poly) && poly.length >= 4, "polygon " + i + " is too small");
    for (const pt of poly) {
      assert.equal(pt.length, 2, "polygon " + i + " has a malformed point");
      const [lon, lat] = pt;
      assert.ok(lon >= -180 && lon <= 180, "polygon " + i + " lon out of range: " + lon);
      assert.ok(lat >= -90 && lat <= 90, "polygon " + i + " lat out of range: " + lat);
    }
    const first = poly[0];
    const last = poly[poly.length - 1];
    assert.deepEqual(first, last, "polygon " + i + " is not closed");
  }
});

test("every vertex projects onto the canvas", () => {
  for (const [i, poly] of W.land.entries()) {
    for (const [lon, lat] of poly) {
      const p = S.project(lat, lon, WIDTH, HEIGHT);
      assert.ok(p.x >= 0 && p.x <= WIDTH, "polygon " + i + " x off canvas: " + p.x);
      assert.ok(p.y >= 0 && p.y <= HEIGHT, "polygon " + i + " y off canvas: " + p.y);
    }
  }
});

test("the outline is recognisable: the continents are where they should be", () => {
  // a point inside a landmass polygon (ray casting, the same test the eye makes)
  function inside(lon, lat) {
    for (const poly of W.land) {
      let hit = false;
      for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
        const [xi, yi] = poly[i];
        const [xj, yj] = poly[j];
        if ((yi > lat) !== (yj > lat) && lon < ((xj - xi) * (lat - yi)) / (yj - yi) + xi) hit = !hit;
      }
      if (hit) return true;
    }
    return false;
  }
  // [longitude, latitude]: land cities on five continents
  const land = {
    "Ottawa (North America)": [-75.7, 45.42],
    "Rome (Italy)": [12.5, 41.9],
    "London (Great Britain)": [-0.1, 51.5],
    "Tokyo (Honshu)": [139.7, 35.7],
    "Nairobi (Africa)": [36.82, -1.29],
    "Cairo (Africa)": [31.2, 30],
    "Brasília (South America)": [-47.9, -15.78],
    "Lima (South America)": [-77, -12],
    "Beijing (Asia)": [116.4, 39.9],
    "Canberra (Australia)": [149.13, -35.28],
    "Reykjavík (Iceland)": [-21.9, 64.1],
    "Antarctica": [0, -80],
  };
  for (const [name, [lon, lat]] of Object.entries(land)) {
    assert.ok(inside(lon, lat), name + " should be inside a landmass");
  }
  // and open water is water
  const sea = {
    "mid-Atlantic": [-30, 0],
    "mid-Pacific": [-140, 0],
    "Indian Ocean": [75, -30],
    "Southern Ocean": [0, -55],
  };
  for (const [name, [lon, lat]] of Object.entries(sea)) {
    assert.ok(!inside(lon, lat), name + " must not be land");
  }
});

test("every polygon is simple: no edge crosses another", () => {
  const orient = (a, b, c) => (b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0]);
  const crosses = (p1, p2, p3, p4) => {
    const d1 = orient(p3, p4, p1), d2 = orient(p3, p4, p2);
    const d3 = orient(p1, p2, p3), d4 = orient(p1, p2, p4);
    return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0));
  };
  for (const [i, poly] of W.land.entries()) {
    const n = poly.length - 1;
    for (let a = 0; a < n; a++) {
      for (let b = a + 1; b < n; b++) {
        if (b === a + 1 || (a === 0 && b === n - 1)) continue; // adjacent edges share a vertex
        assert.ok(
          !crosses(poly[a], poly[a + 1], poly[b], poly[b + 1]),
          "polygon " + i + " has crossing edges " + a + " and " + b
        );
      }
    }
  }
});

test("clustering a real collection keeps the map readable", () => {
  const pts = S.geoPoints(
    [
      { id: "a", hasGeo: true, lat: 45.42, lon: -75.7 },
      { id: "b", hasGeo: true, lat: 45.43, lon: -75.69 },
      { id: "c", hasGeo: true, lat: 41.9, lon: 12.5 },
    ],
    WIDTH,
    HEIGHT
  );
  const clusters = S.cluster(pts, 26);
  assert.equal(clusters.length, 2, "the two Ottawa points should share a marker");
  assert.equal(clusters.find((c) => c.count === 2).items.length, 2);
});
