// Proxier's keymap, search pop-up, keys sheet, dialogs and small widgets.
// Plain JS, no inline handlers (CSP). Every key maps to an element that is
// already on the page: data-goto, data-row, data-key, data-action.
(function () {
  'use strict';

  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  // ---- key line messages -------------------------------------------------
  var msgTimer;
  function say(text) {
    var m = $('#keymsg'), h = $('#keyhints');
    if (!m) return;
    m.textContent = text;
    m.classList.remove('hidden');
    if (h) h.classList.add('hidden');
    clearTimeout(msgTimer);
    msgTimer = setTimeout(function () {
      m.classList.add('hidden');
      if (h) h.classList.remove('hidden');
    }, 3200);
  }

  function typing(el) {
    if (!el) return false;
    var t = el.tagName;
    return t === 'INPUT' || t === 'TEXTAREA' || t === 'SELECT' || el.isContentEditable;
  }

  // ---- row cursor (j k) --------------------------------------------------
  var cursor = -1;
  function rows() { return $$('[data-row]').filter(function (r) { return r.offsetParent !== null; }); }
  function setCursor(i) {
    var rs = rows();
    if (!rs.length) return;
    i = Math.max(0, Math.min(rs.length - 1, i));
    rs.forEach(function (r) { r.classList.remove('pc-cur'); });
    rs[i].classList.add('pc-cur');
    rs[i].scrollIntoView({ block: 'nearest' });
    cursor = i;
    var h = $('#keyhints');
    if (h) h.textContent = rs[i].getAttribute('data-hint') || h.getAttribute('data-default') || '';
  }
  function currentRow() { var rs = rows(); return cursor >= 0 ? rs[cursor] : null; }

  // ---- g + letter --------------------------------------------------------
  var pendingG = false, gTimer;
  function setPending(on) {
    pendingG = on;
    var w = $('#whichkey');
    if (w) w.classList.toggle('hidden', !on);
    clearTimeout(gTimer);
    if (on) gTimer = setTimeout(function () { setPending(false); }, 1800);
  }

  // ---- pop-ups -----------------------------------------------------------
  var pal = null, palMode = 'all', palItems = [], palIdx = 0, palTimer, palSeq = 0;

  function actionItems() {
    var seen = {};
    return $$('[data-action]').map(function (el) {
      var id = el.getAttribute('data-action');
      if (seen[id]) return null;
      seen[id] = 1;
      return { title: el.textContent.trim(), meta: el.getAttribute('data-key') || '', el: el, kind: 'action' };
    }).filter(Boolean);
  }

  function openPalette(mode) {
    pal = pal || $('#palette');
    if (!pal) return;
    closeKeys();
    palMode = mode;
    pal.classList.remove('hidden');
    var input = $('#pal-input');
    input.value = '';
    $('#pal-prompt').textContent = mode === 'act' ? ':' : '/';
    input.focus();
    refreshPalette();
  }
  function closePalette() {
    if (pal && !pal.classList.contains('hidden')) {
      pal.classList.add('hidden');
      document.activeElement && document.activeElement.blur && document.activeElement.blur();
      return true;
    }
    return false;
  }

  function refreshPalette() {
    var q = $('#pal-input').value.trim().toLowerCase();
    var acts = actionItems().filter(function (a) { return !q || a.title.toLowerCase().indexOf(q) >= 0; });
    var seq = ++palSeq;
    if (palMode === 'act') { renderPalette([], acts); return; }
    renderPalette([], acts);
    clearTimeout(palTimer);
    palTimer = setTimeout(function () {
      fetch('/search?q=' + encodeURIComponent(q), { credentials: 'same-origin', headers: { 'HX-Request': 'true' } })
        .then(function (r) { return r.ok ? r.text() : ''; })
        .then(function (html) {
          if (seq !== palSeq) return;
          var doc = new DOMParser().parseFromString(html, 'text/html');
          var hits = $$('a[href]', doc).map(function (a) {
            return { title: a.textContent.trim(), meta: a.getAttribute('data-meta') || '', href: a.getAttribute('href'), kind: 'goto' };
          });
          renderPalette(hits, acts);
        })
        .catch(function () { renderPalette([], acts); });
    }, 90);
  }

  function renderPalette(hits, acts) {
    var list = $('#pal-list');
    list.textContent = '';
    palItems = [];
    function group(title, items) {
      if (!items.length) return;
      var g = document.createElement('div');
      g.className = 'pal-group';
      g.textContent = title;
      list.appendChild(g);
      items.forEach(function (it) {
        var row = document.createElement('div');
        row.className = 'pal-row pc-row';
        row.setAttribute('role', 'option');
        var c = document.createElement('span');
        c.className = 'caret'; c.setAttribute('aria-hidden', 'true'); c.textContent = '›';
        var t = document.createElement('span'); t.className = 'bold'; t.textContent = it.title;
        var m = document.createElement('span'); m.className = 'meta'; m.textContent = it.meta;
        row.appendChild(c); row.appendChild(t); row.appendChild(m);
        row.addEventListener('mousedown', function (e) { e.preventDefault(); });
        row.addEventListener('click', function () { runItem(it); });
        it.row = row;
        list.appendChild(row);
        palItems.push(it);
      });
    }
    if (palMode !== 'act') group(pal.getAttribute('data-t-goto'), hits);
    group(pal.getAttribute('data-t-actions'), acts);
    if (!palItems.length) {
      var p = document.createElement('p');
      p.textContent = pal.getAttribute('data-t-none');
      p.className = 'pal-group';
      list.appendChild(p);
    }
    palIdx = 0;
    markPalette();
  }
  function markPalette() {
    palItems.forEach(function (it, i) { it.row.classList.toggle('pc-cur', i === palIdx); });
    var cur = palItems[palIdx];
    if (cur) cur.row.scrollIntoView({ block: 'nearest' });
  }
  function runItem(it) {
    closePalette();
    if (it.kind === 'goto') { window.location.href = it.href; return; }
    it.el.click();
  }

  var keysOpen = null;
  function openKeys() {
    if (keysOpen) return;
    var t = $('#keys-template');
    if (!t) return;
    closePalette();
    document.body.appendChild(t.content.cloneNode(true));
    keysOpen = $('#keys-sheet');
    keysOpen.addEventListener('mousedown', function (e) { if (e.target === keysOpen) closeKeys(); });
    $('[data-keys-close]', keysOpen).addEventListener('click', closeKeys);
  }
  function closeKeys() {
    if (!keysOpen) return false;
    keysOpen.remove(); keysOpen = null;
    return true;
  }

  // ---- admin menu, drawer -------------------------------------------------
  function closeMenu() {
    var p = $('#admin-pop');
    if (p && !p.classList.contains('hidden')) {
      p.classList.add('hidden');
      var b = $('[data-menu-toggle]'); if (b) b.setAttribute('aria-expanded', 'false');
      return true;
    }
    return false;
  }

  // ---- clicks ------------------------------------------------------------
  document.addEventListener('click', function (e) {
    var t = e.target.closest ? e.target.closest('[data-open-palette],[data-menu-toggle],[data-drawer-toggle],[data-dialog-open],[data-dialog-close],[data-copy],[data-copy-from],[data-switch]') : null;
    var menu = $('#admin-pop');
    if (menu && !menu.classList.contains('hidden') && !(e.target.closest && e.target.closest('.admin-menu'))) closeMenu();
    if (pal && !pal.classList.contains('hidden') && e.target === pal) closePalette();
    if (!t) return;
    if (t.hasAttribute('data-open-palette')) { openPalette(t.getAttribute('data-open-palette')); return; }
    if (t.hasAttribute('data-menu-toggle')) {
      var hidden = menu.classList.toggle('hidden');
      t.setAttribute('aria-expanded', hidden ? 'false' : 'true');
      return;
    }
    if (t.hasAttribute('data-drawer-toggle')) { document.body.classList.toggle('drawer-open'); return; }
    if (t.hasAttribute('data-dialog-open')) {
      var d = document.getElementById(t.getAttribute('data-dialog-open'));
      if (d && d.showModal) d.showModal();
      return;
    }
    if (t.hasAttribute('data-dialog-close')) {
      var dd = t.closest('dialog');
      if (dd) dd.close();
      return;
    }
    if (t.hasAttribute('data-copy-from')) {
      var src = $(t.getAttribute('data-copy-from'));
      if (src && navigator.clipboard) navigator.clipboard.writeText(Array.prototype.map.call(src.children, function (r) { return r.textContent; }).join('\n')).then(function () { say('Copied'); });
      return;
    }
    if (t.hasAttribute('data-copy')) {
      var v = t.getAttribute('data-copy');
      if (navigator.clipboard) navigator.clipboard.writeText(v).then(function () { say(($('#palette') || {getAttribute: function () {}}).getAttribute('data-t-copied') || 'Copied'); });
      return;
    }
    if (t.hasAttribute('data-switch')) {
      var input = document.getElementById(t.getAttribute('data-switch'));
      var on = t.getAttribute('aria-checked') !== 'true';
      t.setAttribute('aria-checked', on ? 'true' : 'false');
      if (input) input.value = on ? 'true' : 'false';
    }
  });

  document.addEventListener('input', function (e) {
    var t = e.target;
    if (t.id === 'pal-input') {
      if (palMode === 'all' && t.value.charAt(0) === ':') { palMode = 'act'; t.value = t.value.slice(1); $('#pal-prompt').textContent = ':'; }
      refreshPalette();
      return;
    }
    if (t.hasAttribute && t.hasAttribute('data-confirm-name')) {
      var dlg = t.closest('dialog');
      var btn = dlg && $('[data-confirm-submit]', dlg);
      if (btn) btn.disabled = t.value !== t.getAttribute('data-confirm-name');
    }
  });

  // ---- keyboard ----------------------------------------------------------
  document.addEventListener('keydown', function (e) {
    var k = e.key;

    // inside the search pop-up
    if (pal && !pal.classList.contains('hidden')) {
      if (k === 'Escape') { closePalette(); e.preventDefault(); }
      else if (k === 'ArrowDown') { palIdx = Math.min(palItems.length - 1, palIdx + 1); markPalette(); e.preventDefault(); }
      else if (k === 'ArrowUp') { palIdx = Math.max(0, palIdx - 1); markPalette(); e.preventDefault(); }
      else if (k === 'Enter') { if (palItems[palIdx]) runItem(palItems[palIdx]); e.preventDefault(); }
      else if (k === 'Backspace' && e.target.id === 'pal-input' && e.target.value === '' && palMode === 'act') {
        palMode = 'all'; $('#pal-prompt').textContent = '/'; refreshPalette(); e.preventDefault();
      }
      return;
    }

    if (k === 'Escape') {
      if (closeKeys() || closeMenu()) { e.preventDefault(); return; }
      if (document.body.classList.contains('drawer-open')) { document.body.classList.remove('drawer-open'); return; }
      if (typing(e.target)) { e.target.blur(); }
      setPending(false);
      return;
    }

    // ⌘↵ / Ctrl↵ submits the form of a dialog or any form with a field focused
    if (k === 'Enter' && (e.metaKey || e.ctrlKey)) {
      var f = e.target.closest && e.target.closest('form');
      if (f) { f.requestSubmit(); e.preventDefault(); }
      return;
    }

    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (typing(e.target) || (e.target.closest && e.target.closest('dialog'))) return;

    if (pendingG) {
      setPending(false);
      if (k === 'g') { setCursor(0); e.preventDefault(); return; }
      var a = $('[data-go-key="' + (window.CSS && CSS.escape ? CSS.escape(k) : k) + '"]');
      if (a) { window.location.href = a.getAttribute('href'); e.preventDefault(); }
      return;
    }

    switch (k) {
      case '/': openPalette('all'); e.preventDefault(); return;
      case ':': openPalette('act'); e.preventDefault(); return;
      case '?': openKeys(); e.preventDefault(); return;
      case 'g': setPending(true); e.preventDefault(); return;
      case 'j': case 'ArrowDown': if (rows().length) { setCursor(cursor + 1); e.preventDefault(); } return;
      case 'k': case 'ArrowUp': if (rows().length) { setCursor(cursor < 0 ? 0 : cursor - 1); e.preventDefault(); } return;
      case 'f': { var lg = $('#log'); if (lg) { follow = !follow; if (follow && lg.lastElementChild) lg.lastElementChild.scrollIntoView(); say(follow ? 'Following' : 'Not following'); e.preventDefault(); } return; }
      case 'G': if (!rows().length) { var l2 = $('#log'); if (l2 && l2.lastElementChild) { l2.lastElementChild.scrollIntoView(); e.preventDefault(); } } else if (rows().length) { setCursor(rows().length - 1); e.preventDefault(); } return;
      case 'Enter': {
        var r = currentRow();
        if (r) { var href = r.getAttribute('data-href'); if (href) { window.location.href = href; e.preventDefault(); } }
        return;
      }
      case '[': case ']': {
        var links = $$('.settings-nav a');
        var at = links.findIndex(function (l) { return l.getAttribute('aria-current') === 'page'; });
        if (at >= 0) {
          var n = links[at + (k === ']' ? 1 : -1)];
          if (n) { window.location.href = n.getAttribute('href'); e.preventDefault(); }
        }
        return;
      }
    }

    // page and row actions: data-key="r"
    if (k.length === 1) {
      var scope = currentRow() || document;
      var el = $('[data-key="' + (window.CSS && CSS.escape ? CSS.escape(k) : k) + '"]', scope) ||
               (scope !== document ? $('[data-key="' + k + '"]') : null);
      // data-row-only: a key that acts on the row under the cursor, never on the first
      if (el && el.offsetParent !== null && !(el.hasAttribute('data-row-only') && scope === document)) { el.click(); e.preventDefault(); }
    }
  });

  // ---- live job log: EventSource reconnects with Last-Event-ID by itself
  var follow = true;
  (function () {
    var log = $('#log[data-stream]');
    if (!log || !window.EventSource) return;
    var es = new EventSource(log.getAttribute('data-stream'));
    function swap(id, html) {
      var old = document.getElementById(id);
      if (!old) return;
      var t = document.createElement('template');
      t.innerHTML = html;
      if (t.content.firstElementChild) old.replaceWith(t.content.firstElementChild);
    }
    es.addEventListener('line', function (e) {
      log.insertAdjacentHTML('beforeend', e.data);
      if (follow && log.lastElementChild) log.lastElementChild.scrollIntoView({ block: 'nearest' });
    });
    es.addEventListener('steps', function (e) { swap('job-steps', e.data); });
    es.addEventListener('state', function (e) { swap('job-head', e.data); });
    es.addEventListener('done', function () { es.close(); });
  })();

  // htmx swaps leave the cursor pointing at nothing
  document.addEventListener('htmx:afterSwap', function () { cursor = -1; });

  // dialog: esc closes natively; a click on the backdrop closes too
  document.addEventListener('mousedown', function (e) {
    if (e.target.tagName === 'DIALOG' && e.target.open) {
      var r = e.target.getBoundingClientRect();
      if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) e.target.close();
    }
  });
})();
