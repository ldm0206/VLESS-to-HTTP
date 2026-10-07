/* v2h control panel — hand-written vanilla JS, no build step, no framework.
 *
 * Every value that comes from the server is rendered through textContent via
 * the el() helper below; innerHTML is never used anywhere in this file, so
 * server data can not become markup.
 */
(function () {
  'use strict';

  // ---------------------------------------------------------------- DOM ----

  function appendChildren(node, children) {
    if (children === null || children === undefined) return;
    var list = Array.isArray(children) ? children : [children];
    for (var i = 0; i < list.length; i++) {
      var child = list[i];
      if (child === null || child === undefined || child === false || child === '') continue;
      if (child instanceof Node) { node.appendChild(child); continue; }
      if (typeof child === 'object') { console.warn('v2h panel: plain object used as DOM child', child); continue; }
      node.appendChild(document.createTextNode(String(child)));
    }
  }

  // el builds one element. props understands class/text/dataset/style/hidden and
  // on* handlers; value is applied after children so selects settle correctly.
  function el(tag, props, children) {
    var node = document.createElement(tag);
    var value;
    if (props) {
      for (var key in props) {
        var v = props[key];
        if (v === null || v === undefined || v === false) continue;
        if (key === 'class') node.className = v;
        else if (key === 'text') node.textContent = v;
        else if (key === 'value') value = v;
        else if (key === 'checked') node.checked = !!v;
        else if (key === 'selected') node.selected = !!v;
        else if (key === 'disabled') node.disabled = !!v;
        else if (key === 'hidden') node.hidden = !!v;
        else if (key === 'dataset') { for (var d in v) { if (v[d] !== null && v[d] !== undefined) node.dataset[d] = v[d]; } }
        else if (key === 'style') { if (typeof v === 'object') Object.assign(node.style, v); else node.setAttribute('style', v); }
        else if (key.indexOf('on') === 0 && typeof v === 'function') node.addEventListener(key.slice(2), v);
        else node.setAttribute(key, v === true ? '' : String(v));
      }
    }
    appendChildren(node, children);
    if (value !== undefined) node.value = value;
    return node;
  }

  function clear(node) { node.replaceChildren(); return node; }
  function $(id) { return document.getElementById(id); }

  // ------------------------------------------------------------ formatting --

  var UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

  function fmtBytes(n) {
    n = Number(n) || 0;
    var neg = n < 0;
    if (neg) n = -n;
    var i = 0;
    while (n >= 1024 && i < UNITS.length - 1) { n /= 1024; i++; }
    var digits = i === 0 ? 0 : (n < 10 ? 2 : (n < 100 ? 1 : 0));
    return (neg ? '-' : '') + n.toFixed(digits) + ' ' + UNITS[i];
  }

  function fmtDuration(sec) {
    sec = Math.max(0, Math.floor(Number(sec) || 0));
    if (sec < 60) return sec + ' 秒';
    var m = Math.floor(sec / 60), h = Math.floor(m / 60), d = Math.floor(h / 24);
    if (h < 1) return m + ' 分钟';
    if (d < 1) return h + ' 小时 ' + (m % 60) + ' 分';
    return d + ' 天 ' + (h % 24) + ' 小时';
  }

  function pad2(n) { return n < 10 ? '0' + n : String(n); }

  function parseTime(t) {
    if (!t) return null;
    var d = new Date(t);
    return isNaN(d.getTime()) ? null : d;
  }

  function fmtTime(t) {
    var d = parseTime(t);
    if (!d) return '—';
    return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' ' +
      pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
  }
  function fmtDay(ms) {
    var d = new Date(ms);
    return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate());
  }

  function fmtClock(t) {
    var d = parseTime(t);
    if (!d) return '--:--:--';
    return pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
  }

  function fmtAgo(t) {
    var d = parseTime(t);
    if (!d) return '从未';
    var sec = Math.floor((Date.now() - d.getTime()) / 1000);
    if (sec < 5) return '刚刚';
    if (sec < 60) return sec + ' 秒前';
    if (sec < 3600) return Math.floor(sec / 60) + ' 分钟前';
    if (sec < 86400) return Math.floor(sec / 3600) + ' 小时前';
    return Math.floor(sec / 86400) + ' 天前';
  }

  // fmtInterval turns Go's "12h0m0s" into something readable in Chinese.
  // (fmtDuration above renders a number of seconds.)
  function fmtInterval(v) {
    if (!v) return '';
    var m = /^(?:([0-9]+)h)?(?:([0-9]+)m(?!s))?(?:([0-9]+)s)?$/.exec(String(v).trim());
    if (!m) return String(v);
    var h = Number(m[1] || 0), min = Number(m[2] || 0), sec = Number(m[3] || 0);
    if (!h && !min && !sec) return '不定时';
    var parts = [];
    if (h) parts.push(h + ' 小时');
    if (min) parts.push(min + ' 分钟');
    if (sec && !h) parts.push(sec + ' 秒');
    return parts.join(' ') || String(v);
  }

  function truncate(s, n) {
    s = String(s === null || s === undefined ? '' : s);
    return s.length > n ? s.slice(0, n - 1) + '…' : s;
  }

  function joinList(list) {
    if (!list || !list.length) return '—';
    return list.join('、');
  }

  var MODE_LABEL = { priority: '按序优先', auto: '自动切换', fixed: '固定节点' };
  var FALLBACK_LABEL = { inherit: '跟随全局', direct: '直连', reject: '拒绝连接' };
  var KIND_LABEL = { auto: '自动识别', clash: 'Clash', v2ray: 'v2ray' };
  var LEVEL_LABEL = { debug: '调试', info: '信息', warning: '警告', error: '错误' };

  function modeLabel(mode) { return MODE_LABEL[mode] || mode || '—'; }
  function fallbackLabel(f) { return FALLBACK_LABEL[f] || f || '—'; }
  function kindLabel(k) { return KIND_LABEL[k] || k || '—'; }

  // ------------------------------------------------------------- building --

  function dot(kind) { return el('span', { class: 'dot dot-' + kind }); }

  function statusPill(kind, label) {
    return el('span', { class: 'status' }, [dot(kind), el('span', {}, [label])]);
  }

  function badge(text, kind) {
    return el('span', { class: 'badge' + (kind ? ' badge-' + kind : '') }, [text]);
  }

  function emptyRow(cols, text) {
    return el('tr', {}, [el('td', { class: 'table-empty', colspan: String(cols) }, [text])]);
  }

  function emptyState(text) { return el('div', { class: 'empty' }, [text]); }

  function copyButton(text, label) {
    return el('button', {
      class: 'btn btn-ghost btn-sm', type: 'button', title: '复制到剪贴板',
      onclick: function () { copyText(text); }
    }, [label || '复制']);
  }

  function codeLine(text, copyLabel) {
    return el('div', { class: 'copy-line' }, [
      el('pre', { class: 'code-block' }, [text]),
      copyButton(text, copyLabel || '复制')
    ]);
  }

  function field(labelText, input, hint) {
    return el('label', { class: 'field' }, [el('span', {}, [labelText]), input, hint ? el('span', { class: 'field-hint' }, [hint]) : null]);
  }

  function card(title, note, actions, body) {
    var head = el('div', { class: 'card-head' }, [
      el('span', { class: 'card-title' }, [title]),
      note ? el('span', { class: 'card-note' }, [note]) : null,
      actions && actions.length ? el('div', { class: 'card-actions' }, actions) : null
    ]);
    var kids = Array.isArray(body) ? body : [body];
    return el('div', { class: 'card' }, [head].concat(kids));
  }

  function sectionHead(title, note) {
    return el('div', { class: 'sec-block' }, [
      el('div', { class: 'sec-title' }, [title]),
      el('div', { class: 'sec-note' }, [note])
    ]);
  }

  function kv(key, value) {
    return el('div', { class: 'kv-row' }, [
      el('span', { class: 'kv-key' }, [key]),
      el('span', { class: 'kv-val' }, [value])
    ]);
  }

  function toggleRow(labelText, input, hint) {
    return el('div', { class: 'field-inline' }, [input, el('span', {}, [labelText]), hint ? el('span', { class: 'field-hint' }, [hint]) : null]);
  }

  // ---------------------------------------------------------------- state --

  var state = {
    csrf: '', user: '', session: null, version: null,
    status: null, proxy: null, users: [], subs: [], passwords: {},
    subNodes: {}, nodes: [], nodeFilter: { sub: '', proto: '', hideUnsupported: false },
    logLines: [], lastSeq: 0, logPaused: false, logStick: true,
    settings: null, help: null,
    overviewAccount: '', theme: null,
    events: null, eventsState: 'idle', eventsAttempts: 0, lastEventAt: 0, eventsTimer: null, watchdog: null
  };

  var currentView = '';
  var ovRefs = null, userRefs = null, subRefs = null, nodeRefs = null, logRefs = null;

  // ---------------------------------------------------------------- theme --

  var THEME_KEY = 'v2h.theme';

  function prefersDark() {
    return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
  }

  function storedTheme() {
    try {
      var v = localStorage.getItem(THEME_KEY);
      return (v === 'light' || v === 'dark') ? v : null;
    } catch (e) { return null; }
  }

  function effectiveTheme() { return storedTheme() || (prefersDark() ? 'dark' : 'light'); }

  function applyTheme() {
    var theme = effectiveTheme();
    state.theme = theme;
    document.documentElement.dataset.theme = theme;
    var btn = $('theme-toggle');
    if (btn) btn.textContent = theme === 'dark' ? '浅色' : '深色';
  }

  function toggleTheme() {
    var next = effectiveTheme() === 'dark' ? 'light' : 'dark';
    try { localStorage.setItem(THEME_KEY, next); } catch (e) { /* private mode */ }
    applyTheme();
  }

  // --------------------------------------------------------------- toasts --

  function toast(message, kind) {
    var box = $('toasts');
    if (!box) return;
    var node = el('div', {
      class: 'toast toast-' + (kind || 'info'),
      role: 'status',
      onclick: function () { node.remove(); }
    }, [el('div', { class: 'toast-text' }, [String(message)])]);
    box.appendChild(node);
    while (box.children.length > 4) box.removeChild(box.firstChild);
    var life = kind === 'error' ? 6000 : 3200;
    setTimeout(function () { node.remove(); }, life);
  }

  function fail(err) {
    toast(err && err.message ? err.message : String(err), 'error');
  }

  // --------------------------------------------------------------- modals --

  var openDialogs = [];

  function openModal(opts) {
    var root = $('modal-root');
    var closed = false;

    function close() {
      if (closed) return;
      closed = true;
      document.removeEventListener('keydown', onKey);
      backdrop.remove();
      var index = openDialogs.indexOf(handle);
      if (index !== -1) openDialogs.splice(index, 1);
      if (opts.onClose) opts.onClose();
    }

    function onKey(e) {
      if (e.key === 'Escape') { e.preventDefault(); close(); }
    }

    var head = el('div', { class: 'modal-head' }, [
      el('div', {}, [
        el('div', { class: 'modal-title' }, [opts.title || '']),
        opts.sub ? el('div', { class: 'modal-sub' }, [opts.sub]) : null
      ]),
      el('button', { class: 'btn btn-ghost btn-sm modal-close', type: 'button', title: '关闭', onclick: close }, ['✕'])
    ]);

    var modal = el('div', { class: 'modal' + (opts.wide ? ' modal-wide' : '') }, [
      head,
      el('div', { class: 'modal-body' }, [opts.body]),
      opts.foot && opts.foot.length ? el('div', { class: 'modal-foot' }, opts.foot) : null
    ]);

    var backdrop = el('div', {
      class: 'modal-backdrop',
      onclick: function (e) { if (e.target === backdrop) close(); }
    }, [modal]);

    root.appendChild(backdrop);
    document.addEventListener('keydown', onKey);
    var handle = { close: close, el: modal };
    openDialogs.push(handle);
    return handle;
  }

  function confirmDialog(title, message, confirmLabel) {
    return new Promise(function (resolve) {
      var settled = false;
      function finish(value) {
        if (settled) return;
        settled = true;
        dialog.close();
        resolve(value);
      }
      var cancel = el('button', { class: 'btn btn-outline', type: 'button', onclick: function () { finish(false); } }, ['取消']);
      var confirm = el('button', { class: 'btn btn-danger', type: 'button', onclick: function () { finish(true); } }, [confirmLabel || '确认']);
      var dialog = openModal({
        title: title,
        body: el('p', { class: 'page-lead' }, [message]),
        foot: [el('div', { class: 'spacer' }), cancel, confirm],
        onClose: function () { if (!settled) { settled = true; resolve(false); } }
      });
      confirm.focus();
    });
  }

  function promptPassword(title, message, password) {
    var dialog = openModal({
      title: title,
      sub: message,
      body: el('div', {}, [
        el('div', { class: 'copy-line' }, [
          el('pre', { class: 'code-block' }, [password]),
          copyButton(password, '复制密码')
        ]),
        el('p', { class: 'field-hint' }, ['请立即复制保存：关闭后只能重置，无法再次查看。'])
      ]),
      foot: [
        el('div', { class: 'spacer' }),
        el('button', { class: 'btn btn-primary', type: 'button', onclick: function () { dialog.close(); } }, ['我已保存'])
      ]
    });
  }

  // ------------------------------------------------------------ clipboard --

  function fallbackCopy(text) {
    var area = el('textarea', {
      class: 'copy-fallback',
      style: { position: 'fixed', top: '-1000px', left: '-1000px', opacity: '0' }
    });
    area.value = text;
    document.body.appendChild(area);
    area.select();
    area.setSelectionRange(0, area.value.length);
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    area.remove();
    return ok;
  }

  function copyText(text) {
    text = String(text === null || text === undefined ? '' : text);
    if (!text) { toast('没有可复制的内容', 'error'); return; }
    var fallback = function () {
      if (fallbackCopy(text)) toast('已复制到剪贴板', 'success');
      else toast('复制失败，请手动选中文本复制', 'error');
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () {
        toast('已复制到剪贴板', 'success');
      }, fallback);
    } else {
      fallback();
    }
  }

  // ------------------------------------------------------------------ api --

  function ApiError(message, status, data) {
    this.name = 'ApiError';
    this.message = message;
    this.status = status;
    this.data = data || null;
  }
  ApiError.prototype = Object.create(Error.prototype);

  function request(method, path, body) {
    var headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (method !== 'GET' && state.csrf) headers['X-CSRF-Token'] = state.csrf;

    return fetch(path, {
      method: method,
      headers: headers,
      credentials: 'same-origin',
      cache: 'no-store',
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        if (text) { try { data = JSON.parse(text); } catch (e) { data = null; } }
        if (res.status === 401) {
          handleUnauthorized();
          throw new ApiError((data && data.error) || '未登录或凭据已失效', 401, data);
        }
        if (!res.ok) {
          throw new ApiError((data && data.error) || ('请求失败（HTTP ' + res.status + '）'), res.status, data);
        }
        return data;
      });
    }, function (err) {
      throw new ApiError('无法连接到面板服务：' + (err && err.message ? err.message : '网络错误'), 0, null);
    });
  }

  var api = {
    get: function (path) { return request('GET', path); },
    post: function (path, body) { return request('POST', path, body === undefined ? {} : body); },
    patch: function (path, body) { return request('PATCH', path, body === undefined ? {} : body); },
    del: function (path) { return request('DELETE', path); }
  };

  function runAction(btn, label, fn) {
    var original = btn ? Array.prototype.slice.call(btn.childNodes) : null;
    if (btn) {
      btn.disabled = true;
      btn.replaceChildren(el('span', { class: 'spinner' }), document.createTextNode(label || '处理中'));
    }
    return Promise.resolve()
      .then(fn)
      .catch(function (err) { fail(err); return undefined; })
      .then(function (result) {
        if (btn && original && btn.isConnected) {
          btn.disabled = false;
          btn.replaceChildren.apply(btn, original);
        }
        return result;
      });
  }

  // ---------------------------------------------------------------- login --

  var turnstileWidget = null;
  var turnstileToken = '';
  var loginCountdown = null;

  function loadTurnstileScript(done) {
    if (window.turnstile) { done(); return; }
    var existing = document.querySelector('script[data-v2h-turnstile]');
    if (!existing) {
      var script = document.createElement('script');
      script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';
      script.async = true;
      script.defer = true;
      script.setAttribute('data-v2h-turnstile', '1');
      script.onload = done;
      script.onerror = function () { /* fail_open decides whether login still works */ };
      document.head.appendChild(script);
    }
    var waited = 0;
    var timer = setInterval(function () {
      waited += 150;
      if (window.turnstile) { clearInterval(timer); done(); }
      else if (waited > 12000) clearInterval(timer);
    }, 150);
  }

  function setupTurnstile(cfg) {
    var slot = $('turnstile-slot');
    turnstileWidget = null;
    turnstileToken = '';
    clear(slot);
    slot.hidden = true;
    if (!cfg || !cfg.enabled || !cfg.site_key) return;
    slot.hidden = false;
    loadTurnstileScript(function () {
      if (!window.turnstile || !slot.isConnected) return;
      try {
        turnstileWidget = window.turnstile.render(slot, {
          sitekey: cfg.site_key,
          callback: function (token) { turnstileToken = token; },
          'expired-callback': function () { turnstileToken = ''; },
          'error-callback': function () { turnstileToken = ''; }
        });
      } catch (e) { /* widget stays absent; server decides */ }
    });
  }

  function resetTurnstile() {
    turnstileToken = '';
    if (window.turnstile && turnstileWidget !== null) {
      try { window.turnstile.reset(turnstileWidget); } catch (e) { /* ignore */ }
    }
  }

  function setLoginError(message) {
    var box = $('login-error');
    if (!message) { box.hidden = true; box.textContent = ''; return; }
    box.hidden = false;
    box.textContent = message;
  }

  function showLogin(session) {
    if (state.events) { state.events.close(); state.events = null; }
    if (state.eventsTimer) { clearTimeout(state.eventsTimer); state.eventsTimer = null; }
    if (state.watchdog) { clearInterval(state.watchdog); state.watchdog = null; }
    closeAllModals();
    state.eventsState = 'idle';
    $('app').hidden = true;
    $('login-screen').hidden = false;
    setLoginError('');
    var version = session && session.version ? session.version : (state.version || null);
    var foot = $('login-version');
    if (version) {
      foot.textContent = 'v' + (version.version || 'dev') + ' · Xray ' + (version.xray || '未知') +
        (version.built ? ' · 构建于 ' + version.built : '');
    } else {
      foot.textContent = '';
    }
    setupTurnstile(session ? session.turnstile : null);
    var hint = $('login-hint');
    hint.hidden = true;
    if (session && session.turnstile && session.turnstile.enabled && session.turnstile.fail_open) {
      hint.textContent = '人机校验服务不可用时仍允许登录（fail_open）。';
      hint.hidden = false;
    }
    var userInput = $('login-user');
    if (!userInput.value) userInput.focus();
    else $('login-pass').focus();
  }

  function closeAllModals() {
    var open = openDialogs.slice();
    for (var i = 0; i < open.length; i++) open[i].close();
    var root = $('modal-root');
    if (root) clear(root);
  }

  function loginBusy(busy, label) {
    var btn = $('login-submit');
    btn.disabled = !!busy;
    btn.textContent = busy ? (label || '登录中…') : '登录';
  }

  function startRetryCountdown(seconds) {
    var left = Math.max(1, Math.floor(seconds || 60));
    loginBusy(true, '请等待 ' + left + ' 秒');
    if (loginCountdown) clearInterval(loginCountdown);
    loginCountdown = setInterval(function () {
      left -= 1;
      if (left <= 0) {
        clearInterval(loginCountdown);
        loginCountdown = null;
        loginBusy(false);
        return;
      }
      loginBusy(true, '请等待 ' + left + ' 秒');
    }, 1000);
  }

  function submitLogin(ev) {
    ev.preventDefault();
    var username = $('login-user').value.trim();
    var password = $('login-pass').value;
    if (loginCountdown) return;
    if (!username) { setLoginError('请输入用户名'); return; }
    if (!password) { setLoginError('请输入密码'); return; }
    if (turnstileWidget !== null && !turnstileToken) { setLoginError('请先完成人机验证'); return; }

    setLoginError('');
    loginBusy(true);
    var body = { username: username, password: password };
    if (turnstileToken) body.turnstile_token = turnstileToken;

    api.post('/api/login', body).then(function (res) {
      state.csrf = (res && res.csrf) || '';
      state.user = (res && res.user) || username;
      $('login-pass').value = '';
      return api.get('/api/session').catch(function () { return null; });
    }).then(function (session) {
      loginBusy(false);
      if (session) state.session = session;
      if (!state.csrf && session && session.csrf) state.csrf = session.csrf;
      startApp();
      toast('已登录，欢迎回来', 'success');
    }).catch(function (err) {
      loginBusy(false);
      resetTurnstile();
      if (err && err.status === 429) {
        var wait = (err.data && err.data.retry_after) || 60;
        setLoginError(err.message + '（约 ' + wait + ' 秒后可重试）');
        startRetryCountdown(wait);
        return;
      }
      setLoginError(err && err.message ? err.message : '登录失败');
    });
  }

  function logout() {
    confirmDialog('退出登录', '退出后需要重新输入管理员密码。', '退出').then(function (ok) {
      if (!ok) return;
      return api.post('/api/logout', {}).catch(function () { /* session may already be gone */ })
        .then(function () {
          state.csrf = '';
          state.user = '';
          state.status = null;
          state.logLines = [];
          state.passwords = {};
          showLogin(state.session);
          toast('已退出登录', 'info');
        });
    });
  }

  function handleUnauthorized() {
    if ($('app').hidden) return;
    state.csrf = '';
    showLogin(state.session);
    toast('登录状态已失效，请重新登录', 'error');
  }

  // ------------------------------------------------------------ app shell --

  function setConn(kind, label) {
    var node = $('conn-state');
    clear(node);
    node.appendChild(dot(kind));
    node.appendChild(el('span', { class: 'conn-label' }, [label]));
    node.title = label;
  }

  function bindShell() {
    $('theme-toggle').addEventListener('click', toggleTheme);
    $('logout').addEventListener('click', logout);
    $('login-form').addEventListener('submit', submitLogin);
    window.addEventListener('hashchange', route);
    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: dark)');
      var onChange = function () { if (!storedTheme()) applyTheme(); };
      if (mq.addEventListener) mq.addEventListener('change', onChange);
      else if (mq.addListener) mq.addListener(onChange);
    }
  }

  function renderVersionFooters() {
    var version = state.version || (state.session && state.session.version) || null;
    if (version) state.version = version;
    var foot = $('sidebar-foot');
    if (foot) {
      foot.textContent = version
        ? ('v2h ' + (version.version || 'dev') + ' · Xray ' + (version.xray || '未知'))
        : 'v2h 控制面板';
    }
  }

  function startApp() {
    $('login-screen').hidden = true;
    $('app').hidden = false;
    $('user-chip').textContent = state.user ? ('管理员 ' + state.user) : '管理员';
    renderVersionFooters();
    if (!location.hash) history.replaceState(null, '', '#/overview');
    route();
    connectEvents();
    api.get('/api/status').then(applyStatusResponse).catch(function () { /* SSE will fill it in */ });
    api.get('/api/logs?limit=200').then(prefillLogs).catch(function () { /* tail stays empty */ });
    refreshPasswords().catch(function () { /* password column shows a hint instead */ });
  }

  function applyStatusResponse(res) {
    if (!res) return;
    if (res.status) state.status = res.status;
    if (res.proxy) state.proxy = res.proxy;
    state.panel = res.panel || null;
    var view = VIEWS[currentView];
    if (view && view.onStatus) view.onStatus();
  }

  function refreshPasswords() {
    return api.get('/api/users').then(function (data) {
      var list = (data && data.users) || [];
      var map = {};
      for (var i = 0; i < list.length; i++) {
        if (list[i].password) map[list[i].id] = list[i].password;
      }
      state.passwords = map;
      // Accounts created before the next SSE tick still show up right away.
      if (list.length) {
        var known = {};
        state.users.forEach(function (u) { known[u.id] = true; });
        var extra = list.filter(function (u) { return !known[u.id]; });
        if (extra.length) state.users = state.users.concat(extra);
      }
      if (!state.overviewAccount && list.length) state.overviewAccount = list[0].id;
      return list;
    });
  }

  function userById(id) {
    var list = state.users || [];
    for (var i = 0; i < list.length; i++) if (list[i].id === id) return list[i];
    return null;
  }

  // -------------------------------------------------------------- routing --

  var VIEWS = {
    overview: { title: '概览', render: renderOverview, onStatus: paintOverview },
    users: { title: '账号', render: renderUsers, onStatus: paintUsers },
    subs: { title: '订阅', render: renderSubs, onStatus: paintSubs },
    nodes: { title: '节点', render: renderNodes },
    logs: { title: '日志', render: renderLogs },
    settings: { title: '设置', render: renderSettings },
    help: { title: '帮助', render: renderHelp }
  };

  function route() {
    var raw = (location.hash || '').replace(/^#\/?/, '');
    var name = raw.split('?')[0].split('/')[0];
    if (!VIEWS[name]) name = 'overview';
    currentView = name;
    ovRefs = userRefs = subRefs = nodeRefs = logRefs = null;

    var links = document.querySelectorAll('.nav-item');
    for (var i = 0; i < links.length; i++) {
      links[i].classList.toggle('active', links[i].dataset.route === name);
    }
    $('page-title').textContent = VIEWS[name].title;
    document.title = VIEWS[name].title + ' · v2h';

    var view = $('view');
    clear(view);
    VIEWS[name].render(view);
  }

  // ------------------------------------------------------------------ SSE --

  function connectEvents() {
    if (state.events) { state.events.close(); state.events = null; }
    if (state.watchdog) { clearInterval(state.watchdog); state.watchdog = null; }

    setConn('amber', state.eventsAttempts ? '重连中' : '连接中');
    var es = new EventSource('/api/events');
    state.events = es;

    es.addEventListener('open', function () {
      state.eventsState = 'open';
      state.eventsAttempts = 0;
      state.lastEventAt = Date.now();
      setConn('green', '实时连接');
    });

    es.addEventListener('status', function (ev) {
      state.lastEventAt = Date.now();
      var payload = null;
      try { payload = JSON.parse(ev.data); } catch (e) { return; }
      applyStatusEvent(payload);
    });

    es.addEventListener('log', function (ev) {
      state.lastEventAt = Date.now();
      var entry = null;
      try { entry = JSON.parse(ev.data); } catch (e) { return; }
      if (entry) pushLog(entry, true);
    });

    es.addEventListener('error', function () {
      if (state.events !== es) return;
      es.close();
      state.events = null;
      state.eventsAttempts += 1;
      var delay = Math.min(30000, 1000 * Math.pow(2, Math.min(5, state.eventsAttempts - 1)));
      setConn('red', '连接中断，' + Math.round(delay / 1000) + ' 秒后重连');
      if (state.eventsTimer) clearTimeout(state.eventsTimer);
      state.eventsTimer = setTimeout(function () {
        state.eventsTimer = null;
        if (state.events) return;
        api.get('/api/session').then(function (session) {
          if (state.events) return;
          state.session = session;
          if (session && session.authenticated) {
            if (session.csrf) state.csrf = session.csrf;
            connectEvents();
          } else {
            showLogin(session);
          }
        }).catch(function () {
          if (!state.events) connectEvents();
        });
      }, delay);
    });

    if (!state.watchdog) {
      state.watchdog = setInterval(function () {
        if (!state.events) return;
        if (state.lastEventAt && Date.now() - state.lastEventAt > 45000) {
          state.events.close();
          state.events = null;
          state.eventsAttempts = 0;
          if (state.eventsTimer) { clearTimeout(state.eventsTimer); state.eventsTimer = null; }
          connectEvents();
        }
      }, 15000);
    }
  }

  function applyStatusEvent(payload) {
    if (!payload) return;
    if (payload.status) state.status = payload.status;
    if (payload.proxy) {
      state.proxy = Object.assign({}, state.proxy || {}, payload.proxy);
    }
    if (state.status) {
      state.users = state.status.users || [];
      state.subs = state.status.subs || [];
    }
    var view = VIEWS[currentView];
    if (view && view.onStatus) view.onStatus();
  }

  // ----------------------------------------------------------------- logs --

  var LOG_LIMIT = 500;

  function pushLog(entry, live) {
    if (entry.seq && entry.seq <= state.lastSeq && live) return;
    if (entry.seq) state.lastSeq = Math.max(state.lastSeq, entry.seq);
    state.logLines.push(entry);
    if (state.logLines.length > LOG_LIMIT) state.logLines.splice(0, state.logLines.length - LOG_LIMIT);
    if (currentView === 'logs' && logRefs && logRefs.view.isConnected) {
      if (!state.logPaused && logMatches(entry)) appendLogLine(logRefs.view, entry);
      updateLogCount();
    }
    if (currentView === 'overview' && ovRefs && ovRefs.tail.isConnected) paintOverviewTail();
  }

  function prefillLogs(data) {
    var entries = (data && data.entries) || [];
    // Lines already pushed over SSE win; history goes in front of them.
    var known = {};
    state.logLines.forEach(function (e) { if (e.seq) known[e.seq] = true; });
    var older = entries.filter(function (e) { return !e.seq || !known[e.seq]; });
    state.logLines = older.concat(state.logLines).slice(-LOG_LIMIT);
    entries.forEach(function (e) { if (e.seq) state.lastSeq = Math.max(state.lastSeq, e.seq); });
    if (currentView === 'logs') paintLogs();
    if (currentView === 'overview') paintOverviewTail();
  }

  function logLevel(entry) {
    var level = String((entry && entry.level) || 'info').toLowerCase();
    if (level === 'warn') return 'warning';
    if (level === 'information') return 'info';
    return level;
  }

  function levelOk(entry) {
    var want = state.logFilter ? state.logFilter.level : '';
    if (!want) return true;
    var rank = { debug: 0, info: 1, warning: 2, error: 3 };
    return (rank[logLevel(entry)] || 1) >= (rank[want] || 0);
  }

  function logMatches(entry) {
    var f = state.logFilter || {};
    if (!levelOk(entry)) return false;
    if (f.user && entry.user !== f.user) return false;
    if (f.q) {
      var hay = ((entry.msg || '') + ' ' + (entry.source || '') + ' ' + (entry.user || '')).toLowerCase();
      if (hay.indexOf(f.q.toLowerCase()) === -1) return false;
    }
    return true;
  }

  function logLine(entry) {
    var level = logLevel(entry);
    var raw = fmtTime(entry.time) + ' [' + level + '] ' + (entry.source || 'v2h') +
      (entry.user ? ' @' + entry.user : '') + ' ' + (entry.msg || '');
    return el('div', { class: 'log-line', dataset: { raw: raw } }, [
      el('span', { class: 'log-time' }, [fmtClock(entry.time)]),
      el('span', { class: 'log-level lv-' + level }, [level]),
      el('span', { class: 'log-source' }, [entry.source || 'v2h']),
      entry.user ? el('span', { class: 'log-user' }, ['@' + entry.user]) : null,
      el('span', { class: 'log-msg' }, [entry.msg || ''])
    ]);
  }

  function appendLogLine(container, entry) {
    container.appendChild(logLine(entry));
    while (container.children.length > LOG_LIMIT) container.removeChild(container.firstChild);
    if (state.logStick) container.scrollTop = container.scrollHeight;
  }

  // -------------------------------------------------------------- overview --

  function proxyHost(listen) {
    if (!listen) return location.hostname || 'HOST';
    var idx = listen.lastIndexOf(':');
    var host = idx > 0 ? listen.slice(0, idx) : listen;
    host = host.replace(/^\[/, '').replace(/\]$/, '');
    if (!host || host === '0.0.0.0' || host === '::' || host === '*') return location.hostname || 'HOST';
    return host;
  }

  function proxyPort(listen) {
    if (!listen) return '';
    var idx = listen.lastIndexOf(':');
    return idx > 0 ? listen.slice(idx + 1) : '';
  }

  function statCard(label, valueNode, sub, actions) {
    return el('div', { class: 'card stat' }, [
      el('div', { class: 'stat-label' }, [label]),
      el('div', { class: 'stat-value' }, [valueNode]),
      sub ? el('div', { class: 'stat-sub' }, [sub]) : null,
      actions ? el('div', { class: 'form-actions' }, actions) : null
    ]);
  }

  function renderOverview(root) {
    ovRefs = {
      cards: el('div', { class: 'grid grid-4' }),
      endpoints: el('div', { class: 'card' }),
      health: el('div', { class: 'card' }),
      tail: el('div', { class: 'card' })
    };
    root.replaceChildren(
      ovRefs.cards,
      el('div', { class: 'grid grid-side' }, [ovRefs.endpoints, ovRefs.health]),
      ovRefs.tail
    );
    paintOverview();
    refreshPasswords().then(function () { paintOverviewEndpoints(); }, function () { /* no passwords available */ });
    // One-shot read so the health card can show usable / unsupported / in-use
    // counts; liveness keeps streaming in over SSE.
    api.get('/api/nodes').then(function (data) {
      state.overviewNodes = (data && data.nodes) || [];
      if (currentView === 'overview') paintOverviewHealth();
    }).catch(function () { /* the SSE summary still renders */ });
  }

  function paintOverview() {
    if (!ovRefs || !ovRefs.cards.isConnected) return;
    paintOverviewCards();
    paintOverviewEndpoints();
    paintOverviewHealth();
    paintOverviewTail();
  }

  function paintOverviewCards() {
    var st = state.status || {};
    var version = state.version || {};
    var totals = st.totals || {};
    var running = !!st.running;

    var restart = el('button', {
      class: 'btn btn-outline btn-sm', type: 'button',
      onclick: function () { restartCore(this); }
    }, ['重启内核']);
    var config = el('button', {
      class: 'btn btn-ghost btn-sm', type: 'button',
      onclick: function () { showCoreConfig(); }
    }, ['查看配置']);

    var up = totals.up || 0, down = totals.down || 0;

    ovRefs.cards.replaceChildren(
      statCard('内核状态', statusPill(running ? 'green' : 'red', running ? '运行中' : '已停止'),
        st.last_error ? el('span', { class: 'danger-text' }, [st.last_error]) : ('Xray ' + (version.xray || '未知')),
        [restart, config]),
      statCard('运行时长', el('span', { class: 'serif' }, [fmtDuration(st.uptime_sec || 0)]),
        st.started_at ? ('启动于 ' + fmtTime(st.started_at)) : '尚未启动'),
      statCard('重启次数', String(st.restarts || 0),
        '配置修订 r' + (st.revision || 0) + (st.dropped_logs ? ' · 丢弃日志 ' + st.dropped_logs : '')),
      statCard('累计流量', el('span', {}, [fmtBytes(up + down)]),
        '上行 ' + fmtBytes(up) + ' · 下行 ' + fmtBytes(down))
    );
  }

  function paintOverviewEndpoints() {
    if (!ovRefs || !ovRefs.endpoints.isConnected) return;
    var proxy = state.proxy || {};
    var body = [];

    if (proxy.http) {
      var httpURL = 'http://' + proxyHost(proxy.http) + ':' + proxyPort(proxy.http);
      body.push(el('div', { class: 'field' }, [
        el('span', {}, ['HTTP 代理' + (proxy.http_on === false ? '（已停用）' : '')]),
        codeLine(httpURL)
      ]));
    }
    if (proxy.socks) {
      var socksURL = 'socks5://' + proxyHost(proxy.socks) + ':' + proxyPort(proxy.socks);
      body.push(el('div', { class: 'field' }, [
        el('span', {}, ['SOCKS5 代理' + (proxy.socks_on === false ? '（已停用）' : '') + (proxy.socks_udp ? ' · 支持 UDP' : '')]),
        codeLine(socksURL)
      ]));
    }
    body.push(el('div', { class: 'field' }, [
      el('span', {}, ['全局兜底策略']),
      el('div', { class: 'kv-val' }, [badge(fallbackLabel(proxy.fallback), 'soft'), el('span', { class: 'field-hint' }, [' 目标节点都不可用时，代理按此策略处理。'])])
    ]));

    var users = state.users || [];
    if (users.length && (!state.overviewAccount || !userById(state.overviewAccount))) {
      state.overviewAccount = users[0].id;
    }
    var select = el('select', {
      onchange: function (e) { state.overviewAccount = e.target.value; paintOverviewEndpoints(); }
    }, users.length ? users.map(function (u) {
      return el('option', { value: u.id, selected: u.id === state.overviewAccount ? true : null, text: u.name });
    }) : [el('option', { value: '', text: '（还没有账号）' })]);

    var current = userById(state.overviewAccount) || users[0] || null;
    var password = current ? state.passwords[current.id] : '';
    var example;
    if (!current) {
      example = '先在「账号」页创建一个代理账号，这里会给出可直接粘贴的 curl 命令。';
    } else if (!password) {
      example = '正在读取账号密码…';
    } else {
      var credential = encodeURIComponent(current.name) + ':' + encodeURIComponent(password);
      var lines = [];
      if (proxy.http && proxy.http_on !== false) {
        lines.push('curl -x http://' + credential + '@' + proxyHost(proxy.http) + ':' + proxyPort(proxy.http) + ' https://api.ipify.org');
      }
      if (proxy.socks && proxy.socks_on !== false) {
        lines.push('curl -x socks5h://' + credential + '@' + proxyHost(proxy.socks) + ':' + proxyPort(proxy.socks) + ' https://api.ipify.org');
      }
      example = lines.length ? lines.join('\n') : '两个代理入口都已停用，请先在「设置」里启用。';
    }

    body.push(el('div', { class: 'divider' }));
    body.push(el('div', { class: 'field' }, [
      el('span', {}, ['客户端连接示例']),
      el('div', { class: 'field-row' }, [el('div', { style: { flex: '1 1 auto' } }, [select])]),
      el('div', { class: 'copy-line' }, [
        el('pre', { class: 'code-block' }, [example]),
        current && password ? copyButton(example, '复制命令') : null
      ]),
      el('span', { class: 'field-hint' }, ['curl 直接验证出口 IP；同样的地址可以填进任何支持 HTTP / SOCKS5 的客户端。'])
    ]));

    ovRefs.endpoints.replaceChildren(
      el('div', { class: 'card-head' }, [
        el('span', { class: 'card-title' }, ['代理入口']),
        el('span', { class: 'card-note' }, ['复制地址 / 命令给客户端使用'])
      ]),
      el('div', {}, body)
    );
  }

  function paintOverviewHealth() {
    if (!ovRefs || !ovRefs.health.isConnected) return;
    var sum = (state.status && state.status.node_summary) || {};
    var nodes = state.overviewNodes;
    var total, usable, unsupported, inUse;
    if (nodes) {
      // The node list carries what the status summary does not.
      usable = nodes.filter(function (n) { return !n.unsupported; }).length;
      unsupported = nodes.length - usable;
      inUse = nodes.filter(function (n) { return !!n.in_use; }).length;
      total = nodes.length;
    } else {
      total = sum.total || 0;
      usable = sum.usable || 0;
      unsupported = sum.unsupported || 0;
      inUse = sum.in_use || 0;
    }
    var unchecked = Math.max(0, total - (sum.alive || 0) - (sum.dead || 0));

    function mini(label, value, kind) {
      return el('div', {}, [
        el('div', { class: 'stat-label' }, [label]),
        el('div', { class: 'stat-value' }, [kind ? statusPill(kind, String(value)) : String(value)])
      ]);
    }

    ovRefs.health.replaceChildren(
      el('div', { class: 'card-head' }, [
        el('span', { class: 'card-title' }, ['节点健康']),
        el('span', { class: 'card-note' }, ['来自实时健康检查'])
      ]),
      el('div', { class: 'grid grid-3' }, [
        mini('节点总数', total),
        mini('可用节点', usable),
        mini('使用中', inUse),
        mini('健康', sum.alive || 0, 'green'),
        mini('失效', sum.dead || 0, 'red'),
        mini('内核不支持', unsupported, 'grey')
      ]),
      el('div', { class: 'card-sub', style: { marginTop: '14px', marginBottom: '0' } }, [
        unchecked ? ('其中 ' + unchecked + ' 个节点还没有检查结果。') : '所有节点都已有检查结果。'
      ])
    );
  }

  function paintOverviewTail() {
    if (!ovRefs || !ovRefs.tail.isConnected) return;
    var lines = state.logLines.slice(-9);
    var view = el('div', { class: 'log-view', style: { maxHeight: '260px' } },
      lines.length ? lines.map(logLine) : [el('div', { class: 'log-empty' }, ['暂无日志。'])]);
    ovRefs.tail.replaceChildren(
      el('div', { class: 'card-head' }, [
        el('span', { class: 'card-title' }, ['实时日志']),
        el('span', { class: 'card-note' }, ['最新 9 条']),
        el('div', { class: 'card-actions' }, [
          el('a', { class: 'btn btn-ghost btn-sm', href: '#/logs' }, ['查看全部'])
        ])
      ]),
      view
    );
  }

  function restartCore(btn) {
    confirmDialog('重启内核', '将重新加载 Xray 内核，正在连接的客户端会短暂中断。', '重启').then(function (ok) {
      if (!ok) return;
      runAction(btn, '重启中', function () {
        return api.post('/api/core/restart', {}).then(function () {
          toast('已请求重启内核', 'success');
        });
      });
    });
  }

  function showCoreConfig() {
    api.get('/api/core/config').then(function (data) {
      var text;
      if (data === null || data === undefined) text = '（内核配置为空）';
      else if (typeof data === 'string') text = data;
      else text = JSON.stringify(data, null, 2);
      openModal({
        title: '当前 Xray 配置',
        sub: '只读视图，可用于排查分流与出站问题。',
        wide: true,
        body: el('pre', { class: 'code-block', style: { maxHeight: '60vh', overflow: 'auto' } }, [text]),
        foot: [el('div', { class: 'spacer' }), copyButton(text, '复制 JSON')]
      });
    }).catch(fail);
  }

  // ----------------------------------------------------------------- users --

  function renderUsers(root) {
    userRefs = {
      tbody: el('tbody'),
      card: el('div', { class: 'card' })
    };
    var table = el('table', { class: 'data fixed' }, [
      colgroup([13, 6, 9, 22, 11, 8, 15, 16]),
      el('thead', {}, [el('tr', {}, [
        el('th', {}, ['名称']),
        el('th', {}, ['状态']),
        el('th', {}, ['模式']),
        el('th', {}, ['出口']),
        el('th', { class: 'num' }, ['流量']),
        el('th', {}, ['在线']),
        el('th', {}, ['密码']),
        el('th', { class: 'num' }, ['操作'])
      ])]),
      userRefs.tbody
    ]);

    userRefs.card.replaceChildren(
      el('div', { class: 'card-head' }, [
        el('span', { class: 'card-title' }, ['账号列表']),
        el('span', { class: 'card-note' }, ['每个账号是一组代理用户名 / 密码']),
        el('div', { class: 'card-actions' }, [
          el('button', { class: 'btn btn-primary btn-sm', type: 'button', onclick: function () { openUserEditor(null); } }, ['新建账号'])
        ])
      ]),
      el('div', { class: 'table-wrap' }, [table])
    );

    root.replaceChildren(
      el('div', { class: 'page-head' }, [
        el('div', {}, [
          el('div', { class: 'page-lead' }, ['账号决定谁能连上代理、可以走哪些节点，以及目标节点都不可用时的兜底策略。'])
        ])
      ]),
      userRefs.card
    );

    paintUsers();
    refreshPasswords().then(function () { paintUsers(); }, function () { /* keep the hint */ });
  }

  // colgroup builds fixed column widths, in percent, for tables whose cells
  // hold multi-line text.
  function colgroup(widths) {
    return el('colgroup', {}, widths.map(function (w) {
      return el('col', { style: 'width:' + w + '%' });
    }));
  }

  function paintUsers() {
    if (!userRefs || !userRefs.tbody.isConnected) return;
    var users = state.users || [];
    if (!users.length) {
      userRefs.tbody.replaceChildren(emptyRow(8, '还没有账号。点击右上角「新建账号」创建第一个。'));
      return;
    }
    userRefs.tbody.replaceChildren.apply(userRefs.tbody, users.map(userRow));
  }

  function userRow(u) {
    var password = state.passwords[u.id];
    var ips = u.online_ips || [];

    var exit;
    if (u.active_node) exit = el('span', { class: 'mono' }, [u.active_node]);
    else if (u.mode === 'auto') exit = el('span', { class: 'muted' }, ['自动选择']);
    else if (u.fallback === 'direct') exit = el('span', { class: 'warn-text' }, ['直连']);
    else exit = el('span', { class: 'muted' }, ['拒绝连接']);

    var trafficCell = el('div', {}, [
      el('div', {}, [fmtBytes((u.traffic && (u.traffic.up + u.traffic.down)) || 0)]),
      el('div', { class: 'cell-sub num' }, ['↑ ' + fmtBytes(u.traffic ? u.traffic.up : 0) + ' ↓ ' + fmtBytes(u.traffic ? u.traffic.down : 0)])
    ]);

    var onlineCell = ips.length
      ? el('div', { title: ips.join('\n') }, [
        el('div', {}, [statusPill('green', String(u.online || ips.length) + ' 个 IP')]),
        el('div', { class: 'cell-sub mono nobreak' }, [truncate(ips[0], 22) + (ips.length > 1 ? ' +' + (ips.length - 1) : '')])
      ])
      : el('span', { class: 'muted' }, ['—']);

    var missing = u.missing || [];
    var sub = u.reason || '';
    if (missing.length) sub = (sub ? sub + ' · ' : '') + missing.length + ' 个目标失效';

    return el('tr', {}, [
      el('td', { class: 'col-name' }, [
        el('div', {}, [u.name]),
        u.note ? el('div', { class: 'cell-sub' }, [u.note]) : null
      ]),
      el('td', { class: 'tight' }, [statusPill(u.enabled ? 'green' : 'grey', u.enabled ? '启用' : '停用')]),
      el('td', { class: 'tight' }, [
        badge(modeLabel(u.mode), u.mode === 'auto' ? 'accent' : 'soft'),
        u.balancer ? el('span', { class: 'cell-sub' }, [' 负载均衡']) : null
      ]),
      el('td', { class: 'col-exit' }, [
        el('div', {}, [exit]),
        sub ? el('div', { class: 'cell-sub' + (missing.length ? ' danger-text' : '') }, [sub]) : null,
        el('div', { class: 'cell-sub' }, [(u.node_count || 0) + ' 个可用节点'])
      ]),
      el('td', { class: 'num tight' }, [trafficCell]),
      el('td', { class: 'tight' }, [onlineCell]),
      el('td', { class: 'col-secret' }, [
        password ? el('div', { class: 'copy-line' }, [
          el('span', { class: 'mono nobreak', title: password }, [password]),
          copyButton(password)
        ]) : el('span', { class: 'muted small' }, ['不可见'])
      ]),
      el('td', {}, [el('div', { class: 'cell-actions' }, [
        el('button', { class: 'btn btn-outline btn-sm', type: 'button', onclick: function () { openUserEditor(u); } }, ['编辑']),
        el('button', { class: 'btn btn-ghost btn-sm', type: 'button', onclick: function () { resetTraffic(u, this); } }, ['重置流量']),
        el('button', { class: 'btn btn-danger-text btn-sm', type: 'button', onclick: function () { deleteUser(u); } }, ['删除'])
      ])])
    ]);
  }

  function resetTraffic(user, btn) {
    runAction(btn, '重置中', function () {
      return api.post('/api/users/' + encodeURIComponent(user.id) + '/traffic/reset', {}).then(function () {
        toast('已重置「' + user.name + '」的流量统计', 'success');
      });
    });
  }

  function deleteUser(user) {
    confirmDialog('删除账号', '将删除账号「' + user.name + '」，使用该账号的客户端会立即断线。', '删除').then(function (ok) {
      if (!ok) return;
      api.del('/api/users/' + encodeURIComponent(user.id)).then(function () {
        toast('已删除账号「' + user.name + '」', 'success');
        return refreshPasswords();
      }).catch(fail);
    });
  }

  // ------------------------------------------------------- user editor -----

  function randomPassword(len) {
    var alphabet = 'abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    var out = '';
    var buf = new Uint8Array(len || 16);
    if (window.crypto && window.crypto.getRandomValues) window.crypto.getRandomValues(buf);
    else for (var i = 0; i < buf.length; i++) buf[i] = Math.floor(Math.random() * 256);
    for (var j = 0; j < buf.length; j++) out += alphabet[buf[j] % alphabet.length];
    return out;
  }

  // The node list carries sub_id only sometimes, so subscription membership is
  // matched on both the id and the subscription name.
  function subNameOf(subId) {
    var subs = state.subs || [];
    for (var i = 0; i < subs.length; i++) if (subs[i].id === subId) return subs[i].name;
    return '';
  }

  function nodesOfSub(subId, pool) {
    var name = subNameOf(subId);
    return (pool || state.nodes || []).filter(function (n) {
      if (subId && n.sub_id === subId) return true;
      return !!(name && n.sub_name === name);
    });
  }

  function loadSubNodes(subId) {
    if (!subId) return Promise.resolve([]);
    if (state.subNodes[subId]) return Promise.resolve(state.subNodes[subId]);
    return api.get('/api/nodes').then(function (data) {
      state.subNodes[subId] = nodesOfSub(subId, (data && data.nodes) || []);
      return state.subNodes[subId];
    });
  }

  function openUserEditor(user) {
    var editing = !!user;
    var draft = {
      id: user ? user.id : '',
      name: user ? user.name : '',
      password: '',
      enabled: user ? !!user.enabled : true,
      mode: user ? (user.mode || 'priority') : 'priority',
      fallback: user ? (user.fallback || 'inherit') : 'inherit',
      note: user ? (user.note || '') : '',
      targets: user ? (user.raw_targets || []).map(function (t) {
        return { sub: t.sub || '', node: t.all ? '' : (t.node || ''), all: !!t.all, limit: t.limit || 0 };
      }) : []
    };

    var nameInput = el('input', { type: 'text', value: draft.name, placeholder: '例如 alice', autocomplete: 'off' });
    var passwordInput = el('input', { type: 'text', value: '', placeholder: editing ? '留空表示不修改' : '留空则由服务器生成', autocomplete: 'off' });
    var enabledInput = el('input', { type: 'checkbox', checked: draft.enabled });
    var noteInput = el('input', { type: 'text', value: draft.note, placeholder: '可选，例如「朋友的笔记本」' });

    var modeBox = el('div', { class: 'segmented' }, ['priority', 'auto', 'fixed'].map(function (mode) {
      var input = el('input', {
        type: 'radio', name: 'user-mode', value: mode, checked: draft.mode === mode,
        onchange: function () {
          draft.mode = mode;
          syncModeClasses();
        }
      });
      var desc = {
        priority: '按你排的顺序切换节点',
        auto: '内核自动选延迟最低的节点且切换不断线',
        fixed: '永远用第一个节点'
      }[mode];
      return el('label', { class: 'segmented-opt' + (draft.mode === mode ? ' checked' : '') }, [
        el('span', { class: 'segmented-title' }, [input, el('span', {}, [modeLabel(mode)])]),
        el('span', { class: 'segmented-desc' }, [desc])
      ]);
    }));

    function syncModeClasses() {
      var boxes = modeBox.children;
      for (var i = 0; i < boxes.length; i++) {
        var input = boxes[i].querySelector('input');
        boxes[i].classList.toggle('checked', !!(input && input.checked));
      }
    }

    var fallbackSelect = el('select', {
      onchange: function (e) { draft.fallback = e.target.value; }
    }, ['inherit', 'direct', 'reject'].map(function (f) {
      return el('option', { value: f, selected: draft.fallback === f ? true : null, text: fallbackLabel(f) + '（' + f + '）' });
    }));

    var listBox = el('div', { class: 'targets' });

    function repaintTargets() {
      if (!draft.targets.length) {
        listBox.replaceChildren(el('div', { class: 'targets-empty' }, ['还没有目标。至少添加一个，否则这个账号没有可用的出口节点。']));
        return;
      }
      listBox.replaceChildren.apply(listBox, draft.targets.map(function (target, index) {
        return targetRow(target, index);
      }));
    }

    function targetRow(target, index) {
      var subs = state.subs || [];
      var subOptions = subs.map(function (s) {
        return el('option', { value: s.id, selected: s.id === target.sub ? true : null, text: s.name });
      });
      if (target.sub && !subs.some(function (s) { return s.id === target.sub; })) {
        subOptions.push(el('option', { value: target.sub, selected: true, text: '（订阅已删除）' }));
      }
      if (!subOptions.length) subOptions.push(el('option', { value: '', text: '还没有订阅' }));

      var subSelect = el('select', {
        onchange: function (e) {
          target.sub = e.target.value;
          target.node = '';
          target.all = true;
          repaintTargets();
          loadSubNodes(target.sub).then(function () { repaintTargets(); }, fail);
        }
      }, subOptions);

      var nodes = state.subNodes[target.sub] || [];
      var nodeOptions = [el('option', {
        value: '*', selected: target.all || !target.node ? true : null,
        text: '整条订阅（按顺序全部使用）'
      })];
      nodes.forEach(function (n) {
        nodeOptions.push(el('option', {
          value: n.name, selected: !target.all && target.node === n.name ? true : null,
          text: n.name + (n.unsupported ? '（内核不支持）' : '')
        }));
      });
      if (!target.all && target.node && !nodes.some(function (n) { return n.name === target.node; })) {
        nodeOptions.push(el('option', { value: target.node, selected: true, text: target.node + '（找不到该节点）' }));
      }

      var nodeSelect = el('select', {
        onchange: function (e) {
          if (e.target.value === '*') { target.all = true; target.node = ''; }
          else { target.all = false; target.node = e.target.value; }
        }
      }, nodeOptions);

      var up = el('button', {
        class: 'btn btn-ghost btn-sm', type: 'button', title: '上移', disabled: index === 0,
        onclick: function () {
          var list = draft.targets;
          var tmp = list[index - 1]; list[index - 1] = list[index]; list[index] = tmp;
          repaintTargets();
        }
      }, ['↑']);
      var down = el('button', {
        class: 'btn btn-ghost btn-sm', type: 'button', title: '下移', disabled: index === draft.targets.length - 1,
        onclick: function () {
          var list = draft.targets;
          var tmp = list[index + 1]; list[index + 1] = list[index]; list[index] = tmp;
          repaintTargets();
        }
      }, ['↓']);
      var remove = el('button', {
        class: 'btn btn-ghost btn-sm btn-danger-text', type: 'button', title: '删除这一行',
        onclick: function () { draft.targets.splice(index, 1); repaintTargets(); }
      }, ['✕']);

      return el('div', { class: 'target-row' }, [
        el('span', { class: 'target-index' }, [String(index + 1)]),
        el('div', { class: 'target-fields' }, [subSelect, nodeSelect]),
        el('div', { class: 'target-actions' }, [up, down, remove])
      ]);
    }

    var generate = el('button', {
      class: 'btn btn-outline btn-sm', type: 'button',
      onclick: function () { passwordInput.value = randomPassword(16); }
    }, ['随机生成']);

    var body = el('div', {}, [
      el('div', { class: 'form-grid' }, [
        field('名称', nameInput, '客户端连接时的用户名。'),
        el('div', { class: 'field' }, [
          el('span', {}, ['密码']),
          el('div', { class: 'field-row' }, [passwordInput, generate]),
          el('span', { class: 'field-hint' }, editing ? '留空表示保持原密码不变。' : '留空时由服务器生成一个随机密码并显示一次。')
        ])
      ]),
      field('备注', noteInput),
      el('div', { class: 'field' }, [
        el('span', {}, ['选路模式']),
        modeBox
      ]),
      el('div', { class: 'form-grid' }, [
        field('兜底策略', fallbackSelect, '所有目标节点都不可用时：直连会直接暴露本机 IP，拒绝连接更安全。')
      ]),
      toggleRow('启用该账号', enabledInput, '停用后客户端无法再通过它连接代理。'),
      el('div', { class: 'divider' }),
      el('div', { class: 'field' }, [
        el('div', { class: 'field-row' }, [
          el('span', {}, ['目标（按优先级）']),
          el('div', { style: { flex: '1 1 auto' } }),
          el('button', {
            class: 'btn btn-outline btn-sm', type: 'button',
            onclick: function () {
              var first = (state.subs || [])[0];
              draft.targets.push({ sub: first ? first.id : '', node: '', all: true, limit: 0 });
              repaintTargets();
              if (first) loadSubNodes(first.id).then(function () { repaintTargets(); }, fail);
            }
          }, ['添加目标'])
        ]),
        listBox,
        el('span', { class: 'field-hint' }, ['按顺序从上到下尝试；「整条订阅」表示该订阅里的全部可用节点按顺序参与。'])
      ])
    ]);

    var foot = [el('div', { class: 'spacer' })];
    if (editing) {
      foot.push(el('button', {
        class: 'btn btn-ghost btn-sm', type: 'button',
        onclick: function () {
          resetTraffic({ id: draft.id, name: draft.name }, this);
        }
      }, ['重置流量']));
    }
    foot.push(el('button', { class: 'btn btn-outline', type: 'button', onclick: function () { dialog.close(); } }, ['取消']));
    foot.push(el('button', {
      class: 'btn btn-primary', type: 'button',
      onclick: function () { saveUser(this, draft, nameInput, passwordInput, enabledInput, noteInput, editing, dialog); }
    }, [editing ? '保存修改' : '创建账号']));

    var dialog = openModal({
      title: editing ? ('编辑账号 · ' + draft.name) : '新建账号',
      sub: editing ? '修改后立即生效，已连接的客户端会自动重连。' : '创建后把用户名和密码填进客户端即可使用。',
      wide: true,
      body: body,
      foot: foot
    });

    repaintTargets();
    var wanted = {};
    draft.targets.forEach(function (t) { if (t.sub) wanted[t.sub] = true; });
    Object.keys(wanted).forEach(function (subId) {
      loadSubNodes(subId).then(function () { repaintTargets(); }, function () { /* the select keeps the stale name */ });
    });
    nameInput.focus();
  }

  function saveUser(btn, draft, nameInput, passwordInput, enabledInput, noteInput, editing, dialog) {
    var name = nameInput.value.trim();
    if (!name) { toast('请填写账号名称', 'error'); nameInput.focus(); return; }

    var targets = draft.targets
      .filter(function (t) { return !!t.sub; })
      .map(function (t) {
        return { sub: t.sub, node: t.all ? '' : t.node, all: !!t.all, limit: t.limit || 0 };
      })
      .filter(function (t) { return t.all || t.node; });

    var body = {
      name: name,
      enabled: !!enabledInput.checked,
      mode: draft.mode,
      fallback: draft.fallback,
      note: noteInput.value.trim(),
      targets: targets
    };
    var password = passwordInput.value;
    if (password) body.password = password;

    runAction(btn, '保存中', function () {
      var call = editing
        ? api.patch('/api/users/' + encodeURIComponent(draft.id), body)
        : api.post('/api/users', body);
      return call.then(function (res) {
        dialog.close();
        var created = res && res.user;
        if (!editing && created && !password && created.password) {
          promptPassword('账号已创建', '「' + created.name + '」的初始密码如下，请复制保存。', created.password);
        } else {
          toast(editing ? '账号已更新' : '账号已创建', 'success');
        }
        return refreshPasswords().then(paintUsers);
      });
    });
  }

  // ------------------------------------------------------------------ subs --

  function renderSubs(root) {
    subRefs = { tbody: el('tbody') };
    var table = el('table', { class: 'data fixed' }, [
      colgroup([20, 8, 7, 8, 18, 14, 8, 17]),
      el('thead', {}, [el('tr', {}, [
        el('th', {}, ['名称']),
        el('th', {}, ['状态']),
        el('th', {}, ['格式']),
        el('th', { class: 'num' }, ['节点数']),
        el('th', {}, ['流量']),
        el('th', {}, ['上次更新']),
        el('th', {}, ['启用']),
        el('th', { class: 'num' }, ['操作'])
      ])]),
      subRefs.tbody
    ]);

    root.replaceChildren(
      el('div', { class: 'page-head' }, [
        el('div', {}, [
          el('div', { class: 'page-lead' }, ['订阅是节点的来源：填一条 Clash / v2ray 链接自动更新，或者直接粘贴内容导入。'])
        ])
      ]),
      el('div', { class: 'toolbar' }, [
        el('button', { class: 'btn btn-primary btn-sm', type: 'button', onclick: openSubDialog }, ['新建订阅']),
        el('button', { class: 'btn btn-outline btn-sm', type: 'button', onclick: openImportDialog }, ['粘贴导入']),
        el('div', { class: 'spacer' })
      ]),
      el('div', { class: 'card' }, [el('div', { class: 'table-wrap' }, [table])])
    );

    paintSubs();
  }

  function paintSubs() {
    if (!subRefs || !subRefs.tbody.isConnected) return;
    var subs = state.subs || [];
    if (!subs.length) {
      subRefs.tbody.replaceChildren(emptyRow(8, '还没有订阅。点击「新建订阅」或「粘贴导入」添加第一个。'));
      return;
    }
    subRefs.tbody.replaceChildren.apply(subRefs.tbody, subs.map(subRow));
  }

  // Providers report the account quota in a response header; what is left of it
  // is what decides whether the subscription keeps working.
  function subTrafficCell(sub) {
    var info = sub.user_info || {};
    var used = (info.upload || 0) + (info.download || 0);
    var total = info.total || 0;
    if (!used && !total && !info.expire) {
      return el('span', { class: 'muted' }, ['—']);
    }

    var rows;
    if (total > 0) {
      var left = Math.max(0, total - used);
      // A nearly spent quota is worth colouring: at zero the nodes stop working.
      rows = [el('div', {
        class: left / total <= 0.1 ? 'danger-text' : ''
      }, [left > 0 ? '剩余 ' + fmtBytes(left) : '流量已用尽'])];
      rows.push(el('div', { class: 'cell-sub' }, ['已用 ' + fmtBytes(used) + ' / ' + fmtBytes(total)]));
    } else {
      rows = [el('div', {}, ['无限流量'])];
      rows.push(el('div', { class: 'cell-sub' }, ['已用 ' + fmtBytes(used)]));
    }
    if (info.expire) rows.push(el('div', { class: 'cell-sub' }, [fmtDay(info.expire * 1000) + ' 到期']));
    return el('div', {}, rows);
  }

  function subRow(sub) {
    var statusKind = 'grey', statusText = '未更新';
    if (sub.last_status === 'ok') { statusKind = 'green'; statusText = '正常'; }
    else if (sub.last_status === 'error') { statusKind = 'red'; statusText = '失败'; }

    var interval = sub.interval ? String(sub.interval) : '';
    var updated = parseTime(sub.last_update)
      ? el('span', { title: fmtTime(sub.last_update) }, [fmtAgo(sub.last_update)])
      : el('span', { class: 'muted' }, ['从未']);

    var enabledInput = el('input', {
      type: 'checkbox', checked: !!sub.enabled, title: '启用 / 停用自动更新',
      onchange: function () {
        var box = this;
        api.patch('/api/subs/' + encodeURIComponent(sub.id), { enabled: !!box.checked })
          .then(function () { toast(box.checked ? '已启用该订阅' : '已停用该订阅', 'success'); })
          .catch(function (err) { box.checked = !box.checked; fail(err); });
      }
    });

    var refreshBtn = el('button', {
      class: 'btn btn-outline btn-sm', type: 'button',
      onclick: function () { refreshSub(sub, this); }
    }, ['立即更新']);

    return el('tr', {}, [
      el('td', {}, [
        el('div', {}, [sub.name]),
        sub.url
          ? el('div', { class: 'cell-sub mono nobreak', title: sub.url }, [sub.url])
          : el('div', { class: 'cell-sub' }, ['本地导入，无链接'])
      ]),
      el('td', {}, [
        statusPill(statusKind, statusText),
        !sub.enabled ? el('div', { class: 'cell-sub' }, ['已停用']) : null,
        sub.error ? el('div', { class: 'cell-sub danger-text' }, [truncate(sub.error, 60)]) : null
      ]),
      el('td', {}, [badge(kindLabel(sub.kind))]),
      el('td', { class: 'num' }, [
        el('div', {}, [(sub.cached_usable || 0) + ' / ' + (sub.cached_nodes || 0)]),
        el('div', { class: 'cell-sub num' }, ['可用 / 缓存'])
      ]),
      el('td', {}, [subTrafficCell(sub)]),
      el('td', {}, [
        el('div', {}, [updated]),
        el('div', { class: 'cell-sub' }, [interval ? '每 ' + fmtInterval(interval) + '自动更新' : '不自动更新'])
      ]),
      el('td', {}, [enabledInput]),
      el('td', {}, [el('div', { class: 'cell-actions' }, [
        refreshBtn,
        el('button', { class: 'btn btn-danger-text btn-sm', type: 'button', onclick: function () { deleteSub(sub); } }, ['删除'])
      ])])
    ]);
  }

  function refreshSub(sub, btn) {
    if (!sub.url) { toast('该订阅是本地导入的，没有可更新的链接', 'error'); return; }
    runAction(btn, '更新中', function () {
      return api.post('/api/subs/' + encodeURIComponent(sub.id) + '/refresh', {}).then(function (res) {
        if (res && res.error) toast('更新失败：' + res.error, 'error');
        else toast('订阅「' + sub.name + '」已更新', 'success');
        // Refresh the table and the cached node lists.
        delete state.subNodes[sub.id];
        return api.get('/api/subs').then(function (data) {
          state.subs = (data && data.subs) || state.subs;
          paintSubs();
        }, function () { /* the SSE feed will catch up */ });
      });
    });
  }

  function deleteSub(sub) {
    confirmDialog('删除订阅', '将删除「' + sub.name + '」，依赖它的账号目标会被一并移除。', '删除').then(function (ok) {
      if (!ok) return;
      api.del('/api/subs/' + encodeURIComponent(sub.id)).then(function () {
        toast('已删除订阅「' + sub.name + '」', 'success');
        delete state.subNodes[sub.id];
        return api.get('/api/subs').then(function (data) {
          state.subs = (data && data.subs) || [];
          paintSubs();
        });
      }).catch(fail);
    });
  }

  function openSubDialog() {
    var nameInput = el('input', { type: 'text', placeholder: '例如 机场A' });
    var urlInput = el('input', { type: 'url', placeholder: 'https://example.com/sub?token=...' });
    var kindSelect = el('select', {}, ['auto', 'clash', 'v2ray'].map(function (k) {
      return el('option', { value: k, text: kindLabel(k) });
    }));
    var intervalInput = el('input', { type: 'text', value: '12h', placeholder: '12h' });
    var refreshInput = el('input', { type: 'checkbox', checked: true });

    var dialog = openModal({
      title: '新建订阅',
      sub: '会立刻拉取一次节点列表。',
      body: el('div', {}, [
        field('名称', nameInput, '用于在账号里选择订阅，重名会被拒绝。'),
        field('订阅链接', urlInput),
        el('div', { class: 'form-grid' }, [
          field('格式', kindSelect, '不确定就选「自动识别」。'),
          field('更新间隔', intervalInput, '例如 12h、30m；填 0 表示不自动更新。')
        ]),
        toggleRow('创建后立即拉取', refreshInput)
      ]),
      foot: [
        el('div', { class: 'spacer' }),
        el('button', { class: 'btn btn-outline', type: 'button', onclick: function () { dialog.close(); } }, ['取消']),
        el('button', {
          class: 'btn btn-primary', type: 'button',
          onclick: function () { createSub(this, dialog, nameInput, urlInput, kindSelect, intervalInput, refreshInput); }
        }, ['创建订阅'])
      ]
    });
    nameInput.focus();
  }

  function createSub(btn, dialog, nameInput, urlInput, kindSelect, intervalInput, refreshInput) {
    var name = nameInput.value.trim();
    var url = urlInput.value.trim();
    if (!name) { toast('请填写订阅名称', 'error'); return; }
    if (!url) { toast('请填写订阅链接，或使用「粘贴导入」', 'error'); return; }
    var interval = intervalInput.value.trim();
    var body = { name: name, url: url, kind: kindSelect.value, refresh: !!refreshInput.checked };
    if (interval && interval !== '0') body.interval = interval;

    runAction(btn, '创建中', function () {
      return api.post('/api/subs', body).then(function () {
        dialog.close();
        toast('订阅已创建，正在后台拉取节点', 'success');
        return api.get('/api/subs').then(function (data) {
          state.subs = (data && data.subs) || [];
          paintSubs();
        });
      });
    });
  }

  function openImportDialog() {
    var nameInput = el('input', { type: 'text', placeholder: '例如 本地节点' });
    var kindSelect = el('select', {}, ['auto', 'clash', 'v2ray'].map(function (k) {
      return el('option', { value: k, text: kindLabel(k) });
    }));
    var contentInput = el('textarea', { placeholder: '粘贴 Clash YAML，或 base64 / 明文节点链接（每行一条）' });

    var dialog = openModal({
      title: '粘贴导入',
      sub: '适合没有链接的本地节点列表，导入后不会自动更新。',
      wide: true,
      body: el('div', {}, [
        el('div', { class: 'form-grid' }, [
          field('名称', nameInput),
          field('格式', kindSelect, '不确定就选「自动识别」。')
        ]),
        field('内容', contentInput)
      ]),
      foot: [
        el('div', { class: 'spacer' }),
        el('button', { class: 'btn btn-outline', type: 'button', onclick: function () { dialog.close(); } }, ['取消']),
        el('button', {
          class: 'btn btn-primary', type: 'button',
          onclick: function () { importSub(this, dialog, nameInput, kindSelect, contentInput); }
        }, ['导入'])
      ]
    });
    nameInput.focus();
  }

  function importSub(btn, dialog, nameInput, kindSelect, contentInput) {
    var name = nameInput.value.trim();
    var content = contentInput.value;
    if (!name) { toast('请填写订阅名称', 'error'); return; }
    if (!content.trim()) { toast('请粘贴要导入的内容', 'error'); return; }

    runAction(btn, '导入中', function () {
      return api.post('/api/subs/import', { name: name, kind: kindSelect.value, content: content }).then(function (res) {
        dialog.close();
        var count = res && res.sub ? res.sub.cached_nodes : 0;
        toast('已导入「' + name + '」，共 ' + (count || 0) + ' 个节点', 'success');
        return api.get('/api/subs').then(function (data) {
          state.subs = (data && data.subs) || [];
          paintSubs();
        });
      });
    });
  }

  // ----------------------------------------------------------------- nodes --

  function fillProtoOptions(select) {
    var protos = [];
    (state.nodes || []).forEach(function (n) {
      if (n.type && protos.indexOf(n.type) === -1) protos.push(n.type);
    });
    protos.sort();
    if (state.nodeFilter.proto && protos.indexOf(state.nodeFilter.proto) === -1) state.nodeFilter.proto = '';
    var options = [el('option', { value: '', text: '全部协议' })];
    protos.forEach(function (p) {
      options.push(el('option', { value: p, selected: p === state.nodeFilter.proto ? true : null, text: p }));
    });
    select.replaceChildren.apply(select, options);
  }

  function renderNodes(root) {
    var subs = state.subs || [];
    var subSelect = el('select', {
      onchange: function (e) { state.nodeFilter.sub = e.target.value; loadNodes(); }
    }, [el('option', { value: '', text: '全部订阅' })].concat(subs.map(function (s) {
      return el('option', { value: s.id, selected: s.id === state.nodeFilter.sub ? true : null, text: s.name });
    })));

    var protoSelect = el('select', {
      onchange: function (e) { state.nodeFilter.proto = e.target.value; paintNodes(); }
    });
    fillProtoOptions(protoSelect);
    nodeRefs = { groups: el('div', { class: 'grid' }), protoSelect: protoSelect };

    var hideToggle = el('input', {
      type: 'checkbox', checked: !!state.nodeFilter.hideUnsupported,
      onchange: function (e) { state.nodeFilter.hideUnsupported = e.target.checked; paintNodes(); }
    });

    root.replaceChildren(
      el('div', { class: 'page-head' }, [
        el('div', {}, [
          el('div', { class: 'page-lead' }, ['节点来自订阅缓存。测速会通过节点真实请求一次探测地址，结果同时用于自动切换。'])
        ])
      ]),
      el('div', { class: 'toolbar' }, [
        el('label', { class: 'muted' }, ['订阅']),
        subSelect,
        el('label', { class: 'muted' }, ['协议']),
        protoSelect,
        el('label', { class: 'toggle' }, [hideToggle, el('span', {}, ['隐藏内核不支持的节点'])]),
        el('div', { class: 'spacer' }),
        el('button', {
          class: 'btn btn-outline btn-sm', type: 'button',
          onclick: function () { runAction(this, '刷新中', loadNodes); }
        }, ['刷新'])
      ]),
      nodeRefs.groups
    );

    loadNodes();
  }

  function loadNodes() {
    return api.get('/api/nodes').then(function (data) {
      state.nodes = (data && data.nodes) || [];
      if (nodeRefs && nodeRefs.protoSelect && nodeRefs.protoSelect.isConnected) fillProtoOptions(nodeRefs.protoSelect);
      paintNodes();
    }).catch(function (err) {
      if (!nodeRefs) return;
      nodeRefs.groups.replaceChildren(emptyState('加载节点失败：' + err.message));
    });
  }

  function visibleNodes() {
    var filter = state.nodeFilter;
    var pool = filter.sub ? nodesOfSub(filter.sub, state.nodes) : (state.nodes || []);
    return pool.filter(function (n) {
      if (filter.proto && n.type !== filter.proto) return false;
      if (filter.hideUnsupported && n.unsupported) return false;
      return true;
    }).map(function (n) {
      return { node: n, subName: n.sub_name || subNameOf(n.sub_id) || '未命名订阅' };
    });
  }

  function paintNodes() {
    if (!nodeRefs || !nodeRefs.groups.isConnected) return;
    var rows = visibleNodes();
    if (!rows.length) {
      nodeRefs.groups.replaceChildren(emptyState('没有符合条件的节点。'));
      return;
    }

    var order = [];
    var bySub = {};
    rows.forEach(function (row) {
      var key = row.node.sub_id || row.subName || '';
      if (!bySub[key]) { bySub[key] = { name: row.subName, rows: [] }; order.push(key); }
      bySub[key].rows.push(row.node);
    });

    nodeRefs.groups.replaceChildren.apply(nodeRefs.groups, order.map(function (key) {
      var group = bySub[key];
      var tbody = el('tbody');
      var table = el('table', { class: 'data fixed' }, [
        colgroup([26, 9, 20, 8, 11, 14, 12]),
        el('thead', {}, [el('tr', {}, [
          el('th', {}, ['名称']),
          el('th', {}, ['协议']),
          el('th', {}, ['地址']),
          el('th', { class: 'num' }, ['延迟']),
          el('th', {}, ['状态']),
          el('th', {}, ['使用者']),
          el('th', { class: 'num' }, ['操作'])
        ])]),
        tbody
      ]);

      var paint = function () {
        var nodes = group.rows.filter(function (n) {
          if (state.nodeFilter.proto && n.type !== state.nodeFilter.proto) return false;
          if (state.nodeFilter.hideUnsupported && n.unsupported) return false;
          return true;
        });
        tbody.replaceChildren.apply(tbody, nodes.length ? nodes.map(nodeRow) : [emptyRow(7, '该订阅没有符合条件的节点。')]);
      };
      tbody._paint = paint;

      return el('div', { class: 'card' }, [
        el('div', { class: 'card-head' }, [
          el('span', { class: 'card-title' }, [group.name || '未命名订阅']),
          el('span', { class: 'card-note' }, [group.rows.length + ' 个节点']),
          el('div', { class: 'card-actions' }, [
            el('button', {
              class: 'btn btn-outline btn-sm', type: 'button',
              // Test by explicit ids: the server cannot filter by subscription
              // when the cached nodes carry no sub_id.
              onclick: function () { testGroup(this, group.rows, group.name); }
            }, ['全部测速'])
          ])
        ]),
        el('div', { class: 'table-wrap' }, [table])
      ]);
    }));

    // Fill the bodies after the cards are in the DOM so every group paints.
    Array.prototype.forEach.call(nodeRefs.groups.querySelectorAll('tbody'), function (tbody) {
      if (tbody._paint) tbody._paint();
    });
  }

  function nodeStatusCell(node) {
    if (node.unsupported) return statusPill('grey', '不支持');
    if (!node.health || !node.health.checked) return statusPill('grey', '未检测');
    if (node.health.alive) return statusPill('green', '正常');
    return statusPill('red', '失效');
  }

  function nodeLatency(node) {
    if (!node.health || !node.health.checked) return el('span', { class: 'muted' }, ['—']);
    if (!node.health.alive) return el('span', { class: 'muted' }, ['—']);
    var ms = node.health.latency_ms || 0;
    return el('span', {}, [ms + ' ms']);
  }

  function nodeRow(node) {
    var unsupported = !!node.unsupported;
    var name = el('div', { class: 'tight', title: node.name || '' }, [node.name || '(未命名)']);
    if (unsupported) {
      name.appendChild(el('div', { class: 'cell-sub', title: node.unsupported }, [truncate(node.unsupported, 40)]));
    }
    if (node.health && node.health.last_error && !node.health.alive) {
      name.appendChild(el('div', {
        class: 'cell-sub danger-text', title: node.health.last_error
      }, [truncate(node.health.last_error, 42)]));
    }
    // A node whose subscription asks for certificate verification to be
    // skipped is trusted by pinning the certificate it presented.
    if (node.pinned_cert_sha256) {
      name.appendChild(el('div', {
        class: 'cell-sub',
        title: '已固定证书指纹：' + node.pinned_cert_sha256
      }, ['已固定证书 ' + node.pinned_cert_sha256.slice(0, 12) + '…']));
    }

    return el('tr', { class: unsupported ? 'row-unsupported' : '' }, [
      el('td', {}, [name]),
      el('td', {}, [badge(node.type)]),
      el('td', {}, [el('span', { class: 'mono nobreak', title: (node.server || '') + ':' + (node.port || '') }, [(node.server || '') + ':' + (node.port || '')])]),
      el('td', { class: 'num tight' }, [nodeLatency(node)]),
      el('td', { class: 'tight' }, [nodeStatusCell(node)]),
      el('td', { class: 'tight', title: (node.used_by || []).join(', ') }, [joinList(node.used_by)]),
      el('td', {}, [el('div', { class: 'cell-actions' }, [
        unsupported ? null : el('button', {
          class: 'btn btn-outline btn-sm', type: 'button',
          onclick: function () { testNodes(this, { ids: [node.id] }, '测速中'); }
        }, ['测速'])
      ])])
    ]);
  }

  function testGroup(btn, nodes, name) {
    var ids = nodes.filter(function (n) { return !n.unsupported; }).map(function (n) { return n.id; });
    if (!ids.length) { toast('该订阅没有可测速的节点', 'error'); return; }
    testNodes(btn, { ids: ids.slice(0, 200) }, '测试 ' + (name || '订阅'));
  }

  function testNodes(btn, payload, label) {
    runAction(btn, label, function () {
      return api.post('/api/nodes/test', payload).then(function (data) {
        var results = (data && data.results) || {};
        var updated = 0;
        (state.nodes || []).forEach(function (node) {
          var result = results[node.id];
          if (!result) return;
          node.health = result;
          updated += 1;
        });
        if (!updated) toast('没有拿到测速结果', 'info');
        else toast('已更新 ' + updated + ' 个节点的测速结果', 'success');
        paintNodes();
      });
    });
  }

  // ------------------------------------------------------------------ logs --

  function renderLogs(root) {
    state.logFilter = state.logFilter || { level: '', q: '', user: '' };
    logRefs = { view: el('div', { class: 'log-view' }), count: el('span', { class: 'card-note' }) };

    var levelSelect = el('select', {
      onchange: function (e) { state.logFilter.level = e.target.value; paintLogs(); }
    }, [el('option', { value: '', text: '全部级别' })].concat(['debug', 'info', 'warning', 'error'].map(function (l) {
      return el('option', { value: l, selected: state.logFilter.level === l ? true : null, text: LEVEL_LABEL[l] + '（' + l + '）' });
    })));

    var searchInput = el('input', {
      type: 'search', placeholder: '搜索日志内容…', value: state.logFilter.q,
      oninput: function (e) {
        var value = e.target.value;
        clearTimeout(searchInput._timer);
        searchInput._timer = setTimeout(function () { state.logFilter.q = value; paintLogs(); }, 180);
      }
    });

    var userSelect = el('select', {
      onchange: function (e) { state.logFilter.user = e.target.value; paintLogs(); }
    }, [el('option', { value: '', text: '全部账号' })].concat((state.users || []).map(function (u) {
      return el('option', { value: u.name, selected: state.logFilter.user === u.name ? true : null, text: u.name });
    })));

    var pauseBtn = el('button', {
      class: 'btn btn-outline btn-sm', type: 'button',
      onclick: function () {
        state.logPaused = !state.logPaused;
        pauseBtn.textContent = state.logPaused ? '继续' : '暂停';
        if (state.logPaused) updateLogCount();
        else paintLogs();
      }
    }, [state.logPaused ? '继续' : '暂停']);

    logRefs.view.addEventListener('scroll', function () {
      var node = logRefs.view;
      state.logStick = node.scrollHeight - node.scrollTop - node.clientHeight < 40;
    });

    root.replaceChildren(
      el('div', { class: 'page-head' }, [
        el('div', {}, [
          el('div', { class: 'page-lead' }, ['日志通过 SSE 实时推送；「暂停」只是冻结画面，日志仍然在后台收集。'])
        ])
      ]),
      el('div', { class: 'toolbar' }, [
        levelSelect,
        userSelect,
        el('div', { style: { flex: '0 1 220px' } }, [searchInput]),
        pauseBtn,
        el('button', {
          class: 'btn btn-outline btn-sm', type: 'button',
          onclick: function () {
            state.logLines = [];
            paintLogs();
            toast('视图已清空，新日志会继续显示', 'info');
          }
        }, ['清空视图']),
        el('button', {
          class: 'btn btn-outline btn-sm', type: 'button',
          onclick: downloadLogs
        }, ['下载日志']),
        el('div', { class: 'spacer' }),
        logRefs.count
      ]),
      logRefs.view
    );

    paintLogs();
    if (state.logStick) logRefs.view.scrollTop = logRefs.view.scrollHeight;
  }

  function paintLogs() {
    if (!logRefs || !logRefs.view.isConnected) return;
    var entries = (state.logLines || []).filter(logMatches);
    logRefs.view.replaceChildren.apply(logRefs.view, entries.length
      ? entries.map(logLine)
      : [el('div', { class: 'log-empty' }, ['没有符合当前筛选条件的日志。'])]);
    if (state.logStick) logRefs.view.scrollTop = logRefs.view.scrollHeight;
    updateLogCount();
  }

  function updateLogCount() {
    if (!logRefs || !logRefs.count.isConnected) return;
    var shown = logRefs.view.querySelectorAll('.log-line').length;
    logRefs.count.textContent = '已显示 ' + shown + ' 条 / 缓存 ' + (state.logLines || []).length + ' 条' +
      (state.logPaused ? ' · 已暂停' : '');
  }

  function downloadLogs() {
    if (!logRefs || !logRefs.view.isConnected) return;
    var lines = Array.prototype.map.call(logRefs.view.querySelectorAll('.log-line'), function (line) {
      return line.dataset.raw || line.textContent;
    });
    if (!lines.length) { toast('当前没有可下载的日志', 'error'); return; }
    var blob = new Blob([lines.join('\n') + '\n'], { type: 'text/plain;charset=utf-8' });
    var url = URL.createObjectURL(blob);
    var now = new Date();
    var name = 'v2h-logs-' + now.getFullYear() + pad2(now.getMonth() + 1) + pad2(now.getDate()) + '-' +
      pad2(now.getHours()) + pad2(now.getMinutes()) + pad2(now.getSeconds()) + '.log';
    var link = el('a', { href: url, download: name, style: { display: 'none' } });
    document.body.appendChild(link);
    link.click();
    link.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 4000);
    toast('已导出 ' + lines.length + ' 条日志', 'success');
  }

  // -------------------------------------------------------------- settings --

  function renderSettings(root) {
    root.replaceChildren(emptyState('正在读取设置…'));
    api.get('/api/settings').then(function (settings) {
      state.settings = settings;
      paintSettings(root);
    }).catch(function (err) {
      root.replaceChildren(emptyState('读取设置失败：' + err.message));
    });
  }

  function paintSettings(root) {
    var s = state.settings || {};
    var panel = s.panel || {};
    var proxy = s.proxy || {};
    var logs = s.logs || {};
    var health = s.health || {};
    var turnstile = panel.turnstile || {};
    var admin = panel.admin || {};

    root.replaceChildren(
      el('div', { class: 'page-head' }, [
        el('div', {}, [el('div', { class: 'page-lead' }, ['每个分区单独保存；保存后写入 config.yaml 并立即生效（监听地址等需要重启容器）。'])])
      ]),
      panelCard(panel),
      adminCard(admin),
      turnstileCard(turnstile),
      proxyCard(proxy),
      healthCard(health),
      logsCard(logs)
    );
  }

  function saveButton(onClick) {
    return el('button', { class: 'btn btn-primary btn-sm', type: 'button', onclick: onClick }, ['保存']);
  }

  function numberOr(input, fallback) {
    var value = Number(input.value);
    return isFinite(value) && value > 0 ? value : fallback;
  }

  function panelCard(panel) {
    var listenInput = el('input', { type: 'text', value: panel.listen || '' });
    var publicInput = el('input', { type: 'text', value: panel.public_url || '', placeholder: 'https://panel.example.com' });
    var hoursInput = el('input', { type: 'number', min: '1', value: String(panel.session_hours || 12) });
    var tokenInput = el('input', { type: 'text', value: panel.api_token || '', readonly: true });

    var save = saveButton(function () {
      runAction(this, '保存中', function () {
        return api.patch('/api/settings', {
          panel: {
            listen: listenInput.value.trim(),
            public_url: publicInput.value.trim(),
            session_hours: numberOr(hoursInput, panel.session_hours || 12)
          }
        }).then(applySaved);
      });
    });

    var rotate = el('button', {
      class: 'btn btn-outline btn-sm', type: 'button',
      onclick: function () {
        var btn = this;
        confirmDialog('轮换 API Token', '旧 Token 会立即失效，正在使用它的 CLI / TUI 需要更新。', '轮换').then(function (ok) {
          if (!ok) return;
          runAction(btn, '轮换中', function () {
            return api.post('/api/settings/token', {}).then(function (res) {
              tokenInput.value = (res && res.api_token) || '';
              toast('API Token 已轮换', 'success');
            });
          });
        });
      }
    }, ['轮换']);

    return card('面板', '控制面板自身的监听与访问方式', null, [
      el('div', { class: 'form-grid' }, [
        field('监听地址', listenInput, '例如 0.0.0.0:9080；修改后需要重启容器才会生效。'),
        field('对外访问地址', publicInput, '用于生成外部链接，可留空。'),
        field('会话有效期（小时）', hoursInput, '登录 Cookie 的有效时间。'),
        el('div', { class: 'field' }, [
          el('span', {}, ['API Token']),
          el('div', { class: 'field-row' }, [tokenInput, copyButton(panel.api_token || '', '复制'), rotate]),
          el('span', { class: 'field-hint' }, ['CLI / TUI 用它与面板通信，只允许 token_ips 里的来源使用。'])
        ])
      ]),
      el('div', { class: 'form-actions' }, [save])
    ]);
  }

  function adminCard(admin) {
    var currentInput = el('input', { type: 'password', autocomplete: 'current-password' });
    var newInput = el('input', { type: 'password', autocomplete: 'new-password' });

    var submit = el('button', {
      class: 'btn btn-primary btn-sm', type: 'button',
      onclick: function () {
        var current = currentInput.value;
        var next = newInput.value;
        if (!current) { toast('请输入当前密码', 'error'); return; }
        if (next.length < 8) { toast('新密码至少 8 位', 'error'); return; }
        runAction(this, '提交中', function () {
          return api.post('/api/settings/password', { current: current, password: next }).then(function () {
            currentInput.value = '';
            newInput.value = '';
            toast('管理员密码已更新', 'success');
          });
        });
      }
    }, ['修改密码']);

    return card('管理员', '面板登录账号', null, [
      el('div', { class: 'kv' }, [
        kv('用户名', el('span', { class: 'mono' }, [admin.username || '—'])),
        kv('密码状态', admin.password_set ? statusPill('green', '已设置') : statusPill('amber', '未设置'))
      ]),
      el('div', { class: 'divider' }),
      el('div', { class: 'form-grid' }, [
        field('当前密码', currentInput),
        field('新密码', newInput, '至少 8 位，建议使用随机生成的长密码。')
      ]),
      el('div', { class: 'form-actions' }, [submit])
    ]);
  }

  function turnstileCard(turnstile) {
    var enabledInput = el('input', { type: 'checkbox', checked: !!turnstile.enabled });
    var siteInput = el('input', { type: 'text', value: turnstile.site_key || '', placeholder: '0x4AAAAAAA...' });
    var secretInput = el('input', {
      type: 'password',
      placeholder: turnstile.secret_set ? '已设置，留空表示不修改' : '粘贴 Secret Key'
    });
    var failOpenInput = el('input', { type: 'checkbox', checked: !!turnstile.fail_open });

    var save = saveButton(function () {
      var payload = {
        panel: {
          turnstile: {
            enabled: !!enabledInput.checked,
            site_key: siteInput.value.trim(),
            fail_open: !!failOpenInput.checked
          }
        }
      };
      var secret = secretInput.value;
      if (secret) payload.panel.turnstile.secret_key = secret;
      var btn = this;
      runAction(btn, '保存中', function () {
        return api.patch('/api/settings', payload).then(function () {
          secretInput.value = '';
          return applySaved();
        });
      });
    });

    return card('Cloudflare Turnstile', '登录页的人机验证', null, [
      toggleRow('启用 Turnstile', enabledInput, '开启后登录必须通过 Cloudflare 验证，可挡住自动化爆破。'),
      el('div', { class: 'form-grid' }, [
        field('Site Key', siteInput, '来自 Cloudflare Turnstile 控制台，会公开在登录页。'),
        el('div', { class: 'field' }, [
          el('span', {}, ['Secret Key' + (turnstile.secret_set ? '（' + '已设置' + '）' : '')]),
          secretInput,
          el('span', { class: 'field-hint' }, ['只有你填入新值时才写入，接口不会返回已保存的密钥。'])
        ])
      ]),
      toggleRow('校验服务不可用时放行（fail_open）', failOpenInput, '公网部署建议关闭：Cloudflare 不可达时拒绝登录更安全。'),
      el('div', { class: 'form-actions' }, [save])
    ]);
  }

  function proxyCard(proxy) {
    var http = proxy.http || {};
    var socks = proxy.socks || {};

    var httpEnabled = el('input', { type: 'checkbox', checked: proxy.http ? !!http.enabled : true });
    var httpListen = el('input', { type: 'text', value: http.listen || '' });
    var socksEnabled = el('input', { type: 'checkbox', checked: proxy.socks ? !!socks.enabled : true });
    var socksListen = el('input', { type: 'text', value: socks.listen || '' });
    var socksUDP = el('input', { type: 'checkbox', checked: !!socks.udp });
    var sniffing = el('input', { type: 'checkbox', checked: !!proxy.sniffing });
    var fallbackSelect = el('select', {}, ['reject', 'direct'].map(function (f) {
      return el('option', { value: f, selected: proxy.fallback === f ? true : null, text: fallbackLabel(f) + '（' + f + '）' });
    }));
    var dnsInput = el('input', { type: 'text', value: (proxy.dns_servers || []).join(', '), placeholder: '1.1.1.1, 8.8.8.8' });

    var save = saveButton(function () {
      var dns = dnsInput.value.split(',').map(function (x) { return x.trim(); }).filter(function (x) { return !!x; });
      runAction(this, '保存中', function () {
        return api.patch('/api/settings', {
          proxy: {
            http: { enabled: !!httpEnabled.checked, listen: httpListen.value.trim() },
            socks: { enabled: !!socksEnabled.checked, listen: socksListen.value.trim(), udp: !!socksUDP.checked },
            sniffing: !!sniffing.checked,
            fallback: fallbackSelect.value,
            dns_servers: dns
          }
        }).then(applySaved);
      });
    });

    return card('代理', '客户端连接的入口', null, [
      sectionHead('HTTP 代理', '普通 HTTP / HTTPS 代理入口。'),
      el('div', { class: 'form-grid' }, [
        field('监听地址', httpListen),
        toggleRow('启用 HTTP 代理', httpEnabled)
      ]),
      el('div', { class: 'divider' }),
      sectionHead('SOCKS5 代理', '支持 UDP 转发，适合游戏和 UDP 应用。'),
      el('div', { class: 'form-grid' }, [
        field('监听地址', socksListen),
        toggleRow('启用 SOCKS5 代理', socksEnabled)
      ]),
      toggleRow('允许 UDP（SOCKS5）', socksUDP),
      el('div', { class: 'divider' }),
      el('div', { class: 'form-grid' }, [
        field('兜底策略', fallbackSelect, '账号没有自己的兜底设置时使用这里的策略。'),
        field('DNS 服务器', dnsInput, '逗号分隔，例如 1.1.1.1, 8.8.8.8；用于内核解析域名。')
      ]),
      toggleRow('开启流量嗅探（sniffing）', sniffing, '按域名分流和统计流量时需要开启。'),
      el('div', { class: 'form-actions' }, [save])
    ]);
  }

  function healthCard(health) {
    var enabled = el('input', { type: 'checkbox', checked: !!health.enabled });
    var probeURL = el('input', { type: 'text', value: health.probe_url || '', placeholder: 'https://www.gstatic.com/generate_204' });
    var interval = el('input', { type: 'text', value: health.interval || '5m0s', placeholder: '5m0s' });
    var timeout = el('input', { type: 'text', value: health.timeout || '30s', placeholder: '30s' });
    var failures = el('input', { type: 'number', min: '1', value: String(health.failures || 3) });
    var successes = el('input', { type: 'number', min: '1', value: String(health.successes || 2) });
    var cooldown = el('input', { type: 'text', value: health.switch_cooldown || '1m0s', placeholder: '1m0s' });

    var save = saveButton(function () {
      runAction(this, '保存中', function () {
        return api.patch('/api/settings', {
          health: {
            enabled: !!enabled.checked,
            probe_url: probeURL.value.trim(),
            interval: interval.value.trim(),
            timeout: timeout.value.trim(),
            failures: numberOr(failures, health.failures || 3),
            successes: numberOr(successes, health.successes || 2),
            switch_cooldown: cooldown.value.trim()
          }
        }).then(applySaved);
      });
    });

    return card('健康检查', '决定节点是否可用，以及按序优先模式的切换时机', null, [
      toggleRow('启用健康检查', enabled, '关闭后只会使用订阅里第一个可用节点，不做存活探测。'),
      field('探测地址', probeURL, '节点通过它来判断网络是否通畅，建议用小体积的 204 地址。'),
      el('div', { class: 'form-grid' }, [
        field('探测间隔', interval, 'Go 时长写法，例如 30s、5m、1h。'),
        field('单次超时', timeout, '例如 10s、30s。'),
        field('连续失败判死次数', failures),
        field('连续成功判活次数', successes),
        field('切换冷却时间', cooldown, '切换目标后的静默期，避免节点频繁抖动。')
      ]),
      el('div', { class: 'form-actions' }, [save])
    ]);
  }

  function logsCard(logs) {
    var levelSelect = el('select', {}, ['debug', 'info', 'warning', 'error', 'none'].map(function (l) {
      return el('option', { value: l, selected: logs.level === l ? true : null, text: l + (LEVEL_LABEL[l] ? '（' + LEVEL_LABEL[l] + '）' : '') });
    }));
    var accessLog = el('input', { type: 'checkbox', checked: !!logs.access_log });
    var maxSize = el('input', { type: 'number', min: '1', value: String(logs.max_size_mb || 10) });
    var maxBackups = el('input', { type: 'number', min: '1', value: String(logs.max_backups || 5) });
    var ringSize = el('input', { type: 'number', min: '1', value: String(logs.ring_size || 1000) });
    var console = el('input', { type: 'checkbox', checked: !!logs.console });

    var save = saveButton(function () {
      runAction(this, '保存中', function () {
        return api.patch('/api/settings', {
          logs: {
            level: levelSelect.value,
            access_log: !!accessLog.checked,
            max_size_mb: numberOr(maxSize, logs.max_size_mb || 10),
            max_backups: numberOr(maxBackups, logs.max_backups || 5),
            ring_size: numberOr(ringSize, logs.ring_size || 1000),
            console: !!console.checked
          }
        }).then(applySaved);
      });
    });

    return card('日志', '文件日志与面板里的日志缓存', null, [
      el('div', { class: 'form-grid' }, [
        field('日志级别', levelSelect, '级别越高写入越少；none 表示关闭文件日志。'),
        field('日志目录', el('input', { type: 'text', value: logs.dir || '', readonly: true }), '配置文件里的目录，需要修改请编辑 config.yaml。'),
        field('单个文件上限（MB）', maxSize),
        field('保留文件数量', maxBackups),
        field('面板缓存条数', ringSize, '决定日志页最多能回溯多少条历史日志。')
      ]),
      toggleRow('记录访问日志', accessLog, '记录每个请求的客户端、目标与流量，用于排查。'),
      toggleRow('同时输出到控制台', console, '容器里 docker logs 能看到这些日志。'),
      el('div', { class: 'form-actions' }, [save])
    ]);
  }

  function applySaved(res) {
    if (res && res.settings) state.settings = res.settings;
    toast('设置已保存', 'success');
    return res;
  }

  // ------------------------------------------------------------------ help --

  function renderHelp(root) {
    if (state.help) { paintHelp(root, state.help); return; }
    root.replaceChildren(emptyState('正在读取帮助…'));
    api.get('/api/help').then(function (help) {
      state.help = help;
      paintHelp(root, help);
    }).catch(function (err) {
      root.replaceChildren(emptyState('读取帮助失败：' + err.message));
    });
  }

  function paintHelp(root, help) {
    var commands = help.commands || [];
    var tuiKeys = help.tui_keys || [];
    var flags = help.global_flags || [];

    var walkthrough = card('怎么用', '三步把一个客户端接上代理', null, [
      el('div', { class: 'steps' }, [
        step('建立订阅', '在「订阅」页填入你的 Clash / v2ray 订阅链接，等节点拉取完成；也可以粘贴内容导入。'),
        step('创建账号', '在「账号」页新建账号，添加目标（订阅 + 节点，或整条订阅），选择选路模式与兜底策略。'),
        step('客户端连接', '把面板给出的 HTTP / SOCKS5 地址和账号密码填进浏览器或客户端，用 curl 可以快速验证出口 IP。')
      ]),
      el('div', { class: 'divider' }),
      el('div', { class: 'field' }, [
        el('span', {}, ['验证命令示例']),
        codeLine('curl -x http://用户名:密码@主机:端口 https://api.ipify.org')
      ])
    ]);

    var commandCards = commands.map(function (cmd) {
      var blocks = [
        el('div', { class: 'help-cmd' }, [
          el('div', {}, [
            el('span', { class: 'help-name' }, [cmd.name]),
            el('span', { class: 'help-summary muted' }, ['  ' + (cmd.summary || '')])
          ]),
          cmd.usage ? el('pre', { class: 'code-block', style: { marginTop: '8px' } }, [cmd.usage]) : null,
          (cmd.details && cmd.details.length)
            ? el('ul', { class: 'help-details' }, cmd.details.map(function (d) { return el('li', {}, [d]); }))
            : null,
          (cmd.examples && cmd.examples.length)
            ? el('div', { class: 'help-examples' }, cmd.examples.map(function (ex) {
              return el('pre', { class: 'code-block' }, [ex]);
            }))
            : null
        ])
      ];
      return el('div', {}, blocks);
    });

    root.replaceChildren(
      el('div', { class: 'page-head' }, [
        el('div', {}, [el('div', { class: 'page-lead' }, ['面板、CLI 和 TUI 操作的是同一份配置，可以混着用。'])])
      ]),
      walkthrough,
      card('CLI 命令', '在容器里执行 v2h <命令>', null, commandCards.length ? commandCards : [emptyState('没有命令说明。')]),
      el('div', { class: 'grid grid-2' }, [
        card('TUI 按键', 'v2h tui', null, [keyTable(tuiKeys)]),
        card('全局参数', '所有命令通用', null, [keyTable(flags)])
      ])
    );
  }

  function step(title, text) {
    return el('div', { class: 'step' }, [
      el('span', { class: 'step-num' }),
      el('div', { class: 'step-body' }, [
        el('div', { class: 'sec-title' }, [title]),
        el('div', { class: 'muted small' }, [text])
      ])
    ]);
  }

  function keyTable(keys) {
    if (!keys.length) return emptyState('暂无内容。');
    return el('table', { class: 'data' }, [
      el('thead', {}, [el('tr', {}, [el('th', {}, ['按键 / 参数']), el('th', {}, ['说明'])])]),
      el('tbody', {}, keys.map(function (k) {
        return el('tr', {}, [
          el('td', {}, [el('span', { class: 'mono' }, [k.key])]),
          el('td', {}, [k.action])
        ]);
      }))
    ]);
  }

  // ------------------------------------------------------------------ boot --

  function boot() {
    applyTheme();
    bindShell();
    api.get('/api/session').then(function (session) {
      state.session = session;
      state.version = session.version || null;
      if (session.authenticated) {
        state.user = session.user || '';
        state.csrf = session.csrf || '';
        startApp();
      } else {
        showLogin(session);
      }
    }).catch(function (err) {
      showLogin(null);
      toast('无法读取面板状态：' + err.message, 'error');
    }).then(function () {
      var splash = $('boot');
      if (splash) splash.remove();
    });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
