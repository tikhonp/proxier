// Passkeys: signing in with one on /login and adding one on Settings →
// Security. The server speaks WebAuthn JSON with base64url for every binary
// field; the browser API wants ArrayBuffers, so both ways are converted here.
(function () {
  'use strict';

  if (!window.PublicKeyCredential || !navigator.credentials) return;

  function bytes(s) {
    s = s.replace(/-/g, '+').replace(/_/g, '/');
    while (s.length % 4) s += '=';
    var raw = atob(s), out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out.buffer;
  }

  function text(buf) {
    var b = new Uint8Array(buf), s = '';
    for (var i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }

  function withIds(list) {
    return (list || []).map(function (c) { return Object.assign({}, c, { id: bytes(c.id) }); });
  }

  function creationOptions(o) {
    var pk = Object.assign({}, o.publicKey);
    pk.challenge = bytes(pk.challenge);
    pk.user = Object.assign({}, pk.user, { id: bytes(pk.user.id) });
    pk.excludeCredentials = withIds(pk.excludeCredentials);
    return pk;
  }

  function requestOptions(o) {
    var pk = Object.assign({}, o.publicKey);
    pk.challenge = bytes(pk.challenge);
    pk.allowCredentials = withIds(pk.allowCredentials);
    return pk;
  }

  function credentialJSON(c) {
    var r = c.response, out = {
      id: c.id, rawId: text(c.rawId), type: c.type,
      authenticatorAttachment: c.authenticatorAttachment || undefined,
      clientExtensionResults: c.getClientExtensionResults ? c.getClientExtensionResults() : {},
      response: { clientDataJSON: text(r.clientDataJSON) }
    };
    if (r.attestationObject) {
      out.response.attestationObject = text(r.attestationObject);
      if (r.getTransports) out.response.transports = r.getTransports();
    } else {
      out.response.authenticatorData = text(r.authenticatorData);
      out.response.signature = text(r.signature);
      if (r.userHandle) out.response.userHandle = text(r.userHandle);
    }
    return out;
  }

  // post sends body and resolves to the JSON answer; a refusal rejects with
  // the server's sentence.
  function post(url, body, headers) {
    return fetch(url, { method: 'POST', body: body, headers: headers || {}, credentials: 'same-origin' })
      .then(function (res) {
        return res.json().catch(function () { return {}; }).then(function (j) {
          if (!res.ok) throw new Error(j.error || res.statusText);
          return j;
        });
      });
  }

  function cancelled(err) { return err && (err.name === 'NotAllowedError' || err.name === 'AbortError'); }

  // ---- sign in -----------------------------------------------------------
  var signIn = document.querySelector('[data-passkey-signin]');
  if (signIn) {
    var form = signIn.form, msg = document.getElementById('pk-msg'), waiting = null;
    var failed = signIn.getAttribute('data-failed') || '';
    var show = function (s) {
      if (!msg || !s) return;
      msg.lastElementChild.textContent = s;
      msg.classList.remove('hidden');
    };
    var next = function () { var n = form.querySelector('[name=next]'); return n ? n.value : '/'; };

    // run asks for a passkey: conditional runs quietly behind the username
    // field's autofill; otherwise the browser's own prompt opens.
    var run = function (conditional) {
      if (waiting) waiting.abort();
      var ctl = new AbortController();
      waiting = ctl;
      var token;
      return post('/login/passkey/options').then(function (j) {
        token = j.token;
        var req = { publicKey: requestOptions(j.options), signal: ctl.signal };
        if (conditional) req.mediation = 'conditional';
        return navigator.credentials.get(req);
      }).then(function (cred) {
        return post('/login/passkey', JSON.stringify({ token: token, next: next(), credential: credentialJSON(cred) }),
          { 'Content-Type': 'application/json' });
      }).then(function (j) {
        location.assign(j.redirect || '/');
      }).catch(function (err) {
        // the quiet request fails quietly: the admin never asked for it
        if (ctl.signal.aborted || conditional) return;
        if (!cancelled(err)) show(err.message && err.name === 'Error' ? err.message : failed);
      });
    };

    signIn.classList.remove('hidden');
    signIn.addEventListener('click', function () { run(false); });
    if (PublicKeyCredential.isConditionalMediationAvailable) {
      PublicKeyCredential.isConditionalMediationAvailable().then(function (ok) { if (ok) run(true); });
    }
  }

  // ---- add (Settings → Security) -----------------------------------------
  var add = document.querySelector('form[data-passkey-add]');
  if (add) {
    var btn = add.querySelector('button[type=submit]');
    var note = add.querySelector('[data-passkey-unsupported]');
    var addFailed = add.getAttribute('data-failed') || '';
    btn.disabled = false;
    if (note) note.classList.add('hidden');

    var alertBox = function (s) {
      var a = add.querySelector('.alert');
      if (!a) {
        a = document.createElement('div');
        a.className = 'alert love';
        a.setAttribute('role', 'alert');
        add.insertBefore(a, add.querySelector('.form-actions'));
      }
      a.textContent = s;
    };

    add.addEventListener('submit', function (e) {
      e.preventDefault();
      var data = new FormData(add), csrf = data.get('_csrf'), token;
      btn.disabled = true;
      post(add.action, new URLSearchParams(data), { 'X-CSRF-Token': csrf }).then(function (j) {
        token = j.token;
        return navigator.credentials.create({ publicKey: creationOptions(j.options) });
      }).then(function (cred) {
        return post('/settings/security/passkeys', JSON.stringify({ token: token, credential: credentialJSON(cred) }),
          { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf });
      }).then(function (j) {
        location.assign(j.redirect);
      }).catch(function (err) {
        btn.disabled = false;
        if (cancelled(err)) return;
        if (err.name === 'InvalidStateError') alertBox(add.getAttribute('data-exists') || addFailed);
        else alertBox(err.name === 'Error' && err.message ? err.message : addFailed);
      });
    });
  }
})();
