/* stenella — vanilla app bundle.
 *
 * Drives the portal (client workspace), the super-admin console, the public
 * feed page and the shared-table page. Everything talks to stenella's own
 * /s/api/* endpoints; stenella itself proxies into atp. The four Emperor42
 * libraries (veni/vidi/vici/vini) are loaded before this file and used where
 * they add value — feature-detected so a missing library never breaks a page.
 *
 * STENELLA Timeline and Card Views Implementation
 * Frontend implementation for public feed page with timeline + card views
 * and SHEPHERD ACL integration using Source.AclClass field
 */
(function () {
  'use strict';

  // ---- tiny dom + fetch helpers --------------------------------------------
  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function safeHref(value) {
    if (value == null || String(value).trim() === '') return '';
    try {
      var parsed = new URL(String(value), document.baseURI);
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:' && parsed.protocol !== 'mailto:') return '';
      return parsed.href;
    } catch (e) {
      return '';
    }
  }

  function el(tag, attrs, children) {
    var node = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        if (k === 'class') node.className = attrs[k];
        else if (k === 'text') node.textContent = attrs[k];
        else if (k === 'href') {
          var href = safeHref(attrs[k]);
          if (href) node.setAttribute('href', href);
        } else if (k.indexOf('on') === 0 && typeof attrs[k] === 'function') node.addEventListener(k.slice(2), attrs[k]);
        else node.setAttribute(k, attrs[k]);
      });
    }
    (children || []).forEach(function (c) {
      if (typeof c === 'string') node.appendChild(document.createTextNode(c));
      else if (c) node.appendChild(c);
    });
    return node;
  }

  function btn(label, onClick, cls) {
    return el('button', { class: 'btn ' + (cls || ''), type: 'button', onclick: onClick }, [label]);
  }

  function row(children) { return el('div', { class: 'row' }, children); }
  function muted(text) { return el('p', { class: 'muted' }, [text]); }

  // shared formatters (used by both portal and admin views)
  function fmtUSD(n) { return '$' + (Math.round((n || 0) * 100) / 100).toFixed(2); }
  function fmtGB(n) { return (Math.round((n || 0) * 1000) / 1000).toFixed(3); }
  function humanSize(n) {
    n = Number(n) || 0;
    if (n < 1024) return n + ' B';
    if (n < 1048576) return (n / 1024).toFixed(1) + ' KiB';
    return (n / 1048576).toFixed(1) + ' MiB';
  }

  function api(url, opts) {
    opts = opts || {};
    return fetch(url, {
      method: opts.method || 'GET',
      headers: Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {}),
      body: opts.body,
      credentials: 'same-origin'
    }).then(function (res) {
      return res.json().catch(function () { return null; }).then(function (body) {
        if (!res.ok) throw new Error((body && body.error) || res.status + ' ' + res.statusText);
        return body || {};
      });
    });
  }
  var get = function (u) { return api(u); };
  var post = function (u, b) { return api(u, { method: 'POST', body: JSON.stringify(b || {}) }); };
  var put = function (u, b) { return api(u, { method: 'PUT', body: JSON.stringify(b || {}) }); };
  var del = function (u) { return api(u, { method: 'DELETE' }); };

  function errEl(msg) { return el('p', { class: 'error' }, ['Error: ' + msg]); }

  // modal builder — returns { root, form, close() }
  function modal(title, fields, onSubmit) {
    var overlay = el('div', { class: 'modal-overlay' });
    var form = el('form', { class: 'modal-panel panel' });
    form.appendChild(el('h2', {}, [title]));
    fields.forEach(function (f) {
      var label = el('label', {}, [f.label]);
      var input;
      if (f.kind === 'textarea') {
        input = el('textarea', { name: f.name, placeholder: f.placeholder || '' }, [f.value || '']);
      } else if (f.kind === 'select') {
        input = el('select', { name: f.name });
        (f.options || []).forEach(function (o) {
          var opt = el('option', { value: o.value }, [o.label]);
          if (String(o.value) === String(f.value)) opt.selected = true;
          input.appendChild(opt);
        });
      } else if (f.kind === 'checkbox') {
        input = el('input', { type: 'checkbox', name: f.name });
        if (f.value) input.checked = true;
      } else {
        input = el('input', { type: (f.kind || 'text'), name: f.name, value: f.value || '', placeholder: f.placeholder || '' });
      }
      label.appendChild(input);
      form.appendChild(label);
    });
    var actions = row([
      el('button', { class: 'btn primary', type: 'submit' }, ['Save']),
      el('button', { class: 'btn ghost', type: 'button', onclick: close }, ['Cancel'])
    ]);
    form.appendChild(actions);
    overlay.appendChild(form);
    document.body.appendChild(overlay);
    var handle = { form: form, close: close };
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var data = {};
      fields.forEach(function (f) {
        var input = form.elements[f.name];
        if (!input) return;
        data[f.name] = (f.kind === 'checkbox') ? input.checked : input.value;
      });
      try {
        var result = onSubmit(data, handle);
        // Auto-close on successful promise resolution
        if (result && typeof result.then === 'function') {
          result.then(function () {
            close();
          }).catch(function (e) {
            // Don't close on error, let user see the alert
          });
        }
      } catch (e) {
        alert(e.message);
      }
    });
    $('input,select,textarea', form).focus();
    function close() { if (overlay.parentNode) overlay.parentNode.removeChild(overlay); }
    return handle;
  }

  function confirmDialog(msg, onYes) {
    var ok = window.confirm(msg);
    if (ok && onYes) onYes();
  }

  // ---- veni: discover + register custom elements ----------------------------
  function initVeni() {
    if (!window.veni) {
      // Native fallback keeps cards working even before the library lands.
      initItemCard();
      return;
    }
    if (typeof window.veni.init === 'function') {
      try { window.veni.init(); } catch (e) { /* library pre-fix: ignore */ }
    }
    var define = window.veni.define || window.veni.register || null;
    if (typeof define === 'function' && !customElements.get('x-item-card')) {
      try { define('x-item-card', ItemCard); } catch (e) { initItemCard(); }
    } else {
      initItemCard();
    }
  }

  var ItemCard = /** @class */ (function () {
    function ItemCard() { return Reflect.construct(HTMLElement, [], ItemCard); }
    Object.setPrototypeOf(ItemCard.prototype, HTMLElement.prototype);
    Object.setPrototypeOf(ItemCard, HTMLElement);
    ItemCard.prototype.connectedCallback = function () {
      this.innerHTML = '';
      var a = el('a', { href: this.getAttribute('link') || '#', target: '_blank', rel: 'noopener' }, [this.getAttribute('title') || '']);
      this.appendChild(a);
      var m = el('p', { class: 'muted' }, [esc(this.getAttribute('source') || '') + ' · ' + esc(this.getAttribute('when') || '')]);
      this.appendChild(m);
      if (this.getAttribute('summary')) this.appendChild(el('p', { class: 'summary' }, [this.getAttribute('summary')]));
    };
    return ItemCard;
  })();

  function initItemCard() {
    if (customElements.get('x-item-card')) return;
    try { customElements.define('x-item-card', ItemCard); } catch (e) { /* ignore */ }
  }

  // ---- vici: cookie helpers routed through the library when present ----------
  function viciGet(k, dflt) {
    try {
      if (window.vici && typeof window.vici.cookieGet === 'function') {
        var v = window.vici.cookieGet(k);
        return v == null || v === '' ? dflt : v;
      }
    } catch (e) { /* ignore */ }
    var m = document.cookie.match(new RegExp('(?:^|; )' + k + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : dflt;
  }

  function viciSet(k, v, days) {
    try {
      if (window.vici && typeof window.vici.cookieSet === 'function') {
        window.vici.cookieSet(k, v, days);
        return;
      }
    } catch (e) { /* ignore */ }
    var d = new Date(); d.setTime(d.getTime() + (days || 365) * 86400000);
    document.cookie = k + '=' + encodeURIComponent(v) + '; path=/; expires=' + d.toUTCString() + '; SameSite=Lax';
  }

  var page = document.body.dataset.page;

  // ===========================================================================
  // Home: search / link interface
  // ===========================================================================
  function initHome() {
    var q = $('#q');
    if (!q) return;
    var cards = $$('#cards .card');
    q.addEventListener('input', function () {
      var t = q.value.trim().toLowerCase();
      cards.forEach(function (c) {
        var hay = (c.getAttribute('data-search') || '').toLowerCase();
        c.style.display = (!t || hay.indexOf(t) !== -1) ? '' : 'none';
      });
    });
  }

  // ===========================================================================
  // Public feed page: lazy pagination
  // ===========================================================================
  // initFeed drives "Load more" on the public feed page. The list itself is
  // rendered by the server and then re-rendered as cards by initTimeline, so
  // this only has to append a further page to whatever list is on the page.
  function initFeed() {
    var client = document.body.dataset.client;
    var more = $('#feed-more'), list = $('#timeline-items');
    if (!client || !more || !list) return;
    var next = 2;
    more.addEventListener('click', function () {
      get('/s/feed/' + encodeURIComponent(client) + '/items?page=' + next).then(function (data) {
        (data.items || []).forEach(function (it) {
          list.appendChild(el('li', { class: 'feed-item', 'data-id': it.id }, [
            el('h2', {}, [el('a', { href: it.link, target: '_blank', rel: 'noopener' }, [it.title])]),
            muted(esc(it.source_name || '') + ' · ' + esc(it.published || '')),
            el('p', { class: 'summary' }, [esc(it.summary || '')])
          ]));
        });
        next += 1;
        if (!data.has_more) more.parentNode.removeChild(more);
      }).catch(function (e) { alert(e.message); });
    });
  }

  // ===========================================================================
  // STENELLA Timeline and Card Views: Semantic HTML with SHEPHERD ACL integration
  // ===========================================================================
  function initTimeline() {
    var client = document.body.dataset.client;
    var timelineStatus = $('#timeline-status');
    var timelineItems = $('#timeline-items');
    var sourceList = $('#source-list');
    var filterBtns = $$('.filter-btn');

    if (!client || !timelineItems) return;

    var currentFilter = 'all';
    var feedItems = [];

    function statusLabel(status) {
      return {
        'defined': 'Not started',
        'running': 'Running',
        'paused': 'Paused',
        'done': 'Complete',
        'ended': 'Ended'
      }[status] || status;
    }

    function loadFeedItems() {
      fetch('/s/feed/' + encodeURIComponent(client) + '/items')
        .then(function(response) {
          if (!response.ok) throw new Error('HTTP ' + response.status);
          return response.json();
        })
        .then(function(data) {
          onFeedItemsLoaded(data.items || []);
        })
        .catch(function(error) {
          if (timelineStatus) timelineStatus.textContent = 'Error loading items: ' + error.message;
          console.error('Failed to load feed items:', error);
        });
    }

    function onFeedItemsLoaded(items) {
      feedItems = items;
      renderTimeline();
      if (timelineStatus) timelineStatus.textContent = 'Loaded ' + items.length + ' items';

      // Calculate filter counts
      updateFilterCountsUI(items);
    }

    function updateFilterCountsUI(items) {
      var allBtn = document.querySelector('.filter-all');
      var publicBtn = document.querySelector('.filter-public');
      var privateBtn = document.querySelector('.filter-private');
      var protectedBtn = document.querySelector('.filter-protected');

      if (allBtn) {
        var allText = 'All';
        var allCount = items.length;
        if (allCount > 0) allText = 'All (' + allCount + ')';
        allBtn.textContent = allText;
      }
      if (publicBtn) {
        var publicCount = items.filter(function(item) {
          return item.source && item.source.acl_class === 'public';
        }).length;
        var publicText = 'Public';
        if (publicCount > 0) publicText = 'Public (' + publicCount + ')';
        publicBtn.textContent = publicText;
      }
      if (privateBtn) {
        var privateCount = items.filter(function(item) {
          return item.source && item.source.acl_class === 'private';
        }).length;
        var privateText = 'Private';
        if (privateCount > 0) privateText = 'Private (' + privateCount + ')';
        privateBtn.textContent = privateText;
      }
      if (protectedBtn) {
        var protectedCount = items.filter(function(item) {
          return item.source && item.source.acl_class === 'protected';
        }).length;
        var protectedText = 'Protected';
        if (protectedCount > 0) protectedText = 'Protected (' + protectedCount + ')';
        protectedBtn.textContent = protectedText;
      }
    }

    function renderTimeline() {
      if (!timelineItems) return;

      var filteredItems = feedItems;
      if (currentFilter !== 'all') {
        filteredItems = feedItems.filter(function(item) {
          return item.source && item.source.acl_class === currentFilter;
        });
      }

      timelineItems.innerHTML = '';

      if (filteredItems.length === 0) {
        timelineItems.innerHTML = '<p class="empty">No items found for selected filter.</p>';
        return;
      }

      // Sort by published date (newest first)
      filteredItems.sort(function(a, b) {
        var dateA = a.published ? new Date(a.published) : new Date(0);
        var dateB = b.published ? new Date(b.published) : new Date(0);
        return dateB - dateA;
      });

      filteredItems.forEach(function(item) {
        timelineItems.appendChild(createTimelineCard(item));
      });

      // Update source list
      updateSourceList(filteredItems);
    }

    // The list is an <ol>, so each entry is an <li> wrapping the <article>.
    // Plan §5.1 asks for a card timeline; keeping the list item as the direct
    // child is what makes it valid markup rather than a styled-up invalid one.
    function createTimelineCard(item) {
      var row = document.createElement('li');
      row.className = 'feed-item';
      if (item.id) row.setAttribute('data-id', item.id);

      var card = document.createElement('article');
      card.className = 'card';

      if (item.source && item.source.acl_class) {
        card.classList.add('acl-' + item.source.acl_class);
      }

      var header = document.createElement('header');
      header.className = 'card-header';

      var title = document.createElement('h3');
      title.textContent = item.title || 'Untitled';

      var meta = document.createElement('div');
      meta.className = 'card-meta';

      if (item.source) {
        var sourceInfo = document.createElement('span');
        sourceInfo.className = 'source-info';
        sourceInfo.textContent = item.source.name || 'Unknown Source';

        if (item.source.acl_class) {
          var aclBadge = document.createElement('span');
          aclBadge.className = 'acl-badge acl-' + item.source.acl_class;
          aclBadge.textContent = item.source.acl_class.toUpperCase();
          sourceInfo.appendChild(aclBadge);
        }

        meta.appendChild(sourceInfo);
      }

      if (item.published) {
        var time = document.createElement('time');
        time.dateTime = item.published;
        time.textContent = formatDate(item.published);
        meta.appendChild(time);
      }

      header.appendChild(title);
      header.appendChild(meta);
      card.appendChild(header);

      if (item.summary || item.content) {
        var body = document.createElement('div');
        body.className = 'card-body';

        var content = document.createElement('p');
        content.textContent = item.summary || item.content || '';
        body.appendChild(content);
        card.appendChild(body);
      }

      var footer = document.createElement('footer');
      footer.className = 'card-footer';

      if (item.link) {
        var linkBtn = document.createElement('a');
        linkBtn.href = item.link;
        linkBtn.target = '_blank';
        linkBtn.rel = 'noopener';
        // The shared stylesheet's button class is .btn.primary, not
        // .btn-primary; the latter matched nothing and rendered as bare text.
        linkBtn.className = 'btn primary';
        linkBtn.textContent = 'Read more »';
        footer.appendChild(linkBtn);
      }

      if (item.source && item.source.url) {
        var sourceUrl = document.createElement('a');
        sourceUrl.href = item.source.url;
        sourceUrl.target = '_blank';
        sourceUrl.rel = 'noopener';
        sourceUrl.className = 'source-url';
        sourceUrl.textContent = '(' + item.source.url + ')';
        footer.appendChild(sourceUrl);
      }

      card.appendChild(footer);
      row.appendChild(card);
      return row;
    }

    function updateSourceList(items) {
      if (!sourceList) return;

      var sources = [];
      var index = {};
      items.forEach(function(item) {
        if (!item.source || !item.source.name) return;
        var key = item.source.name + '\u0000' + (item.source.acl_class || 'public');
        var seen = index[key];
        if (seen) {
          // Tally in place. The original counted every source at 1, so the
          // sidebar reported "1" for a feed with fifty items.
          seen.count += 1;
          return;
        }
        var entry = {
          name: item.source.name,
          url: item.source.url || '',
          acl_class: item.source.acl_class || 'public',
          count: 1
        };
        index[key] = entry;
        sources.push(entry);
      });

      if (sources.length === 0) {
        sourceList.innerHTML = '<p>No sources</p>';
        return;
      }

      sourceList.innerHTML = '';
      sources.forEach(function(source) {
        var sourceItem = document.createElement('div');
        sourceItem.className = 'source-item';

        var name = document.createElement('span');
        name.className = 'source-name';
        name.textContent = source.name;

        var aclBadge = document.createElement('span');
        aclBadge.className = 'acl-badge acl-' + source.acl_class;
        aclBadge.textContent = source.acl_class.toUpperCase();

        var count = document.createElement('span');
        count.className = 'source-count';
        count.textContent = source.count;

        sourceItem.appendChild(name);
        sourceItem.appendChild(aclBadge);
        sourceItem.appendChild(count);

        sourceList.appendChild(sourceItem);
      });
    }

    function formatDate(isoString) {
      var date = new Date(isoString);
      var now = new Date();
      var diffTime = Math.abs(now - date);
      var diffDays = Math.ceil(diffTime / (1000 * 60 * 60 * 24));

      if (diffDays === 0) return 'Today';
      if (diffDays === 1) return 'Yesterday';
      if (diffDays < 7) return diffDays + ' days ago';

      return date.toLocaleDateString();
    }

    filterBtns.forEach(function(btn) {
      btn.addEventListener('click', function(e) {
        e.preventDefault();
        currentFilter = this.dataset.filter;
        updateFilterUI();
        renderTimeline();
      });
    });

    function updateFilterUI() {
      filterBtns.forEach(function(btn) {
        var btnFilter = btn.dataset.filter;
        if (btnFilter) {
          btn.classList.toggle('active', btnFilter === currentFilter);
        }
      });
    }

    currentFilter = 'all';
    updateFilterUI();
    if (timelineStatus) loadFeedItems();
  }

  // ===========================================================================
  // Share page: table shares render through vidi
  // ===========================================================================
  function initShare() {
    var kind = document.body.dataset.kind;
    var jsonURL = document.body.dataset.json;
    if (kind !== 'table' || !jsonURL) return;
    var VidiClass = window.Vidi || window.vidi;
    if (typeof VidiClass !== 'function') {
      var msg = 'Shared tables need the vidi library (reload served from /s/static/lib/vidi.js).';
      var cont = $('#vidi-cards-container');
      if (cont) cont.appendChild(muted(msg));
      return;
    }
    try {
      new VidiClass({ dataSource: jsonURL });
    } catch (e) {
      var box = $('#vidi-cards-container');
      if (box) box.appendChild(errEl(e.message));
    }
  }

  // ===========================================================================
  // Client portal
  // ===========================================================================
  function initPortal() {
    var client = document.body.dataset.client || '';
    var loginBtn = $('#login-btn'), loginForm = $('#login-form'), badge = $('#id-badge');
    var logoutBtn = $('#logout-btn');
    var tabs = $('#tabs'), label = $('#id-label');

    if (!client) {
      label.textContent = 'No client selected — append ?client=<id> to the URL.';
      return;
    }
    var qs = encodeURIComponent(client);

    function loadAll() { loadFeeds(); loadItems(1); loadLinks(); loadShares(); loadTables(); loadSites(); loadSecrets(); loadBilling(); loadPayment(); loadKeysList(); }

    // identity ---------------------------------------------------------------
    function refreshIdentity() {
      return get('/s/api/portal/whoami?client=' + qs).then(function (who) {
        badge.textContent = who.role === 'admin' ? 'super admin' : (who.client.id);
        badge.classList.remove('hidden');
        loginBtn.classList.add('hidden');
        // Sign out appears only once there is a session to end.
        if (logoutBtn) logoutBtn.classList.remove('hidden');
        label.textContent = (who.client.name || who.client.id) + ' — ' + (who.silo_url || '/c/' + client + '/');
        tabs.classList.remove('hidden');
        return true;
      }).catch(function () {
        badge.classList.add('hidden');
        loginBtn.classList.remove('hidden');
        if (logoutBtn) logoutBtn.classList.add('hidden');
        tabs.classList.add('hidden');
        return false;
      });
    }

    loginBtn.addEventListener('click', function () { loginForm.classList.remove('hidden'); });
    $('#login-cancel').addEventListener('click', function () { loginForm.classList.add('hidden'); });
    loginForm.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var c = $('#login-client').value.trim() || client;
      post('/s/api/client/login', { client: c, secret: $('#login-secret').value })
        .then(function () {
          viciSet('stenella_last_client', c, 365);
          loginForm.classList.add('hidden');
          return refreshIdentity();
        })
        .then(function (ok) { if (ok) loadAll(); })
        .catch(function (e) { alert('Sign in failed: ' + e.message); });
    });
    var last = viciGet('stenella_last_client', '');
    if (last && !client) { /* URL wins; nothing to do */ }

    // tabs -------------------------------------------------------------------
    $$('#tabs button').forEach(function (b) {
      b.addEventListener('click', function () {
        $$('#tabs button').forEach(function (x) { x.classList.toggle('active', x === b); });
        $$('.pane').forEach(function (p) { p.classList.toggle('active', p.id === 'pane-' + b.getAttribute('data-tab')); });
      });
    });

    // feeds ------------------------------------------------------------------
    function loadFeeds() {
      var box = $('#feeds-list');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/feeds?client=' + qs).then(function (data) {
        box.innerHTML = '';
        if (!data.sources.length) { box.appendChild(muted('No sources yet — add one below.')); return; }
        data.sources.forEach(function (src) {
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [src.name || src.url, src.enabled ? '' : ' (paused)']),
            muted(esc(src.url) + ' · ' + esc(src.kind || '') + ' · every ' + (src.interval_min || 15) + ' min'),
            row([
              btn('Fetch now', function () {
                post('/s/api/portal/feeds/' + encodeURIComponent(src.id) + '/fetch?client=' + qs)
                  .then(function (r) {
                    loadFeeds();
                    loadItems(1);
                    flash('Fetched ' + r.items + ' item(s) from ' + r.source, true);
                  }).catch(function (e) { flash(e.message, false); });
              }, 'small'),
              btn(src.enabled ? 'Pause' : 'Enable', function () {
                put('/s/api/portal/feeds/' + encodeURIComponent(src.id) + '?client=' + qs, { enabled: !src.enabled })
                  .then(loadFeeds).catch(function (e) { flash(e.message, false); });
              }, 'small'),
              btn('Delete', function () {
                confirmDialog('Delete source ' + src.name + '?', function () {
                  del('/s/api/portal/feeds/' + encodeURIComponent(src.id) + '?client=' + qs)
                    .then(loadFeeds).catch(function (e) { flash(e.message, false); });
                });
              }, 'small danger')
            ])
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }

    // public feed ---------------------------------------------------------------
    var publicFeedPage = 1;
    function loadPublicFeed(reset) {
      if (reset) publicFeedPage = 1;
      var box = $('#public-feed-list'), moreWrap = $('#public-feed-more');
      var client = document.body.dataset.client || '';
      if (!client) return;
      get('/s/feed/' + encodeURIComponent(client) + '/items?page=' + publicFeedPage).then(function (data) {
        if (reset) box.innerHTML = '';
        (data.items || []).forEach(function (it) {
          box.appendChild(el('article', { class: 'feed-item', 'data-id': it.id }, [
            el('h2', {}, [el('a', { href: it.link, target: '_blank', rel: 'noopener', text: it.title })]),
            muted(esc(it.source_name || '') + ' · ' + esc(it.published || '')),
            el('p', { class: 'summary' }, [esc(it.summary || '')])
          ]));
        });
        moreWrap.classList.toggle('hidden', !data.has_more);
        publicFeedPage += 1;
      }).catch(function (e) { box.appendChild(errEl(e.message)); });
    }
    $('#public-feed-next').addEventListener('click', function () { loadPublicFeed(false); });

    // Load public feed when tab is clicked
    $$('#tabs button[data-tab="public"]').forEach(function (b) {
      b.addEventListener('click', function () { loadPublicFeed(true); });
    });

    $('#add-feed-btn').addEventListener('click', function () {
      modal('Add feed source', [
        { name: 'url', label: 'Feed URL (RSS/Atom/JSON/OPML)', placeholder: 'https://…' },
        { name: 'name', label: 'Display name (optional)' },
        { name: 'interval_min', label: 'Refresh interval (minutes)', value: '15' }
      ], function (data) {
        return post('/s/api/portal/feeds?client=' + qs, {
          url: data.url, name: data.name, interval_min: parseInt(data.interval_min, 10) || 15
        }).then(function () { loadFeeds(); }).then(null, function (e) { alert(e.message); });
      });
    });

    // items ------------------------------------------------------------------
    // The portal items endpoint returns envelopes, not plaintext: the server
    // holds the content key but does not spend it on the wire. So this renderer
    // only lays out the envelope metadata and leaves the headline and summary
    // empty; collaboration.js decrypts them in and fills the gaps, and owns the
    // search box as well. The two halves are separate on purpose — app.js stays
    // usable on pages where there is no key at all.
    var itemsPage = 1, itemsTotal = 0;
    function loadItems(reset) {
      if (reset) itemsPage = 1;
      var box = $('#items-list'), moreWrap = $('#items-more');
      get('/s/api/portal/items?client=' + qs + '&page=' + itemsPage + '&pageSize=20').then(function (data) {
        if (reset) box.innerHTML = '';
        (data.items || []).forEach(function (it) {
          // <article>, not <li>: #items-list is a div, and a list item outside a
          // list is invalid markup that assistive technology drops.
          var node = el('article', { class: 'feed-item', 'data-id': it.id }, [
            el('h2', {}, [el('a', {
              href: it.link || '#', target: '_blank', rel: 'noopener',
              text: it.link ? it.link.replace(/^https?:\/\//, '').slice(0, 48) : '(no link)'
            })]),
            muted(esc(it.source_name || '') + ' · ' + esc(it.published || '') +
              (it.author ? ' · ' + esc(it.author) : '') + ' · ' +
              (it.acl_class || 'public') +
              (it.content_bytes ? ' · ' + it.content_bytes + ' B' : '')),
            el('p', { class: 'summary' }, []),
            row([btn('Link…', function () { openLinkDialog(it); }, 'small')])
          ]);
          box.appendChild(node);
        });
        itemsTotal = data.total || 0;
        moreWrap.classList.toggle('hidden', !data.has_more);
        itemsPage += 1;
        // Hand the envelopes to the collaboration bundle. The alternative — it
        // re-fetching the page to learn the same ciphertext — would be a second
        // request per page for data this call already had.
        if (window.stenellaApp) window.stenellaApp.setItems(data.items || []);
        if (window.stenellaCollab) window.stenellaCollab.decorate();
      }).catch(function (e) { box.appendChild(errEl(e.message)); });
    }
    $('#items-next').addEventListener('click', function () { loadItems(false); });
    $('#refresh-feed-btn').addEventListener('click', function () {
      post('/s/api/portal/refresh?client=' + qs).then(function () {
        $('#items-list').innerHTML = '';
        loadItems(true);
        flash('Refreshed all sources.', true);
      }).catch(function (e) { flash(e.message, false); });
    });

    // links ------------------------------------------------------------------
    function loadLinks() {
      var box = $('#links-list');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/links?client=' + qs).then(function (data) {
        box.innerHTML = '';
        if (!data.links.length) { box.appendChild(muted('No links between feed elements yet.')); return; }
        data.links.forEach(function (ln) {
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [ln.label || (ln.from_title + ' ↔ ' + ln.to_title)]),
            muted(ln.from_title + ' → ' + (ln.to_kind === 'url' ? ln.to_url : ln.to_title) + ' (' + ln.relation + ')'),
            row([btn('Delete', function () {
              confirmDialog('Delete this link?', function () {
                del('/s/api/portal/links/' + encodeURIComponent(ln.id) + '?client=' + qs).then(loadLinks).catch(function (e) { alert(e.message); });
              });
            }, 'small danger')])
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }

    function openLinkDialog(fromItem) {
      modal('New link', [
        { name: 'from_id', label: 'From item id', value: fromItem.id },
        { name: 'to_kind', label: 'Second end', kind: 'select', options: [{ value: 'item', label: 'Another feed item' }, { value: 'url', label: 'External URL' }] },
        { name: 'to_id', label: 'To item id (when item)' },
        { name: 'to_url', label: 'External URL (when url)', placeholder: 'https://…' },
        { name: 'relation', label: 'Relation', value: 'related' },
        { name: 'label', label: 'Label (optional)' }
      ], function (data) {
        return post('/s/api/portal/links?client=' + qs, data).then(function () {
          loadLinks();
        }).then(null, function (e) { alert(e.message); });
      });
    }

    $('#add-link-btn').addEventListener('click', function () { openLinkDialog(null); });

    // Signing out has to reach two places: the server session, and the browser's
    // copy of the content key. The event is how the second one happens — the key
    // bundle owns the storage, so it does the clearing.
    if (logoutBtn) {
      logoutBtn.addEventListener('click', function () {
        post('/s/api/client/logout?client=' + qs).then(function () {
          document.dispatchEvent(new CustomEvent('stenella:signed-out', { detail: { client: qs } }));
          location.reload();
        }).catch(function (e) { alert(e.message); });
      });
    }

    // The pieces the collaboration bundle needs but should not reimplement: the
    // current page of envelopes, the link dialog (the graph is the links
    // store's, so there is one dialog), and the modal helper it has no reason to
    // reinvent.
    window.stenellaApp = {
      items: [],
      setItems: function (list) { this.items = list; },
      openLinkDialog: openLinkDialog
    };

    // shares -----------------------------------------------------------------
    function loadShares() {
      var box = $('#shares-list');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/shares?client=' + qs).then(function (data) {
        box.innerHTML = '';
        if (!data.shares.length) { box.appendChild(muted('No public shares yet — share an item, a link or a whole table.')); return; }
        data.shares.forEach(function (sh) {
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [sh.title]),
            muted('kind ' + sh.kind + ' · target ' + sh.target + (sh.expires ? ' · expires ' + sh.expires : '')),
            row([
              el('a', { class: 'btn small', href: sh.url, target: '_blank', rel: 'noopener' }, ['Open']),
              btn('JSON', function () {
                var ta = modal('Share JSON URL', [{ name: 'j', label: 'Copy this JSON URL (vidi dataSource)', value: sh.json_url }], function () {});
                setTimeout(ta.close, 60000);
              }, 'small'),
              btn('Delete', function () {
                confirmDialog('Revoke this share?', function () {
                  del('/s/api/portal/shares/' + encodeURIComponent(sh.id) + '?client=' + qs).then(loadShares).catch(function (e) { alert(e.message); });
                });
              }, 'small danger')
            ])
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }

    $('#add-share-btn').addEventListener('click', function () {
      modal('New share', [
        { name: 'kind', label: 'What to share', kind: 'select', options: [
          { value: 'item', label: 'Feed item' }, { value: 'link', label: 'A link between elements' }, { value: 'table', label: 'Whole table' }] },
        { name: 'target', label: 'Target (item id, link id, or table name)' },
        { name: 'title', label: 'Title' },
        { name: 'days', label: 'Expire after days (0 = never)', value: '30' }
      ], function (data) {
        return post('/s/api/portal/shares?client=' + qs, data).then(function (out) {
          loadShares();
          window.alert('Share ready!\n' + out.share.url);
        }).then(null, function (e) { alert(e.message); });
      });
    });

    // database ---------------------------------------------------------------
    var dbTables = $('#db-tables');
    var dbNewTableBtn = $('#db-new-table-btn');
    var dbBulkBtn = $('#db-bulk-btn');

    // Creating a table is the one schema-changing call the portal makes, so it
    // validates the name client-side too and reports the server's verdict
    // verbatim rather than swallowing it.
    function createTable() {
      modal('New table', [
        { name: 'table', label: 'Table name (letters, digits and dashes)', placeholder: 'products' },
        { name: 'columns', label: 'Columns (comma separated)', value: 'id, name', placeholder: 'id, name, price' }
      ], function (data, handle) {
        var cols = (data.columns || '').split(',').map(function (c) { return c.trim(); }).filter(Boolean);
        if (!data.table || !data.table.trim()) return alert('A table name is required.');
        if (!cols.length) return alert('At least one column is required.');
        return post('/s/api/portal/db/table/create?client=' + qs, { table: data.table.trim(), columns: cols })
          .then(function () {
            handle.close();
            return loadTables().then(function () {
              dbTables.value = data.table.trim();
              return loadRecords();
            });
          }).then(null, function (e) { alert(e.message); });
      });
    }

    // Bulk import accepts a JSON array of objects. Column names come from the
    // first row, which matches how the table was created.
    function bulkImport() {
      var table = dbTables.value;
      if (!table) { alert('Pick a table first.'); return; }
      var handle = modal('Bulk import into ' + table, [
        { name: 'rows', label: 'JSON array of objects', kind: 'textarea', value: '[\n  {"id": "1", "name": "first"}\n]' }
      ], function (data) {
        var parsed;
        try {
          parsed = JSON.parse(data.rows);
        } catch (err) {
          return alert('That is not valid JSON: ' + err.message);
        }
        if (!Array.isArray(parsed) || !parsed.length) return alert('Provide a non-empty JSON array of objects.');
        var bad = parsed.findIndex(function (r) { return !r || typeof r !== 'object' || Array.isArray(r); });
        if (bad >= 0) return alert('Row ' + (bad + 1) + ' is not an object.');
        return post('/s/api/portal/db/table/bulk?client=' + qs + '&table=' + encodeURIComponent(table),
          { records: parsed })
          .then(function (out) {
            handle.close();
            alert('Imported ' + out.inserted + ' record(s) into ' + table + '.');
            return loadTables().then(loadRecords);
          }).then(null, function (e) { alert(e.message); });
      });
    }

    if (dbNewTableBtn) dbNewTableBtn.addEventListener('click', createTable);
    if (dbBulkBtn) dbBulkBtn.addEventListener('click', bulkImport);

    function loadTables() {
      return get('/s/api/portal/db/tables?client=' + qs).then(function (data) {
        dbTables.innerHTML = '';
        (data.tables || []).forEach(function (t) {
          dbTables.appendChild(el('option', { value: t.name }, t.name + ' (' + t.count + ')'));
        });
        if (data.tables && data.tables.length) loadRecords();
      }).catch(function (e) { });
    }
    function loadRecords() {
      var table = dbTables.value;
      if (!table) return;
      var box = $('#db-records');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/db/table?client=' + qs + '&table=' + encodeURIComponent(table) + '&page=1&pageSize=50' + ($('#db-q').value ? '&q=' + encodeURIComponent($('#db-q').value) : '')).then(function (data) {
        box.innerHTML = '';
        var recs = data.records || [];
        if (!recs.length) { box.appendChild(muted('No records in ' + table + '.')); return; }
        var cols = Object.keys(recs[0]);
        var tableEl = el('table', { class: 'data' });
        var thead = el('thead', {}, [el('tr', {}, cols.map(function (c) { return el('th', {}, [c]); }))]);
        tableEl.appendChild(thead);
        var tbody = el('tbody');
        recs.forEach(function (rec) {
          var tr = el('tr', {}, cols.map(function (c) {
            return el('td', {}, [truncate(esc(rec[c] || ''), 120)]);
          }));
          tr.appendChild(el('td', {}, [btn('✕', function () {
            confirmDialog('Delete record ' + rec.id + '?', function () {
              del('/s/api/portal/db/record?client=' + qs + '&table=' + encodeURIComponent(table) + '&id=' + encodeURIComponent(rec.id))
                .then(loadRecords).catch(function (e) { alert(e.message); });
            });
          }, 'small danger')]));
          tbody.appendChild(tr);
        });
        tableEl.appendChild(tbody);
        box.appendChild(tableEl);
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }
    function truncate(s, n) { return s.length > n ? s.slice(0, n) + '…' : s; }
    dbTables.addEventListener('change', loadRecords);
    $('#db-q').addEventListener('keyup', function (e) { if (e.key === 'Enter') loadRecords(); });
    $('#db-add-btn').addEventListener('click', function () {
      var table = dbTables.value;
      if (!table) { alert('Pick a table first.'); return; }
      modal('Add record to ' + table, [
        { name: 'id', label: 'ID (leave blank to generate)' },
        { name: 'dup', label: 'Field name (first row)' },
        { name: 'val', label: 'Value (first row)' }
      ], function (data) {
        var fields = {};
        if (data.id) fields.id = data.id;
        if (data.dup) fields[data.dup] = data.val;
        return post('/s/api/portal/db/table?client=' + qs + '&table=' + encodeURIComponent(table), { table: table, fields: fields })
          .then(loadRecords).then(null, function (e) { alert(e.message); });
      });
    });

    // sites ------------------------------------------------------------------
    function loadSites() {
      var box = $('#sites-list');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/sites/meta?client=' + qs).then(function (meta) {
        if (meta.files === 0) {
          box.appendChild(muted('No hosted files yet. Your site lives at ' + meta.public_url + '.'));
        } else {
          box.appendChild(muted(meta.files + ' file(s) hosted at ' + meta.public_url + '.'));
        }
        return get('/s/api/portal/sites/files?client=' + qs);
      }).then(function (data) {
        (data.files || []).forEach(function (f) {
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [esc(f.name || f.path || '')]),
            muted(esc(f.path || '') + ' · ' + esc(f.content_type || '') + ' · ' + humanSize(f.size || 0) + (f.encrypted ? ' · encrypted' : '')),
            row([
              btn('Edit', function () { editFile(f.path); }, 'small'),
              btn('Delete', function () {
                confirmDialog('Delete ' + f.path + '?', function () {
                  del('/s/api/portal/sites/file?client=' + qs + '&path=' + encodeURIComponent(f.path || f.name))
                    .then(loadSites).catch(function (e) { alert(e.message); });
                });
              }, 'small danger')
            ])
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }
    function editFile(path) {
      modal('Edit ' + path, [
        { name: 'path', label: 'Path', value: path },
        { name: 'content', label: 'Content', kind: 'textarea' },
        { name: 'encrypt', label: 'Encrypt text nodes', kind: 'checkbox' }
      ], function (data) {
        return put('/s/api/portal/sites/file?client=' + qs, { path: data.path, content: data.content, encrypt: data.encrypt, overwrite: true })
          .then(loadSites).then(null, function (e) { alert(e.message); });
      });
    }
    $('#add-file-btn').addEventListener('click', function () {
      modal('New hosted file', [
        { name: 'path', label: 'Path (e.g. index.html)', value: 'index.html' },
        { name: 'content', label: 'Content', kind: 'textarea', value: '<!DOCTYPE html>\n<html>\n<head><title>Hello</title></head>\n<body>Hello from song.</body>\n</html>' },
        { name: 'encrypt', label: 'Encrypt text nodes', kind: 'checkbox' }
      ], function (data) {
        return post('/s/api/portal/sites/file?client=' + qs, { path: data.path, content: data.content, encrypt: data.encrypt })
          .then(loadSites).then(null, function (e) { alert(e.message); });
      });
    });

    // secrets ----------------------------------------------------------------
    function loadSecrets() {
      var box = $('#secrets-list');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/secrets?client=' + qs).then(function (data) {
        box.innerHTML = '';
        if (!data.secrets.length) { box.appendChild(muted('No secrets in the vault. Add an API key for an authenticated feed, or the portal secret that signs you in (managed by the super admin).')); return; }
        data.secrets.forEach(function (s) {
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [esc(s.name)]),
            muted(esc(s.note || '') + (s.set ? ' · updated ' + esc(s.set) : '')),
            row([btn('Delete', function () {
              confirmDialog('Delete secret ' + s.name + '?', function () {
                del('/s/api/portal/secrets?client=' + qs + '&name=' + encodeURIComponent(s.name)).then(loadSecrets).catch(function (e) { alert(e.message); });
              });
            }, 'small danger')])
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }
    $('#add-secret-btn').addEventListener('click', function () {
      modal('Add vault secret', [
        { name: 'name', label: 'Name (e.g. api_key, feed_token)' },
        { name: 'value', label: 'Value (plaintext; stored encrypted by atp)' },
        { name: 'note', label: 'Note' }
      ], function (data) {
        return post('/s/api/portal/secrets?client=' + qs, data).then(loadSecrets).then(null, function (e) { alert(e.message); });
      });
    });

    // billing ----------------------------------------------------------------
    function loadBilling() {
      var box = $('#billing-view');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/billing?client=' + qs).then(function (data) {
        var c = data.cost || {};
        box.appendChild(el('div', { class: 'kv' }, [
          el('dt', {}, ['Price']), el('dd', {}, [fmtUSD(data.price_per_gb_hour) + ' / GiB·h']),
          el('dt', {}, ['Current size']), el('dd', {}, [fmtGB(c.current_silo_gb) + ' GiB']),
          el('dt', {}, ['Avg size (window)']), el('dd', {}, [fmtGB(c.avg_silo_gb) + ' GiB over ' + (c.hours || 0) + ' h']),
          el('dt', {}, ['Total this window']), el('dd', {}, [fmtUSD(c.total_cost)]),
          el('dt', {}, ['Requests']), el('dd', {}, [String(c.total_requests || 0)])
        ]));
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }
    function loadPayment() {
      var box = $('#payment-view');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/portal/payment?client=' + qs).then(function (data) {
        var p = data.payment || {};
        box.innerHTML = '';
        box.appendChild(el('div', { class: 'kv' }, [
          el('dt', {}, ['Billing name']), el('dd', {}, [esc(p.name || '—')]),
          el('dt', {}, ['Email']), el('dd', {}, [esc(p.email || '—')]),
          el('dt', {}, ['Billing email']), el('dd', {}, [esc(p.billing_email || '—')]),
          el('dt', {}, ['Card (last 4)']), el('dd', {}, [esc(p.card_last4 || '—')]),
          el('dt', {}, ['Currency']), el('dd', {}, [esc(p.currency || 'USD')])
        ]));
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }
    $('#edit-payment-btn').addEventListener('click', function () {
      get('/s/api/portal/payment?client=' + qs).then(function (data) {
        var p = data.payment || {};
        modal('Payment information', [
          { name: 'name', label: 'Billing name', value: p.name },
          { name: 'email', label: 'Email', value: p.email },
          { name: 'billing_email', label: 'Billing email', value: p.billing_email },
          { name: 'card_last4', label: 'Card (last 4)', value: p.card_last4 },
          { name: 'currency', label: 'Currency', value: p.currency || 'USD' },
          { name: 'address', label: 'Address', value: p.address },
          { name: 'notes', label: 'Notes', value: p.notes }
        ], function (d) {
          return put('/s/api/portal/payment?client=' + qs, d).then(loadPayment).then(null, function (e) { alert(e.message); });
        });
      });
    });

    // security (shepherd through atp) ------------------------------------------
    function loadKeysList() {
      var box = $('#keys-list');
      box.innerHTML = ''; box.appendChild(muted('Verify tokens with the field below. Issued keys are returned once.' ));
    }
    $('#issue-key-btn').addEventListener('click', function () {
      modal('Issue capability key', [
        { name: 'subject', label: 'Subject', value: 'widget' },
        { name: 'scopes', label: 'Scopes (comma separated)' },
        { name: 'ttl', label: 'TTL (e.g. 24h)', value: '24h' }
      ], function (data) {
        post('/s/api/portal/keys?client=' + qs, { subject: data.subject, scopes: (data.scopes || '').split(',').filter(Boolean), ttl: data.ttl })
          .then(function (out) {
            window.alert('Key issued:\n' + out.token);
          }).catch(function (e) { alert(e.message); });
      });
    });

    refreshIdentity().then(function (ok) { if (ok) loadAll(); });
  }

  // ===========================================================================
  // Super admin console
  // ===========================================================================
  function initAdmin() {
    var badge = $('#id-badge'), label = $('#id-label');

    function loadSummary() {
      return get('/s/api/admin/summary').then(function (s) {
        badge.textContent = 'admin';
        badge.classList.remove('hidden');
        label.textContent = 'atp ' + (s.version || '') + ' · ' + (s.clients_total || 0) + ' clients · uptime ' + Math.round((s.uptime_seconds || 0) / 60) + ' min';
        var box = $('#summary-cards');
        box.innerHTML = '';
        box.appendChild(stat('Clients', String(s.clients_total || 0), 'chargeable: ' + (s.clients_chargeable || 0)));
        box.appendChild(stat('Revenue (window)', fmtUSD(s.total_revenue || 0), 'window'));
        box.appendChild(stat('Silo size', fmtGB(s.total_silo_gb || 0) + ' GiB', 'across all clients'));
        box.appendChild(stat('Feeds', String(Object.keys(s.feed_counts || {}).length), 'clients with sources'));
        return true;
      }).catch(function (e) {
        badge.classList.add('hidden');
        label.textContent = 'Not signed in as an atp admin — open /login (same origin) to authenticate, then reload.';
        return false;
      });
    }
    function stat(title, value, sub) {
      return el('div', { class: 'card' }, [
        el('div', { class: 'stat' }, [value]),
        el('div', { class: 'stat-label' }, [title]),
        el('div', { class: 'muted' }, [sub])
      ]);
    }

    // tabs -------------------------------------------------------------------
    $$('#tabs button').forEach(function (b) {
      b.addEventListener('click', function () {
        $$('#tabs button').forEach(function (x) { x.classList.toggle('active', x === b); });
        var paneId = 'pane-' + b.getAttribute('data-tab');
        $$('.pane').forEach(function (p) { p.classList.toggle('active', p.id === paneId); });
        if (paneId === 'pane-shepherd') loadShepherd();
      });
    });

    // income -----------------------------------------------------------------
    function loadIncome() {
      var box = $('#income-view');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/admin/income').then(function (rep) {
        box.innerHTML = '';
        var cfg = rep.config || {};
        var head = el('div', { class: 'kv' }, [
          el('dt', {}, ['Rate']), el('dd', {}, ['You charge ' + fmtUSD(rep.revenue) + ' · hosting ' + fmtUSD(rep.host_cost) + ' · net ' + fmtUSD(rep.net)]),
          el('dt', {}, ['31-day projection']), el('dd', {}, [fmtUSD(rep.projected_30d.revenue) + ' revenue · ' + fmtUSD(rep.projected_30d.host_cost) + ' hosting · ' + fmtUSD(rep.projected_30d.net) + ' net'])
        ]);
        box.appendChild(head);
        box.appendChild(el('h3', {}, ['Per-client income']));
        var t = el('table', { class: 'data' });
        var thead = el('thead', {}, [el('tr', {}, ['Client', 'Price/GiB·h', 'Avg GiB', 'Hours', 'Revenue', 'Host cost', 'Net'].map(function (c) { return el('th', {}, [c]); }))]);
        t.appendChild(thead);
        var tb = el('tbody');
        (rep.clients || []).forEach(function (cl) {
          tb.appendChild(el('tr', {}, [
            el('td', {}, [esc(cl.id)]),
            el('td', {}, [fmtUSD(cl.price_per_gb_hour)]),
            el('td', {}, [fmtGB(cl.avg_silo_gb)]),
            el('td', {}, [String(cl.hours || 0)]),
            el('td', {}, [fmtUSD(cl.revenue)]),
            el('td', {}, [fmtUSD(cl.host_cost)]),
            el('td', {}, [fmtUSD(cl.net)])
          ]));
        });
        t.appendChild(tb);
        box.appendChild(t);
        box.appendChild(el('button', { class: 'btn', type: 'button', onclick: function () {
          modal('Hosting cost model', [
            { name: 'host_price_per_gb_hour', label: 'Your infra cost per GiB·h (USD)', value: String(cfg.host_price_per_gb_hour || 0.01) },
            { name: 'fixed_cost_per_month', label: 'Fixed monthly cost (USD)', value: String(cfg.fixed_cost_per_month || 0) }
          ], function (d) {
            return put('/s/api/admin/income', { host_price_per_gb_hour: parseFloat(d.host_price_per_gb_hour) || 0, fixed_cost_per_month: parseFloat(d.fixed_cost_per_month) || 0 })
              .then(loadIncome).then(null, function (e) { alert(e.message); });
          });
        } }, ['Edit hosting cost model']));
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }

    // clients ----------------------------------------------------------------
    function loadClients() {
      var box = $('#clients-list');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/admin/clients').then(function (data) {
        box.innerHTML = '';
        (data.clients || []).forEach(function (o) {
          var c = o.client || {};
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [esc(c.name || c.id), ' ', el('a', { href: '/s/portal?client=' + encodeURIComponent(c.id) }, ['portal']), ' ', el('a', { href: '/c/' + encodeURIComponent(c.id) + '/' }, ['site'])]),
            muted(esc(c.id) + ' · ' + (c.chargeable ? fmtUSD(c.price_per_gb_hour) + '/GiB·h' : 'not chargeable') + ' · ' + (o.sources || 0) + ' sources · ' + (o.items || 0) + ' items · ' + esc(c.notes || '')),
            row([
              btn('Edit', function () { editClient(c); }, 'small'),
              btn('Secrets', function () { adminSecretsFor(c.id); }, 'small'),
              btn('Set portal login', function () { setPortalSecret(c.id); }, 'small'),
              c.disabled ? btn('Enable', function () { patchClient(c.id, { disabled: false }); }, 'small') : null,
              btn('Delete', function () {
                confirmDialog('Delete client ' + c.id + '? (data stays on disk)', function () {
                  del('/s/api/admin/client/' + encodeURIComponent(c.id)).then(loadClients).then(null, function (e) { alert(e.message); });
                });
              }, 'small danger')
            ].filter(Boolean))
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }
    function editClient(c) {
      modal('Edit client', [
        { name: 'name', label: 'Name', value: c.name },
        { name: 'notes', label: 'Notes', value: c.notes },
        { name: 'chargeable', label: 'Chargeable', kind: 'checkbox', value: !!c.chargeable },
        { name: 'disabled', label: 'Disabled', kind: 'checkbox', value: !!c.disabled },
        { name: 'price_per_gb_hour', label: 'Price per GiB·h (USD)', value: String(c.price_per_gb_hour) },
        { name: 'retention_hours', label: 'Billing window (hours)', value: String(c.retention_hours || 0) }
      ], function (d) {
        return put('/s/api/admin/client/' + encodeURIComponent(c.id), {
          name: d.name, notes: d.notes, chargeable: d.chargeable, disabled: d.disabled,
          price_per_gb_hour: parseFloat(d.price_per_gb_hour) || 0, retention_hours: parseInt(d.retention_hours, 10) || 0
        }).then(loadClients).then(null, function (e) { alert(e.message); });
      });
    }
    function patchClient(id, patch) {
      put('/s/api/admin/client/' + encodeURIComponent(id), patch).then(loadClients).catch(function (e) { alert(e.message); });
    }
    function setPortalSecret(id) {
      modal('Set portal login secret for ' + id, [
        { name: 'value', label: 'Portal secret (the client signs in with this)' }
      ], function (d) {
        return post('/s/api/admin/secrets?client=' + encodeURIComponent(id), { name: 'portal', value: d.value, note: 'portal login secret' })
          .then(loadClients).then(null, function (e) { alert(e.message); });
      });
    }
    $('#add-client-btn').addEventListener('click', function () {
      modal('New client', [
        { name: 'id', label: 'ID (letters, digits, dashes)', placeholder: 'acme' },
        { name: 'name', label: 'Name', placeholder: 'Acme Inc' },
        { name: 'notes', label: 'Notes' }
      ], function (d) {
        return post('/s/api/admin/client', { id: d.id, name: d.name, notes: d.notes }).then(function () {
          loadClients();
          window.alert('Client ' + d.id + ' created. Now add a portal secret so they can sign in.');
        }).then(null, function (e) { alert(e.message); });
      });
    });

    // feeds (bird's eye) -------------------------------------------------------
    function loadFeeds() {
      var box = $('#feeds-view');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/admin/feeds').then(function (data) {
        box.innerHTML = '';
        (data.clients || []).forEach(function (cv) {
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [esc(cv.name || cv.client), ' — ', String(cv.total_items), ' items']),
            muted(cv.sources.map(function (sv) { return esc(sv.name || sv.url) + ' (' + sv.items + ')'; }).join(' · ') || 'no sources')
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }

    // secrets (any client's vault) ---------------------------------------------
    function adminSecretsFor(clientId) {
      var box = $('#secrets-list');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/admin/secrets?client=' + encodeURIComponent(clientId)).then(function (data) {
        box.innerHTML = '';
        if (!data.secrets.length) { box.appendChild(muted('No secrets for ' + clientId + '.')); return; }
        data.secrets.forEach(function (s) {
          box.appendChild(el('div', { class: 'list-item' }, [
            el('h3', {}, [esc(s.name)]),
            muted(esc(s.note || '') + (s.set ? ' · ' + esc(s.set) : '')),
            row([btn('Delete', function () {
              confirmDialog('Delete ' + s.name + '?', function () {
                del('/s/api/admin/secrets?client=' + encodeURIComponent(clientId) + '&name=' + encodeURIComponent(s.name)).then(function () { adminSecretsFor(clientId); }).catch(function (e) { alert(e.message); });
              });
            }, 'small danger')])
          ]));
        });
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }
    $('#add-secret-btn').addEventListener('click', function () {
      var clientId = $('#secret-client').value.trim();
      if (!clientId) { alert('Enter a client id first.'); return; }
      modal('Add secret for ' + clientId, [
        { name: 'name', label: 'Name' },
        { name: 'value', label: 'Value' },
        { name: 'note', label: 'Note' }
      ], function (d) {
        return post('/s/api/admin/secrets?client=' + encodeURIComponent(clientId), d).then(function () { adminSecretsFor(clientId); }).then(null, function (e) { alert(e.message); });
      });
    });

    // settings ----------------------------------------------------------------
    function loadSettings() {
      var box = $('#settings-view');
      box.innerHTML = ''; box.appendChild(muted('Loading…'));
      get('/s/api/admin/config').then(function (cfg) {
        box.innerHTML = '';
        var settings = cfg.settings || {};
        box.appendChild(el('div', { class: 'kv' }, [
          el('dt', {}, ['Admin user']), el('dd', {}, [esc(cfg.admin_user || '')]),
          el('dt', {}, ['Port']), el('dd', {}, [esc(cfg.port || '')]),
          el('dt', {}, ['Default price']), el('dd', {}, [fmtUSD(settings.default_price_per_gb_hour || 0) + ' / GiB·h']),
          el('dt', {}, ['Billing window']), el('dd', {}, [String(settings.default_retention_hours || 0) + ' h']),
          el('dt', {}, ['Max upload']), el('dd', {}, [humanSize(settings.max_upload_bytes || 0)]),
          el('dt', {}, ['Request log cap']), el('dd', {}, [String(settings.request_log_limit_per_client || 0) + ' / client'])
        ]));
        box.appendChild(el('button', { class: 'btn', type: 'button', onclick: function () {
          modal('Platform settings', [
            { name: 'default_price_per_gb_hour', label: 'Default price per GiB·h (USD)', value: String(settings.default_price_per_gb_hour || 0.05) },
            { name: 'default_retention_hours', label: 'Default billing window (hours)', value: String(settings.default_retention_hours || 24) },
            { name: 'max_upload_bytes', label: 'Max upload (bytes)', value: String(settings.max_upload_bytes || 67108864) },
            { name: 'request_log_limit_per_client', label: 'Request log cap per client', value: String(settings.request_log_limit_per_client || 2000) }
          ], function (d) {
            return put('/s/api/admin/config', {
              default_price_per_gb_hour: parseFloat(d.default_price_per_gb_hour) || 0,
              default_retention_hours: parseInt(d.default_retention_hours, 10) || 0,
              max_upload_bytes: parseInt(d.max_upload_bytes, 10) || 0,
              request_log_limit_per_client: parseInt(d.request_log_limit_per_client, 10) || 0
            }).then(loadSettings).then(null, function (e) { alert(e.message); });
          });
        } }, ['Edit settings']));
      }).catch(function (e) { box.innerHTML = ''; box.appendChild(errEl(e.message)); });
    }

    loadSummary().then(function (ok) {
      if (!ok) return;
      loadIncome(); loadClients(); loadFeeds(); loadSettings();
    });
  }

  // ===========================================================================
  // boot
  // ===========================================================================
  switch (page) {
    case 'home': initHome(); break;
    case 'feed': initFeed(); initTimeline(); break;
    case 'share': initShare(); break;
    case 'portal': initPortal(); break;
    case 'admin': initAdmin(); break;
  }
  initVeni();
})();