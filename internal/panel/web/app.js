(function () {
  'use strict';
  var BASE = document.body.getAttribute('data-base') || '/warden';
  var state = {
    session: { authenticated: false },
    csrf: '',
    view: 'overview',
    range: '1h',
    overview: null,
    metrics: null,
    proxies: null,
    effect: null,
    diagnostics: null,
    cn: null,
    singbox: null,
    backups: [],
    audit: [],
    settings: null,
    loading: {}
  };
  var views = ['overview', 'metrics', 'proxies', 'singbox', 'effect', 'diagnostics', 'backups', 'audit', 'settings'];

  function qs(sel) { return document.querySelector(sel); }
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) { e.className = cls; }
    if (text !== undefined && text !== null) { e.textContent = String(text); }
    return e;
  }
  function add(parent, child) { if (child) { parent.appendChild(child); } return parent; }
  function clear(node) { while (node.firstChild) { node.removeChild(node.firstChild); } }
  function fmtBytes(n) {
    n = Number(n) || 0;
    var units = ['B', 'KB', 'MB', 'GB', 'TB'];
    var i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n.toFixed(0) : n.toFixed(n >= 100 ? 0 : 1)) + ' ' + units[i];
  }
  function fmtRate(n) { return fmtBytes(n) + '/s'; }
  function fmtDuration(sec) {
    sec = Math.max(0, Number(sec) || 0);
    var d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    if (d > 0) { return d + ' 天 ' + h + ' 小时'; }
    if (h > 0) { return h + ' 小时 ' + m + ' 分'; }
    if (m > 0) { return m + ' 分钟'; }
    return Math.floor(sec) + ' 秒';
  }
  function fmtTime(ts) {
    if (!ts) { return '—'; }
    var d = typeof ts === 'number' ? new Date(ts * 1000) : new Date(ts);
    if (isNaN(d.getTime())) { return String(ts); }
    return d.toLocaleString('zh-CN', { hour12: false });
  }
  function pct(v) { return (Number(v) || 0).toFixed(1) + '%'; }
  function showToast(msg, isError) {
    var t = qs('#toast');
    t.textContent = msg;
    t.className = 'toast' + (isError ? ' error' : '');
    t.hidden = false;
    clearTimeout(showToast.timer);
    showToast.timer = setTimeout(function () { t.hidden = true; }, 4200);
  }
  function setStatus(ok, text) {
    var p = qs('#top-status');
    p.textContent = text;
    p.className = 'pill ' + (ok ? 'online' : 'offline');
  }
  function setBusy(button, busy) {
    if (!button) { return; }
    button.disabled = !!busy;
  }

  async function api(path, opts) {
    opts = opts || {};
    var method = opts.method || 'GET';
    var headers = {};
    var init = { method: method, headers: headers, credentials: 'same-origin' };
    if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(opts.body);
    }
    if (state.csrf && method !== 'GET' && method !== 'HEAD') {
      headers['X-CSRF-Token'] = state.csrf;
    }
    var res;
    try {
      res = await fetch(BASE + path, init);
    } catch (err) {
      throw new Error('无法连接服务器: ' + err.message);
    }
    var text = await res.text();
    var data = null;
    if (text) {
      try { data = JSON.parse(text); } catch (err) { data = { error: text }; }
    }
    if (res.status === 401 && path !== '/api/login') {
      showLogin();
      throw new Error('登录已失效，请重新登录');
    }
    if (!res.ok) {
      throw new Error((data && data.error) || ('请求失败 HTTP ' + res.status));
    }
    return data || {};
  }

  function boot() {
    api('/api/session').then(function (s) {
      applySession(s);
      if (s.authenticated) { showApp(); } else { showLogin(); }
    }).catch(function () {
      showLogin();
    }).finally(function () {
      qs('#boot').hidden = true;
    });
  }

  function applySession(s) {
    state.session = s || { authenticated: false };
    state.csrf = (s && s.csrf) || '';
    qs('#panel-version').textContent = s.version ? ('v' + String(s.version).replace(/^v/, '')) : '';
    qs('#panel-key').textContent = s.key_fingerprint ? ('密钥 ' + s.key_fingerprint) : '';
  }

  function showLogin() {
    state.session = { authenticated: false };
    state.csrf = '';
    qs('#app').hidden = true;
    qs('#login').hidden = false;
    qs('#login-error').hidden = true;
    qs('#login-key').value = '';
    setStatus(false, '未登录');
    setTimeout(function () { qs('#login-key').focus(); }, 60);
  }

  function showApp() {
    qs('#login').hidden = true;
    qs('#app').hidden = false;
    qs('#top-url').textContent = state.session.public_url || '';
    setStatus(true, '已连接');
    navigate(state.view || 'overview');
    loadOverview();
  }

  function navigate(view) {
    if (views.indexOf(view) < 0) { view = 'overview'; }
    state.view = view;
    views.forEach(function (v) {
      qs('#view-' + v).hidden = v !== view;
    });
    Array.prototype.forEach.call(document.querySelectorAll('.nav-item'), function (b) {
      b.classList.toggle('active', b.getAttribute('data-view') === view);
    });
    if (view === 'overview') { loadOverview(); }
    if (view === 'metrics') { loadMetrics(); }
    if (view === 'proxies') { loadProxies(); }
    if (view === 'singbox') { loadSingbox(); }
    if (view === 'effect') { loadEffect(); }
    if (view === 'diagnostics') { loadDiagnostics(); }
    if (view === 'backups') { loadBackups(); }
    if (view === 'audit') { loadAudit(); }
    if (view === 'settings') { loadSettings(); }
  }

  function pageNote(id, text) { var n = qs(id); if (n) { n.textContent = text; } }
  function statCard(label, value, sub, tone, percent) {
    var card = el('div', 'stat-card');
    add(card, el('div', 'label', label));
    add(card, el('div', 'value', value));
    if (sub) { add(card, el('div', 'sub', sub)); }
    if (typeof percent === 'number') {
      var meter = el('div', 'meter' + (tone ? ' ' + tone : ''));
      var bar = el('span');
      bar.className = 'bar-' + Math.max(0, Math.min(100, Math.round(percent / 5) * 5));
      meter.appendChild(bar);
      card.appendChild(meter);
    }
    return card;
  }
  function statusPill(text, kind) {
    var p = el('span', 'status-pill' + (kind ? ' ' + kind : ''), text);
    return p;
  }
  function infoList(items) {
    var dl = el('dl', 'dl');
    items.forEach(function (it) {
      add(dl, el('dt', '', it[0]));
      var dd = el('dd');
      if (it[2]) { dd.appendChild(it[2]); } else { dd.textContent = it[1] === undefined || it[1] === null ? '—' : String(it[1]); }
      add(dl, dd);
    });
    return dl;
  }

  async function loadOverview() {
    if (state.loading.overview) { return; }
    state.loading.overview = true;
    var box = qs('#overview-content');
    pageNote('#overview-note', '加载中…');
    try {
      var data = await api('/api/overview');
      state.overview = data;
      renderOverview(data);
      pageNote('#overview-note', '更新于 ' + fmtTime(data.as_of) + '，每 20 秒自动刷新');
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      pageNote('#overview-note', '加载失败');
      showToast(err.message, true);
    } finally {
      state.loading.overview = false;
    }
  }

  function renderOverview(data) {
    var box = qs('#overview-content');
    clear(box);
    var host = data.host || {};
    var disk = host.disk || {};
    var memPct = host.mem_total ? host.mem_used / host.mem_total * 100 : 0;
    var diskPct = disk.total ? disk.used / disk.total * 100 : 0;
    var cpu = Number(host.cpu_percent) || 0;
    var stats = el('div', 'stat-grid');
    stats.appendChild(statCard('CPU 使用率', pct(cpu), (host.load1 || 0).toFixed(2) + ' 负载 · ' + (host.processes || 0) + ' 进程', cpu > 85 ? 'danger' : cpu > 65 ? 'warn' : '', cpu));
    stats.appendChild(statCard('内存', fmtBytes(host.mem_used) + ' / ' + fmtBytes(host.mem_total), pct(memPct) + ' 已使用 · 可用 ' + fmtBytes(host.mem_available), memPct > 85 ? 'danger' : memPct > 65 ? 'warn' : '', memPct));
    stats.appendChild(statCard('磁盘', fmtBytes(disk.used) + ' / ' + fmtBytes(disk.total), pct(diskPct) + ' 已使用 · 剩余 ' + fmtBytes(disk.free), diskPct > 85 ? 'danger' : diskPct > 65 ? 'warn' : '', diskPct));
    stats.appendChild(statCard('网络', fmtRate(host.net_rx_rate) + ' ↓', fmtRate(host.net_tx_rate) + ' ↑ · TCP ' + (host.tcp_connections || 0), '', undefined));
    box.appendChild(stats);

    if (data.certificate && data.certificate.ok === false) {
      var bad = el('div', 'alert bad');
      add(bad, el('span', '', '证书'));
      add(bad, el('span', '', data.certificate.error || '证书不可用'));
      box.appendChild(bad);
    } else if (data.certificate && typeof data.certificate.days_remaining === 'number' && data.certificate.days_remaining < 21) {
      var warn = el('div', 'alert');
      add(warn, el('span', '', '证书'));
      add(warn, el('span', '', '证书将在 ' + data.certificate.days_remaining + ' 天后到期，请检查自动续期。'));
      box.appendChild(warn);
    }

    var grid = el('div', 'overview-grid');
    var left = el('div', 'stack');

    left.appendChild(servicePanel(data.services || []));
    left.appendChild(proxySummaryPanel(data.proxies || []));
    grid.appendChild(left);

    var right = el('div', 'stack');
    right.appendChild(certPanel(data.certificate || {}));
    right.appendChild(edgePanel(data));
    right.appendChild(panel('最近审计', auditList(data.audit || [], true)));
    grid.appendChild(right);
    box.appendChild(grid);
  }

  function panel(title, body, cls) {
    var p = el('div', 'panel' + (cls ? ' ' + cls : ''));
    add(p, el('h3', 'section-title', title));
    add(p, body);
    return p;
  }

  function servicePanel(services) {
    var p = el('div');
    if (!services.length) { add(p, el('div', 'empty', '没有发现可管理的服务')); return panel('服务', p); }
    var list = el('div', 'service-list');
    services.forEach(function (s) {
      var row = el('div', 'service-card');
      var main = el('div');
      add(main, el('div', 'name', s.name || s.id));
      add(main, el('div', 'desc', (s.description || '') + ' · ' + (s.unit || '')));
      add(row, main);
      var right = el('div');
      right.className = 'service-actions';
      var active = (s.active || '').toLowerCase() === 'active';
      right.appendChild(statusPill(active ? '运行中' : (s.active || '未知'), active ? 'ok' : 'bad'));
      ['restart', 'start', 'stop'].forEach(function (action) {
        var b = el('button', 'btn small' + (action === 'stop' ? ' danger' : ''), action === 'restart' ? '重启' : action === 'start' ? '启动' : '停止');
        b.type = 'button';
        b.addEventListener('click', function () {
          if (action === 'restart' || action === 'stop') {
            confirmDialog(action === 'restart' ? '重启服务' : '停止服务', '确认对 ' + (s.unit || s.id) + ' 执行“' + (action === 'restart' ? '重启' : '停止') + '”？').then(function (yes) {
              if (yes) { serviceAction(s.id, action, b); }
            });
          } else { serviceAction(s.id, action, b); }
        });
        right.appendChild(b);
      });
      add(row, right);
      list.appendChild(row);
    });
    add(p, list);
    return panel('服务', p);
  }

  async function serviceAction(id, action, button) {
    setBusy(button, true);
    try {
      var out = await api('/api/services/' + encodeURIComponent(id) + '/action', { method: 'POST', body: { action: action } });
      showToast((out.unit || id) + ' 已执行 ' + (out.action || action));
      loadOverview();
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(button, false);
    }
  }

  function proxySummaryPanel(proxies) {
    var wrap = el('div');
    var list = el('div', 'service-list');
    proxies.forEach(function (p) {
      var row = el('div', 'service-card');
      var main = el('div');
      add(main, el('div', 'name', p.label || p.name));
      var desc = (p.path || '') + ' · 请求 ' + (p.requests || 0) + ' · 流量 ' + fmtBytes((p.bytes_in || 0) + (p.bytes_out || 0));
      if (p.requests) { desc += ' · 成功率 ' + pct(p.success_rate || 0); }
      var rates = p.rates || {};
      if (rates.requests_per_min) { desc += ' · ' + rates.requests_per_min + ' 次/分'; }
      add(main, el('div', 'desc', desc));
      add(row, main);
      var pillBox = el('div', 'service-pills');
      pillBox.appendChild(statusPill(p.enabled ? '启用' : '停用', p.enabled ? 'ok' : 'warn'));
      if (Number(p.in_flight) > 0) { pillBox.appendChild(statusPill('进行中 ' + p.in_flight, 'warn')); }
      add(row, pillBox);
      list.appendChild(row);
    });
    add(wrap, list);
    return panel('代理', wrap);
  }

  function certPanel(cert) {
    var body;
    if (!cert || cert.ok === false) {
      body = el('div', 'empty', (cert && cert.error) || '尚未检测到证书');
    } else {
      var d = typeof cert.days_remaining === 'number' ? cert.days_remaining : 0;
      body = infoList([
        ['域名', (cert.dns_names || []).join(', ') || '—'],
        ['到期', fmtTime(cert.not_after) + ' (' + d + ' 天)'],
        ['签发', cert.issuer || '—'],
        ['状态', cert.ok ? '有效' : '无效']
      ]);
    }
    return panel('证书', body);
  }

  function edgePanel(data) {
    var dns = data.dns || {};
    var sni = data.sni || {};
    var body = infoList([
      ['SNI 入口', (sni.enabled ? '启用' : '停用') + ' · ' + (sni.entry || '—')],
      ['面板域名', sni.panel_domain || '—'],
      ['DNS 分流', dns.enabled ? ('启用 · ' + (dns.listen || '')) : '停用'],
      ['DNS 域名', (dns.domains || []).join(', ') || '—'],
      ['备份数量', (data.backups && data.backups.count) || 0]
    ]);
    return panel('入口', body);
  }

  function auditList(items, compact) {
    if (!items || !items.length) { return el('div', 'empty', '暂无记录'); }
    var list = el('div', 'audit-list');
    items.forEach(function (it) {
      var row = el('div', 'audit-row');
      var main = el('div');
      add(main, el('div', '', it.action || ''));
      add(main, el('div', 'meta', (it.ip || '') + ' · ' + fmtTime(it.ts)));
      add(row, main);
      add(row, statusPill(it.result || '', it.result === 'ok' ? 'ok' : 'bad'));
      list.appendChild(row);
    });
    return list;
  }

  var proxyNames = { docker: 'Docker 镜像代理', github: 'GitHub 下载代理', dns: 'DNS 分流', sni: 'SNI 入口' };

  function proxyLabel(name) { return proxyNames[name] || name || '未知通道'; }

  function kvRows(items) {
    var box = el('div', 'kv-grid');
    items.forEach(function (it) {
      var cell = el('div', 'kv-cell');
      add(cell, el('span', '', it[0]));
      add(cell, el('b', '', it[1]));
      box.appendChild(cell);
    });
    return box;
  }

  function msText(v) { return (Number(v) || 0) + ' ms'; }

  function latencyRows(latency) {
    var list = el('div', 'lat-list');
    var hist = (latency && latency.histogram) || [];
    var max = 0;
    hist.forEach(function (b) { if ((Number(b.count) || 0) > max) { max = Number(b.count) || 0; } });
    if (!max) { add(list, el('p', 'muted', '近一小时没有请求。')); return list; }
    hist.forEach(function (b) {
      var count = Number(b.count) || 0;
      var row = el('div', 'lat-row');
      add(row, el('span', 'lat-label', b.label || ''));
      var track = el('span', 'lat-track');
      var step = Math.round(count / max * 20) * 5;
      if (step < 5) { step = count > 0 ? 5 : 0; }
      track.appendChild(el('span', 'lat-fill hf-' + step));
      row.appendChild(track);
      add(row, el('span', 'lat-count', String(count)));
      list.appendChild(row);
    });
    return list;
  }

  async function loadMetrics() {
    var box = qs('#metrics-content');
    pageNote('#metrics-note', '加载中…');
    try {
      var data = await api('/api/metrics?range=' + encodeURIComponent(state.range));
      state.metrics = data;
      renderMetrics(data);
      pageNote('#metrics-note', '范围 ' + data.range + ' · 更新于 ' + fmtTime(data.as_of));
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      pageNote('#metrics-note', '加载失败');
      showToast(err.message, true);
    }
  }

  function renderMetrics(data) {
    var box = qs('#metrics-content');
    clear(box);
    var points = data.points || [];
    var latest = data.latest || (points.length ? points[points.length - 1] : {});
    var host = data.host || {};
    var counters = data.counters || {};
    var traffic = data.traffic || [];
    var per = data.proxy_series || {};
    var names = Object.keys(per);
    var memPct = latest.mem_total ? latest.mem_used / latest.mem_total * 100 : 0;
    var swapPct = latest.swap_total ? latest.swap_used / latest.swap_total * 100 : 0;
    var load1 = Number(latest.load1) || 0;
    var load5 = Number(latest.load5) || 0;
    var load15 = Number(latest.load15) || 0;
    var cpus = Number(host.cpus) || 1;
    var loadPct = Math.min(100, load1 / cpus * 100);
    var cpuPct = Number(latest.cpu_percent) || 0;

    var stats = el('div', 'stat-grid');
    stats.appendChild(statCard('CPU 使用率', pct(cpuPct), '负载 ' + load1.toFixed(2) + ' / ' + load5.toFixed(2) + ' / ' + load15.toFixed(2), cpuPct > 85 ? 'danger' : cpuPct > 65 ? 'warn' : '', cpuPct));
    stats.appendChild(statCard('内存', fmtBytes(latest.mem_used) + ' / ' + fmtBytes(latest.mem_total), pct(memPct) + ' 已使用 · 可用 ' + fmtBytes(latest.mem_available), memPct > 85 ? 'danger' : memPct > 65 ? 'warn' : '', memPct));
    stats.appendChild(statCard('交换分区', fmtBytes(latest.swap_used) + ' / ' + fmtBytes(latest.swap_total), latest.swap_total ? pct(swapPct) + ' 已使用' : '未启用 swap', swapPct > 60 ? 'warn' : '', latest.swap_total ? swapPct : undefined));
    stats.appendChild(statCard('系统负载', load1.toFixed(2), cpus + ' 核 · 利用率 ' + pct(loadPct), loadPct > 85 ? 'danger' : loadPct > 65 ? 'warn' : '', loadPct));
    box.appendChild(stats);

    var grid = el('div', 'chart-grid');
    grid.appendChild(chartCard('CPU 使用率', '范围 ' + data.range, 'chart-cpu', [{ label: 'CPU %', color: '#52d3c5' }]));
    grid.appendChild(chartCard('内存与交换', '占用百分比', 'chart-mem', [{ label: '内存', color: '#7cc4ff' }, { label: '交换', color: '#f472b6' }]));
    grid.appendChild(chartCard('网络速率', '字节 / 秒', 'chart-net', [{ label: '下行', color: '#52d3c5' }, { label: '上行', color: '#fbbf24' }]));
    grid.appendChild(chartCard('磁盘速率', '字节 / 秒', 'chart-disk', [{ label: '读取', color: '#7cc4ff' }, { label: '写入', color: '#f472b6' }]));
    grid.appendChild(chartCard('系统负载', '1 / 5 / 15 分钟平均值', 'chart-load', [{ label: '1 分', color: '#52d3c5' }, { label: '5 分', color: '#7cc4ff' }, { label: '15 分', color: '#fbbf24' }]));
    grid.appendChild(chartCard('TCP 连接', '总数与已建立', 'chart-tcp', [{ label: '连接', color: '#52d3c5' }, { label: '已建立', color: '#7cc4ff' }]));
    box.appendChild(grid);

    var stats2 = el('div', 'stat-grid');
    stats2.appendChild(statCard('网络吞吐', fmtRate(latest.net_rx_rate) + ' ↓', '上行 ' + fmtRate(latest.net_tx_rate) + ' · ' + (data.ifaces || []).length + ' 个接口', '', undefined));
    stats2.appendChild(statCard('磁盘 I/O', fmtRate(latest.disk_read_rate), '写入 ' + fmtRate(latest.disk_write_rate), '', undefined));
    stats2.appendChild(statCard('TCP 连接', String(latest.tcp_connections || 0), '已建立 ' + (latest.established || 0) + ' · TIME_WAIT ' + (latest.time_wait || 0), '', undefined));
    stats2.appendChild(statCard('连接跟踪', Number(latest.conntrack) >= 0 && latest.conntrack !== undefined ? String(latest.conntrack) : '—', '监听 ' + (latest.listen || 0) + ' · 进程 ' + (latest.processes || 0), '', undefined));
    box.appendChild(stats2);

    var tgrid = el('div', 'chart-grid');
    tgrid.appendChild(chartCard('代理请求量', '全部通道 · 近 60 分钟', 'chart-req', [{ label: '请求', color: '#52d3c5' }, { label: '失败', color: '#f87171' }]));
    tgrid.appendChild(chartCard('代理流量', '全部通道 · 入 / 出 · 近 60 分钟', 'chart-bytes', [{ label: '入', color: '#7cc4ff' }, { label: '出', color: '#fbbf24' }]));
    var trafficPanel = panel('代理通道', tgrid);
    if (names.length) {
      var chan = el('div', 'chan-grid');
      names.forEach(function (name) {
        var list = per[name] || [];
        var totalIn = 0, totalOut = 0, totalReq = 0, totalFail = 0;
        list.forEach(function (pt) {
          totalIn += Number(pt.bytes_in) || 0;
          totalOut += Number(pt.bytes_out) || 0;
          totalReq += Number(pt.requests) || 0;
          totalFail += Number(pt.failures) || 0;
        });
        var card = el('div', 'chan-card');
        var head = el('div', 'chan-head');
        add(head, el('strong', '', proxyLabel(name)));
        add(head, el('span', 'muted', totalReq + ' 次 · 失败 ' + totalFail + ' · ' + fmtBytes(totalIn + totalOut)));
        card.appendChild(head);
        var canvas = el('canvas', 'chan-canvas');
        canvas.id = 'chan-' + name;
        card.appendChild(canvas);
        chan.appendChild(card);
      });
      trafficPanel.appendChild(chan);
    } else {
      add(trafficPanel, el('p', 'muted', '所选范围内还没有代理流量。'));
    }
    box.appendChild(trafficPanel);

    var lower = el('div', 'overview-grid');
    var leftStack = el('div', 'stack');
    var ifaces = data.ifaces || [];
    if (ifaces.length) {
      leftStack.appendChild(tablePanel('网络接口', ['接口', '下行', '上行'], ifaces.map(function (f) { return [f.name, fmtRate(f.rx_rate), fmtRate(f.tx_rate)]; })));
    }
    var hit = Number(counters.dns_hit) || 0;
    var miss = Number(counters.dns_miss) || 0;
    var dnsBox = el('div', 'panel');
    add(dnsBox, el('h3', 'section-title', 'DNS 分流命中'));
    if (hit + miss > 0) {
      add(dnsBox, el('p', 'muted', '命中分流的查询由网关直接应答，其余查询转发上游。'));
      dnsBox.appendChild(infoList([
        ['命中分流', hit + ' 次'],
        ['转发上游', miss + ' 次'],
        ['分流比例', pct(hit / (hit + miss) * 100)],
        ['总计', (hit + miss) + ' 次查询']
      ]));
    } else {
      add(dnsBox, el('p', 'muted', '本次运行还没有处理 DNS 查询。'));
    }
    leftStack.appendChild(dnsBox);
    lower.appendChild(leftStack);

    var rightStack = el('div', 'stack');
    var hostBox = el('div', 'panel');
    add(hostBox, el('h3', 'section-title', '主机'));
    hostBox.appendChild(infoList([
      ['主机名', host.hostname || '—'],
      ['系统', host.os || '—'],
      ['内核', host.kernel || '—'],
      ['架构', (host.arch || '—') + ' · ' + cpus + ' 核'],
      ['运行时间', fmtDuration(host.uptime_seconds)],
      ['磁盘', fmtBytes((host.disk || {}).used) + ' / ' + fmtBytes((host.disk || {}).total)],
      ['样本数', String(points.length)]
    ]));
    rightStack.appendChild(hostBox);
    lower.appendChild(rightStack);
    box.appendChild(lower);

    setTimeout(function () {
      drawSeries('chart-cpu', points, [function (p) { return Number(p.cpu_percent) || 0; }], '#52d3c5', 100);
      drawSeries('chart-mem', points, [function (p) { return p.mem_total ? p.mem_used / p.mem_total * 100 : 0; }, function (p) { return p.swap_total ? p.swap_used / p.swap_total * 100 : 0; }], ['#7cc4ff', '#f472b6'], 100);
      drawSeries('chart-net', points, [function (p) { return Number(p.net_rx_rate) || 0; }, function (p) { return Number(p.net_tx_rate) || 0; }], ['#52d3c5', '#fbbf24'], 0);
      drawSeries('chart-disk', points, [function (p) { return Number(p.disk_read_rate) || 0; }, function (p) { return Number(p.disk_write_rate) || 0; }], ['#7cc4ff', '#f472b6'], 0);
      drawSeries('chart-load', points, [function (p) { return Number(p.load1) || 0; }, function (p) { return Number(p.load5) || 0; }, function (p) { return Number(p.load15) || 0; }], ['#52d3c5', '#7cc4ff', '#fbbf24'], 0);
      drawSeries('chart-tcp', points, [function (p) { return Number(p.tcp_connections) || 0; }, function (p) { return Number(p.established) || 0; }], ['#52d3c5', '#7cc4ff'], 0);
      drawSeries('chart-req', traffic, [function (p) { return Number(p.requests) || 0; }, function (p) { return Number(p.failures) || 0; }], ['#52d3c5', '#f87171'], 0);
      drawSeries('chart-bytes', traffic, [function (p) { return Number(p.bytes_in) || 0; }, function (p) { return Number(p.bytes_out) || 0; }], ['#7cc4ff', '#fbbf24'], 0);
      names.forEach(function (name) {
        drawSeries('chan-' + name, per[name] || [], [function (p) { return Number(p.bytes_in) || 0; }, function (p) { return Number(p.bytes_out) || 0; }], ['#7cc4ff', '#fbbf24'], 0);
      });
    }, 30);
  }

  function chartCard(title, sub, id, legend) {
    var card = el('div', 'chart-card');
    add(card, el('h3', '', title));
    add(card, el('div', 'chart-sub', sub));
    var canvas = el('canvas', 'chart-canvas');
    canvas.id = id;
    card.appendChild(canvas);
    var leg = el('div', 'chart-legend');
    legend.forEach(function (l) {
      var item = el('span');
      var dot = el('span', 'dot');
      dot.className = 'dot dot-' + (legend.indexOf(l) + 1);
      item.appendChild(dot);
      item.appendChild(document.createTextNode(l.label));
      leg.appendChild(item);
    });
    card.appendChild(leg);
    return card;
  }

  function drawSeries(id, points, getters, colors, fixedMax) {
    var canvas = document.getElementById(id);
    if (!canvas) { return; }
    var dpr = window.devicePixelRatio || 1;
    var width = canvas.clientWidth || 400;
    var height = canvas.clientHeight || 180;
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    var ctx = canvas.getContext('2d');
    if (!ctx) { return; }
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, width, height);
    ctx.fillStyle = 'rgba(7, 12, 17, 0.55)';
    ctx.fillRect(0, 0, width, height);
    var padL = 38, padR = 8, padT = 10, padB = 18;
    var w = width - padL - padR, h = height - padT - padB;
    var max = Number(fixedMax) || 0;
    var values = [];
    getters.forEach(function (g) {
      var arr = points.map(function (p) { var v = Number(g(p)) || 0; if (v < 0) { v = 0; } values.push(v); return v; });
      if (!max) { arr.forEach(function (v) { if (v > max) { max = v; } }); }
    });
    if (!max || !isFinite(max)) { max = 1; }
    max = max * 1.15;
    ctx.strokeStyle = 'rgba(35, 49, 63, 0.65)';
    ctx.lineWidth = 1;
    for (var i = 0; i <= 3; i++) {
      var y = padT + h * i / 3;
      ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(padL + w, y); ctx.stroke();
    }
    ctx.fillStyle = '#64748b';
    ctx.font = '10px system-ui, sans-serif';
    ctx.textAlign = 'right';
    for (var j = 0; j <= 3; j++) {
      var val = max * (3 - j) / 3.45;
      var yy = padT + h * j / 3 + 3;
      ctx.fillText(shortNumber(val), padL - 6, yy);
    }
    if (points.length < 2) { return; }
    getters.forEach(function (g, idx) {
      ctx.strokeStyle = Array.isArray(colors) ? colors[idx] : colors;
      ctx.lineWidth = 1.6;
      ctx.beginPath();
      points.forEach(function (p, k) {
        var v = Number(g(p)) || 0;
        var x = padL + w * (k / (points.length - 1));
        var y = padT + h - (v / max) * h;
        if (y < padT) { y = padT; }
        if (y > padT + h) { y = padT + h; }
        if (k === 0) { ctx.moveTo(x, y); } else { ctx.lineTo(x, y); }
      });
      ctx.stroke();
    });
  }

  function shortNumber(v) {
    v = Number(v) || 0;
    if (v >= 1024 * 1024 * 1024) { return (v / 1024 / 1024 / 1024).toFixed(1) + 'G'; }
    if (v >= 1024 * 1024) { return (v / 1024 / 1024).toFixed(1) + 'M'; }
    if (v >= 1024) { return (v / 1024).toFixed(1) + 'K'; }
    return v.toFixed(0);
  }

  function setRange(range) {
    state.range = range;
    Array.prototype.forEach.call(document.querySelectorAll('#metrics-range .range'), function (b) {
      b.classList.toggle('active', b.getAttribute('data-range') === range);
    });
    loadMetrics();
  }

  async function loadProxies() {
    var box = qs('#proxies-content');
    pageNote('#proxies-note', '加载中…');
    try {
      var data = await api('/api/proxies');
      state.proxies = data;
      renderProxies(data);
      pageNote('#proxies-note', '更新于 ' + fmtTime(data.as_of));
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      showToast(err.message, true);
    }
  }

  function proxyCard(p) {
    var card = el('div', 'proxy-card');
    var head = el('div', 'proxy-head');
    add(head, el('h3', '', p.label || proxyLabel(p.name)));
    head.appendChild(statusPill(p.enabled ? '启用' : '停用', p.enabled ? 'ok' : 'warn'));
    if (Number(p.in_flight) > 0) { head.appendChild(statusPill('进行中 ' + p.in_flight, 'warn')); }
    card.appendChild(head);
    var meta = p.path || '';
    if (p.last_at) { meta += ' · 最后请求 ' + fmtTime(p.last_at); }
    if (p.last_error) { meta += ' · ' + p.last_error; }
    add(card, el('div', 'meta', meta));

    var rates = p.rates || {};
    var win = p.window || {};
    var minute = p.last_minute || {};
    var stats = el('div', 'proxy-stats');
    [
      ['请求总数', String(p.requests || 0)],
      ['成功率', p.requests ? pct(p.success_rate || 0) : '—'],
      ['近一分钟', (minute.requests || 0) + ' 次 / ' + (minute.failures || 0) + ' 失败'],
      ['近一分钟流量', fmtRate(minute.in_per_sec || 0) + ' ↓ / ' + fmtRate(minute.out_per_sec || 0) + ' ↑']
    ].forEach(function (pair) {
      var item = el('div');
      add(item, el('span', '', pair[0]));
      add(item, el('b', '', pair[1]));
      stats.appendChild(item);
    });
    card.appendChild(stats);

    var classes = p.status_classes || {};
    var lat = p.latency || {};
    var up = p.upstream || {};
    var sum = el('div', 'proxy-summary');
    add(sum, el('span', 'muted', '2xx ' + (classes['2xx'] || 0) + ' · 3xx ' + (classes['3xx'] || 0) + ' · 4xx ' + (classes['4xx'] || 0) + ' · 5xx ' + (classes['5xx'] || 0)));
    if (Number(lat.count) > 0) {
      add(sum, el('span', 'muted', '延迟 平均 ' + msText(lat.avg_millis) + ' · P50 ' + msText(lat.p50) + ' · P90 ' + msText(lat.p90) + ' · P99 ' + msText(lat.p99) + ' · 最大 ' + msText(lat.max) + '（' + lat.count + ' 次采样）'));
    } else {
      add(sum, el('span', 'muted', '延迟 近一小时没有样本'));
    }
    if (up.count) { add(sum, el('span', 'muted', '上游响应 平均 ' + msText(up.avg_millis) + '（' + up.count + ' 次采样）')); }
    add(sum, el('span', 'muted', '近一小时 请求 ' + (win.requests || 0) + ' · 失败 ' + (win.failures || 0) + ' · 入 ' + fmtBytes(win.bytes_in) + ' · 出 ' + fmtBytes(win.bytes_out)));
    card.appendChild(sum);

    var body = el('div', 'proxy-body');
    var latPanel = el('div', 'proxy-sub');
    add(latPanel, el('h4', '', '延迟分布'));
    latPanel.appendChild(latencyRows(lat));
    body.appendChild(latPanel);
    var tgtPanel = el('div', 'proxy-sub');
    add(tgtPanel, el('h4', '', '热点目标'));
    var targets = p.targets || [];
    if (targets.length) {
      var list = el('div', 'target-list');
      targets.slice(0, 6).forEach(function (t) {
        var row = el('div', 'target-row');
        add(row, el('code', 'target-name', t.target || '—'));
        add(row, el('span', 'muted', (t.requests || 0) + ' 次 · ' + fmtBytes(t.bytes)));
        list.appendChild(row);
      });
      tgtPanel.appendChild(list);
    } else {
      add(tgtPanel, el('p', 'muted', '还没有记录到目标。'));
    }
    body.appendChild(tgtPanel);
    card.appendChild(body);

    var canvas = el('canvas', 'chan-canvas');
    canvas.id = 'px-' + p.name;
    card.appendChild(canvas);

    var actions = el('div', 'proxy-actions');
    var test = el('button', 'btn small', '测试');
    test.type = 'button';
    test.addEventListener('click', async function () {
      setBusy(test, true);
      try {
        var out = await api('/api/proxies/test', { method: 'POST', body: { name: p.name } });
        showToast((p.label || p.name) + '：' + (out.ok ? '正常' : '异常') + (out.status ? ' HTTP ' + out.status : '') + ' · ' + (out.latency_ms || 0) + ' ms', !out.ok);
      } catch (err) {
        showToast(err.message, true);
      } finally {
        setBusy(test, false);
      }
    });
    actions.appendChild(test);
    card.appendChild(actions);
    return card;
  }

  function renderProxies(data) {
    var box = qs('#proxies-content');
    clear(box);
    var proxies = data.proxies || [];
    if (!proxies.length) { box.appendChild(el('div', 'empty', '没有代理配置')); return; }

    var totalReq = 0, totalFail = 0, totalIn = 0, totalOut = 0, enabledCount = 0;
    proxies.forEach(function (p) {
      totalReq += Number(p.requests) || 0;
      totalFail += Number(p.failures) || 0;
      totalIn += Number(p.bytes_in) || 0;
      totalOut += Number(p.bytes_out) || 0;
      if (p.enabled) { enabledCount++; }
    });
    var stats = el('div', 'stat-grid');
    stats.appendChild(statCard('启用的通道', enabledCount + ' / ' + proxies.length, 'Docker、GitHub、DNS 与 SNI', ''));
    stats.appendChild(statCard('累计请求', String(totalReq), totalFail ? '失败 ' + totalFail + ' · 成功率 ' + pct((totalReq - totalFail) / totalReq * 100) : '暂无失败', ''));
    stats.appendChild(statCard('入流量', fmtBytes(totalIn), '客户端与上游之间接收的总量', ''));
    stats.appendChild(statCard('出流量', fmtBytes(totalOut), '返回给客户端的总量', ''));
    box.appendChild(stats);

    var grid = el('div', 'proxy-grid');
    proxies.forEach(function (p) { grid.appendChild(proxyCard(p)); });
    box.appendChild(grid);

    setTimeout(function () {
      proxies.forEach(function (p) {
        drawSeries('px-' + p.name, p.series || [], [function (pt) { return Number(pt.requests) || 0; }, function (pt) { return Number(pt.failures) || 0; }], ['#52d3c5', '#f87171'], 0);
      });
    }, 30);

    var recent = data.recent || [];
    if (recent.length) {
      var rows = recent.slice(-20).reverse().map(function (r) {
        return [r.proxy, r.method, r.path, r.status, fmtBytes(r.bytes), r.millis + ' ms', r.remote || '', fmtTime(r.at)];
      });
      box.appendChild(tablePanel('最近请求', ['代理', '方法', '路径', '状态', '字节', '耗时', '来源', '时间'], rows));
    }
  }

  async function loadEffect() {
    var box = qs('#effect-content');
    if (!box) { return; }
    pageNote('#effect-note', '加载中…');
    clear(box);
    box.appendChild(el('div', 'skeleton'));
    try {
      var data = await api('/api/effect');
      state.effect = data;
      renderEffect(data);
      pageNote('#effect-note', '最近 ' + data.window_minutes + ' 分钟 · 更新于 ' + fmtTime(data.as_of));
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      pageNote('#effect-note', '加载失败');
      showToast(err.message, true);
    }
  }

  function renderEffect(data) {
    var box = qs('#effect-content');
    clear(box);
    var totals = data.totals || {};
    var overhead = data.overhead || {};
    var dns = data.dns || {};
    var channels = data.channels || [];
    var requests = Number(totals.requests) || 0;
    var success = Number(totals.success_rate) || 0;
    var latency = Number(totals.avg_latency_ms) || 0;
    var upstream = Number(overhead.avg_upstream_ms) || 0;
    var gateway = Number(overhead.gateway_ms) || 0;
    var bytes = (Number(totals.bytes_in) || 0) + (Number(totals.bytes_out) || 0);

    var stats = el('div', 'stat-grid');
    stats.appendChild(statCard('请求总数', String(requests), '最近 ' + (data.window_minutes || 60) + ' 分钟 · 并发 ' + (totals.in_flight || 0), '', undefined));
    stats.appendChild(statCard('成功率', pct(success), (totals.failures || 0) + ' 次失败', success < 95 ? 'warn' : '', success));
    stats.appendChild(statCard('平均耗时', latency ? latency + ' ms' : '—', 'P90 ' + (totals.p90_ms || 0) + ' ms · 最大 ' + (totals.max_latency_ms || 0) + ' ms', '', undefined));
    stats.appendChild(statCard('承载流量', fmtBytes(bytes), '入 ' + fmtRate(totals.in_per_sec) + ' · 出 ' + fmtRate(totals.out_per_sec), '', undefined));
    box.appendChild(stats);

    var stats2 = el('div', 'stat-grid');
    stats2.appendChild(statCard('上游等待', upstream ? upstream + ' ms' : '—', '上游样本 ' + (overhead.upstream_samples || 0), '', undefined));
    stats2.appendChild(statCard('网关转发', gateway ? gateway + ' ms' : '—', '总耗时减去上游等待', '', undefined));
    stats2.appendChild(statCard('DNS 命中率', dns.total ? pct(Number(dns.hit_rate) || 0) : '—', '命中 ' + (dns.hit || 0) + ' · 转发 ' + (dns.miss || 0), '', dns.total ? Number(dns.hit_rate) : undefined));
    stats2.appendChild(statCard('延迟样本', String(totals.latency_samples || 0), '已完成请求的耗时样本', '', undefined));
    box.appendChild(stats2);

    if (data.explain) { add(box, el('p', 'muted', data.explain)); }

    var grid = el('div', 'chart-grid');
    grid.appendChild(chartCard('请求趋势', '每分钟请求与失败', 'chart-effect-req', [{ label: '请求', color: '#52d3c5' }, { label: '失败', color: '#f472b6' }]));
    grid.appendChild(chartCard('流量趋势', '每分钟入站与出站字节', 'chart-effect-bytes', [{ label: '入站', color: '#7cc4ff' }, { label: '出站', color: '#fbbf24' }]));
    box.appendChild(grid);

    var rows = channels.map(function (c) {
      return [
        c.label || c.name, String(c.requests || 0), pct(Number(c.success_rate) || 0),
        (c.avg_latency_ms || 0) + ' ms', (c.p90_ms || 0) + ' ms',
        (c.upstream_count ? (c.upstream_ms || 0) + ' ms' : '—'),
        (c.upstream_count ? (c.gateway_ms || 0) + ' ms' : '—'),
        fmtBytes(c.bytes_in), fmtBytes(c.bytes_out),
        fmtRate(c.in_per_sec) + ' / ' + fmtRate(c.out_per_sec)
      ];
    });
    box.appendChild(tablePanel('通道承载', ['通道', '请求', '成功率', '平均耗时', 'P90', '上游等待', '网关转发', '入站', '出站', '速率（入/出）'], rows));

    var targetRows = [];
    channels.forEach(function (c) {
      (c.targets || []).forEach(function (t) {
        targetRows.push([c.label || c.name, t.target || '', String(t.requests || 0), String(t.failures || 0), fmtBytes(t.bytes), t.last_at ? fmtTime(t.last_at) : '—']);
      });
    });
    targetRows.sort(function (a, b) { return Number(b[2]) - Number(a[2]); });
    if (targetRows.length) {
      box.appendChild(tablePanel('热门目标', ['通道', '目标', '请求', '失败', '字节', '最近'], targetRows.slice(0, 20)));
    }

    var series = data.series || [];
    drawSeries('chart-effect-req', series, [function (p) { return p.requests; }, function (p) { return p.failures; }], ['#52d3c5', '#f472b6']);
    drawSeries('chart-effect-bytes', series, [function (p) { return p.bytes_in; }, function (p) { return p.bytes_out; }], ['#7cc4ff', '#fbbf24']);
  }

  function tablePanel(title, headers, rows) {
    var wrap = el('div', 'panel');
    add(wrap, el('h3', '', title));
    var box = el('div', 'table-wrap');
    var table = el('table', 'data-table');
    var thead = el('thead'), trh = el('tr');
    headers.forEach(function (h) { add(trh, el('th', '', h)); });
    thead.appendChild(trh); table.appendChild(thead);
    var tbody = el('tbody');
    rows.forEach(function (row) {
      var tr = el('tr');
      row.forEach(function (cell, i) { add(tr, el('td', i > 2 ? 'num' : '', cell === undefined || cell === null ? '' : cell)); });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody); box.appendChild(table); wrap.appendChild(box);
    return wrap;
  }

  function statusKind(status) {
    if (status === 'ok') { return ['正常', 'ok']; }
    if (status === 'warn') { return ['注意', 'warn']; }
    if (status === 'fail') { return ['异常', 'bad']; }
    return ['跳过', ''];
  }

  async function loadDiagnostics() {
    var box = qs('#diagnostics-content');
    pageNote('#diagnostics-note', '检查中…');
    clear(box);
    box.appendChild(el('div', 'skeleton'));
    try {
      var results = await Promise.all([api('/api/diagnostics'), api('/api/cn')]);
      var report = results[0];
      state.diagnostics = report;
      state.cn = results[1];
      renderDiagnostics(report, results[1]);
      pageNote('#diagnostics-note', '检查于 ' + fmtTime(report.as_of));
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      pageNote('#diagnostics-note', '检查失败');
      showToast(err.message, true);
    }
  }

  function renderDiagnostics(report, cn) {
    var box = qs('#diagnostics-content');
    clear(box);
    var warns = 0;
    ((report.project && report.project.items) || []).forEach(function (item) { if (item.status === 'warn') { warns++; } });
    var level = report.ok ? (warns > 0 ? 'warn' : 'good') : 'bad';
    var banner = el('div', 'alert ' + level);
    add(banner, el('span', '', report.ok ? (warns > 0 ? '检查完成，有 ' + warns + ' 项需要注意' : '全部检查通过') : '存在异常项，请按提示处理'));
    add(banner, el('span', '', '仅检查网关自身的配置、监听与证书'));
    box.appendChild(banner);
    box.appendChild(diagSection(report.project));
    if (cn) { box.appendChild(cnCommandPanel(cn)); }
  }

  function diagSection(section) {
    var p = el('div', 'panel diag-panel');
    if (!section) { add(p, el('div', 'empty', '没有检查结果')); return p; }
    var head = el('div', 'diag-head');
    add(head, el('h3', '', section.title || '检查'));
    var warns = 0;
    (section.items || []).forEach(function (item) { if (item.status === 'warn') { warns++; } });
    if (!section.ok) {
      head.appendChild(statusPill('待处理', 'bad'));
    } else if (warns > 0) {
      head.appendChild(statusPill('注意（' + warns + '）', 'warn'));
    } else {
      head.appendChild(statusPill('通过', 'ok'));
    }
    p.appendChild(head);
    var list = el('div', 'diag-list');
    (section.items || []).forEach(function (item) {
      var row = el('div', 'diag-item');
      var main = el('div');
      add(main, el('div', 'name', item.label || item.id || ''));
      if (item.detail) { add(main, el('div', 'desc', item.detail)); }
      add(row, main);
      var kind = statusKind(item.status);
      row.appendChild(statusPill(kind[0], kind[1]));
      list.appendChild(row);
    });
    if (!(section.items || []).length) { add(list, el('div', 'empty', '没有检查项')); }
    p.appendChild(list);
    if (section.hint) { add(p, el('p', 'muted diag-hint', section.hint)); }
    return p;
  }

  function cnCommandPanel(cn) {
    var p = el('div', 'panel');
    add(p, el('h3', '', '给国内机使用的命令'));
    add(p, el('p', 'muted', '这些命令由其他服务器（国内机）执行，网关本身不执行国内机设置。命令会写入 hosts 或 DNS、配置 Docker 与 Git 镜像，修改前自动备份。'));
    var commands = cn.commands || {};
    p.appendChild(commandRow('hosts 分流', commands.hosts || cn.command || ''));
    p.appendChild(commandRow('DNS 分流', commands.dns || ''));
    p.appendChild(commandRow('hosts + DNS', commands.both || ''));
    p.appendChild(commandRow('恢复命令', cn.restore_command || ''));
    var dns = cn.dns || {};
    add(p, infoList([
      ['网关地址', cn.gateway_ip || '—'],
      ['面板域名', cn.panel_host || '—'],
      ['网关 DNS 服务', dns.running ? ('运行中 · ' + (dns.active_listen || dns.listen || '')) : (dns.enabled ? '已启用但未监听' : '未启用')],
      ['DNS 分流可用', cn.dns_ready ? '是' : '否（需要 53 端口）'],
      ['分流域名', ((cn.domains || []).join(', ') || '—')]
    ]));
    return p;
  }

  function commandRow(label, command) {
    var wrap = el('div', 'command-row');
    add(wrap, el('div', 'command-label', label));
    var box = el('div', 'command-box');
    box.appendChild(el('code', '', command || '—'));
    var btn = el('button', 'btn small', '复制');
    btn.type = 'button';
    btn.addEventListener('click', function () { copyText(command, btn); });
    box.appendChild(btn);
    wrap.appendChild(box);
    return wrap;
  }

  function copyText(text, btn) {
    if (!text) { showToast('没有可复制的内容', true); return; }
    function done() {
      showToast('已复制');
      if (btn) {
        var old = btn.textContent;
        btn.textContent = '已复制';
        setTimeout(function () { btn.textContent = old; }, 1500);
      }
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text, done); });
      return;
    }
    fallbackCopy(text, done);
  }

  function fallbackCopy(text, done) {
    var ta = el('textarea', 'copy-sink');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    document.body.removeChild(ta);
    if (ok) { done(); } else { showToast('复制失败，请手动选择文本', true); }
  }

  async function loadBackups() {
    var box = qs('#backups-content');
    try {
      var data = await api('/api/backups');
      state.backups = data.backups || [];
      renderBackups();
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      showToast(err.message, true);
    }
  }

  function renderBackups() {
    var box = qs('#backups-content');
    clear(box);
    if (!state.backups.length) {
      box.appendChild(el('div', 'empty', '还没有备份。点击“创建备份”生成第一个备份。'));
      return;
    }
    var list = el('div', 'backup-list');
    state.backups.forEach(function (b) {
      var row = el('div', 'backup-row');
      var main = el('div');
      add(main, el('div', 'name', b.id));
      add(main, el('div', 'meta', fmtTime(b.created) + ' · ' + fmtBytes(b.size) + ' · ' + (b.files || []).length + ' 个文件'));
      add(row, main);
      var actions = el('div', 'service-actions');
      var dl = el('a', 'btn small', '下载');
      dl.href = BASE + '/api/backups/' + encodeURIComponent(b.id) + '/download';
      dl.setAttribute('download', b.id + '.tar.gz');
      actions.appendChild(dl);
      var restore = el('button', 'btn small', '恢复');
      restore.type = 'button';
      restore.addEventListener('click', function () {
        confirmDialog('恢复备份', '恢复 ' + b.id + ' 后将覆盖当前配置、ACME 证书和 systemd 单元，并自动重启服务。确认继续？').then(function (yes) {
          if (yes) { restoreBackup(b.id, restore); }
        });
      });
      actions.appendChild(restore);
      var del = el('button', 'btn small danger', '删除');
      del.type = 'button';
      del.addEventListener('click', function () {
        confirmDialog('删除备份', '确认删除 ' + b.id + '？此操作不可撤销。').then(function (yes) {
          if (yes) { deleteBackup(b.id, del); }
        });
      });
      actions.appendChild(del);
      add(row, actions);
      list.appendChild(row);
    });
    box.appendChild(list);
  }

  async function createBackup(button) {
    setBusy(button, true);
    try {
      var man = await api('/api/backups', { method: 'POST', body: {} });
      showToast('已创建备份 ' + man.id);
      await loadBackups();
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(button, false);
    }
  }

  async function deleteBackup(id, button) {
    setBusy(button, true);
    try {
      await api('/api/backups/' + encodeURIComponent(id), { method: 'DELETE' });
      showToast('已删除备份 ' + id);
      await loadBackups();
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(button, false);
    }
  }

  async function restoreBackup(id, button) {
    setBusy(button, true);
    try {
      await api('/api/backups/' + encodeURIComponent(id) + '/restore', { method: 'POST', body: {} });
      showToast('恢复完成，服务正在重启…');
      setTimeout(function () { window.location.reload(); }, 3500);
    } catch (err) {
      showToast(err.message, true);
      setBusy(button, false);
    }
  }

  async function loadAudit(q) {
    var box = qs('#audit-content');
    try {
      var data = await api('/api/audit?limit=200' + (q ? '&q=' + encodeURIComponent(q) : ''));
      state.audit = data.items || [];
      clear(box);
      if (!state.audit.length) { box.appendChild(el('div', 'empty', '没有匹配的记录')); return; }
      var rows = state.audit.map(function (it) { return [fmtTime(it.ts), it.action, it.detail, it.result, it.ip]; });
      box.appendChild(tablePanel('操作记录', ['时间', '操作', '详情', '结果', '来源 IP'], rows));
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      showToast(err.message, true);
    }
  }

  async function loadSettings() {
    var box = qs('#settings-content');
    try {
      var results = await Promise.all([api('/api/settings'), api('/api/cn')]);
      var data = results[0];
      state.settings = data;
      state.cn = results[1];
      renderSettings(data);
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      showToast(err.message, true);
    }
  }

  var cnModes = ['hosts', 'dns', 'both'];

  function cnModeHint(mode) {
    if (mode === 'dns') {
      return '目标机把 /etc/resolv.conf 或 systemd-resolved 指向网关，需要目标机能访问网关 53 端口（UDP/TCP）。';
    }
    if (mode === 'both') {
      return '同时写入 hosts 托管块并把 DNS 指向网关；hosts 已覆盖的域名不会再走 DNS，两者互为兜底。';
    }
    return '目标机写入 hosts 托管块，不修改 resolver 配置；适合不能访问网关 53 端口的机器。';
  }

  function selectField(label, options, value) {
    var wrap = el('label');
    add(wrap, el('span', '', label));
    var select = el('select');
    options.forEach(function (opt) {
      var item = el('option', '', opt.label);
      item.value = opt.value;
      if (opt.value === value) { item.selected = true; }
      select.appendChild(item);
    });
    wrap.appendChild(select);
    return { wrap: wrap, select: select };
  }

  function cnSettingsBody() {
    var wrap = el('div', 'stack');
    var cn = state.cn || {};
    var mode = cnModes.indexOf(cn.cn_mode) >= 0 ? cn.cn_mode : 'hosts';
    var form = el('form', 'form-grid');
    var picker = selectField('分发方式', [
      { value: 'hosts', label: 'hosts 分流（修改 hosts）' },
      { value: 'dns', label: 'DNS 分流（resolver 指向网关）' },
      { value: 'both', label: '两者同时（hosts + DNS）' }
    ], mode);
    form.appendChild(picker.wrap);
    var hint = el('p', 'muted', cnModeHint(mode));
    form.appendChild(hint);
    wrap.appendChild(form);
    var commandBox = el('div', 'stack');
    function renderCommands() {
      clear(commandBox);
      var commands = cn.commands || {};
      commandBox.appendChild(commandRow('在国内机上执行', commands[mode] || cn.command || ''));
      commandBox.appendChild(commandRow('恢复命令', cn.restore_command || ''));
    }
    renderCommands();
    picker.select.addEventListener('change', function () {
      mode = picker.select.value;
      hint.textContent = cnModeHint(mode);
      renderCommands();
    });
    wrap.appendChild(commandBox);
    var dns = cn.dns || {};
    wrap.appendChild(infoList([
      ['网关地址', cn.gateway_ip || '—'],
      ['面板域名', cn.panel_host || '—'],
      ['网关 DNS 服务', dns.running ? ('运行中 · ' + (dns.active_listen || dns.listen || '')) : (dns.enabled ? '已启用但未监听' : '未启用')],
      ['DNS 分流可用', cn.dns_ready ? '是' : '否（需要 53 端口）'],
      ['分流域名', ((cn.domains || []).join(', ') || '—')]
    ]));
    return wrap;
  }

  function dnsPanel() {
    var cn = state.cn || {};
    var dns = cn.dns || {};
    var running = !!dns.running;
    var enabled = !!dns.enabled;
    var activePort = dns.active_port || '';
    var panel = el('div', 'panel');
    add(panel, el('h3', '', '网关 DNS 分流服务'));
    add(panel, el('p', 'muted', '启用后优先监听 53 端口应答分流域名并转发其余查询；53 被占用时自动回退到其他端口，不会影响面板与其他代理。修改后网关自动重启。'));
    var items = [
      ['状态', running ? '运行中' : (enabled ? '已启用但未监听' : '未启用')],
      ['配置监听', dns.listen || '0.0.0.0:53'],
      ['实际监听', dns.active_listen || '—'],
      ['返回地址', dns.gateway_ip || cn.gateway_ip || '—'],
      ['上游', dns.upstream || '1.1.1.1:53'],
      ['分流域名', String((dns.domains || cn.domains || []).length) + ' 个']
    ];
    if (dns.error) { items.push(['错误', dns.error]); }
    panel.appendChild(infoList(items));
    if (running && activePort && activePort !== '53') {
      var warnBox = el('div', 'alert warn');
      add(warnBox, el('span', '', '53 端口被占用，已回退到 ' + activePort + '。'));
      add(warnBox, el('span', '', '国内机 resolver 无法指定端口，DNS 分流此时不可用；建议关闭 systemd-resolved 的 53 监听后重新启用。'));
      panel.appendChild(warnBox);
    }
    var actions = el('div', 'form-actions');
    var toggle = el('button', 'btn', enabled ? '停用 DNS 服务' : '启用 DNS 服务');
    toggle.type = 'button';
    toggle.addEventListener('click', async function () {
      var next = !enabled;
      var yes = await confirmDialog(next ? '启用网关 DNS 服务' : '停用网关 DNS 服务', next
        ? '将自动选择可用的监听地址（优先 53 端口），应答分流域名并把其余查询转发上游，网关服务会自动重启。确认继续？'
        : '停用后使用 DNS 分流的机器将无法解析分流域名，网关服务会自动重启。确认继续？');
      if (!yes) { return; }
      setBusy(toggle, true);
      try {
        var out = await api('/api/dns/config', { method: 'POST', body: { enabled: next } });
        showToast(out.note || (next ? 'DNS 服务已启用，网关正在重启' : 'DNS 服务已停用，网关正在重启'), !!out.note);
        setTimeout(function () { window.location.reload(); }, 3500);
      } catch (err) {
        showToast(err.message, true);
      } finally {
        setBusy(toggle, false);
      }
    });
    actions.appendChild(toggle);
    panel.appendChild(actions);
    return panel;
  }

  function renderSettings(data) {
    var box = qs('#settings-content');
    clear(box);
    var grid = el('div', 'settings-grid');

    var keyPanel = el('div', 'panel');
    add(keyPanel, el('h3', '', '访问密钥'));
    add(keyPanel, el('p', 'muted', '修改后旧登录会话立即失效。新密钥至少 10 个字符，不能包含空白。'));
    var form = el('form', 'form-grid');
    var current = field('当前访问密钥', 'password');
    var next = field('新访问密钥', 'password');
    var confirm = field('确认新访问密钥', 'password');
    form.appendChild(current.wrap); form.appendChild(next.wrap); form.appendChild(confirm.wrap);
    var actions = el('div', 'form-actions');
    var submit = el('button', 'btn primary', '保存新密钥');
    submit.type = 'submit';
    actions.appendChild(submit);
    form.appendChild(actions);
    var errBox = el('p', 'form-error');
    errBox.hidden = true;
    form.appendChild(errBox);
    form.addEventListener('submit', async function (ev) {
      ev.preventDefault();
      errBox.hidden = true;
      if (next.input.value !== confirm.input.value) {
        errBox.textContent = '两次输入的新密钥不一致';
        errBox.hidden = false;
        return;
      }
      setBusy(submit, true);
      try {
        var s = await api('/api/key/change', { method: 'POST', body: { current_key: current.input.value, new_key: next.input.value } });
        applySession(s);
        showToast('访问密钥已更新，其他会话已失效');
        current.input.value = ''; next.input.value = ''; confirm.input.value = '';
      } catch (err) {
        errBox.textContent = err.message;
        errBox.hidden = false;
      } finally {
        setBusy(submit, false);
      }
    });
    keyPanel.appendChild(form);
    grid.appendChild(keyPanel);

    var certPanel = el('div', 'panel');
    add(certPanel, el('h3', '', '证书'));
    var cert = state.overview && state.overview.certificate ? state.overview.certificate : null;
    if (cert && cert.ok !== false) {
      add(certPanel, infoList([
        ['域名', (cert.dns_names || []).join(', ') || data.domain || '—'],
        ['到期', fmtTime(cert.not_after)],
        ['剩余', String(cert.days_remaining || 0) + ' 天'],
        ['签发', cert.issuer || '—']
      ]));
    } else {
      add(certPanel, el('div', 'empty', (cert && cert.error) || '尚未获取证书信息'));
    }
    var renew = el('button', 'btn small', '检查续期');
    renew.type = 'button';
    renew.addEventListener('click', async function () {
      setBusy(renew, true);
      try {
        var out = await api('/api/cert/renew', { method: 'POST', body: {} });
        showToast(out.message || '已检查');
      } catch (err) { showToast(err.message, true); }
      finally { setBusy(renew, false); }
    });
    certPanel.appendChild(renew);
    grid.appendChild(certPanel);

    var cnPanel = el('div', 'panel span-2');
    add(cnPanel, el('h3', '', '国内机设置命令'));
    add(cnPanel, el('p', 'muted', '网关本身不执行国内机设置。选择分发方式后，把生成的命令复制到需要走网关的服务器上执行；命令会配置 hosts 或 DNS、Docker 与 Git 镜像，修改前自动备份。'));
    cnPanel.appendChild(cnSettingsBody());
    grid.appendChild(cnPanel);

    grid.appendChild(dnsPanel());

    var configPanel = el('div', 'panel span-2');
    add(configPanel, el('h3', '', '运行配置'));
    var cv = el('div', 'config-view');
    var pre = el('pre');
    pre.textContent = JSON.stringify(data, null, 2);
    cv.appendChild(pre);
    configPanel.appendChild(cv);
    grid.appendChild(configPanel);

    box.appendChild(grid);
  }

  function field(label, type) {
    var wrap = el('label');
    add(wrap, el('span', '', label));
    var input = el('input');
    input.type = type || 'text';
    input.autocomplete = 'new-password';
    wrap.appendChild(input);
    return { wrap: wrap, input: input };
  }

  function confirmDialog(title, message) {
    return new Promise(function (resolve) {
      var modal = qs('#modal');
      var titleEl = qs('#modal-title');
      var body = qs('#modal-body');
      clear(body);
      titleEl.textContent = title;
      add(body, el('p', '', message));
      var actions = el('div', 'form-actions');
      var cancel = el('button', 'btn', '取消');
      cancel.type = 'button';
      var ok = el('button', 'btn primary', '确认');
      ok.type = 'button';
      function close(value) { modal.hidden = true; resolve(value); }
      cancel.addEventListener('click', function () { close(false); });
      ok.addEventListener('click', function () { close(true); });
      add(actions, cancel); add(actions, ok); body.appendChild(actions);
      modal.hidden = false;
      ok.focus();
    });
  }


  var sbNames = {
    'hysteria2': 'Hysteria2',
    'vless-reality': 'VLESS Reality',
    'trojan': 'Trojan',
    'shadowsocks': 'Shadowsocks',
    'socks': 'SOCKS5'
  };
  function sbName(id) { return sbNames[id] || id || '节点'; }
  function sbNodeLabel(node) { return node.name || sbNodeLabel(node); }

  function openModal(title, build) {
    var body = qs('#modal-body');
    qs('#modal-title').textContent = title;
    clear(body);
    build(body);
    qs('#modal').hidden = false;
  }
  function closeModal() { qs('#modal').hidden = true; }

  async function loadSingbox() {
    var box = qs('#singbox-content');
    if (!state.singbox) { clear(box); box.appendChild(el('div', 'skeleton')); }
    pageNote('#singbox-note', '加载中…');
    try {
      var data = await api('/api/singbox');
      state.singbox = data;
      renderSingbox(data);
      pageNote('#singbox-note', (data.installed && data.version ? 'sing-box ' + data.version + ' · ' : '') + '更新于 ' + fmtTime(data.as_of));
    } catch (err) {
      clear(box);
      box.appendChild(el('div', 'empty', err.message));
      pageNote('#singbox-note', '加载失败');
    }
  }

  function renderSingbox(data) {
    var box = qs('#singbox-content');
    clear(box);
    var tips = [];
    var level = 'good';
    var stateText = '运行中';
    if (!data.installed) {
      level = 'bad';
      stateText = '未安装';
      tips.push('未检测到 sing-box。在下方“内核”卡片点击“安装内核”，本项目会下载并安装，无需手动操作服务器。');
    } else if (!data.running) {
      level = 'warn';
      stateText = '已停止';
      tips.push('sing-box 已安装但当前没有运行，点击下方“启动服务”。');
    } else {
      tips.push('sing-box 运行中，节点改动保存后立即生效。');
    }
    if (data.check && data.check.ok === false) {
      level = 'bad';
      tips.push('配置校验未通过：' + (data.check.output || 'sing-box check 失败'));
    }
    (data.warnings || []).forEach(function (w) { tips.push(w); });
    var banner = el('div', 'alert ' + level);
    add(banner, el('span', '', tips.join(' ')));
    add(banner, el('span', '', [data.service, data.installed ? (stateText + ' · ' + data.version) : ''].filter(Boolean).join(' · ')));
    box.appendChild(banner);
    box.appendChild(sbCorePanel(data));

    var nodes = data.nodes || [];
    var enabled = nodes.filter(function (n) { return n.enabled; }).length;
    var conn = data.connections || {};
    var stats = el('div', 'stat-grid');
    stats.appendChild(statCard('节点', String(nodes.length), enabled + ' 个启用'));
    stats.appendChild(statCard('实时连接', String(conn.connections || 0), conn.available ? '统计已开启' : '未开启，可在下方开启'));
    stats.appendChild(statCard('上传', fmtBytes(conn.upload_total), 'sing-box 累计'));
    stats.appendChild(statCard('下载', fmtBytes(conn.download_total), 'sing-box 累计'));
    box.appendChild(stats);

    var bar = el('div', 'sb-toolbar');
    var create = el('button', 'btn primary', '新建节点');
    create.type = 'button';
    create.disabled = !data.installed;
    create.addEventListener('click', function () { sbCreateDialog(data); });
    bar.appendChild(create);

    var restart = el('button', 'btn', data.running ? '重启服务' : '启动服务');
    restart.type = 'button';
    restart.disabled = !data.installed;
    restart.addEventListener('click', async function () {
      var yes = await confirmDialog('重启 sing-box', '重启约需一秒，已连接的客户端会短暂断开后自动重连。确认继续？');
      if (!yes) { return; }
      setBusy(restart, true);
      try {
        await api('/api/singbox/action', { method: 'POST', body: { id: '', action: 'restart' } });
        showToast('sing-box 已重启');
        setTimeout(loadSingbox, 1200);
      } catch (err) { showToast(err.message, true); }
      finally { setBusy(restart, false); }
    });
    bar.appendChild(restart);

    var verify = el('button', 'btn', '校验配置');
    verify.type = 'button';
    verify.disabled = !data.installed;
    verify.addEventListener('click', async function () {
      setBusy(verify, true);
      try {
        var out = await api('/api/singbox/action', { method: 'POST', body: { id: '', action: 'check' } });
        var check = out.check || {};
        showToast(check.ok ? '配置校验通过（' + (check.duration || 0) + ' ms）' : ('配置校验失败：' + (check.output || '')), !check.ok);
      } catch (err) { showToast(err.message, true); }
      finally { setBusy(verify, false); }
    });
    bar.appendChild(verify);

    var logs = el('button', 'btn', '查看日志');
    logs.type = 'button';
    logs.disabled = !data.installed;
    logs.addEventListener('click', function () { sbLogDialog(); });
    bar.appendChild(logs);
    box.appendChild(bar);

    if (!nodes.length) {
      box.appendChild(el('div', 'empty', data.installed ? '还没有节点。点击“新建节点”生成第一个可用节点。' : '安装 sing-box 后才能创建节点。'));
    } else {
      var list = el('div', 'sb-nodes');
      nodes.forEach(function (node) { list.appendChild(sbNodeCard(node, data)); });
      box.appendChild(list);
    }

    box.appendChild(sbTrafficPanel(conn, data));

    var backups = data.backups || [];
    if (backups.length) {
      box.appendChild(tablePanel('配置备份', ['文件', '大小', '时间'], backups.map(function (b) {
        return [b.name, fmtBytes(b.size), fmtTime(b.modified)];
      })));
    }
  }

  async function sbCoreAction(btn, action, message) {
    var yes = await confirmDialog("内核操作", message);
    if (!yes) { return; }
    setBusy(btn, true);
    showToast("操作进行中，下载或写入可能需要一段时间…");
    try {
      await api("/api/singbox/core", { method: "POST", body: { action: action } });
      showToast("内核操作完成");
      setTimeout(loadSingbox, 900);
    } catch (err) {
      showToast(err.message, true);
      setBusy(btn, false);
    }
  }

  function sbCorePanel(data) {
    var core = data.core || {};
    var wrap = el("div", "panel sb-core");
    add(wrap, el("h3", "", "内核"));
    add(wrap, el("p", "muted", ["sing-box 内核由本项目负责安装、更新与回滚", core.platform ? ("平台 " + core.platform) : ""].filter(Boolean).join(" · ")));

    var stats = el("div", "stat-grid");
    stats.appendChild(statCard("当前版本", core.installed ? core.version : "未安装", core.unit_installed ? "系统服务由本项目管理" : "使用主机已有服务"));
    var latestCard = statCard("最新版本", "检查中…", "正在向 GitHub 查询");
    stats.appendChild(latestCard);
    wrap.appendChild(stats);

    var hint = el("div", "sb-core-hint muted", core.note || "");
    wrap.appendChild(hint);

    var bar = el("div", "sb-toolbar");
    var install = el("button", "btn primary", core.installed ? "更新到最新" : "安装内核");
    install.type = "button";
    install.addEventListener("click", function () {
      sbCoreAction(install, "install", core.installed
        ? "将下载最新内核并替换当前版本，期间 sing-box 会短暂重启。确认继续？"
        : "将从 GitHub 下载 sing-box 内核并安装为系统服务，确认继续？");
    });
    bar.appendChild(install);

    if (core.installed) {
      var back = el("button", "btn", "回滚上一版");
      back.type = "button";
      back.disabled = !(core.backups && core.backups.length);
      if (back.disabled) { back.title = "没有可回滚的内核备份"; }
      back.addEventListener("click", function () {
        sbCoreAction(back, "rollback-previous", "将把内核恢复为上一个备份版本并重启 sing-box。确认继续？");
      });
      bar.appendChild(back);

      var pre = el("button", "btn", "回滚到安装前");
      pre.type = "button";
      pre.addEventListener("click", function () {
        sbCoreAction(pre, "rollback-preinstall", "将恢复到本项目第一次安装内核之前的状态（内核、系统服务与启停状态）。确认继续？");
      });
      bar.appendChild(pre);

      var restart = el("button", "btn", "重启内核");
      restart.type = "button";
      restart.addEventListener("click", function () {
        sbCoreAction(restart, "restart", "重启 sing-box 服务，已连接客户端会短暂断开。确认继续？");
      });
      bar.appendChild(restart);

      var uninstall = el("button", "btn danger", "卸载内核");
      uninstall.type = "button";
      uninstall.addEventListener("click", function () {
        sbCoreAction(uninstall, "uninstall", "将停止服务、移除本项目安装的内核与系统服务（节点配置保留，归档到数据目录）。确认继续？");
      });
      bar.appendChild(uninstall);
    }
    wrap.appendChild(bar);

    api("/api/singbox/core").then(function (status) {
      var latest = status.latest || "";
      latestCard.querySelector(".value").textContent = latest ? ("v" + String(latest).replace(/^v/, "")) : "未知";
      var sub = latestCard.querySelector(".sub");
      if (sub) {
        if (status.latest_error) { sub.textContent = "检查失败：" + status.latest_error; }
        else if (status.update_available) { sub.textContent = "有新版本，可点击“更新到最新”"; }
        else if (status.installed) { sub.textContent = "已是最新"; }
        else { sub.textContent = "点击“安装内核”完成安装"; }
      }
      if (latest && !status.latest_error) {
        install.textContent = status.update_available ? "更新到最新" : "重新安装最新版";
      }
    }).catch(function () {
      latestCard.querySelector(".value").textContent = "未知";
      var sub = latestCard.querySelector(".sub");
      if (sub) { sub.textContent = "无法获取 GitHub 版本信息"; }
    });

    return wrap;
  }
  function sbNodeCard(node, data) {
    var card = el('div', 'sb-node');
    var head = el('div', 'sb-node-head');
    var left = el('div');
    add(left, el('h3', '', sbNodeLabel(node)));
    var listen = node.listen && node.listen !== '::' && node.listen !== '0.0.0.0' ? node.listen : '';
    var meta = [sbName(node.type), '端口 ' + node.port, node.transport || '', listen].filter(Boolean).join(' · ');
    add(left, el('div', 'sb-node-meta', meta));
    add(head, left);
    head.appendChild(statusPill(node.enabled ? '启用' : '已停用', node.enabled ? 'ok' : 'warn'));
    card.appendChild(head);

    if (node.error) {
      add(card, el('div', 'alert bad', node.error));
    } else {
      var links = node.links || [];
      if (!links.length) {
        add(card, el('div', 'muted', '这个节点还没有可用的分享链接。'));
      } else {
        var linkBox = el('div', 'sb-links');
        links.forEach(function (link, i) {
          var row = el('div', 'sb-link');
          add(row, el('div', 'sb-link-label', link.label || ('用户 ' + (i + 1))));
          row.appendChild(el('code', '', link.uri || link.error || '—'));
          var copy = el('button', 'btn small', '复制');
          copy.type = 'button';
          copy.addEventListener('click', function () { copyText(link.uri, copy); });
          row.appendChild(copy);
          var qr = el('button', 'btn small', '二维码');
          qr.type = 'button';
          qr.addEventListener('click', function () { sbShowQR(node, link); });
          row.appendChild(qr);
          linkBox.appendChild(row);
        });
        card.appendChild(linkBox);
      }
    }

    var actions = el('div', 'sb-actions');
    var edit = el('button', 'btn small', '编辑配置');
    edit.type = 'button';
    edit.addEventListener('click', function () { sbConfigDialog(node); });
    actions.appendChild(edit);

    var reset = el('button', 'btn small', '重置密钥');
    reset.type = 'button';
    reset.disabled = !node.enabled;
    reset.addEventListener('click', async function () {
      var yes = await confirmDialog('重置密钥', '重新生成 ' + (sbNodeLabel(node)) + ' 的密钥，旧链接立即失效，需要把新链接重新发给客户端。确认重置？');
      if (!yes) { return; }
      setBusy(reset, true);
      try {
        var out = await api('/api/singbox/action', { method: 'POST', body: { id: node.id, action: 'reset' } });
        showToast('已重置，请复制新链接');
        await loadSingbox();
        if (out.node) { sbShowLinks(out.node); }
      } catch (err) { showToast(err.message, true); }
      finally { setBusy(reset, false); }
    });
    actions.appendChild(reset);

    var toggle = el('button', 'btn small', node.enabled ? '停用' : '启用');
    toggle.type = 'button';
    toggle.addEventListener('click', async function () {
      var next = !node.enabled;
      setBusy(toggle, true);
      try {
        await api('/api/singbox/action', { method: 'POST', body: { id: node.id, action: next ? 'enable' : 'disable' } });
        showToast(next ? '节点已启用' : '节点已停用');
        await loadSingbox();
      } catch (err) { showToast(err.message, true); }
      finally { setBusy(toggle, false); }
    });
    actions.appendChild(toggle);

    var del = el('button', 'btn small danger', '删除');
    del.type = 'button';
    del.addEventListener('click', async function () {
      var yes = await confirmDialog('删除节点', '删除 ' + (sbNodeLabel(node)) + '（端口 ' + node.port + '）。删除前会自动备份配置文件，确认删除？');
      if (!yes) { return; }
      setBusy(del, true);
      try {
        await api('/api/singbox/action', { method: 'POST', body: { id: node.id, action: 'delete' } });
        showToast('节点已删除');
        await loadSingbox();
      } catch (err) { showToast(err.message, true); }
      finally { setBusy(del, false); }
    });
    actions.appendChild(del);
    card.appendChild(actions);
    return card;
  }

  function sbTrafficPanel(conn, data) {
    var panel = el('div', 'panel');
    var head = el('div', 'sb-node-head');
    add(head, el('h3', '', '实时连接'));
    panel.appendChild(head);
    var controller = (data && data.stats && data.stats.controller) || '127.0.0.1:19090';
    if (!conn || !conn.available) {
      add(panel, el('p', 'muted', '开启后可以查看当前连接、来源、目标与上下行流量。面板会把 Clash API 写入 sing-box 主配置（只监听 ' + controller + '），然后重启 sing-box 使其生效。'));
      var on = el('button', 'btn primary', '开启实时统计');
      on.type = 'button';
      on.disabled = !(data && data.installed);
      on.addEventListener('click', async function () {
        var yes = await confirmDialog('开启实时统计', '将写入 experimental.clash_api 并重启 sing-box，已连接的客户端会短暂断开后自动重连。确认继续？');
        if (!yes) { return; }
        setBusy(on, true);
        try {
          await api('/api/singbox/action', { method: 'POST', body: { id: '', action: 'stats-enable' } });
          showToast('实时统计已开启');
          setTimeout(loadSingbox, 1600);
        } catch (err) { showToast(err.message, true); setBusy(on, false); }
      });
      panel.appendChild(on);
      if (conn && conn.error) { add(panel, el('p', 'muted', conn.error)); }
      return panel;
    }
    var off = el('button', 'btn small', '停用统计');
    off.type = 'button';
    off.addEventListener('click', async function () {
      var yes = await confirmDialog('停用实时统计', '将从 sing-box 主配置移除 Clash API 并重启 sing-box，实时连接与速率不再显示。确认停用？');
      if (!yes) { return; }
      setBusy(off, true);
      try {
        await api('/api/singbox/action', { method: 'POST', body: { id: '', action: 'stats-disable' } });
        showToast('实时统计已停用');
        setTimeout(loadSingbox, 1600);
      } catch (err) { showToast(err.message, true); setBusy(off, false); }
    });
    head.appendChild(off);
    var items = conn.items || [];
    if (!items.length) {
      add(panel, el('div', 'empty', '当前没有活动连接。'));
      return panel;
    }
    var rows = items.slice(0, 50).map(function (c) {
      return [
        c.network || '—',
        c.target || '—',
        c.inbound || '—',
        c.outbound || '—',
        fmtBytes(c.upload),
        fmtBytes(c.download)
      ];
    });
    panel.appendChild(tablePanel('当前连接（最多 50 条）', ['网络', '目标', '入站', '出站', '上传', '下载'], rows));
    return panel;
  }

  function sbShowQR(node, link) {
    openModal('扫描二维码', function (body) {
      add(body, el('p', 'muted', (sbNodeLabel(node)) + ' · ' + (sbName(node.type)) + ' · 端口 ' + node.port));
      if (!link || !link.uri) {
        add(body, el('div', 'empty', (link && link.error) || '没有可用的分享链接'));
        return;
      }
      var wrap = el('div', 'sb-qr');
      var img = el('img', 'sb-qr-img');
      img.alt = '节点二维码';
      img.src = BASE + '/api/singbox/qr?id=' + encodeURIComponent(node.id) + '&user=' + encodeURIComponent(link.user || 0);
      wrap.appendChild(img);
      body.appendChild(wrap);
      var box = el('div', 'command-box');
      box.appendChild(el('code', '', link.uri));
      var copy = el('button', 'btn small', '复制');
      copy.type = 'button';
      copy.addEventListener('click', function () { copyText(link.uri, copy); });
      box.appendChild(copy);
      body.appendChild(box);
      add(body, el('p', 'muted', '用客户端扫描二维码，或复制上面的链接导入。'));
    });
  }

  function sbShowLinks(node) {
    openModal('节点已就绪', function (body) {
      add(body, el('p', 'muted', '把下面的链接发给客户端，或直接扫码导入。链接里包含密码，请通过可信渠道传递。'));
      var links = node.links || [];
      if (!links.length) {
        add(body, el('div', 'empty', '没有生成分享链接，请检查配置。'));
        return;
      }
      var list = el('div', 'sb-links');
      links.forEach(function (link) {
        var row = el('div', 'sb-link');
        add(row, el('div', 'sb-link-label', link.label || '链接'));
        row.appendChild(el('code', '', link.uri || link.error || '—'));
        var copy = el('button', 'btn small', '复制');
        copy.type = 'button';
        copy.addEventListener('click', function () { copyText(link.uri, copy); });
        row.appendChild(copy);
        var qr = el('button', 'btn small', '二维码');
        qr.type = 'button';
        qr.addEventListener('click', function () { sbShowQR(node, link); });
        row.appendChild(qr);
        list.appendChild(row);
      });
      body.appendChild(list);
    });
  }

  function sbCreateDialog(data) {
    var types = data.types || [];
    var choice = types.length ? types[0].id : 'hysteria2';
    openModal('新建节点', function (body) {
      add(body, el('p', 'muted', '第一步选协议，第二步填名称和端口。密钥、证书与 Reality 密钥对自动生成，创建后立即生效。'));
      var grid = el('div', 'sb-types');
      var portField = null;
      var methodField = null;
      types.forEach(function (t) {
        var b = el('button', 'sb-type');
        b.type = 'button';
        add(b, el('div', 'sb-type-name', t.label || sbName(t.id)));
        add(b, el('div', 'sb-type-hint', t.hint || ''));
        add(b, el('div', 'sb-type-meta', (t.transport ? t.transport + ' · ' : '') + '默认端口 ' + t.default_port));
        if (t.id === choice) { b.classList.add('active'); }
        b.addEventListener('click', function () {
          choice = t.id;
          Array.prototype.forEach.call(grid.children, function (c) { c.classList.remove('active'); });
          b.classList.add('active');
          if (portField) { portField.input.placeholder = String(t.default_port); }
          if (methodField) { methodField.wrap.hidden = t.id !== 'shadowsocks'; }
        });
        grid.appendChild(b);
      });
      body.appendChild(grid);

      var form = el('form', 'form-grid sb-form');
      var nameField = field('名称（可选，方便区分）', 'text');
      nameField.input.placeholder = '例如 手机';
      portField = field('端口（留空自动选择）', 'number');
      portField.input.placeholder = String((types[0] && types[0].default_port) || '');
      methodField = selectField('Shadowsocks 加密方式', [
        { value: '2022-blake3-aes-128-gcm', label: '2022-blake3-aes-128-gcm' },
        { value: '2022-blake3-aes-256-gcm', label: '2022-blake3-aes-256-gcm' },
        { value: '2022-blake3-chacha20-poly1305', label: '2022-blake3-chacha20-poly1305' }
      ], '2022-blake3-aes-128-gcm');
      methodField.wrap.hidden = choice !== 'shadowsocks';
      form.appendChild(nameField.wrap);
      form.appendChild(portField.wrap);
      form.appendChild(methodField.wrap);
      var err = el('p', 'form-error');
      err.hidden = true;
      form.appendChild(err);
      var actions = el('div', 'form-actions');
      var cancel = el('button', 'btn', '取消');
      cancel.type = 'button';
      cancel.addEventListener('click', closeModal);
      var submit = el('button', 'btn primary', '创建节点');
      submit.type = 'button';
      submit.addEventListener('click', async function () {
        err.hidden = true;
        setBusy(submit, true);
        try {
          var out = await api('/api/singbox/nodes', {
            method: 'POST',
            body: {
              type: choice,
              name: nameField.input.value.trim(),
              port: Number(portField.input.value) || 0,
              method: methodField.select.value
            }
          });
          closeModal();
          showToast('节点已创建并生效');
          await loadSingbox();
          if (out.node) { sbShowLinks(out.node); }
        } catch (e) {
          err.textContent = e.message;
          err.hidden = false;
        } finally {
          setBusy(submit, false);
        }
      });
      add(actions, cancel);
      add(actions, submit);
      form.appendChild(actions);
      body.appendChild(form);
    });
  }

  async function sbConfigDialog(node) {
    openModal('编辑配置', function (body) {
      add(body, el('p', 'muted', '直接编辑 ' + (node.file || node.id + '.json') + '。保存前会先校验，校验失败不写入。'));
      var box = el('div');
      var ta = el('textarea', 'config-editor');
      ta.spellcheck = false;
      ta.value = '加载中…';
      box.appendChild(ta);
      body.appendChild(box);
      var err = el('p', 'form-error');
      err.hidden = true;
      body.appendChild(err);
      var actions = el('div', 'form-actions');
      var cancel = el('button', 'btn', '关闭');
      cancel.type = 'button';
      cancel.addEventListener('click', closeModal);
      var save = el('button', 'btn primary', '保存并校验');
      save.type = 'button';
      save.disabled = true;
      save.addEventListener('click', async function () {
        err.hidden = true;
        setBusy(save, true);
        try {
          var out = await api('/api/singbox/config', { method: 'POST', body: { id: node.id, text: ta.value } });
          closeModal();
          showToast('配置已保存并通过校验');
          await loadSingbox();
          return out;
        } catch (e) {
          err.textContent = e.message;
          err.hidden = false;
        } finally {
          setBusy(save, false);
        }
      });
      add(actions, cancel);
      add(actions, save);
      body.appendChild(actions);
      api('/api/singbox/config?id=' + encodeURIComponent(node.id)).then(function (data) {
        ta.value = data.text || '';
        save.disabled = false;
      }).catch(function (e) {
        ta.value = '';
        err.textContent = e.message;
        err.hidden = false;
      });
    });
  }

  async function sbLogDialog() {
    openModal('sing-box 日志', function (body) {
      var pre = el('pre', '', '加载中…');
      body.appendChild(pre);
      var actions = el('div', 'form-actions');
      var refresh = el('button', 'btn', '刷新');
      refresh.type = 'button';
      refresh.addEventListener('click', function () { load(); });
      var close = el('button', 'btn primary', '关闭');
      close.type = 'button';
      close.addEventListener('click', closeModal);
      add(actions, refresh);
      add(actions, close);
      body.appendChild(actions);
      function load() {
        api('/api/singbox/log?lines=120').then(function (data) {
          var lines = data.lines || [];
          pre.textContent = lines.length ? lines.join('\n') : '没有日志输出';
        }).catch(function (e) {
          pre.textContent = e.message;
        });
      }
      load();
    });
  }

  function bind() {
    qs('#login-form').addEventListener('submit', async function (ev) {
      ev.preventDefault();
      var err = qs('#login-error');
      var btn = qs('#login-submit');
      err.hidden = true;
      setBusy(btn, true);
      try {
        var s = await api('/api/login', { method: 'POST', body: { key: qs('#login-key').value } });
        applySession(s);
        showApp();
      } catch (e) {
        err.textContent = e.message;
        err.hidden = false;
      } finally {
        setBusy(btn, false);
      }
    });
    qs('#logout').addEventListener('click', async function () {
      try { await api('/api/logout', { method: 'POST', body: {} }); } catch (e) { /* ignore */ }
      showLogin();
    });
    Array.prototype.forEach.call(document.querySelectorAll('.nav-item'), function (b) {
      b.addEventListener('click', function () { navigate(b.getAttribute('data-view')); });
    });
    qs('#refresh-overview').addEventListener('click', loadOverview);
    qs('#refresh-proxies').addEventListener('click', loadProxies);
    qs('#refresh-singbox').addEventListener('click', function () { loadSingbox(); });
    qs('#refresh-effect').addEventListener('click', loadEffect);
    qs('#refresh-diagnostics').addEventListener('click', loadDiagnostics);
    qs('#create-backup').addEventListener('click', function () { createBackup(this); });
    qs('#metrics-range').addEventListener('click', function (ev) {
      var b = ev.target.closest('.range');
      if (b) { setRange(b.getAttribute('data-range')); }
    });
    qs('#audit-search').addEventListener('submit', function (ev) {
      ev.preventDefault();
      loadAudit(qs('#audit-q').value.trim());
    });
    qs('#modal-close').addEventListener('click', function () { qs('#modal').hidden = true; });
    qs('#modal').addEventListener('click', function (ev) { if (ev.target === qs('#modal')) { qs('#modal').hidden = true; } });
    setInterval(function () {
      if (state.session.authenticated && state.view === 'overview') { loadOverview(); }
    }, 20000);
  }

  bind();
  boot();
})();