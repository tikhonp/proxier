// The page a browser gets at a link's URL: platform tabs and Copy link.
// Without this script every platform's section shows, one under another,
// and the tabs jump to them.
(function () {
  'use strict';

  var root = document.querySelector('.lp[data-open]');
  if (!root) return;

  var tabs = Array.prototype.slice.call(root.querySelectorAll('[data-lp-tab]'));
  var panels = root.querySelectorAll('[data-lp-plat]');

  function open(platform, focus) {
    tabs.forEach(function (t) {
      var on = t.getAttribute('data-lp-tab') === platform;
      t.classList.toggle('lp-on', on);
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      t.tabIndex = on ? 0 : -1;
      if (on && focus) t.focus();
    });
    panels.forEach(function (p) { p.hidden = p.getAttribute('data-lp-plat') !== platform; });
  }

  if (tabs.length) {
    root.classList.add('lp-js');
    tabs[0].parentNode.setAttribute('role', 'tablist');
    tabs.forEach(function (t, i) {
      t.setAttribute('role', 'tab');
      t.setAttribute('aria-controls', 'lp-' + t.getAttribute('data-lp-tab'));
      t.addEventListener('click', function (e) {
        e.preventDefault();
        open(t.getAttribute('data-lp-tab'), false);
      });
      t.addEventListener('keydown', function (e) {
        var step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
        if (!step) return;
        e.preventDefault();
        open(tabs[(i + step + tabs.length) % tabs.length].getAttribute('data-lp-tab'), true);
      });
    });
    panels.forEach(function (p) { p.setAttribute('role', 'tabpanel'); });

    var first = root.getAttribute('data-open');
    // iPadOS asks for pages as a Mac does; a Mac has no touch screen
    if (first === 'macos' && navigator.maxTouchPoints > 1) {
      first = 'ios';
      var qr = root.querySelector('.lp-qr');
      if (qr) qr.open = false;
    }
    open(first, false);
  }

  function copied(b) {
    if (!b.hasAttribute('data-label')) b.setAttribute('data-label', b.textContent);
    b.textContent = b.getAttribute('data-copied');
    clearTimeout(b._lpTimer);
    b._lpTimer = setTimeout(function () { b.textContent = b.getAttribute('data-label'); }, 2000);
  }

  // Plain http or an old browser has no clipboard API: select the link
  // instead, so the system's own Copy is one tap away.
  function selectLink(b) {
    var u = root.querySelector('.lp-url');
    if (!u) return;
    var r = document.createRange();
    r.selectNodeContents(u);
    var s = window.getSelection();
    s.removeAllRanges();
    s.addRange(r);
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    if (ok) copied(b);
    u.scrollIntoView({ block: 'center' });
  }

  root.querySelectorAll('[data-lp-copy]').forEach(function (b) {
    b.addEventListener('click', function () {
      var text = b.getAttribute('data-lp-copy');
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(function () { copied(b); }, function () { selectLink(b); });
      } else {
        selectLink(b);
      }
    });
  });
})();
