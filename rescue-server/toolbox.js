(function () {
  'use strict';
  var $ = function (id) { return document.getElementById(id); };
  var pending = new Set();
  var status = null;
  var confirmResolve = null;
  var confirmTrigger = null;
  var theme = 'auto';
  var themeNames = { auto: '跟随系统', light: '浅色', dark: '深色' };
  var mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');

  function applyTheme() {
    document.documentElement.dataset.theme = theme;
    var dark = theme === 'dark' || (theme === 'auto' && mediaQuery.matches);
    document.querySelector('meta[name="theme-color"]').content = dark ? '#121411' : '#f9faf4';
    $('btn-theme').setAttribute('aria-label', '外观：' + themeNames[theme] + '，点击切换');
    $('btn-theme').title = '外观：' + themeNames[theme];
  }
  try {
    var saved = localStorage.getItem('fc.toolbox.theme');
    if (Object.prototype.hasOwnProperty.call(themeNames, saved)) theme = saved;
  } catch (_) {}
  applyTheme();
  if (mediaQuery.addEventListener) mediaQuery.addEventListener('change', applyTheme);
  else mediaQuery.addListener(applyTheme);

  function toast(message) {
    $('toast').textContent = message;
    $('toast').classList.add('show');
    clearTimeout(toast.timer);
    toast.timer = setTimeout(function () { $('toast').classList.remove('show'); }, 4200);
  }

  async function request(url, options) {
    var response = await fetch(url, options);
    var data = await response.json();
    if (!response.ok || (data && (data.ok === false || data.error))) {
      var error = new Error((data && data.error) || ('请求失败（' + response.status + '）'));
      error.output = data && data.out;
      throw error;
    }
    return data;
  }
  function post(url, data) {
    return request(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data || {})
    });
  }
  function updateButtons() {
    document.querySelectorAll('button[data-action]').forEach(function (button) {
      var action = button.dataset.action;
      var serviceBusy = ['start', 'restart', 'stop'].some(function (key) { return pending.has(key); });
      button.disabled = pending.has(action) ||
        (['start', 'restart', 'stop'].indexOf(action) !== -1 && serviceBusy) ||
        (action === 'fixip' && (!status || status.hasAlias || !status.aliasIP));
    });
  }
  async function busy(action, button, label, work) {
    if (pending.has(action)) return;
    pending.add(action);
    var text = button.querySelector('[data-label]') || button;
    var previous = text.textContent;
    text.textContent = label;
    button.setAttribute('aria-busy', 'true');
    updateButtons();
    try { await work(); }
    catch (error) { toast(error.message || '请求失败，请再试一次'); }
    finally {
      pending.delete(action);
      text.textContent = previous;
      button.removeAttribute('aria-busy');
      updateButtons();
      if (action === 'fixip' && status) renderIP(status);
    }
  }

  function closeConfirm(accepted) {
    $('confirm-root').classList.add('hidden');
    document.body.style.overflow = '';
    if (confirmResolve) confirmResolve(accepted);
    confirmResolve = null;
    if (confirmTrigger) confirmTrigger.focus();
  }
  function confirmAction(title, description, label, danger) {
    confirmTrigger = document.activeElement;
    $('confirm-title').textContent = title;
    $('confirm-description').textContent = description;
    $('confirm-ok').textContent = label;
    $('confirm-ok').className = 'button filled' + (danger ? ' danger' : '');
    $('confirm-root').classList.remove('hidden');
    document.body.style.overflow = 'hidden';
    $('confirm-cancel').focus();
    return new Promise(function (resolve) { confirmResolve = resolve; });
  }
  $('confirm-cancel').addEventListener('click', function () { closeConfirm(false); });
  $('confirm-ok').addEventListener('click', function () { closeConfirm(true); });
  document.querySelector('[data-dismiss]').addEventListener('click', function () { closeConfirm(false); });
  document.addEventListener('keydown', function (event) {
    if ($('confirm-root').classList.contains('hidden')) return;
    if (event.key === 'Escape') { event.preventDefault(); closeConfirm(false); }
    if (event.key === 'Tab') {
      var first = $('confirm-cancel'), last = $('confirm-ok');
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    }
  });

  function renderIP(data) {
    var ips = (data.ips || []).filter(function (ip) { return ip !== '127.0.0.1'; });
    $('st-ip').textContent = '当前地址：' + (ips.join('、') || location.hostname);
    var alias = data.aliasIP || '未配置（可选）';
    if (!data.aliasIP) {
      $('alias-note').textContent = '固定地址是可选功能，需要时通过 BOX_ALIAS_IP 环境变量配置。';
      $('alias-note').className = 'note';
      $('btn-fixip').querySelector('[data-label]').textContent = '未配置固定地址';
      updateButtons();
      return;
    }
    $('alias-note').textContent = data.hasAlias
      ? '常用地址 ' + alias + ' 可以正常使用。'
      : '常用地址 ' + alias + ' 暂时没连上。恢复地址时网络可能短暂断开，请避开播放或下载。';
    $('alias-note').className = 'note' + (data.hasAlias ? '' : ' bad');
    if (!pending.has('fixip')) {
      $('btn-fixip').querySelector('[data-label]').textContent = data.hasAlias ? '常用地址已恢复' : '恢复常用地址';
    }
    updateButtons();
  }
  function renderStatus(data) {
    status = data;
    var service = data.svc || {};
    $('st-svc').textContent = service.running ? '运行中' : '未运行';
    $('st-svc').className = service.running ? 'ok' : 'bad';
    $('st-pid').textContent = service.running ? '进程 ' + service.pid : '可以点下面的启动服务';
    $('st-http').textContent = service.http ? '正常' : '无响应';
    $('st-http').className = service.http ? 'ok' : 'bad';
    var library = (service.lib || '').match(/共 (\d+) 部（(.+)）/);
    $('st-lib').textContent = service.http && library ? library[1] + ' 部' : '—';
    $('st-lib-note').textContent = service.http && library ? library[2] : '盒子里保存的片子';
    $('connection').textContent = '盒子在线';
    $('connection').className = 'badge ok';
    $('last-update').textContent = '更新于 ' + new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    var mounts = (data.mounts || []).map(function (mount) { return mount.path; });
    $('st-mount').textContent = mounts.length ? '硬盘位置：' + mounts.join('、') : '还没有找到硬盘，请检查连接';
    $('st-mount').className = mounts.length ? '' : 'bad';
    renderIP(data);
  }
  async function refresh() {
    if (pending.has('status')) return;
    pending.add('status');
    try { renderStatus(await request('/api/status')); }
    catch (_) {
      $('connection').textContent = '连接中断';
      $('connection').className = 'badge bad';
      $('last-update').textContent = '暂时连不上，稍后自动再试';
      $('st-svc').textContent = '待连接';
      $('st-http').textContent = '待连接';
      $('st-svc').className = $('st-http').className = '';
      $('st-lib').textContent = '—';
      $('st-lib-note').textContent = '连接恢复后更新';
      $('st-pid').textContent = '正在尝试重新连接';
    } finally { pending.delete('status'); }
  }

  var fixes = {
    disk_repair: 'repair', svc_start: 'start', svc_restart: 'restart',
    scan: 'scan', fix_ip: 'fixip', scan_hosts: 'find'
  };
  function renderDiag(data) {
    var list = $('diag-list');
    list.textContent = '';
    (data.checks || []).forEach(function (check) {
      var item = document.createElement('li');
      item.className = 'diag-item' + (check.ok ? '' : ' bad');
      var heading = document.createElement('div');
      heading.className = 'diag-heading';
      var dot = document.createElement('span');
      dot.className = 'diag-dot';
      heading.appendChild(dot);
      heading.appendChild(document.createTextNode(check.name + ' · ' + (check.ok ? '正常' : '需处理')));
      var detail = document.createElement('p');
      detail.className = 'diag-detail';
      detail.textContent = check.detail;
      item.appendChild(heading);
      item.appendChild(detail);
      if (!check.ok && fixes[check.fix]) {
        var button = document.createElement('button');
        button.type = 'button';
        button.className = 'button tonal diag-fix';
        button.dataset.action = fixes[check.fix];
        button.textContent = check.fixLabel || '点这里修复';
        item.appendChild(button);
      }
      list.appendChild(item);
    });
    $('diag-sum').className = 'diag-summary' + (data.problems > 0 ? ' bad' : '');
    $('diag-sum').textContent = data.verdict || '体检完成';
    updateButtons();
  }
  async function diagnose(button) {
    await busy('diagnose', button, '正在体检…', async function () {
      $('diag-results').classList.remove('hidden');
      $('diag-sum').className = 'diag-summary';
      $('diag-sum').textContent = '正在逐项检查，请稍等…';
      $('diag-list').textContent = '';
      try { renderDiag(await request('/api/diagnose')); }
      catch (error) {
        $('diag-sum').className = 'diag-summary bad';
        $('diag-sum').textContent = '体检没能完成，请确认盒子在线后再试一次。';
        throw error;
      }
    });
  }
  async function service(action, button) {
    var labels = { start: '启动服务', restart: '重启服务', stop: '停止服务' };
    if (action !== 'start' && !await confirmAction(labels[action] + '？',
      '这会打断正在播放的视频和下载任务，请确认家里现在没有人在使用。', labels[action], action === 'stop')) return;
    await busy(action, button, '正在' + labels[action] + '…', async function () {
      try {
        var data = await post('/api/svc', { action: action });
        $('main-log').textContent = data.out || '操作已完成';
        toast(labels[action] + '完成');
      } catch (error) {
        $('main-log').textContent = error.output || error.message;
        throw error;
      } finally { await refresh(); }
    });
  }
  async function scanLibrary(button) {
    await busy('scan', button, '正在刷新…', async function () {
      await post('/api/scan');
      toast('已开始刷新片子列表，稍后会自动更新');
      await new Promise(function (resolve) { setTimeout(resolve, 6000); });
      await refresh();
    });
  }
  async function findHosts(button) {
    await busy('find', button, '正在搜索…', async function () {
      var data = await request('/api/scan-hosts');
      var panel = $('found-hosts');
      panel.classList.remove('hidden');
      panel.textContent = data.found && data.found.length ? '找到以下盒子，点击地址打开：' : '没有找到正在运行的盒子服务，请稍后再试。';
      (data.found || []).forEach(function (host) {
        var link = document.createElement('a');
        link.href = 'http://' + host + '/';
        link.textContent = host;
        panel.appendChild(link);
      });
      await refresh();
    });
  }
  async function fixIP(button) {
    if (!await confirmAction('恢复常用地址？', '操作时网络可能短暂断开。如果正在播放或下载，请等结束后再恢复。', '恢复地址')) return;
    await busy('fixip', button, '正在恢复…', async function () {
      var data = await post('/api/fix-ip');
      toast(data.out || '常用地址已恢复');
      await refresh();
    });
  }
  async function showLogs(button) {
    await busy('logs', button, '正在读取…', async function () {
      var data = await request('/api/logs');
      $('main-log').textContent = data.logs || '暂无日志';
      $('logs-section').scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  }
  async function showDisk(button) {
    await busy('disk', button, '正在读取…', async function () {
      var data = await request('/api/status');
      $('disk-log').classList.remove('hidden');
      $('disk-log').textContent = data.disk || '暂时没有磁盘信息';
    });
  }
  async function showTasks(button) {
    await busy('tasks', button, '正在读取…', async function () {
      var tasks = await request('/api/tasks');
      var list = $('task-list');
      list.classList.remove('hidden');
      list.textContent = '';
      if (!tasks || !tasks.length) {
        var empty = document.createElement('li');
        empty.className = 'supporting';
        empty.textContent = '当前没有下载任务';
        list.appendChild(empty);
        return;
      }
      var states = { running: '正在下载', processing: '正在整理入库', done: '已完成', stopped: '已停止', error: '下载出错' };
      tasks.forEach(function (task) {
        var item = document.createElement('li');
        var name = document.createElement('p');
        name.className = 'task-name';
        name.textContent = task.name;
        item.appendChild(name);
        var progress = Math.max(0, Math.min(100, task.progress || 0));
        var speed = task.speedKBs > 1024 ? (task.speedKBs / 1024).toFixed(1) + ' MB' : (task.speedKBs || 0).toFixed(0) + ' KB';
        var detail = document.createElement('p');
        detail.className = 'task-detail';
        detail.textContent = (task.stage || states[task.status] || task.status) + ' · 已下载 ' + progress.toFixed(1) + '% · 每秒 ' + speed;
        item.appendChild(detail);
        if (task.error) {
          var error = document.createElement('p');
          error.className = 'task-error';
          error.textContent = task.error;
          item.appendChild(error);
        }
        var bar = document.createElement('div'), fill = document.createElement('i');
        bar.className = 'bar';
        fill.style.width = progress + '%';
        bar.appendChild(fill);
        item.appendChild(bar);
        list.appendChild(item);
      });
    });
  }
  async function clearCache(button) {
    if (!await confirmAction('清空下载缓存？', '还没下载完的视频会丢失下载进度。这是盒子上的缓存，请确认后再清空。', '清空缓存', true)) return;
    await busy('clear', button, '正在清理…', async function () {
      var data = await post('/api/cache/clear');
      toast('已清理下载缓存' + (data.freedHuman ? '，释放 ' + data.freedHuman : ''));
    });
  }
  async function repair(button) {
    if (!await confirmAction('开始修复硬盘？', '会恢复硬盘路径、重建损坏的下载记录，并重启媒体服务。\n\n播放和下载会被打断，请等家里没人使用时再修复。', '开始修复')) return;
    await busy('repair', button, '正在修复…', function () {
      return new Promise(function (resolve, reject) {
        var log = $('repair-log');
        log.classList.remove('hidden');
        log.textContent = '';
        $('repair-prog').classList.remove('hidden');
        var stream = new EventSource('/api/repair');
        function finish() { stream.close(); $('repair-prog').classList.add('hidden'); refresh(); }
        stream.onmessage = function (event) { log.textContent += event.data + '\n'; log.scrollTop = log.scrollHeight; };
        stream.addEventListener('done', function (event) {
          finish();
          try {
            var data = JSON.parse(event.data);
            if (!data.ok) throw new Error(data.error || '修复失败，请查看修复记录');
            log.textContent += '\n修复完成。';
            toast('修复完成');
            resolve();
          } catch (error) { log.textContent += '\n' + error.message; reject(error); }
        });
        stream.onerror = function () {
          finish();
          log.textContent += '\n连接中断，暂时无法确认修复结果。请查看盒子状态后再操作。';
          reject(new Error('修复连接中断，请查看下方记录'));
        };
      });
    });
  }
  document.addEventListener('click', function (event) {
    var button = event.target.closest('button[data-action]');
    if (!button || button.disabled) return;
    var action = button.dataset.action;
    if (['start', 'restart', 'stop'].indexOf(action) !== -1) { service(action, button); return; }
    var actions = {
      diagnose: diagnose, scan: scanLibrary, find: findHosts, fixip: fixIP,
      logs: showLogs, disk: showDisk, tasks: showTasks, clear: clearCache, repair: repair
    };
    if (actions[action]) actions[action](button);
    if (action === 'theme') {
      theme = { auto: 'light', light: 'dark', dark: 'auto' }[theme];
      try { localStorage.setItem('fc.toolbox.theme', theme); } catch (_) {}
      applyTheme();
      toast('外观已设为' + themeNames[theme]);
    }
  });
  $('cinema-link').href = 'http://' + location.hostname + ':8088/';
  window.addEventListener('scroll', function () {
    document.querySelector('.topbar').classList.toggle('scrolled', window.scrollY > 4);
  }, { passive: true });
  setInterval(function () { if (!document.hidden) refresh(); }, 8000);
  document.addEventListener('visibilitychange', function () { if (!document.hidden) refresh(); });
  refresh();
})();
