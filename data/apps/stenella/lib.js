(function (global) {
  'use strict';
  // Determine the global object
  var root = typeof window !== 'undefined' ? window : (typeof globalThis !== 'undefined' ? globalThis : (typeof global !== 'undefined' ? global : this));

  // Try to get SongView from the global scope (set by _song/viewkit.js)
  var V = {};
  if (root.SongView) {
    V = root.SongView;
  } else if (root.Stenella && root.Stenella.buildQuery) {
    // Already initialized, use what we have
    V = { buildQuery: root.Stenella.buildQuery };
  }

  // Ensure buildQuery always exists
  function buildQuery(params) {
    params = params || {};
    var parts = [];
    for (var k in params) {
      if (params.hasOwnProperty(k) && params[k] != null && params[k] !== '') {
        parts.push(encodeURIComponent(k) + '=' + encodeURIComponent(String(params[k])));
      }
    }
    return parts.join('&');
  }

  // DOM helpers
  function $(id) { return document.getElementById(id); }
  function el(tag, attrs, ...kids) {
    var n = document.createElement(tag);
    for (var k of Object.keys(attrs || {})) {
      var v = attrs[k];
      if (v == null || v === false) continue;
      if (k === 'class') n.className = v;
      else if (k === 'text') n.textContent = v;
      else if (k === 'html') n.innerHTML = v;
    }
    for (var i = 2; i < arguments.length; i++) {
      var kid = arguments[i];
      if (typeof kid === 'string') kid = document.createTextNode(kid);
      else if (typeof kid === 'object' && kid.appendChild) n.appendChild(kid);
      else if (kid != null) n.appendChild(document.createTextNode(String(kid)));
    }
    return n;
  }

  // queryFromFilters - safe version
  function queryFromFilters(f) {
    var parts = [];
    if (!f) return '';
    if (f.category) parts.push('tag:' + (V.quoteValue ? V.quoteValue(f.category) : String(f.category)));
    if (f.source) parts.push('source:' + (V.quoteValue ? V.quoteValue(f.source) : String(f.source)));
    if (f.from) parts.push('from:' + (V.quoteValue ? V.quoteValue(f.from) : String(f.from)));
    if (f.to) parts.push('to:' + (V.quoteValue ? V.quoteValue(f.to) : String(f.to)));
    if (f && f.text) parts.push(String(f.text).trim());
    return parts.filter(Boolean).join(' ');
  }

  // treeNode - safe
  function treeNode(label, kind, children, id) {
    return { label: label || '', kind: kind || 'unknown', children: children || [], id: id || '' };
  }

  // treeOf - safe, doesn't depend on V for structure
  function treeOf(data) {
    data = data || {};
    var items = data.items || [];
    var groups = (data.groups || []).map(function (g) {
      var tags = (g.tags || []).map(function (t) { return treeNode(t, 'tag'); });
      return treeNode(g.name || 'group', 'group', tags, g.id);
    });
    var bySource = new Map();
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      var key = it.source || '(unknown)';
      if (!bySource.has(key)) bySource.set(key, []);
      bySource.get(key).push(it);
    }
    var sources = [];
    bySource.forEach(function (list, name) {
      var nodes = list.map(function (it) {
        var children = (it.entities || []).map(function (e) { return treeNode(e.name || e.id, 'entity'); });
        return treeNode(it.title || 'item', 'item', children, it.id);
      });
      sources.push(treeNode(name, 'source', nodes));
    });
    sources.sort(function (a, b) { return a.label.localeCompare(b.label); });
    return groups.concat(sources);
  }

  // outlineOf - safe
  function outlineOf(nodes, depth) {
    depth = depth || 0;
    var out = '';
    for (var i = 0; i < (nodes || []).length; i++) {
      var n = nodes[i];
      out += '  '.repeat(depth) + '- ' + (n.label || '') + '\n';
      if (n && n.children && n.children.length) out += outlineOf(n.children, depth + 1);
    }
    return out;
  }

  // result object
  var result = {};
  // Copy properties from V if available
  if (V && typeof V === 'object') {
    for (var prop in V) {
      if (V.hasOwnProperty(prop)) result[prop] = V[prop];
    }
  }
  // Always ensure these functions exist
  result.queryFromFilters = queryFromFilters;
  result.treeNode = treeNode;
  result.treeOf = treeOf;
  result.outlineOf = outlineOf;
  result.buildQuery = V && V.buildQuery ? V.buildQuery : buildQuery;

  // Set Stenella global - always define, even if empty
  root.Stenella = result;
  if (typeof window !== 'undefined') window.Stenella = result;
  if (typeof globalThis !== 'undefined') globalThis.Stenella = result;

  // Debug: verify Stenella is set
  if (typeof console !== 'undefined' && console !== null) {
    try { console.debug('Stenella client loaded, buildQuery:', typeof result.buildQuery); } catch(e) {}
  }
})(typeof globalThis !== 'undefined' ? globalThis : (typeof window !== 'undefined' ? window : this));
