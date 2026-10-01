/* stenella collaboration bundle.
 *
 * Everything in this file depends on the encrypted-at-rest data model, and is
 * therefore deliberately separate from app.js: app.js drives pages that work
 * without a content key (the public feed, shares, the admin console's plumbing),
 * while this file only runs where the browser is holding a real decryption
 * capability.
 *
 * The four guarantees this file implements, in the order they matter:
 *
 *   1. Decryption. The portal fetches the client's content passphrase once, then
 *      opens every *_enc envelope with vici — the same library and the same
 *      PBKDF2-SHA256/AES-256-GCM parameters the server sealed with.
 *   2. Encrypt-before-submit. Comment bodies are encrypted in the browser and
 *      POSTed as ciphertext. The server never receives the plaintext, so it
 *      cannot read, index, log or leak a comment.
 *   3. Client-side search. The server can only match envelope metadata, so the
 *      full-text box filters the decrypted page in here. The UI says so rather
 *      than implying the server searched everything.
 *   4. Tombstone reconciliation. A retention sweep drops an item server-side; the
 *      browser polls for tombstones and deletes its own decrypted copy, so
 *      "expired" means gone rather than merely hidden.
 *
 * vici is feature-detected. Without it the page still renders metadata; it just
 * cannot show bodies, write comments or search text.
 */
(function () {
  "use strict";

  var page = document.body.dataset.page;
  if (page !== "portal" && page !== "admin") return;

  var client = document.body.dataset.client || new URLSearchParams(location.search).get("client") || "";
  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) {
    return Array.prototype.slice.call((root || document).querySelectorAll(sel));
  };

  var passphrase = null;
  var decrypted = {};   // item id -> {title, summary, content}
  var tombstones = {};  // item id -> the tombstone that removed it
  var pins = {};        // item id -> pin record

  // The current page of envelopes. app.js owns the request and publishes the
  // result; this bundle re-fetches nothing, because the ciphertext it needs is
  // exactly what that response already carried.
  function pageItems() {
    return (window.stenellaApp && window.stenellaApp.items) || [];
  }

  // ---- small helpers ------------------------------------------------------------

  function el(tag, attrs, children) {
    var node = document.createElement(tag);
    attrs = attrs || {};
    Object.keys(attrs).forEach(function (k) {
      if (k === "text") node.textContent = attrs[k];
      else if (k === "html") node.innerHTML = attrs[k];
      else if (k.slice(0, 2) === "on") node.addEventListener(k.slice(2), attrs[k]);
      else if (attrs[k] !== null && attrs[k] !== undefined) node.setAttribute(k, attrs[k]);
    });
    (children || []).forEach(function (c) { if (c) node.appendChild(c); });
    return node;
  }

  function fmtBytes(n) {
    n = Number(n) || 0;
    if (n < 1024) return n + " B";
    var units = ["KB", "MB", "GB", "TB"], i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
    return n.toFixed(n < 10 ? 1 : 0) + " " + units[i];
  }

  function fmtNum(n) {
    return String(Number(n) || 0).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  }

  function api(path, opts) {
    opts = opts || {};
    var url = path;
    if (client && url.indexOf("client=") === -1) {
      url += (url.indexOf("?") === -1 ? "?" : "&") + "client=" + encodeURIComponent(client);
    }
    return fetch(url, {
      method: opts.method || "GET",
      credentials: "same-origin",
      headers: opts.body ? { "Content-Type": "application/json" } : undefined,
      body: opts.body ? JSON.stringify(opts.body) : undefined
    }).then(function (res) {
      return res.json().then(function (data) {
        if (!res.ok) throw new Error(data && data.error ? data.error : "request failed: " + res.status);
        return data;
      });
    });
  }

  // ---- 1. the content key -------------------------------------------------------

  // localStorage keeps the key across reloads so a refresh does not re-ask the
  // server for it. The value is a capability: anything with it can read this
  // client's ciphertext, which is why it is stored per client and cleared on
  // logout. This is the browser half of the boundary documented in plan §4.4 —
  // key material reaching an authenticated browser is deliberate, not a leak
  // through an oversight.
  var KEY_PREFIX = "stenella.contentkey.";

  function cachedKey() {
    try { return window.localStorage.getItem(KEY_PREFIX + client); } catch (e) { return null; }
  }
  function storeKey(value) {
    try { window.localStorage.setItem(KEY_PREFIX + client, value); } catch (e) { /* private mode */ }
  }
  function clearKey() {
    try { window.localStorage.removeItem(KEY_PREFIX + client); } catch (e) { /* ignore */ }
  }

  // loadKey resolves the content passphrase, preferring the cached copy. It never
  // throws: an unavailable key is a degraded-but-working page.
  function loadKey() {
    var cached = cachedKey();
    if (cached) { passphrase = cached; return Promise.resolve(cached); }
    if (!client) return Promise.resolve(null);
    return api("/s/api/portal/vault").then(function (data) {
      passphrase = data && data.passphrase ? data.passphrase : null;
      if (passphrase) storeKey(passphrase);
      return passphrase;
    }).catch(function () { return null; });
  }

  function haveCrypto() { return typeof window.vici !== "undefined" && window.vici; }

  // decrypt opens one envelope. A payload that will not open — a rotated key, a
  // truncated record — blanks the field rather than taking the page down.
  function decrypt(payload) {
    if (!payload) return "";
    if (!haveCrypto() || !passphrase) return "";
    return Promise.resolve(window.vici.decrypt(payload, passphrase)).catch(function () { return ""; });
  }

  // decryptItem fills the decrypted cache for one item, in parallel across its
  // three body fields.
  function decryptItem(it) {
    if (!haveCrypto() || !passphrase) return Promise.resolve();
    if (decrypted[it.id]) return Promise.resolve();
    return Promise.all([
      decrypt(it.title_enc),
      decrypt(it.summary_enc),
      decrypt(it.content_enc)
    ]).then(function (parts) {
      decrypted[it.id] = { title: parts[0], summary: parts[1], content: parts[2] };
    });
  }

  // ---- 2. comment threads --------------------------------------------------------

  // commentNode renders one item's thread inside a <details>, per the semantic
  // card structure in plan §5.1. Bodies arrive as ciphertext and are opened here.
  function threadNode(it) {
    var wrap = el("details", { class: "thread" });
    var listId = "thread-" + it.id;
    wrap.appendChild(el("summary", {}, [
      el("span", { text: "Comments" }),
      el("span", { class: "muted", text: it.comment_count ? " (" + it.comment_count + ")" : "" })
    ]));

    var body = el("div", { class: "thread-body", id: listId });
    body.appendChild(el("p", { class: "muted", text: "Loading…" }));
    wrap.appendChild(body);

    var form = el("form", { class: "comment-form" });
    var field = el("textarea", {
      rows: "3", placeholder: "Write a comment…",
      "aria-label": "Comment on " + (it.title || decrypted[it.id] && decrypted[it.id].title || "this item")
    });
    form.appendChild(field);
    form.appendChild(el("div", { class: "row" }, [
      el("button", { class: "btn primary", type: "submit", text: "Post comment" }),
      el("span", { class: "muted comment-note", text: "Encrypted in this browser before it is sent." })
    ]));
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      submitComment(it, field.value, function () { field.value = ""; loadThread(it, body); });
    });
    wrap.appendChild(form);

    // Lazy reveal: the thread is only fetched once it is opened.
    wrap.addEventListener("toggle", function () {
      if (wrap.open && body.dataset.loaded !== "1") loadThread(it, body);
    });
    return wrap;
  }

  function loadThread(it, body) {
    body.dataset.loaded = "1";
    api("/s/api/portal/items/comments?item=" + encodeURIComponent(it.id)).then(function (data) {
      body.textContent = "";
      var comments = (data && data.comments) || [];
      if (!comments.length) {
        body.appendChild(el("p", { class: "muted", text: "No comments yet." }));
        return;
      }
      comments.forEach(function (c) {
        var node = el("article", { class: "comment" });
        var header = el("header", {});
        header.appendChild(el("span", { class: "comment-author", text: c.author_token || "anonymous" }));
        if (c.created) {
          var when = new Date(c.created);
          if (!isNaN(when.getTime())) {
            header.appendChild(el("time", { datetime: c.created, text: when.toLocaleString() }));
          }
        }
        node.appendChild(header);
        // Decrypt before rendering, and never render the ciphertext itself.
        decrypt(c.body_enc).then(function (text) {
          node.appendChild(el("p", {
            text: text || (c.body_enc ? "(this comment could not be decrypted in this browser)" : ""),
            class: text ? "comment-body" : "comment-body muted"
          }));
        });
        body.appendChild(node);
      });
    }).catch(function (err) {
      body.textContent = "";
      body.appendChild(el("p", { class: "error", text: err.message }));
    });
  }

  // submitComment encrypts in the browser and POSTs only the envelope. The
  // plaintext never leaves this function's scope.
  function submitComment(it, text, done) {
    if (!haveCrypto() || !passphrase) {
      alert("Encryption is unavailable, so the comment was not sent. Nothing is posted unencrypted.");
      return;
    }
    text = (text || "").trim();
    if (!text) return;
    window.vici.encrypt(text, passphrase).then(function (payload) {
      return api("/s/api/portal/items/comments", {
        method: "POST",
        body: { item: it.id, body_enc: payload, bytes: text.length }
      });
    }).then(function () { done(); }).catch(function (err) {
      alert("Could not post the comment: " + err.message);
    });
  }

  // ---- 3. pins -------------------------------------------------------------------

  function pinButton(it) {
    var pinned = !!pins[it.id];
    var btn = el("button", {
      type: "button",
      class: "btn ghost pin-btn" + (pinned ? " pinned" : ""),
      "aria-pressed": pinned ? "true" : "false",
      title: pinned ? "Unpin — keeps this item past the retention window" : "Pin — keeps this item past the retention window",
      text: pinned ? "★ pinned" : "☆ pin"
    });
    btn.addEventListener("click", function () {
      if (pins[it.id]) {
        api("/s/api/portal/items/pin?item=" + encodeURIComponent(it.id), { method: "DELETE" })
          .then(function () { delete pins[it.id]; swap(false); });
      } else {
        api("/s/api/portal/items/pin", { method: "POST", body: { item: it.id } })
          .then(function (p) { pins[it.id] = p; swap(true); });
      }
    });
    function swap(next) {
      btn.className = "btn ghost pin-btn" + (next ? " pinned" : "");
      btn.textContent = next ? "★ pinned" : "☆ pin";
      btn.setAttribute("aria-pressed", next ? "true" : "false");
      var it2 = findItem(it.id);
      if (it2) it2.pinned = next;
    }
    return btn;
  }

  function findItem(id) {
    var list = pageItems();
    for (var i = 0; i < list.length; i++) if (list[i].id === id) return list[i];
    return null;
  }

  function loadPins() {
    return api("/s/api/portal/items/pins").then(function (data) {
      pins = {};
      ((data && data.pins) || []).forEach(function (p) { pins[p.item_ref] = p; });
    }).catch(function () { /* pins are optional */ });
  }

  // ---- 4. client-side search -----------------------------------------------------

  // The server matched envelope metadata only (plan §4.4). This filters the
  // decrypted page on top of that, and the label says which one is running so a
  // reader never mistakes a metadata hit for a full-text hit.
  // wireSearch attaches the local filter once per page load, not once per
  // decorate() — decorate runs on every items fetch, and a second listener would
  // mean every keystroke ran the filter twice.
  function wireSearch() {
    var input = $("#items-q");
    if (!input || input.dataset.searchWired === "1") return;
    input.dataset.searchWired = "1";
    var note = $("#items-search-note");
    var timer = null;

    input.addEventListener("input", function () {
      window.clearTimeout(timer);
      timer = window.setTimeout(applyLocalFilter, 160);
    });

    function applyLocalFilter() {
      var raw = input.value.trim();
      var q = raw.toLowerCase();
      var shown = 0, total = 0;
      $$("#items-list .feed-item").forEach(function (node) {
        total++;
        var hit = !q || matches(node, q);
        node.hidden = !hit;
        if (hit) shown++;
      });
      if (!note) return;
      note.textContent = !q ? "" :
        shown + " of " + total + " items on this page match “" + raw +
        "”. Bodies are decrypted and matched here, in your browser; the server " +
        "only searches envelope metadata, so load more pages to widen the search.";
      note.hidden = !q;
    }
  }

  function matches(node, q) {
    var terms = q.split(/\s+/);
    var hay = (node.dataset.search || "") + " " + node.textContent;
    hay = hay.toLowerCase();
    return terms.every(function (t) { return hay.indexOf(t) !== -1; });
  }

  // ---- 5. item rendering ---------------------------------------------------------

  // decorate walks the items app.js already rendered and adds the parts that need
  // a key: decrypted titles, pin state and comment threads. It is a separate pass
  // so app.js stays unaware of the encrypted data model.
  function decorate() {
    if (!client) return;
    loadPins();
    loadKey().then(function () {
      var nodes = $$("#items-list .feed-item");
      nodes.forEach(function (node) {
        var id = node.dataset.id;
        if (!id || node.dataset.decorated === "1") return;
        var it = findItem(id) || { id: id };
        node.dataset.decorated = "1";

        decryptItem(it).then(function () {
          var plain = decrypted[id];
          if (!plain) return;
          // Prefer the decrypted headline: app.js rendered the server's plaintext
          // copy when it had one, and a truncated title when it did not.
          if (plain.title) {
            var heading = node.querySelector("h2");
            if (heading && !heading.querySelector("a")) heading.textContent = plain.title;
          }
          if (plain.summary) {
            var sum = node.querySelector(".summary");
            if (sum && !sum.textContent.trim()) sum.textContent = plain.summary;
          }
          node.dataset.search = [plain.title, plain.summary, plain.content,
            it.source_name, it.author, (it.categories || []).join(" ")]
            .filter(Boolean).join(" ").toLowerCase();
        });

        // Only the pin and the thread are added here. Linking is already on the
        // row app.js rendered, and that button opens the links-store dialog —
        // one graph, one dialog.
        var bar = el("div", { class: "item-actions" });
        bar.appendChild(pinButton(it));
        node.appendChild(bar);
        node.appendChild(threadNode(it));

        if (tombstones[id]) markExpired(node);
      });
      wireSearch();
    });
  }

  // markExpired strikes an item through and forgets its plaintext. The guard
  // matters: tombstones are re-applied on every poll until localStorage advances,
  // and a second note under an item that already has one reads like a bug.
  function markExpired(node) {
    delete decrypted[node.dataset.id];
    delete pins[node.dataset.id];
    if (node.getAttribute("data-expired") === "1") return;
    node.setAttribute("data-expired", "1");
    node.classList.add("expired");
    node.appendChild(el("p", {
      class: "expired-note",
      text: "Removed by the retention sweep. This browser has deleted its decrypted copy."
    }));
  }

  // ---- 6. tombstones -------------------------------------------------------------

  // pollTombstones reconciles the browser with the retention sweep. Without this
  // a deleted item would stay readable in a tab that already had it decrypted.
  function pollTombstones() {
    if (!client) return;
    var since = "";
    try { since = window.localStorage.getItem("stenella.tombstones." + client) || ""; } catch (e) { /* ignore */ }
    api("/s/api/portal/retention?since=" + encodeURIComponent(since)).then(function (data) {
      var list = (data && data.tombstones) || [];
      // Advance the cursor to the newest tombstone the server reported, not to
      // the wall clock. A tombstone recorded between this response and the write
      // would otherwise be skipped on the next poll and stay decrypted forever.
      var newest = since;
      list.forEach(function (t) {
        tombstones[t.item_id] = t;
        if (t.removed && (!newest || t.removed > newest)) newest = t.removed;
      });
      if (newest !== since) {
        try { window.localStorage.setItem("stenella.tombstones." + client, newest); } catch (e) { /* ignore */ }
      }
      if (!list.length) return;
      Object.keys(tombstones).forEach(function (id) {
        var node = document.querySelector('#items-list .feed-item[data-id="' + CSS.escape(id) + '"]');
        if (node) markExpired(node);
      });
    }).catch(function () { /* offline: retry on the next tick */ });
  }

  // ---- 7. the admin usage dashboard -----------------------------------------------

  // renderUsage builds the dashboard from /s/api/admin/usage. Every visual is a
  // semantic element the browser can measure or restyle: <meter> for a magnitude,
  // a CSS-grid row for a ranked bar, and hand-generated inline SVG for the series.
  function renderUsage(container, data) {
    container.textContent = "";
    var rows = (data && data.clients) || [];
    if (!rows.length) {
      container.appendChild(el("p", { class: "muted", text: "No clients yet." }));
      return;
    }

    // KPI cards.
    var totalDisk = Number(data.total_disk_bytes) || 0;
    var totalItems = rows.reduce(function (a, r) { return a + (r.items || 0); }, 0);
    var totalSources = rows.reduce(function (a, r) { return a + (r.sources || 0); }, 0);
    container.appendChild(el("section", { class: "cards small", "aria-label": "Platform totals" }, [
      statCard(fmtNum(rows.length), "clients"),
      statCard(fmtNum(totalItems), "cached items"),
      statCard(fmtNum(totalSources), "sources"),
      statCard(fmtBytes(totalDisk), "metered peak")
    ]));

    // Ranked bars, biggest first. Rows are already sorted by the server.
    var list = el("section", { class: "ranked", "aria-label": "Storage by client" });
    list.appendChild(el("h3", { text: "Storage by client" }));
    var peak = rows.reduce(function (a, r) { return Math.max(a, r.disk_bytes || 0); }, 0) || 1;
    rows.forEach(function (r) {
      var meter = el("meter", {
        min: "0", max: String(peak), value: String(r.disk_bytes || 0),
        class: "ranked-meter"
      });
      list.appendChild(el("div", { class: "ranked-row" }, [
        el("span", { class: "ranked-label", text: r.name || r.client }),
        meter,
        el("span", { class: "ranked-value", text: fmtBytes(r.disk_bytes) })
      ]));
    });
    container.appendChild(list);

    var hourly = (data && data.hourly) || [];
    if (hourly.length > 1) {
      container.appendChild(seriesChart(hourly));
    }

    var table = el("table", { class: "data" });
    table.appendChild(el("caption", { text: "Per-client detail (envelope metadata only)" }));
    var head = el("tr");
    ["Client", "Sources", "Items", "Pins", "Comments", "Links", "Site files", "Metered"]
      .forEach(function (h) { head.appendChild(el("th", { scope: "col", text: h })); });
    table.appendChild(el("thead", {}, [head]));
    var tbody = el("tbody");
    rows.forEach(function (r) {
      tbody.appendChild(el("tr", {}, [
        el("th", { scope: "row", text: r.client }),
        el("td", { text: fmtNum(r.sources) }),
        el("td", { text: fmtNum(r.items) }),
        el("td", { text: fmtNum(r.pins) }),
        el("td", { text: fmtNum(r.comments) }),
        el("td", { text: fmtNum(r.links) }),
        el("td", { text: fmtNum(r.site_files) }),
        el("td", { text: fmtBytes(r.disk_bytes) })
      ]));
    });
    table.appendChild(tbody);
    container.appendChild(table);
  }

  function statCard(value, label) {
    return el("article", { class: "stat-card" }, [
      el("p", { class: "stat-value", text: value }),
      el("p", { class: "stat-label", text: label })
    ]);
  }

  // seriesChart draws the hourly averages as an inline SVG sparkline. It is
  // hand-rolled rather than a charting library: no dependency, no global, and it
  // scales to whatever width the container reports.
  function seriesChart(hourly) {
    var W = 640, H = 140, pad = 4;
    var svgNS = "http://www.w3.org/2000/svg";
    var svg = document.createElementNS(svgNS, "svg");
    svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    svg.setAttribute("role", "img");
    svg.setAttribute("preserveAspectRatio", "none");
    svg.classList.add("series-chart");
    var first = hourly[0].hour, last = hourly[hourly.length - 1].hour;
    svg.setAttribute("aria-label",
      "Metered disk from " + first + " to " + last + ", peaking at " +
      fmtBytes(hourly.reduce(function (a, h) { return Math.max(a, h.disk_bytes || 0); }, 0)));

    var peak = hourly.reduce(function (a, h) { return Math.max(a, h.disk_bytes || 0); }, 0) || 1;
    var step = (W - pad * 2) / (hourly.length - 1);
    var points = hourly.map(function (h, i) {
      var x = pad + i * step;
      var y = H - pad - (H - pad * 2) * ((h.disk_bytes || 0) / peak);
      return [x, y];
    });

    var area = document.createElementNS(svgNS, "polygon");
    area.setAttribute("class", "series-area");
    area.setAttribute("points", points.map(function (p) { return p.join(","); }).join(" ") +
      " " + points[points.length - 1][0] + "," + (H - pad) + " " + points[0][0] + "," + (H - pad));
    svg.appendChild(area);

    var line = document.createElementNS(svgNS, "polyline");
    line.setAttribute("class", "series-line");
    line.setAttribute("fill", "none");
    line.setAttribute("points", points.map(function (p) { return p.join(","); }).join(" "));
    svg.appendChild(line);

    var fig = el("figure", { class: "series" });
    fig.appendChild(svg);
    fig.appendChild(el("figcaption", {
      class: "muted",
      text: "Hourly average disk across all clients · peak " + fmtBytes(peak) +
        " · " + first + " → " + last
    }));
    return fig;
  }

  // ---- 8. admin retention --------------------------------------------------------

  function renderRetention(container, data) {
    container.textContent = "";
    var rows = (data && data.clients) || [];
    container.appendChild(el("p", { class: "muted", text:
      "Hourly sweep, last run " + (data.last_run || "never") +
      ". Items are kept when pinned, commented on, or reachable through the link graph." }));
    var table = el("table", { class: "data" });
    var head = el("tr");
    ["Client", "Window (days)", "Pinned", "Commented items"].forEach(function (h) {
      head.appendChild(el("th", { scope: "col", text: h }));
    });
    table.appendChild(el("thead", {}, [head]));
    var tbody = el("tbody");
    rows.forEach(function (r) {
      tbody.appendChild(el("tr", {}, [
        el("th", { scope: "row", text: r.client }),
        el("td", { text: String(r.window_days) }),
        el("td", { text: fmtNum(r.pinned) }),
        el("td", { text: fmtNum(r.commented_items) })
      ]));
    });
    table.appendChild(tbody);
    container.appendChild(table);
  }

  function wireAdmin() {
    var usagePane = $("#pane-usage");
    if (!usagePane) return;

    var usageBox = $("#usage-view");
    var retentionBox = $("#retention-view");
    var loaded = false;

    function load() {
      if (loaded) return;
      loaded = true;
      api("/s/api/admin/usage").then(function (data) {
        renderUsage(usageBox, data);
      }).catch(function (err) {
        usageBox.textContent = "";
        usageBox.appendChild(el("p", { class: "error", text: err.message }));
      });
      fetch("/s/api/admin/retention", { credentials: "same-origin" })
        .then(function (r) { return r.json(); })
        .then(function (data) { renderRetention(retentionBox, data); })
        .catch(function () { /* optional panel */ });
    }

    // The admin bundle switches panes by id; hook the load to the tab click so the
    // dashboard is not fetched for an admin who never looks at it.
    var tab = document.querySelector('#tabs button[data-tab="usage"]');
    if (tab) tab.addEventListener("click", load);

    var sweep = $("#retention-sweep-btn");
    if (sweep) {
      sweep.addEventListener("click", function () {
        sweep.disabled = true;
        fetch("/s/api/admin/retention/sweep", { method: "POST", credentials: "same-origin" })
          .then(function (r) { return r.json(); })
          .then(function (data) {
            var reports = (data && data.reports) || [];
            var removed = reports.reduce(function (a, r) { return a + (r.removed || 0); }, 0);
            var kept = reports.reduce(function (a, r) {
              return a + (r.kept_pinned || 0) + (r.kept_commented || 0) + (r.kept_linked || 0);
            }, 0);
            sweep.textContent = removed + " removed, " + kept + " kept";
            loaded = false;
            load();
          })
          .catch(function (err) { sweep.textContent = "Sweep failed: " + err.message; })
          .then(function () { sweep.disabled = false; });
      });
    }
  }

  // ---- boot ------------------------------------------------------------------------

  function boot() {
    if (page === "admin") { wireAdmin(); return; }

    // Portal: keep the decrypted page in step with the items pane.
    var pane = $("#pane-items");
    if (pane) {
      var tab = document.querySelector('#tabs button[data-tab="items"]');
      if (tab) tab.addEventListener("click", function () { window.setTimeout(decorate, 60); });
      decorate();
    }
    pollTombstones();
    window.setInterval(pollTombstones, 5 * 60 * 1000);

    // Signing out must leave neither the content key nor the plaintext this tab
    // decrypted, so the handler purges both rather than just the key.
    document.addEventListener("stenella:signed-out", function () {
      if (window.stenellaCollab) window.stenellaCollab.purge();
      else clearKey();
    });

    window.stenellaCollab = {
      decorate: decorate,
      clearKey: clearKey,
      decrypt: decrypt,
      hasKey: function () { return !!passphrase; },
      // Signing out must not leave a decrypted page behind either.
      purge: function () {
        decrypted = {};
        tombstones = {};
        clearKey();
      }
    };
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();