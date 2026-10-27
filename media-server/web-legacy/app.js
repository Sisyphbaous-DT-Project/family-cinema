/* 家庭影院 · 前端逻辑
   模块：媒体库网格 / 输入即搜下拉 / 详情与磁力 / 播放器 / 下载任务 */
(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };
  var api = function (p) { return "/api" + p; };
  var enc = encodeURIComponent;

  var S = {
    cat: "all",
    items: [],
    lastScan: 0,
    seq: 0,
    timer: null,
    cursor: -1,
    hits: [],
    playQueue: [],
    playIndex: -1,
    playKey: null,
    playPath: null,
    tasksTimer: null,
    detailCtx: null,
    view: "lib",     // lib | res | mng
    resSeq: 0,
    resHits: [],     // 结果页数据
    resKw: "",
    resScope: "all", // all | local | remote
    downloading: [], // 媒体库里的下载中卡片
    mnMode: "magnet",
    mnCat: "movie",
    mnDest: "new",   // new | existing（并入已有条目）
    mnTarget: null,  // 并入目标条目
    upTarget: null,  // 上传时的并入目标条目
    mnFile: null,    // 已选的 .torrent（base64）
    mnCover: null,   // 已选的封面（data URL）
    mngItems: [],    // 管理页条目
    mngCat: "all",
    cacheEntries: [], // 下载缓存清单
    edItem: null,    // 正在编辑的条目
    edCover: null,
    delItem: null    // 待删除的条目
  };

  /* ---------- 工具 ---------- */
  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function fmtSize(n) {
    if (!n && n !== 0) return "";
    if (n < 1024) return n + " B";
    var u = ["KB", "MB", "GB", "TB"], i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
    return (n >= 100 ? Math.round(n) : n.toFixed(1)) + " " + u[i];
  }
  function fmtTime(ts) {
    if (!ts) return "";
    var d = new Date(ts * 1000), p = function (v) { return v < 10 ? "0" + v : v; };
    return d.getMonth() + 1 + "月" + d.getDate() + "日 " + p(d.getHours()) + ":" + p(d.getMinutes());
  }
  function icon(id, cls) { return '<svg class="ic ' + (cls || "") + '"><use href="#' + id + '"/></svg>'; }
  var toastTimer;
  function toast(msg) {
    var t = $("toast");
    t.textContent = msg;
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.hidden = true; }, 2600);
  }
  function catName(c) { return c === "movie" ? "电影电视剧" : "番剧"; }
  function coverURL(p) { return api("/cover?path=" + enc(p)); }

  /* ---------- 观看进度（存 localStorage，每台设备各自独立） ---------- */
  function progKey(path) { return "pos:" + path; }
  // 读进度，返回 {pos,dur,ts} 或 null；兼容旧格式（只存了秒数的纯数字）
  function getProg(path) {
    // App 里进度由原生存（不按网址隔离，在线离线共享同一份），浏览器才用 localStorage
    if (hasBridge()) {
      var bo = bjson(function (b) { return b.getProg(progKey(path)); }, null);
      return (bo && typeof bo.pos === "number") ? bo : null;
    }
    try {
      var raw = localStorage.getItem(progKey(path));
      if (!raw) return null;
      if (raw.charAt(0) !== "{") {
        var s = parseFloat(raw);
        return isNaN(s) ? null : { pos: s, dur: 0, ts: 0 };
      }
      var o = JSON.parse(raw);
      return (o && typeof o.pos === "number") ? o : null;
    } catch (e) { return null; }
  }
  function setProg(path, pos, dur) {
    if (hasBridge()) { bcall(function (b) { return b.setProg(progKey(path), pos, dur); }); return; }
    try { localStorage.setItem(progKey(path), JSON.stringify({ pos: pos, dur: dur || 0, ts: Math.floor(Date.now() / 1000) })); } catch (e) {}
  }
  function clearProg(path) {
    if (hasBridge()) { bcall(function (b) { return b.clearProg(progKey(path)); }); return; }
    try { localStorage.removeItem(progKey(path)); } catch (e) {}
  }
  // 看了几成（0~1），时长未知记 0
  function progRatio(path) {
    var p = getProg(path);
    if (!p || !p.dur) return 0;
    return Math.min(1, p.pos / p.dur);
  }
  function isWatched(path) { return progRatio(path) >= 0.95; }

  /* ---------- App 原生桥（浏览器里没有，自动降级） ---------- */
  var Bridge = (typeof window.AndroidBridge !== "undefined") ? window.AndroidBridge : null;
  function hasBridge() { return !!Bridge; }
  // 安全调原生方法：Bridge 不存在或调用出错都返回 fallback
  function bcall(fn, fallback) {
    var fb = (fallback === undefined) ? null : fallback;
    if (!Bridge) return fb;
    try { var r = fn(Bridge); return (r === undefined || r === null) ? fb : r; } catch (e) { return fb; }
  }
  function bjson(fn, fallback) {
    var fb = (fallback === undefined) ? null : fallback;
    var raw = bcall(fn, null);
    if (!raw) return fb;
    try { return JSON.parse(raw); } catch (e) { return fb; }
  }
  // 离线：App 检测不到服务器时，前端只显示已缓存内容
  function isOffline() {
    return hasBridge() && bcall(function (b) { return b.isOnline(); }, "1") === "0";
  }

  // 移动端横屏：有 App 桥走原生（用户开系统旋转锁定也生效），浏览器退化为网页接口
  function lockLandscape() {
    if (hasBridge()) { bcall(function (b) { return b.setOrientation("landscape"); }); return; }
    if (window.screen && screen.orientation && screen.orientation.lock) screen.orientation.lock("landscape").catch(function () {});
  }
  function unlockOrientation() {
    if (hasBridge()) { bcall(function (b) { return b.setOrientation("portrait"); }); return; }
    if (window.screen && screen.orientation && screen.orientation.unlock) screen.orientation.unlock();
  }

  // 播放期间屏幕常亮：App 走原生 FLAG_KEEP_SCREEN_ON（内网 http 下唯一可靠手段），
  // 浏览器顺带试 Wake Lock API（非 secure context 会静默失败，无妨）。
  // 策略：播放器弹层开着就常亮（暂停也算，用户随时会回来），关掉播放器立即恢复息屏。
  var wakeLock = null;
  function acquireWakeLock() {
    if (hasBridge()) bcall(function (b) { return b.setKeepScreenOn(true); });
    if ("wakeLock" in navigator && !wakeLock) {
      navigator.wakeLock.request("screen").then(function (l) { wakeLock = l; }).catch(function () {});
    }
  }
  function releaseWakeLock() {
    if (hasBridge()) bcall(function (b) { return b.setKeepScreenOn(false); });
    if (wakeLock) { wakeLock.release().catch(function () {}); wakeLock = null; }
  }
  // 页面切后台时系统会自动收回 Wake Lock，回前台时播放器还开着就重新申请
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden && $("m-player") && !$("m-player").hidden) acquireWakeLock();
  });
  // 整部进度：各集进度的平均值
  function itemProg(item) {
    var eps = (item && item.episodes) || [];
    if (!eps.length) return 0;
    var sum = 0;
    for (var i = 0; i < eps.length; i++) sum += progRatio(eps[i].path);
    return sum / eps.length;
  }

  /* ---------- 媒体库 ---------- */
  function loadLibrary() {
    // 离线：App 检测不到服务器，只显示已缓存
    if (isOffline()) { loadLibraryOffline(); return; }
    $("loading").hidden = false;
    $("empty").hidden = true;
    fetch(api("/library?cat=" + S.cat))
      .then(function (r) { return r.json(); })
      .then(function (d) {
        S.items = d.items || [];
        S.downloading = d.downloading || [];
        S.lastScan = d.lastScan || 0;
        applyCachedMarks();
        renderGrid();
        renderContinue();
        var cats = d.categories || [];
        var total = (cats[0] || {}).count || 0;
        $("lib-meta").textContent = total + " 部资源" + (S.lastScan ? " · 更新于 " + fmtTime(S.lastScan) : "");
        $("loading").hidden = true;
      })
      .catch(function () {
        // App 里网络抖动时降级到已缓存，而不是只报错
        if (hasBridge()) { loadLibraryOffline(); return; }
        $("loading").hidden = true;
        $("empty").hidden = false;
        $("empty").querySelector(".state-title").textContent = "无法连接服务";
        $("empty").querySelector(".state-desc").textContent = "请确认媒体服务正在运行";
      });
  }

  // 离线模式：从壳读已缓存清单，只显示这些
  function loadLibraryOffline() {
    var d = bjson(function (b) { return b.getLocalLibrary(); }, null) || { items: [] };
    S.items = d.items || [];
    S.downloading = [];
    S.lastScan = 0;
    applyCachedMarks();
    renderGrid();
    renderContinue();
    $("loading").hidden = true;
    $("lib-meta").textContent = "离线模式 · 仅显示已缓存";
    if (!S.items.length) {
      $("empty").hidden = false;
      $("empty").querySelector(".state-title").textContent = "离线模式";
      $("empty").querySelector(".state-desc").textContent = "当前不在内网，且暂无已缓存内容";
    }
  }

  // 在线时给已缓存的集打标记（详情页显示"已缓存"）
  function applyCachedMarks() {
    if (!hasBridge()) return;
    var cached = bjson(function (b) { return b.listCached(); }, []) || [];
    var set = {};
    cached.forEach(function (c) {
      if (c.done === false) return; // 只标已完成的
      if (c.path) set[c.path] = 1;
      if (c.epId) set[c.epId] = 1;
    });
    S.items.forEach(function (it) {
      (it.episodes || []).forEach(function (ep) { ep.cached = !!set[ep.path]; });
    });
  }

  /* 继续观看：扫本地进度，把"看了个开头没看完"的集排在媒体库前面 */
  function renderContinue() {
    var sec = $("continue-sec"), row = $("continue-row");
    if (!sec) return;
    var list = [];
    S.items.forEach(function (it) {
      (it.episodes || []).forEach(function (ep, ei) {
        var p = getProg(ep.path);
        if (!p || !p.dur) return;
        var ratio = p.pos / p.dur;
        if (ratio > 0.02 && ratio < 0.95) list.push({ it: it, ep: ep, ei: ei, ratio: ratio, ts: p.ts || 0 });
      });
    });
    if (!list.length) { sec.hidden = true; row.innerHTML = ""; return; }
    list.sort(function (a, b) { return b.ts - a.ts; });
    list = list.slice(0, 12);
    sec.hidden = false;
    row.innerHTML = list.map(function (x, k) {
      var pct = Math.round(x.ratio * 100);
      var thumb = x.it.cover
        ? '<img src="' + esc(coverURL(x.it.cover)) + '" alt="" loading="lazy">'
        : '<div class="poster-fallback">' + icon("i-film") + "</div>";
      return '<div class="ct-card" data-k="' + k + '">' +
        '<div class="ct-poster">' + thumb + '<span class="ct-play">' + icon("i-play") + "</span>" +
        '<span class="ct-prog"><span class="ct-prog-fill" style="width:' + pct + '%"></span></span></div>' +
        '<p class="ct-title">' + esc(x.it.title) + "</p>" +
        '<p class="ct-sub">' + esc(x.ep.title || x.ep.filename) + " · 看到 " + pct + "%</p></div>";
    }).join("");
    row.querySelectorAll(".ct-card").forEach(function (el) {
      var x = list[+el.dataset.k];
      el.addEventListener("click", function () {
        S.playQueue = x.it.episodes.slice();
        S.playIndex = x.ei;
        playCurrent(x.it.title);
      });
    });
  }

  function renderGrid() {
    var g = $("grid");
    var dls = S.downloading || [];
    if (!S.items.length && !dls.length) { g.innerHTML = ""; $("empty").hidden = false; return; }
    $("empty").hidden = true;

    // 下载中的卡片排在最前面
    var html = dls.map(dlCard).join("") + S.items.map(function (it, i) {
      var eps = it.episodes || [];
      var thumb = it.cover
        ? '<img src="' + esc(coverURL(it.cover)) + '" alt="" loading="lazy">'
        : '<div class="poster-fallback">' + icon("i-film") + "</div>";
      var badge = eps.length > 1 ? '<span class="badge-eps">' + eps.length + " 集</span>" : "";
      var prog = Math.round(itemProg(it) * 100);
      var progBar = prog > 0
        ? '<div class="card-prog"><span class="card-prog-fill" style="width:' + prog + '%"></span></div>'
        : "";
      return '<article class="card" data-i="' + i + '">' +
        '<div class="poster">' + thumb + badge +
        '<div class="poster-play"><span>' + icon("i-play") + "</span></div></div>" +
        '<div class="card-meta"><h3 class="card-title">' + esc(it.title) + "</h3>" +
        '<p class="card-sub">' + catName(it.category) + " · " + fmtSize(it.size) + "</p>" + progBar + "</div></article>";
    }).join("");
    g.innerHTML = html;
    g.querySelectorAll(".card[data-i]").forEach(function (el) {
      el.addEventListener("click", function () { openLocalDetail(S.items[+el.dataset.i]); });
    });
    g.querySelectorAll(".card-downloading").forEach(function (el) {
      el.addEventListener("click", openTasks);
    });
  }

  // refreshDownloadCards 只更新下载卡片的进度，避免整页重绘打断滚动
  function refreshDownloadCards(active) {
    var prevCount = S.downloading.length;
    // 任务数量变化（新增/完成）时整体重载，让入库条目出现
    if (active.length !== prevCount) {
      loadLibrary();
      return;
    }
    // 数量不变则原地更新进度数值
    var cards = $("grid").querySelectorAll(".card-downloading");
    active.forEach(function (t, i) {
      var el = cards[i];
      if (!el) return;
      var d = S.downloading[i];
      if (d) {
        d.progress = t.progress;
        d.speedKBs = t.speedKBs;
        d.status = t.status;
        d.stage = t.stage;
        d.done = t.bytesDone;
        d.total = t.bytesTotal;
      }
      var processing = t.status === "processing";
      var pct = Math.max(0, Math.min(100, t.progress || 0));
      var pctEl = el.querySelector(".dl-pct");
      var fill = el.querySelector(".dl-fill");
      var sub = el.querySelector(".dl-sub");
      var badge = el.querySelector(".badge-dl");
      if (pctEl) pctEl.textContent = processing ? "整理中" : pct.toFixed(0) + "%";
      if (fill) {
        fill.classList.toggle("is-indeterminate", processing);
        fill.style.width = pct + "%";
      }
      if (badge) badge.textContent = processing ? "整理中" : "下载中";
      if (sub) {
        var speed = t.speedKBs
          ? (t.speedKBs > 1024 ? (t.speedKBs / 1024).toFixed(1) + " MB/s" : Math.round(t.speedKBs) + " KB/s")
          : "";
        sub.textContent = processing
          ? (t.stage || "正在整理文件")
          : (t.bytesTotal ? fmtSize(t.bytesDone) + " / " + fmtSize(t.bytesTotal) + (speed ? " · " + speed : "") : "正在连接节点");
      }
    });
  }

  // dlCard 下载中的占位卡片（Apple 风格绿色进度条）
  function dlCard(d) {
    var pct = Math.max(0, Math.min(100, d.progress || 0));
    var processing = d.status === "processing";
    var thumb = d.cover
      ? '<img src="' + esc(coverURL(d.cover)) + '" alt="" loading="lazy">'
      : '<div class="poster-fallback">' + icon("i-film") + "</div>";
    var speed = d.speedKBs
      ? (d.speedKBs > 1024 ? (d.speedKBs / 1024).toFixed(1) + " MB/s" : Math.round(d.speedKBs) + " KB/s")
      : "";
    var sub = processing
      ? (d.stage || "正在整理文件")
      : (d.total ? fmtSize(d.done) + " / " + fmtSize(d.total) + (speed ? " · " + speed : "") : "正在连接节点");
    // 整理阶段进度未知，用不定长动画条
    var fillCls = processing ? "dl-fill is-indeterminate" : "dl-fill";
    var pctText = processing ? "整理中" : pct.toFixed(0) + "%";
    return '<article class="card card-downloading" title="点击查看下载任务">' +
      '<div class="poster">' + thumb +
      '<span class="badge-dl">' + (processing ? "整理中" : "下载中") + "</span>" +
      '<div class="dl-veil">' +
      '<span class="dl-pct">' + pctText + "</span>" +
      '<div class="dl-track"><i class="' + fillCls + '" style="width:' + pct + '%"></i></div>' +
      '<span class="dl-sub">' + esc(sub) + "</span>" +
      "</div></div>" +
      '<div class="card-meta"><h3 class="card-title">' + esc(d.title) + "</h3>" +
      '<p class="card-sub">' + catName(d.category) + "</p></div></article>";
  }

  /* ---------- 视图切换 ---------- */
  var MAIN_VIEWS = ["lib", "res", "mng", "cache", "me"];
  // 统一切换主视图：控制五个视图的显隐 + 底部导航高亮
  function setViewVisible(name) {
    // 离开缓存页就停掉进度轮询（在缓存页内部由 showCache 重新启动）
    if (name !== "cache") stopCacheTimer();
    MAIN_VIEWS.forEach(function (v) {
      var el = $("view-" + v);
      if (el) el.hidden = (v !== name);
    });
    document.querySelectorAll(".tabbar-btn").forEach(function (b) {
      b.classList.toggle("is-active", b.dataset.view === name);
    });
  }

  function showLib(pushHistory) {
    S.view = "lib";
    setViewVisible("lib");
    window.scrollTo(0, 0);
    if (pushHistory !== false) {
      try { history.pushState({ view: "lib" }, "", location.pathname); } catch (e) {}
    }
  }

  function showRes(kw, pushHistory) {
    S.view = "res";
    S.resKw = kw;
    setViewVisible("res");
    $("res-kw").textContent = kw ? "：" + kw : "";
    window.scrollTo(0, 0);
    if (pushHistory !== false) {
      try { history.pushState({ view: "res", kw: kw }, "", "#q=" + enc(kw)); } catch (e) {}
    }
  }

  /* ---------- 搜索结果页 ---------- */
  function submitSearch(kw) { runSubmit(kw, true); }
  function submitSearchNoPush(kw) { runSubmit(kw, false); }

  function runSubmit(kw, pushHistory) {
    kw = (kw || "").trim();
    if (!kw) return;
    $("dropdown").hidden = true;
    $("q").blur();
    showRes(kw, pushHistory);
    renderResList([], true);

    var seq = ++S.resSeq;
    $("res-loading").hidden = false;
    $("res-empty").hidden = true;
    $("res-tabs").hidden = true;
    $("res-meta").textContent = "";
    $("res-list").innerHTML = "";

    fetch(api("/search?q=" + enc(kw)))
      .then(function (r) { return r.json(); })
      .then(function (hits) {
        if (seq !== S.resSeq) return;
        S.resHits = hits || [];
        S.resScope = "all";
        syncScopeTabs();
        $("res-loading").hidden = true;
        renderResList(S.resHits);
      })
      .catch(function () {
        if (seq !== S.resSeq) return;
        $("res-loading").hidden = true;
        $("res-empty").hidden = false;
        $("res-empty").querySelector(".state-title").textContent = "检索失败";
        $("res-empty").querySelector(".state-desc").textContent = "请确认服务运行正常后重试";
      });
  }

  function scopedHits() {
    if (S.resScope === "local") return S.resHits.filter(function (h) { return h.local; });
    if (S.resScope === "remote") return S.resHits.filter(function (h) { return (h.results || []).length; });
    return S.resHits;
  }

  function syncScopeTabs() {
    var localN = S.resHits.filter(function (h) { return h.local; }).length;
    var remoteN = S.resHits.filter(function (h) { return (h.results || []).length; }).length;
    $("res-tabs").hidden = S.resHits.length === 0;
    var tabs = $("res-tabs").querySelectorAll(".tab");
    tabs[0].textContent = "全部 " + S.resHits.length;
    tabs[1].textContent = "本地 " + localN;
    tabs[2].textContent = "全网 " + remoteN;
    tabs.forEach(function (t) { t.classList.toggle("is-active", t.dataset.scope === S.resScope); });
    $("res-meta").textContent = S.resHits.length
      ? "共 " + S.resHits.length + " 条结果，其中本地 " + localN + " 条、全网 " + remoteN + " 条"
      : "";
  }

  function renderResList(hits, skip) {
    if (skip) return;
    var host = $("res-list");
    var list = hits || [];
    if (!list.length) {
      host.innerHTML = "";
      $("res-empty").hidden = false;
      return;
    }
    $("res-empty").hidden = true;

    var locals = list.filter(function (h) { return h.local; });
    var remotes = list.filter(function (h) { return !h.local; });
    var html = "";
    if (locals.length) {
      html += '<div class="res-group"><div class="res-group-head">' + icon("i-folder") +
        '<span class="res-group-title">本地已有</span>' +
        '<span class="res-group-count">' + locals.length + " 条</span></div>" +
        locals.map(resRow).join("") + "</div>";
    }
    if (remotes.length) {
      html += '<div class="res-group"><div class="res-group-head">' + icon("i-globe") +
        '<span class="res-group-title">全网资源</span>' +
        '<span class="res-group-count">' + remotes.length + " 条</span></div>" +
        remotes.map(resRow).join("") + "</div>";
    }
    host.innerHTML = html;
    host.querySelectorAll(".res-card").forEach(function (el) {
      el.addEventListener("click", function () {
        var h = S.resHits[+el.dataset.k];
        if (h) openHitDetail(h);
      });
    });
  }

  function resRow(h) {
    var k = S.resHits.indexOf(h);
    var loc = h.local;
    var cover = h.image
      ? '<img src="' + esc(h.image) + '" alt="" loading="lazy">'
      : (loc && loc.cover
        ? '<img src="' + esc(coverURL(loc.cover)) + '" alt="" loading="lazy">'
        : '<div class="poster-fallback">' + icon("i-film") + "</div>");

    var chips = "";
    if (loc) {
      chips += '<span class="chip chip-local">本地已有</span>';
      chips += '<span class="chip">' + catName(loc.category) + "</span>";
    }
    (h.results || []).forEach(function (r) {
      var n = (r.torrents || []).length;
      chips += '<span class="chip chip-brand">' + esc(r.siteName || r.site) +
        (n ? " " + n : "") + "</span>";
    });

    var facts = [];
    if (loc) {
      var eps = loc.episodes || [];
      facts.push(eps.length > 1 ? eps.length + " 集已入库" : "已入库");
      var total = eps.reduce(function (s, e) { return s + (e.size || 0); }, 0);
      if (total) facts.push(fmtSize(total));
    }
    var srcN = (h.results || []).length;
    var torN = (h.results || []).reduce(function (s, r) { return s + ((r.torrents || []).length); }, 0);
    if (srcN) facts.push(srcN + " 个来源站");
    if (torN) facts.push(torN + " 个可下载资源");

    var action = loc ? "查看并播放" : "查看资源与测速";
    return '<article class="res-card" data-k="' + k + '">' +
      '<div class="res-poster">' + cover + "</div>" +
      '<div class="res-body">' +
      '<h3 class="res-name">' + esc(h.title) + "</h3>" +
      '<div class="res-chips">' + chips + "</div>" +
      (facts.length ? '<p class="res-facts">' + facts.map(esc).join(" · ") + "</p>" : "") +
      "</div>" +
      '<div class="res-go">' + action + icon("i-back", "ic-flip") + "</div>" +
      "</article>";
  }

  /* ---------- 搜索 ---------- */
  function bindSearch() {
    var input = $("q"), dd = $("dropdown"), clear = $("q-clear");

    // 表单提交（点搜索按钮 / 回车）进入结果页
    $("search").addEventListener("submit", function (e) {
      e.preventDefault();
      submitSearch(input.value);
    });

    $("res-back").addEventListener("click", function () { showLib(); });

    $("res-tabs").addEventListener("click", function (e) {
      var t = e.target.closest(".tab");
      if (!t) return;
      S.resScope = t.dataset.scope;
      syncScopeTabs();
      renderResList(scopedHits());
    });

    // 浏览器前进后退
    window.addEventListener("popstate", function (e) {
      var st = e.state || {};
      if (st.view === "res" && st.kw) {
        if (st.kw !== S.resKw) { input.value = st.kw; submitSearchNoPush(st.kw); }
        else showRes(st.kw, false);
      } else if (st.view === "mng") {
        showMng(false);
      } else showLib(false);
    });

    input.addEventListener("input", function () {
      var v = input.value.trim();
      clear.hidden = !v;
      clearTimeout(S.timer);
      if (!v) { dd.hidden = true; S.hits = []; return; }
      dd.hidden = false;
      dd.innerHTML = '<div class="dd-hint"><span class="spinner"></span>正在检索本地与全网资源</div>';
      S.timer = setTimeout(function () { runSearch(v); }, 300);
    });

    input.addEventListener("keydown", function (e) {
      if (dd.hidden || !S.hits.length) return;
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        S.cursor += e.key === "ArrowDown" ? 1 : -1;
        if (S.cursor < 0) S.cursor = S.hits.length - 1;
        if (S.cursor >= S.hits.length) S.cursor = 0;
        highlight();
      } else if (e.key === "Enter" && S.cursor >= 0) {
        // 方向键选中了某条才直接打开详情，否则交给 form submit 进结果页
        e.preventDefault();
        pickHit(S.hits[S.cursor]);
      } else if (e.key === "Escape") {
        dd.hidden = true;
        input.blur();
      }
    });

    clear.addEventListener("click", function () {
      input.value = "";
      clear.hidden = true;
      dd.hidden = true;
      S.hits = [];
      input.focus();
    });

    document.addEventListener("click", function (e) {
      if (!$("search").contains(e.target)) dd.hidden = true;
    });
  }

  function highlight() {
    var items = $("dropdown").querySelectorAll(".dd-item");
    items.forEach(function (el, i) { el.classList.toggle("is-cursor", i === S.cursor); });
    if (S.cursor >= 0 && items[S.cursor]) items[S.cursor].scrollIntoView({ block: "nearest" });
  }

  function runSearch(q) {
    var seq = ++S.seq;
    fetch(api("/search?q=" + enc(q)))
      .then(function (r) { return r.json(); })
      .then(function (hits) {
        if (seq !== S.seq) return;
        S.hits = hits || [];
        S.cursor = -1;
        renderDropdown();
      })
      .catch(function () {
        if (seq !== S.seq) return;
        $("dropdown").innerHTML = '<div class="dd-hint">检索失败，请稍后重试</div>';
      });
  }

  function renderDropdown() {
    var dd = $("dropdown");
    if (!S.hits.length) {
      dd.innerHTML = '<div class="dd-hint">没有找到相关资源</div>';
      return;
    }
    // 下拉只作快速联想，各组最多 5 条，完整结果去结果页看
    var locals = S.hits.filter(function (h) { return h.local; });
    var remotes = S.hits.filter(function (h) { return !h.local; });
    var shownLocal = locals.slice(0, 5);
    var shownRemote = remotes.slice(0, 5);
    var html = "";
    if (shownLocal.length) {
      html += '<p class="dd-group">本地资源' +
        (locals.length > shownLocal.length ? " · 共 " + locals.length + " 条" : "") + "</p>" +
        shownLocal.map(row).join("");
    }
    if (shownRemote.length) {
      html += '<p class="dd-group">全网资源' +
        (remotes.length > shownRemote.length ? " · 共 " + remotes.length + " 条" : "") + "</p>" +
        shownRemote.map(row).join("");
    }
    html += '<button class="dd-more" type="button">' + icon("i-search") +
      "查看全部 " + S.hits.length + " 条结果</button>";
    dd.innerHTML = html;
    dd.querySelectorAll(".dd-item").forEach(function (el) {
      el.addEventListener("click", function () { pickHit(S.hits[+el.dataset.k]); });
    });
    dd.querySelector(".dd-more").addEventListener("click", function () {
      submitSearch($("q").value);
    });

    function row(h) {
      var k = S.hits.indexOf(h);
      var img = h.image
        ? '<img src="' + esc(h.image) + '" alt="" loading="lazy">'
        : (h.local && h.local.cover
          ? '<img src="' + esc(coverURL(h.local.cover)) + '" alt="" loading="lazy">'
          : icon("i-film"));
      var chips = "";
      if (h.local) {
        var n = (h.local.episodes || []).length;
        chips += '<span class="chip chip-local">本地</span>';
        chips += "<span>" + catName(h.local.category) + (n > 1 ? " · " + n + " 集" : "") + "</span>";
      }
      (h.from || []).forEach(function (f) {
        if (f !== "本地资源") chips += '<span class="chip">' + esc(f) + "</span>";
      });
      var tors = (h.results || []).reduce(function (s, r) { return s + ((r.torrents || []).length); }, 0);
      if (tors) chips += "<span>" + tors + " 个资源</span>";
      return '<div class="dd-item" data-k="' + k + '">' +
        '<div class="dd-thumb">' + img + "</div>" +
        '<div class="dd-body"><p class="dd-title">' + esc(h.title) + "</p>" +
        '<p class="dd-sub">' + chips + "</p></div></div>";
    }
  }

  function pickHit(hit) {
    if (!hit) return;
    $("dropdown").hidden = true;
    openHitDetail(hit);
  }

  /* ---------- 详情 ---------- */
  function openLocalDetail(item) {
    if (!item) return;
    S.detailCtx = { title: item.title, category: item.category, episodes: item.episodes || [], cover: item.cover, results: [] };
    renderDetail();
    show("m-detail");
  }

  function openHitDetail(hit) {
    var local = hit.local;
    S.detailCtx = {
      title: hit.title,
      category: local ? local.category : (hasMikan(hit) ? "series" : "movie"),
      episodes: local ? (local.episodes || []) : [],
      cover: local ? local.cover : null,
      image: hit.image,
      results: hit.results || []
    };
    renderDetail();
    show("m-detail");
  }

  function hasMikan(hit) {
    return (hit.results || []).some(function (r) { return r.site === "mikan"; });
  }

  function renderDetail() {
    var c = S.detailCtx, box = $("detail");
    var poster = c.cover
      ? '<img src="' + esc(coverURL(c.cover)) + '" alt="">'
      : (c.image ? '<img src="' + esc(c.image) + '" alt="">' : icon("i-film"));
    var chips = '<span class="chip chip-brand">' + catName(c.category) + "</span>";
    if (c.episodes.length) chips += '<span class="chip chip-local">已入库</span>';
    (c.results || []).forEach(function (r) {
      chips += '<span class="chip">' + esc(r.siteName || r.site) + "</span>";
    });

    var facts = [];
    if (c.episodes.length) {
      facts.push(c.episodes.length > 1 ? c.episodes.length + " 集" : "单片");
      var total = c.episodes.reduce(function (s, e) { return s + (e.size || 0); }, 0);
      if (total) facts.push(fmtSize(total));
    }
    var srcCount = (c.results || []).length;
    if (srcCount) facts.push(srcCount + " 个来源站");

    var html = '<div class="detail-hero">' +
      '<div class="detail-poster">' + poster + "</div>" +
      '<div class="detail-head"><h2 class="detail-title">' + esc(c.title) + "</h2>" +
      '<div class="detail-chips">' + chips + "</div>" +
      (facts.length ? '<p class="detail-facts"><span>' + facts.map(esc).join("</span><span>") + "</span></p>" : "") +
      "</div></div>";

    // 本地剧集
    if (c.episodes.length) {
      var cacheAll = (hasBridge() && !isOffline())
        ? '<button class="btn btn-ghost btn-sm sec-cache-all" id="cache-all">' + icon("i-download") + "全部缓存</button>"
        : "";
      html += '<section class="sec"><div class="sec-head">' + icon("i-folder") +
        '<span class="sec-title">本地播放</span><span class="sec-count">' + c.episodes.length + " 个文件</span>" + cacheAll + "</div>" +
        '<div class="eps" id="local-eps"></div></section>';
    }

    // 在线来源
    if (srcCount) {
      html += '<section class="sec"><div class="sec-head">' + icon("i-globe") +
        '<span class="sec-title">在线资源</span><span class="sec-count">' + srcCount + " 个来源</span></div>" +
        '<div id="src-list"></div></section>';
    }

    if (!c.episodes.length && !srcCount) {
      html += '<div class="state state-sm">' + icon("i-film", "state-ic") +
        '<p class="state-desc">这个条目暂时没有可用资源</p></div>';
    }

    box.innerHTML = html;

    if (c.episodes.length) renderEpisodes($("local-eps"), c);
    var ca = $("cache-all");
    if (ca) ca.addEventListener("click", function () {
      c.episodes.forEach(function (ep) { cacheEpisode(c, ep); });
      toast("已加入全部 " + c.episodes.length + " 个缓存任务");
    });
    if (srcCount) {
      var list = $("src-list");
      c.results.forEach(function (r) { list.appendChild(buildSource(r, c)); });
    }
  }

  function renderEpisodes(host, c) {
    host.innerHTML = "";
    var canDl = hasBridge() && !isOffline();
    c.episodes.forEach(function (ep, i) {
      var ratio = progRatio(ep.path);
      var watched = ratio >= 0.95;
      var b = document.createElement("button");
      b.className = "ep" + (watched ? " is-watched" : "");
      var right = "";
      if (ep.cached) right += '<span class="ep-cached">' + icon("i-check") + "已缓存</span>";
      else if (canDl) right += '<span class="ep-cache" title="缓存到本机">' + icon("i-download") + "缓存</span>";
      b.innerHTML = icon("i-play") + "<span class=\"ep-name\">" + esc(ep.title || ep.filename) + "</span>" +
        (ep.size ? '<span class="ep-size">' + fmtSize(ep.size) + "</span>" : "") +
        (watched ? '<span class="ep-done">' + icon("i-check") + "</span>" : "") +
        right +
        '<span class="ep-prog"><span class="ep-prog-fill" style="width:' + Math.round(ratio * 100) + '%"></span></span>';
      b.addEventListener("click", function () {
        S.playQueue = c.episodes.slice();
        S.playIndex = i;
        playCurrent(c.title);
      });
      var cb = b.querySelector(".ep-cache");
      if (cb) cb.addEventListener("click", function (e) {
        e.stopPropagation();
        cacheEpisode(c, ep);
        toast("已加入缓存：" + (ep.title || ep.filename));
      });
      host.appendChild(b);
    });
  }

  // 调原生下载把某一集缓存到本机（App 专属，浏览器里不会出现入口）
  // 同时把封面地址传过去，原生下载完视频会一并存封面，缓存页/离线首页才能显示海报
  function cacheEpisode(c, ep) {
    var absUrl = location.origin + api("/stream?path=" + enc(ep.path));
    var cov = c.cover ? (location.origin + coverURL(c.cover)) : "";
    var epName = ep.title || ep.filename || c.title;   // "第1集" 这类集标题，离线要靠它显示集数
    bcall(function (b) { return b.downloadVideo(absUrl, c.title, c.title, ep.path, String(ep.size || 0), cov, c.category || "movie", epName); });
  }

  /* ---------- 来源与磁力 ---------- */
  function buildSource(src, ctx) {
    var wrap = document.createElement("div");
    wrap.className = "src";
    var tors = src.torrents || [];
    wrap.innerHTML = '<div class="src-head">' +
      '<span class="src-name">' + esc(src.siteName || src.site) + "</span>" +
      (src.url && /^https?:/.test(src.url)
        ? '<a class="src-link" href="' + esc(src.url) + '" target="_blank" rel="noopener">原站页面' + icon("i-external") + "</a>"
        : "") +
      '<div class="src-actions">' +
      (tors.length > 1 ? '<button class="btn btn-ghost btn-sm" data-act="test-all">' + icon("i-gauge") + "全部测速</button>" : "") +
      "</div></div>" +
      '<div class="src-body"></div>';

    var body = wrap.querySelector(".src-body");
    if (!tors.length) {
      body.innerHTML = '<p class="src-empty">该来源未解析到磁力，可点击上方原站页面查看</p>';
      // 尝试异步补取详情页磁力
      if (src.url && /^https?:/.test(src.url)) {
        fetch(api("/detail?site=" + enc(src.site) + "&url=" + enc(src.url)))
          .then(function (r) { return r.json(); })
          .then(function (d) {
            if (d && d.torrents && d.torrents.length) {
              src.torrents = d.torrents;
              body.innerHTML = "";
              d.torrents.forEach(function (t) { body.appendChild(buildTorrent(t, src, ctx)); });
            }
          })
          .catch(function () {});
      }
    } else {
      tors.forEach(function (t) { body.appendChild(buildTorrent(t, src, ctx)); });
    }

    var all = wrap.querySelector('[data-act="test-all"]');
    if (all) {
      all.addEventListener("click", function () {
        all.disabled = true;
        all.classList.add("btn-loading");
        var rows = Array.prototype.slice.call(body.querySelectorAll(".tor"));
        var i = 0;
        (function next() {
          if (i >= rows.length) {
            all.disabled = false;
            all.classList.remove("btn-loading");
            markFastest(body);
            return;
          }
          var btn = rows[i++].querySelector('[data-act="test"]');
          if (!btn) return next();
          btn.click();
          var wait = setInterval(function () {
            if (!btn.classList.contains("btn-loading")) { clearInterval(wait); next(); }
          }, 400);
        })();
      });
    }
    return wrap;
  }

  function buildTorrent(t, src, ctx) {
    var row = document.createElement("div");
    row.className = "tor";
    var label = t.name || "磁力资源";
    row.innerHTML = '<div class="tor-main"><p class="tor-name">' + esc(label) + "</p>" +
      '<p class="tor-facts">' + (t.size ? "<span>" + esc(t.size) + "</span>" : "") +
      '<span class="tor-speed" data-speed></span></p></div>' +
      '<div class="tor-acts">' +
      '<button class="btn btn-ghost btn-sm" data-act="test" title="测速">' + icon("i-gauge") + "测速</button>" +
      '<button class="btn btn-ghost btn-sm" data-act="copy" title="复制磁力">' + icon("i-copy") + "</button>" +
      '<button class="btn btn-primary btn-sm" data-act="get">' + icon("i-download") + "下载</button>" +
      "</div>";

    var speedEl = row.querySelector("[data-speed]");
    var testBtn = row.querySelector('[data-act="test"]');
    var getBtn = row.querySelector('[data-act="get"]');

    testBtn.addEventListener("click", function () {
      testBtn.classList.add("btn-loading");
      speedEl.textContent = "";
      speedEl.classList.remove("is-bad");
      fetch(api("/speedtest"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ magnet: t.magnet })
      }).then(function (r) { return r.json(); }).then(function (res) {
        testBtn.classList.remove("btn-loading");
        if (res.error) {
          speedEl.textContent = res.error;
          speedEl.classList.add("is-bad");
          row.dataset.mbps = "-1";
        } else {
          var mb = res.mbps || 0;
          speedEl.textContent = mb.toFixed(2) + " MB/s · " + (res.peers || 0) + " 个节点" + (res.size ? " · " + res.size : "");
          row.dataset.mbps = String(mb);
        }
      }).catch(function () {
        testBtn.classList.remove("btn-loading");
        speedEl.textContent = "测速请求失败";
        speedEl.classList.add("is-bad");
      });
    });

    row.querySelector('[data-act="copy"]').addEventListener("click", function () {
      copyText(t.magnet);
    });

    getBtn.addEventListener("click", function () {
      var cat = src.site === "mikan" ? "series" : (ctx.category || "movie");
      var name = window.prompt("保存名称（会创建同名文件夹）", ctx.title);
      if (!name) return;
      name = name.trim();
      if (!name) return;
      if (!window.confirm("确认下载？\n\n" + label + "\n\n保存到：" + catName(cat) + " / " + name + "/")) return;
      getBtn.classList.add("btn-loading");
      fetch(api("/download"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ magnet: t.magnet, title: name, category: cat })
      }).then(function (r) { return r.json(); }).then(function (task) {
        getBtn.classList.remove("btn-loading");
        if (!task || !task.id) { toast("提交失败"); return; }
        toast("已加入下载队列");
        openTasks();
      }).catch(function () {
        getBtn.classList.remove("btn-loading");
        toast("下载请求失败");
      });
    });

    return row;
  }

  function markFastest(body) {
    var rows = Array.prototype.slice.call(body.querySelectorAll(".tor"));
    var best = null, bestV = 0;
    rows.forEach(function (r) {
      var v = parseFloat(r.dataset.mbps || "0");
      if (v > bestV) { bestV = v; best = r; }
    });
    if (best && bestV > 0) {
      var el = best.querySelector("[data-speed]");
      if (el && el.textContent.indexOf("最快") < 0) el.textContent += " · 最快";
      best.querySelector('[data-act="get"]').classList.remove("btn-ghost");
    }
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(function () { toast("磁力链接已复制"); },
        function () { fallbackCopy(text); });
    } else fallbackCopy(text);
  }
  function fallbackCopy(text) {
    var ta = document.createElement("textarea");
    ta.value = text;
    ta.style.cssText = "position:fixed;opacity:0";
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand("copy"); toast("磁力链接已复制"); }
    catch (e) { toast("复制失败，请手动选取"); }
    document.body.removeChild(ta);
  }

  /* ---------- 播放器 ---------- */
  var P = {
    rate: 1, fit: "contain", vol: 1, bri: 100, con: 100, sat: 100, sharp: 0,
    seek: 10, loop: false, auto: true, resume: true, mirror: false
  };

  function loadPrefs() {
    try {
      var saved = JSON.parse(localStorage.getItem("player-prefs") || "{}");
      Object.keys(P).forEach(function (k) { if (k in saved) P[k] = saved[k]; });
    } catch (e) {}
  }
  function savePrefs() {
    try { localStorage.setItem("player-prefs", JSON.stringify(P)); } catch (e) {}
  }

  function applyPrefs() {
    var v = $("video");
    v.playbackRate = P.rate;
    v.volume = P.vol;
    v.loop = P.loop;
    v.style.objectFit = P.fit;
    // 锐度用 contrast 微调模拟（浏览器无原生锐化滤镜）
    var f = "brightness(" + P.bri / 100 + ") contrast(" + (P.con + P.sharp * 0.25) / 100 +
      ") saturate(" + P.sat / 100 + ")";
    v.style.filter = f;
    v.style.transform = P.mirror ? "scaleX(-1)" : "";
    // 同步控件
    $("s-rate").value = String(P.rate);
    $("s-fit").value = P.fit;
    $("s-vol").value = Math.round(P.vol * 100);
    $("s-bri").value = P.bri;
    $("s-con").value = P.con;
    $("s-sat").value = P.sat;
    $("s-sharp").value = P.sharp;
    $("s-seek").value = P.seek;
    $("s-loop").checked = P.loop;
    $("s-auto").checked = P.auto;
    $("s-resume").checked = P.resume;
    $("s-mirror").checked = P.mirror;
    $("v-vol").textContent = Math.round(P.vol * 100) + "%";
    $("v-bri").textContent = P.bri + "%";
    $("v-con").textContent = P.con + "%";
    $("v-sat").textContent = P.sat + "%";
    $("v-sharp").textContent = P.sharp;
    $("v-seek").textContent = P.seek + "s";
  }

  function playCurrent(albumTitle) {
    var ep = S.playQueue[S.playIndex];
    if (!ep) return;
    var v = $("video");
    S.playKey = "pos:" + ep.path;
    S.playPath = ep.path;
    $("pl-title").textContent = albumTitle || ep.filename;
    $("pl-sub").textContent = (ep.title ? ep.title + " · " : "") + ep.filename;
    v.src = api("/stream?path=" + enc(ep.path));
    applyPrefs();
    if (P.resume) {
      var pr = getProg(ep.path);
      // 没看完（<95%）才续播；看完的从头再播
      if (pr && pr.dur && pr.pos > 5 && pr.pos / pr.dur < 0.95) v.currentTime = pr.pos;
    }
    v.play().catch(function () {});
    renderPlayerEps(albumTitle);
    show("m-player");
    acquireWakeLock();
    // 移动端：点开播放自动全屏并尝试横屏（真全屏、完整分辨率）
    if (window.innerWidth <= 760) {
      var stage = $("m-player");
      if (stage.requestFullscreen) stage.requestFullscreen().catch(function () {});
      lockLandscape();
    }
  }

  function renderPlayerEps(albumTitle) {
    var bar = $("pl-eps");
    if (S.playQueue.length < 2) { bar.hidden = true; return; }
    bar.hidden = false;
    bar.innerHTML = "";
    S.playQueue.forEach(function (ep, i) {
      var b = document.createElement("button");
      b.className = "ep" + (i === S.playIndex ? " is-playing" : "");
      b.textContent = ep.title || ep.filename;
      b.addEventListener("click", function () {
        S.playIndex = i;
        playCurrent(albumTitle);
      });
      bar.appendChild(b);
    });
    var cur = bar.children[S.playIndex];
    if (cur) cur.scrollIntoView({ block: "nearest", inline: "center" });
  }

  function bindPlayer() {
    var v = $("video");

    // 设置面板
    $("btn-settings").addEventListener("click", function () {
      $("pl-settings").hidden = !$("pl-settings").hidden;
    });

    function on(id, ev, fn) { $(id).addEventListener(ev, fn); }
    on("s-rate", "change", function () { P.rate = parseFloat(this.value); v.playbackRate = P.rate; savePrefs(); });
    on("s-fit", "change", function () { P.fit = this.value; v.style.objectFit = P.fit; savePrefs(); });
    on("s-vol", "input", function () { P.vol = this.value / 100; v.volume = P.vol; $("v-vol").textContent = this.value + "%"; savePrefs(); });
    ["bri", "con", "sat", "sharp"].forEach(function (k) {
      on("s-" + k, "input", function () {
        P[k] = +this.value;
        $("v-" + k).textContent = k === "sharp" ? this.value : this.value + "%";
        applyFilter();
        savePrefs();
      });
    });
    on("s-seek", "input", function () { P.seek = +this.value; $("v-seek").textContent = this.value + "s"; savePrefs(); });
    on("s-loop", "change", function () { P.loop = this.checked; v.loop = P.loop; savePrefs(); });
    on("s-auto", "change", function () { P.auto = this.checked; savePrefs(); });
    on("s-resume", "change", function () { P.resume = this.checked; savePrefs(); });
    on("s-mirror", "change", function () { P.mirror = this.checked; v.style.transform = P.mirror ? "scaleX(-1)" : ""; savePrefs(); });
    on("s-reset", "click", function () {
      P = { rate: 1, fit: "contain", vol: 1, bri: 100, con: 100, sat: 100, sharp: 0, seek: 10, loop: false, auto: true, resume: true, mirror: false };
      applyPrefs();
      savePrefs();
      toast("已恢复默认设置");
    });

    function applyFilter() {
      // 与 applyPrefs 保持一致：锐度用 contrast 微调模拟
      v.style.filter = "brightness(" + P.bri / 100 + ") contrast(" +
        (P.con + P.sharp * 0.25) / 100 + ") saturate(" + P.sat / 100 + ")";
    }

    $("btn-pip").addEventListener("click", function () {
      if (!document.pictureInPictureEnabled) { toast("当前浏览器不支持画中画"); return; }
      if (document.pictureInPictureElement) document.exitPictureInPicture();
      else v.requestPictureInPicture().catch(function () { toast("无法进入画中画"); });
    });
    $("btn-full").addEventListener("click", toggleFull);

    // 进度记忆
    var lastSave = 0;
    v.addEventListener("timeupdate", function () {
      if (!P.resume || !S.playKey) return;
      var now = Date.now();
      if (now - lastSave > 4000) {
        lastSave = now;
        setProg(S.playPath, v.currentTime, v.duration);
      }
    });
    v.addEventListener("ended", function () {
      if (S.playPath) { setProg(S.playPath, v.duration, v.duration); } // 看完记满进度（详情页打勾用），不再删除
      if (v.loop) return;
      if (P.auto && S.playIndex < S.playQueue.length - 1) {
        S.playIndex++;
        playCurrent($("pl-title").textContent);
      }
    });
    v.addEventListener("error", function () {
      if (v.src) toast("视频无法播放，可能是编码不受支持");
    });

    // 快捷键
    document.addEventListener("keydown", function (e) {
      if ($("m-player").hidden) return;
      var tag = (e.target.tagName || "").toLowerCase();
      if (tag === "input" || tag === "select" || tag === "textarea") return;
      switch (e.key) {
        case " ": e.preventDefault(); v.paused ? v.play() : v.pause(); break;
        case "ArrowLeft": e.preventDefault(); v.currentTime = Math.max(0, v.currentTime - P.seek); break;
        case "ArrowRight": e.preventDefault(); v.currentTime += P.seek; break;
        case "ArrowUp": e.preventDefault(); setVol(Math.min(1, v.volume + .05)); break;
        case "ArrowDown": e.preventDefault(); setVol(Math.max(0, v.volume - .05)); break;
        case "f": case "F": toggleFull(); break;
        case "m": case "M": v.muted = !v.muted; toast(v.muted ? "已静音" : "已取消静音"); break;
        case "[": stepRate(-1); break;
        case "]": stepRate(1); break;
      }
    });
    function setVol(x) {
      v.volume = x; P.vol = x;
      $("s-vol").value = Math.round(x * 100);
      $("v-vol").textContent = Math.round(x * 100) + "%";
      savePrefs();
    }
    function stepRate(dir) {
      var opts = [0.5, 0.75, 1, 1.25, 1.5, 2, 3];
      var i = opts.indexOf(P.rate);
      if (i < 0) i = 2;
      i = Math.min(opts.length - 1, Math.max(0, i + dir));
      P.rate = opts[i];
      v.playbackRate = P.rate;
      $("s-rate").value = String(P.rate);
      savePrefs();
      toast("播放速度 " + P.rate + "×");
    }
  }

  function toggleFull() {
    // 视频全屏优先用原生元素全屏，兼容性最好（Safari/旧内核也支持）
    var v = $("video");
    if (document.fullscreenElement || document.webkitFullscreenElement) {
      (document.exitFullscreen || document.webkitExitFullscreen || function () {}).call(document);
      return;
    }
    var req = v.requestFullscreen || v.webkitRequestFullscreen || v.webkitEnterFullscreen;
    if (req) req.call(v);
    else toast("当前浏览器不支持全屏");
  }

  /* ---------- 管理页 ---------- */
  function showMng(pushHistory) {
    S.view = "mng";
    setViewVisible("mng");
    window.scrollTo(0, 0);
    if (pushHistory !== false) {
      try { history.pushState({ view: "mng" }, "", "#manage"); } catch (e) {}
    }
    loadMng();
  }

  /* ---------- 缓存 / 我的（底部导航新视图，App 专属，浏览器降级） ---------- */
  function showCache(pushHistory) {
    S.view = "cache";
    setViewVisible("cache");
    window.scrollTo(0, 0);
    if (pushHistory !== false) { try { history.pushState({ view: "cache" }, "", "#cache"); } catch (e) {} }
    loadCacheView();
    startCacheTimer();
  }

  // 缓存页有下载中的任务时，每秒刷新一次进度；全下完就停，不空转
  var cacheTimer = null;
  function startCacheTimer() {
    stopCacheTimer();
    cacheTimer = setInterval(function () {
      if (S.view !== "cache") { stopCacheTimer(); return; }
      loadCacheView();
    }, 1000);
  }
  function stopCacheTimer() {
    if (cacheTimer) { clearInterval(cacheTimer); cacheTimer = null; }
  }
  function showMe(pushHistory) {
    S.view = "me";
    setViewVisible("me");
    window.scrollTo(0, 0);
    if (pushHistory !== false) { try { history.pushState({ view: "me" }, "", "#me"); } catch (e) {} }
    loadMeView();
  }

  function loadCacheView() {
    var listEl = $("cachev-list"), metaEl = $("cachev-meta"), hintEl = $("cachev-hint");
    if (!listEl) return;
    if (!hasBridge()) {
      if (hintEl) { hintEl.hidden = false; hintEl.textContent = "离线缓存是 App 专属功能，请在安卓 App 中使用本页。"; }
      listEl.innerHTML = "";
      if (metaEl) metaEl.textContent = "";
      return;
    }
    if (hintEl) hintEl.hidden = true;
    var items = bjson(function (b) { return b.listCached(); }, []) || [];
    var size = bjson(function (b) { return b.getCacheSize(); }, null);
    if (metaEl) metaEl.textContent = size ? ("已缓存 " + (size.count != null ? size.count : items.length) + " 个 · 占用 " + fmtSize(size.total || 0)) : ("已缓存 " + items.length + " 个");
    if (!items.length) {
      listEl.innerHTML = '<div class="state state-sm">' + icon("i-download", "state-ic") +
        '<p class="state-desc">还没有缓存内容</p><p class="state-desc">在任意一集上点"缓存"，没网也能看</p></div>';
      return;
    }
    listEl.innerHTML = items.map(function (c, i) {
      var done = c.done !== false;
      // speed>0 才是真在下载；speed=0 的未完成项是进程死后留下的暂停态
      var running = !done && (c.speed || 0) > 0;
      var prog = done ? 100 : Math.round((c.progress || 0) * 100);
      var covUrl = bcall(function (b) { return b.coverUrl(c.epId); }, "");
      var poster = covUrl
        ? '<img class="cc-poster" src="' + esc(covUrl) + '" alt="" loading="lazy">'
        : '<div class="cc-ic">' + icon(done ? "i-film" : "i-download") + "</div>";
      var sub = esc(c.epTitle || "") + (c.size ? " · " + fmtSize(c.size) : "");
      if (!done) sub += running
        ? " · 下载中 " + prog + "% · " + fmtSize(c.speed) + "/s"
        : " · 已暂停 " + prog + "%";
      return '<div class="cc-item" data-i="' + i + '">' +
        poster +
        '<div class="cc-body"><p class="cc-title">' + esc(c.title || "") + "</p>" +
        '<p class="cc-sub">' + sub + "</p>" +
        (done ? "" : '<div class="cc-prog"><span class="cc-prog-fill' + (running ? "" : " is-paused") + '" style="width:' + prog + '%"></span></div>') +
        "</div>" +
        '<div class="cc-act">' +
        (done ? '<button class="btn btn-primary btn-sm" data-act="play">播放</button>' : "") +
        (!done && !running ? '<button class="btn btn-primary btn-sm" data-act="resume">继续</button>' : "") +
        '<button class="btn btn-ghost btn-sm danger" data-act="del">删除</button>' +
        "</div></div>";
    }).join("");
    // 只有真在下载才每秒轮询；全是暂停/完成就停掉，不空转耗电
    if (!items.some(function (c) { return c.done === false && (c.speed || 0) > 0; })) stopCacheTimer();
    listEl.querySelectorAll(".cc-item").forEach(function (el) {
      var c = items[+el.dataset.i];
      el.querySelector('[data-act="del"]').addEventListener("click", function () {
        bcall(function (b) { return b.deleteCached(c.epId); });
        toast("已删除缓存"); loadCacheView();
      });
      var pb = el.querySelector('[data-act="play"]');
      if (pb) pb.addEventListener("click", function () {
        var url = bcall(function (b) { return b.playLocalUrl(c.epId); }, null);
        if (url) playCachedUrl(c, url); else toast("无法播放该缓存");
      });
      var rb = el.querySelector('[data-act="resume"]');
      if (rb) rb.addEventListener("click", function () { resumeCache(c); });
    });
  }

  // 继续下载暂停的缓存。旧数据没存下载地址，用当前服务器地址按 path 重拼；
  // 断点续传会认出已下载的部分，接着下而不是重来。
  function resumeCache(c) {
    if (isOffline()) { toast("离线状态无法继续下载"); return; }
    var url = location.origin + api("/stream?path=" + enc(c.path || c.epId));
    bcall(function (b) { return b.downloadVideo(url, c.title, c.itemId, c.epId, String(c.size || 0), "", c.category || "movie", c.epTitle || c.title); });
    toast("已继续下载：" + (c.epTitle || c.title));
    startCacheTimer();
  }

  // 播放本地缓存：走壳提供的本地 URL（离线也能播）
  function playCachedUrl(c, url) {
    var v = $("video");
    S.playQueue = [{ path: c.epId, title: c.epTitle, filename: c.epTitle || c.title, localUrl: url }];
    S.playIndex = 0;
    S.playKey = "pos:" + c.epId;
    S.playPath = c.epId;
    $("pl-title").textContent = c.title || c.epTitle || "";
    $("pl-sub").textContent = c.epTitle || "";
    v.src = url;
    applyPrefs();
    if (P.resume) {
      var pr = getProg(c.epId);
      if (pr && pr.dur && pr.pos > 5 && pr.pos / pr.dur < 0.95) v.currentTime = pr.pos;
    }
    v.play().catch(function () {});
    renderPlayerEps(c.title);
    show("m-player");
    acquireWakeLock();
    // 移动端：点开播放自动全屏并尝试横屏（与在线播放行为一致）
    if (window.innerWidth <= 760) {
      var stage = $("m-player");
      if (stage.requestFullscreen) stage.requestFullscreen().catch(function () {});
      lockLandscape();
    }
  }

  function loadMeView() {
    var verEl = $("me-version"), addrEl = $("me-addr"), cacheEl = $("me-cache"), netEl = $("me-net");
    if (netEl) netEl.textContent = hasBridge() ? (isOffline() ? "离线 · 仅显示已缓存" : "在线") : "浏览器访问";
    if (addrEl) addrEl.value = bcall(function (b) { return b.getServerAddr(); }, location.origin) || location.origin;
    var ver = bjson(function (b) { return b.getVersion(); }, null);
    if (verEl) verEl.textContent = ver ? ("App " + (ver.app || "?") + " · 界面 " + (ver.web || "?")) : "网页版";
    var size = bjson(function (b) { return b.getCacheSize(); }, null);
    if (cacheEl) cacheEl.textContent = size ? fmtSize(size.total || 0) : "—";
    renderBgStatus();
  }

  /* 后台缓存体检：锁屏/切后台想继续下载，要通知权限 + 电池白名单两样都放行。
     两项都正常就不占地方，有问题才显示可点的修复入口。 */
  function renderBgStatus() {
    var box = $("me-bg");
    if (!box) return;
    if (!hasBridge()) { box.hidden = true; return; }
    var st = bjson(function (b) { return b.getBackgroundStatus(); }, null);
    if (!st) { box.hidden = true; return; }
    var rows = [];
    if (!st.battery) {
      rows.push({ act: "battery", title: "允许后台运行", desc: "系统省电策略会中断锁屏后的缓存，点此放行" });
    }
    if (!st.notify) {
      rows.push({ act: "notify", title: "开启通知权限", desc: "缓存进度需要通知栏显示，关闭后看不到进度" });
    }
    if (!rows.length) {
      box.hidden = false;
      box.innerHTML = '<div class="bg-ok">' + icon("i-check") +
        "<span>后台缓存已就绪，锁屏和切换应用都会继续下载</span></div>";
      return;
    }
    box.hidden = false;
    box.innerHTML = '<p class="me-label">后台缓存待处理</p>' + rows.map(function (r) {
      return '<button class="bg-row" type="button" data-act="' + r.act + '">' +
        '<span class="bg-dot"></span>' +
        '<span class="bg-body"><span class="bg-title">' + esc(r.title) + "</span>" +
        '<span class="bg-desc">' + esc(r.desc) + "</span></span>" +
        icon("i-back", "ic-flip") + "</button>";
    }).join("");
    box.querySelectorAll(".bg-row").forEach(function (el) {
      el.addEventListener("click", function () {
        if (el.dataset.act === "battery") bcall(function (b) { return b.requestBatteryWhitelist(); });
        else bcall(function (b) { return b.openAppSettings(); });
      });
    });
  }

  function bindMe() {
    var save = $("me-addr-save");
    if (save) save.addEventListener("click", function () {
      var addr = $("me-addr").value.trim();
      if (!addr) { toast("请输入服务器地址"); return; }
      bcall(function (b) { return b.setServerAddr(addr); });
      toast("已保存服务器地址");
    });
    // 从系统设置页回来时刷新体检结果
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && S.view === "me") renderBgStatus();
    });
  }

  function loadMng() {
    fetch(api("/manage/stats"))
      .then(function (r) { return r.json(); })
      .then(function (d) {
        S.mngItems = d.items || [];
        var cats = d.categories || [];
        var byId = {};
        cats.forEach(function (c) { byId[c.id] = c.count; });
        $("mng-stats").innerHTML =
          stat("资源总数", byId.all || 0, "部") +
          stat("电影电视剧", byId.movie || 0, "部") +
          stat("番剧", byId.series || 0, "部") +
          stat("磁盘占用", d.totalSizeH || "0", "") +
          stat("缺少封面", d.missingCover || 0, "部", (d.missingCover || 0) > 0) +
          stat("任务记录", d.taskCount || 0, "条");
        $("mng-meta").textContent = (byId.all || 0) + " 部资源 · 共占用 " + (d.totalSizeH || "0");
        applyMngCat();  // 统一走这里：停在"文件管理"tab 时也能正确恢复视图
      })
      .catch(function () { toast("加载管理数据失败"); });

    loadCache();

    function stat(label, value, unit, warn) {
      return '<div class="stat' + (warn ? " stat-warn" : "") + '">' +
        '<div class="stat-label">' + esc(label) + "</div>" +
        '<div class="stat-value">' + esc(String(value)) +
        (unit ? "<small>" + esc(unit) + "</small>" : "") + "</div></div>";
    }
  }

  function renderMngTable() {
    var kw = ($("mng-filter").value || "").trim().toLowerCase();
    var list = (S.mngItems || []).filter(function (it) {
      if (S.mngCat !== "all" && it.category !== S.mngCat) return false;
      if (!kw) return true;
      return (it.title || "").toLowerCase().indexOf(kw) >= 0 ||
        (it.rawTitle || "").toLowerCase().indexOf(kw) >= 0;
    });
    var body = $("mng-body");
    $("mng-empty").hidden = list.length > 0;
    body.innerHTML = list.map(function (it) {
      // 有封面显示绿色图标，缺封面显示灰色胶片图标（便于一眼扫出哪些要补图）
      var coverCell = it.hasCover
        ? '<div class="t-cover-none" style="background:var(--ok-wash);color:var(--ok)" title="已有封面">' + icon("i-image") + "</div>"
        : '<div class="t-cover-none" title="缺少封面">' + icon("i-film") + "</div>";
      return '<tr data-id="' + esc(it.id) + '">' +
        "<td>" + coverCell + "</td>" +
        '<td><div class="t-name">' + esc(it.title) + "</div>" +
        (it.rawTitle && it.rawTitle !== it.title ? '<div class="t-raw">' + esc(it.rawTitle) + "</div>" : "") +
        "</td>" +
        "<td>" + catName(it.category) + "</td>" +
        '<td class="num">' + (it.episodes || 0) + "</td>" +
        '<td class="num">' + esc(it.sizeHuman || "") + "</td>" +
        '<td class="act"><span class="t-acts">' +
        '<button class="icon-btn" data-act="edit" title="编辑">' + icon("i-edit") + "</button>" +
        '<button class="icon-btn danger" data-act="del" title="删除">' + icon("i-trash") + "</button>" +
        "</span></td></tr>";
    }).join("");

    body.querySelectorAll("tr").forEach(function (tr) {
      var it = findMngItem(tr.dataset.id);
      tr.querySelector('[data-act="edit"]').addEventListener("click", function () { openEdit(it); });
      tr.querySelector('[data-act="del"]').addEventListener("click", function () { openDelete(it); });
    });
  }

  function findMngItem(id) {
    return (S.mngItems || []).filter(function (x) { return x.id === id; })[0];
  }

  /* ---------- 下载缓存 ---------- */
  function loadCache() {
    fetch(api("/manage/cache"))
      .then(function (r) { return r.json(); })
      .then(function (d) {
        var list = d.entries || [];
        S.cacheEntries = list;
        $("cache-empty").hidden = list.length > 0;
        $("cache-clear").disabled = !list.length;
        $("cache-meta").textContent = list.length
          ? list.length + " 项 · 共占用 " + (d.totalHuman || "0") + " · " + (d.dir || "")
          : "缓存目录：" + (d.dir || "");
        $("cache-list").innerHTML = list.map(cacheRow).join("");
        $("cache-list").querySelectorAll('[data-act="cache-del"]').forEach(function (b) {
          b.addEventListener("click", function () { delCache(b.dataset.name); });
        });
      })
      .catch(function () {
        $("cache-meta").textContent = "无法读取缓存信息";
      });
  }

  function cacheRow(e) {
    var tags = "";
    if (e.inUse) tags += '<span class="chip chip-brand">正在下载</span>';
    else if (e.partial) tags += '<span class="chip">未下载完</span>';
    var sub = [e.sizeHuman];
    if (e.modified) sub.push(fmtTime(e.modified));
    if (e.inUse && e.taskName) sub.push("任务：" + e.taskName);
    return '<div class="cache-item">' +
      '<div class="cache-ic">' + icon("i-folder") + "</div>" +
      '<div class="cache-body">' +
      '<p class="cache-name">' + esc(e.title || e.name) + "</p>" +
      '<p class="cache-sub">' + esc(sub.join(" · ")) + "</p>" +
      (tags ? '<div class="cache-tags">' + tags + "</div>" : "") +
      "</div>" +
      '<button class="icon-btn danger" data-act="cache-del" data-name="' + esc(e.name) + '"' +
      (e.inUse ? " disabled title=\"正在下载，请先停止任务\"" : ' title="删除这份缓存"') + ">" +
      icon("i-trash") + "</button></div>";
  }

  function delCache(name) {
    var e = (S.cacheEntries || []).filter(function (x) { return x.name === name; })[0];
    if (!e) return;
    if (!window.confirm("删除这份缓存？\n\n" + (e.title || e.name) + "\n占用 " + e.sizeHuman +
      "\n\n已下载的进度会一并丢失，需要重新下载。")) return;
    postJSON("/manage/cache/delete", { name: name })
      .then(function (d) {
        toast("已释放 " + (d.freedHuman || "0"));
        loadCache();
      })
      .catch(function (err) { toast(String(err.message || "删除失败")); });
  }

  function clearAllCache() {
    var list = S.cacheEntries || [];
    if (!list.length) return;
    var busy = list.filter(function (x) { return x.inUse; }).length;
    var msg = "清空全部下载缓存？\n\n共 " + list.length + " 项";
    if (busy) msg += "（其中 " + busy + " 项正在下载，会自动跳过）";
    msg += "\n\n所有未完成的下载进度都会丢失。";
    if (!window.confirm(msg)) return;
    postJSON("/manage/cache/delete", { all: true })
      .then(function (d) {
        toast("已清理 " + (d.removed || "") + "，释放 " + (d.freedHuman || "0"));
        loadCache();
      })
      .catch(function (err) { toast(String(err.message || "清理失败")); });
  }

  function bindManage() {
    $("btn-manage").addEventListener("click", function () { showMng(); });
    $("mng-back").addEventListener("click", function () { showLib(); });
    $("mng-filter").addEventListener("input", renderMngTable);
    $("mng-tabs").addEventListener("click", function (e) {
      var t = e.target.closest(".tab");
      if (!t) return;
      S.mngCat = t.dataset.cat;
      $("mng-tabs").querySelectorAll(".tab").forEach(function (x) {
        x.classList.toggle("is-active", x === t);
      });
      applyMngCat();
    });
    $("mng-covers").addEventListener("click", startCoverJob);
    $("cache-clear").addEventListener("click", clearAllCache);
    $("mng-clear-tasks").addEventListener("click", function () {
      fetch(api("/manage/clear-tasks"), { method: "POST" })
        .then(function (r) { return r.json(); })
        .then(function (d) {
          toast("已清理 " + (d.cleared || 0) + " 条任务记录");
          loadMng();
          refreshTasks();
        })
        .catch(function () { toast("清理失败"); });
    });

    // 编辑
    $("ed-cover-pick").addEventListener("click", function (e) {
      if (e.target.id !== "ed-cover-file") $("ed-cover-file").click();
    });
    $("ed-cover-file").addEventListener("change", function () {
      var f = this.files && this.files[0];
      if (!f) return;
      if (f.size > 10 * 1024 * 1024) { edErr("图片过大（上限 10MB）"); return; }
      var fr = new FileReader();
      fr.onload = function () {
        S.edCover = String(fr.result);
        $("ed-cover-preview").src = S.edCover;
        $("ed-cover-preview").hidden = false;
        $("ed-cover-ph").hidden = true;
        $("ed-cover-url").value = "";
      };
      fr.readAsDataURL(f);
    });
    $("ed-cover-url").addEventListener("input", function () {
      if (this.value.trim()) {
        S.edCover = null;
        $("ed-cover-preview").hidden = true;
        $("ed-cover-ph").hidden = false;
      }
    });
    $("ed-save").addEventListener("click", saveEdit);

    // 删除
    $("del-ok").addEventListener("click", confirmDelete);
  }

  function edErr(m) { var e = $("ed-err"); e.textContent = m || ""; e.hidden = !m; }

  function openEdit(it) {
    if (!it) return;
    S.edItem = it;
    S.edCover = null;
    $("ed-target").textContent = catName(it.category) + " · " + (it.episodes || 0) + " 个文件 · " + (it.sizeHuman || "");
    $("ed-name").value = it.title;
    $("ed-cover-url").value = "";
    $("ed-cover-file").value = "";
    $("ed-cover-preview").hidden = true;
    $("ed-cover-preview").removeAttribute("src");
    $("ed-cover-ph").hidden = false;
    edErr("");
    show("m-edit");
  }

  function saveEdit() {
    var it = S.edItem;
    if (!it) return;
    var btn = $("ed-save");
    var name = $("ed-name").value.trim();
    var url = $("ed-cover-url").value.trim();
    var cover = S.edCover || url;
    if (!name) { edErr("名称不能为空"); return; }

    btn.classList.add("btn-loading");
    btn.disabled = true;
    edErr("");

    var jobs = [];
    if (name !== it.title) {
      jobs.push(postJSON("/manage/rename", { id: it.id, name: name }));
    }
    if (cover) {
      jobs.push(postJSON("/manage/cover", { id: it.id, cover: cover }));
    }
    if (!jobs.length) { btn.classList.remove("btn-loading"); btn.disabled = false; hide("m-edit"); return; }

    Promise.all(jobs).then(function () {
      btn.classList.remove("btn-loading");
      btn.disabled = false;
      hide("m-edit");
      toast("已保存");
      setTimeout(function () { loadMng(); loadLibrary(); }, 800);
    }).catch(function (err) {
      btn.classList.remove("btn-loading");
      btn.disabled = false;
      edErr(String(err.message || err).slice(0, 160));
    });
  }

  function postJSON(path, body) {
    return fetch(api(path), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }).then(function (r) {
      return r.text().then(function (t) {
        if (!r.ok) throw new Error(t || ("HTTP " + r.status));
        return t ? JSON.parse(t) : {};
      });
    });
  }

  function openDelete(it) {
    if (!it) return;
    S.delItem = it;
    $("del-text").innerHTML = "将从硬盘删除 <b>" + esc(it.title) + "</b>（" +
      (it.episodes || 0) + " 个文件，" + esc(it.sizeHuman || "") + "）。<br>此操作不可撤销。";
    $("del-force").checked = false;
    $("del-force-wrap").hidden = true;
    var e = $("del-err"); e.hidden = true; e.textContent = "";
    show("m-del");
  }

  function confirmDelete() {
    var it = S.delItem;
    if (!it) return;
    var btn = $("del-ok");
    btn.classList.add("btn-loading");
    btn.disabled = true;
    postJSON("/manage/delete", { id: it.id, force: $("del-force").checked })
      .then(function (res) {
        btn.classList.remove("btn-loading");
        btn.disabled = false;
        hide("m-del");
        toast("已删除，释放 " + (res.freedHuman || ""));
        setTimeout(function () { loadMng(); loadLibrary(); }, 800);
      })
      .catch(function (err) {
        btn.classList.remove("btn-loading");
        btn.disabled = false;
        var msg = String(err.message || err);
        var e = $("del-err");
        e.textContent = msg.slice(0, 200);
        e.hidden = false;
        // 后端提示需要强制删除时，把选项显出来
        if (msg.indexOf("强制删除") >= 0) $("del-force-wrap").hidden = false;
      });
  }

  /* ---------- 封面补全 ---------- */
  function startCoverJob() {
    var b = $("btn-covers");
    b.disabled = true;
    fetch(api("/covers/fetch"), { method: "POST" })
      .then(function (r) {
        return r.text().then(function (t) {
          if (!r.ok) throw new Error(t);
          return t;
        });
      })
      .then(function () {
        toast("开始抓取封面，完成后会自动刷新");
        pollCoverJob();
      })
      .catch(function (err) {
        b.disabled = false;
        toast(String(err.message || err).slice(0, 80) || "启动失败");
      });
  }

  function pollCoverJob() {
    var timer = setInterval(function () {
      fetch(api("/covers/status"))
        .then(function (r) { return r.json(); })
        .then(function (st) {
          if (!st) return;
          if (st.running) {
            $("lib-meta").textContent = "正在抓取封面 " + st.done + " / " + st.total +
              "，已找到 " + st.found + " 张";
            return;
          }
          clearInterval(timer);
          $("btn-covers").disabled = false;
          if (st.total) {
            toast("封面抓取完成：" + st.found + " / " + st.total + " 张");
          } else {
            toast("所有条目都已有封面");
          }
          loadLibrary();
        })
        .catch(function () {
          clearInterval(timer);
          $("btn-covers").disabled = false;
        });
    }, 2000);
  }

  /* ---------- 手动添加下载 ---------- */
  function openManual() {
    mnErr("");
    show("m-manual");
    setTimeout(function () { $("mn-magnet").focus(); }, 80);
  }

  function mnErr(msg) {
    var el = $("mn-err");
    el.textContent = msg || "";
    el.hidden = !msg;
  }

  function mnReset() {
    $("mn-magnet").value = "";
    $("mn-title").value = "";
    $("mn-cover-url").value = "";
    $("mn-file").value = "";
    $("mn-cover-file").value = "";
    $("mn-file-name").textContent = "点击选择或拖入 .torrent 文件";
    $("mn-drop").classList.remove("has-file");
    $("mn-cover-preview").hidden = true;
    $("mn-cover-preview").removeAttribute("src");
    $("mn-cover-ph").hidden = false;
    $("mn-pick-q").value = "";
    $("mn-pick-list").innerHTML = "";
    $("mn-picked").hidden = true;
    S.mnFile = null;
    S.mnCover = null;
    S.mnTarget = null;
    mnErr("");
  }

  // 并入已有条目：搜索并选择目标文件夹
  function bindPicker() {
    var q = $("mn-pick-q");
    var list = $("mn-pick-list");
    var timer;

    $("mn-dest").addEventListener("click", function (e) {
      var b = e.target.closest(".seg-btn");
      if (!b) return;
      S.mnDest = b.dataset.dest;
      $("mn-dest").querySelectorAll(".seg-btn").forEach(function (x) {
        x.classList.toggle("is-active", x === b);
      });
      var existing = S.mnDest === "existing";
      $("mn-new-wrap").hidden = existing;
      $("mn-existing-wrap").hidden = !existing;
      mnErr("");
      if (existing && !list.children.length) loadFolders("");
    });

    q.addEventListener("input", function () {
      clearTimeout(timer);
      var kw = q.value.trim();
      timer = setTimeout(function () { loadFolders(kw); }, 250);
    });

    function loadFolders(kw) {
      list.innerHTML = '<div class="pick-hint">正在加载…</div>';
      fetch(api("/folders?q=" + enc(kw)))
        .then(function (r) { return r.json(); })
        .then(function (items) {
          items = items || [];
          if (!items.length) {
            list.innerHTML = '<div class="pick-hint">没有匹配的条目</div>';
            return;
          }
          list.innerHTML = items.map(function (f, i) {
            var thumb = f.cover
              ? '<img src="' + esc(coverURL(f.cover)) + '" alt="" loading="lazy">'
              : icon("i-film");
            return '<div class="pick-item" data-i="' + i + '">' +
              '<div class="pick-thumb">' + thumb + "</div>" +
              '<div class="pick-body"><div class="pick-name">' + esc(f.title) + "</div>" +
              '<div class="pick-meta">' + catName(f.category) + " · " +
              f.episodes + " 个文件 · " + esc(f.sizeHuman || "") + "</div></div></div>";
          }).join("");
          list.querySelectorAll(".pick-item").forEach(function (el) {
            el.addEventListener("click", function () { pickFolder(items[+el.dataset.i]); });
          });
        })
        .catch(function () { list.innerHTML = '<div class="pick-hint">加载失败</div>'; });
    }

    function pickFolder(f) {
      S.mnTarget = f;
      $("mn-picked-name").textContent = f.title;
      $("mn-picked-meta").textContent = catName(f.category) + " · 已有 " + f.episodes + " 个文件 · " + (f.sizeHuman || "");
      $("mn-picked").hidden = false;
      list.innerHTML = "";
      q.value = "";
      mnErr("");
    }

    $("mn-picked-clear").addEventListener("click", function () {
      S.mnTarget = null;
      $("mn-picked").hidden = true;
    });
  }

  function bindManual() {
    $("btn-manual").addEventListener("click", openManual);

    // 磁力 / 种子 切换
    $("mn-mode").addEventListener("click", function (e) {
      var b = e.target.closest(".seg-btn");
      if (!b) return;
      S.mnMode = b.dataset.mode;
      $("mn-mode").querySelectorAll(".seg-btn").forEach(function (x) {
        x.classList.toggle("is-active", x === b);
      });
      $("mn-magnet-field").hidden = S.mnMode !== "magnet";
      $("mn-torrent-field").hidden = S.mnMode !== "torrent";
      mnErr("");
    });

    // 分类切换
    $("mn-cat").addEventListener("click", function (e) {
      var b = e.target.closest(".seg-btn");
      if (!b) return;
      S.mnCat = b.dataset.cat;
      $("mn-cat").querySelectorAll(".seg-btn").forEach(function (x) {
        x.classList.toggle("is-active", x === b);
      });
    });

    // 选种子文件
    var drop = $("mn-drop");
    $("mn-file").addEventListener("change", function () {
      if (this.files && this.files[0]) readTorrent(this.files[0]);
    });
    ["dragenter", "dragover"].forEach(function (ev) {
      drop.addEventListener(ev, function (e) { e.preventDefault(); drop.classList.add("is-over"); });
    });
    ["dragleave", "drop"].forEach(function (ev) {
      drop.addEventListener(ev, function (e) { e.preventDefault(); drop.classList.remove("is-over"); });
    });
    drop.addEventListener("drop", function (e) {
      var f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
      if (f) readTorrent(f);
    });

    function readTorrent(f) {
      if (!/\.torrent$/i.test(f.name)) { mnErr("请选择 .torrent 文件"); return; }
      if (f.size > 8 * 1024 * 1024) { mnErr("种子文件过大"); return; }
      var fr = new FileReader();
      fr.onload = function () {
        S.mnFile = String(fr.result).split(",")[1] || "";
        $("mn-file-name").textContent = f.name + " · " + fmtSize(f.size);
        drop.classList.add("has-file");
        mnErr("");
        // 没填名称时用种子文件名兜底
        if (!$("mn-title").value.trim()) {
          $("mn-title").value = f.name.replace(/\.torrent$/i, "");
        }
      };
      fr.onerror = function () { mnErr("读取种子文件失败"); };
      fr.readAsDataURL(f);
    }

    // 选封面
    $("mn-cover-pick").addEventListener("click", function (e) {
      if (e.target.id !== "mn-cover-file") $("mn-cover-file").click();
    });
    $("mn-cover-file").addEventListener("change", function () {
      var f = this.files && this.files[0];
      if (!f) return;
      if (f.size > 10 * 1024 * 1024) { mnErr("封面图片过大（上限 10MB）"); return; }
      var fr = new FileReader();
      fr.onload = function () {
        S.mnCover = String(fr.result);
        $("mn-cover-preview").src = S.mnCover;
        $("mn-cover-preview").hidden = false;
        $("mn-cover-ph").hidden = true;
        $("mn-cover-url").value = "";
        mnErr("");
      };
      fr.readAsDataURL(f);
    });
    $("mn-cover-url").addEventListener("input", function () {
      if (this.value.trim()) {
        S.mnCover = null;
        $("mn-cover-preview").hidden = true;
        $("mn-cover-ph").hidden = false;
        $("mn-cover-file").value = "";
      }
    });
    $("mn-cover-clear").addEventListener("click", function () {
      S.mnCover = null;
      $("mn-cover-url").value = "";
      $("mn-cover-file").value = "";
      $("mn-cover-preview").hidden = true;
      $("mn-cover-preview").removeAttribute("src");
      $("mn-cover-ph").hidden = false;
    });

    bindPicker();

    // 提交
    $("mn-submit").addEventListener("click", function () {
      var btn = $("mn-submit");
      var title = $("mn-title").value.trim();
      var magnet = $("mn-magnet").value.trim();
      var coverURL = $("mn-cover-url").value.trim();
      var toExisting = S.mnDest === "existing";

      if (S.mnMode === "magnet" && !magnet) { mnErr("请粘贴磁力链接"); return; }
      if (S.mnMode === "magnet" && !/^magnet:\?/i.test(magnet)) {
        mnErr("磁力链接应以 magnet:? 开头"); return;
      }
      if (S.mnMode === "torrent" && !S.mnFile) { mnErr("请选择 .torrent 文件"); return; }
      if (toExisting && !S.mnTarget) { mnErr("请选择要并入的条目"); return; }
      if (!toExisting && !title) { mnErr("请填写保存名称"); return; }

      var payload = {};
      if (toExisting) {
        // 并入已有：分类与名称都由后端沿用目标条目
        payload.targetId = S.mnTarget.id;
        payload.title = S.mnTarget.title;
      } else {
        payload.title = title;
        payload.category = S.mnCat;
      }
      if (S.mnMode === "torrent") payload.torrent = S.mnFile;
      else payload.magnet = magnet;
      if (S.mnCover) payload.cover = S.mnCover;
      else if (coverURL) payload.cover = coverURL;

      btn.classList.add("btn-loading");
      btn.disabled = true;
      mnErr("");
      fetch(api("/manual"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      }).then(function (r) {
        return r.text().then(function (txt) {
          if (!r.ok) throw new Error(txt || ("HTTP " + r.status));
          return JSON.parse(txt);
        });
      }).then(function (task) {
        btn.classList.remove("btn-loading");
        btn.disabled = false;
        if (!task || !task.id) { mnErr("提交失败"); return; }
        hide("m-manual");
        mnReset();
        toast("已加入下载队列");
        loadLibrary();
        openTasks();
      }).catch(function (err) {
        btn.classList.remove("btn-loading");
        btn.disabled = false;
        mnErr(String(err.message || err).slice(0, 200));
      });
    });
  }

  /* ---------- 下载任务 ---------- */
  function openTasks() { show("m-tasks"); refreshTasks(); }

  function refreshTasks() {
    fetch(api("/tasks")).then(function (r) { return r.json(); }).then(function (list) {
      list = list || [];
      var host = $("tasks");
      var running = list.filter(function (t) { return t.status === "running"; }).length;
      $("task-dot").hidden = running === 0;
      $("tasks-empty").hidden = list.length > 0;
      if (!list.length) { host.innerHTML = ""; return; }
      host.innerHTML = list.map(function (t) {
        var st = t.status, cls = "", txt = "";
        if (st === "running") { txt = (t.progress || 0).toFixed(1) + "%"; }
        else if (st === "done") { cls = "is-done"; txt = "已完成"; }
        else if (st === "error") { cls = "is-error"; txt = "失败"; }
        else { cls = "is-stopped"; txt = "已停止"; }
        var speed = t.speedKBs ? (t.speedKBs > 1024 ? (t.speedKBs / 1024).toFixed(2) + " MB/s" : Math.round(t.speedKBs) + " KB/s") : "";
        return '<div class="task">' +
          '<div class="task-top"><span class="task-name">' + esc(t.name) + "</span>" +
          '<span class="task-state ' + cls + '">' + txt + "</span></div>" +
          '<div class="bar"><i class="' + (st === "done" ? "is-done" : "") + '" style="width:' +
          Math.min(100, st === "done" ? 100 : (t.progress || 0)) + '%"></i></div>' +
          '<div class="task-facts"><span>' + fmtSize(t.bytesDone) + " / " + fmtSize(t.bytesTotal) + "</span>" +
          (speed ? '<span class="grow">' + speed + "</span>" : '<span class="grow"></span>') +
          (t.peers ? "<span>" + t.peers + " 节点</span>" : "") + "</div>" +
          (t.error ? '<p class="task-err">' + esc(t.error) + "</p>" : "") +
          (st === "done" && t.targetDir ? '<p class="task-path">' + icon("i-folder") + esc(t.targetDir) + "</p>" : "") +
          (st === "running" ? '<div class="task-foot"><button class="btn btn-danger btn-sm" data-stop="' + esc(t.id) + '">停止任务</button></div>' : "") +
          "</div>";
      }).join("");
      host.querySelectorAll("[data-stop]").forEach(function (b) {
        b.addEventListener("click", function () {
          if (!window.confirm("停止该下载任务？已下载的临时数据会被清理。")) return;
          fetch(api("/task/stop"), {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ id: b.dataset.stop })
          }).then(function () { toast("任务已停止"); refreshTasks(); });
        });
      });
    }).catch(function () {});
  }

  /* ---------- 弹层控制 ---------- */
  function show(id) {
    $(id).hidden = false;
    document.body.style.overflow = "hidden";
  }
  function hide(id) {
    $(id).hidden = true;
    if (id === "m-player") {
      var v = $("video");
      if (P.resume && S.playPath && v.currentTime > 5) {
        setProg(S.playPath, v.currentTime, v.duration);
      }
      v.pause();
      v.removeAttribute("src");
      v.load();
      $("pl-settings").hidden = true;
      releaseWakeLock();
      if (document.fullscreenElement && document.exitFullscreen) document.exitFullscreen().catch(function () {});
      unlockOrientation();
    }
    if (!document.querySelector(".modal:not([hidden])")) document.body.style.overflow = "";
  }

  function bindModals() {
    document.querySelectorAll("[data-close]").forEach(function (el) {
      el.addEventListener("click", function () { hide(el.dataset.close); });
    });
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      // 从最上层往下依次关闭
      var order = ["m-player", "m-del", "m-edit", "m-upload", "m-manual", "m-tasks", "m-detail"];
      for (var i = 0; i < order.length; i++) {
        if (!$(order[i]).hidden) { hide(order[i]); return; }
      }
    });
  }

  /* ---------- 上传本地视频 ---------- */
  // 多选文件排队传：一个传完再传下一个（盒子 USB 写入和 WiFi 都是瓶颈，并发反而慢）
  var upFiles = [];     // 待传的 File 列表
  var upBusy = false;   // 正在传则禁止再点开始
  var upXhr = null;     // 当前 XHR，取消时 abort

  function upErr(msg) {
    var el = $("up-err");
    el.textContent = msg || "";
    el.hidden = !msg;
  }

  function upAddFiles(list) {
    var added = 0;
    for (var i = 0; i < list.length; i++) {
      var f = list[i];
      if (!/\.(mkv|mp4|avi|mov|m4v|ts|m2ts|webm|wmv|rm|rmvb|flv|mpg|mpeg|iso)$/i.test(f.name)) continue;
      upFiles.push(f);
      added++;
    }
    if (!added) { upErr("只支持视频文件"); return; }
    upErr("");
    renderUpFiles();
  }

  function renderUpFiles() {
    var box = $("up-filelist");
    box.hidden = !upFiles.length;
    $("up-file-name").textContent = upFiles.length
      ? "已选 " + upFiles.length + " 个文件 · 共 " + fmtSize(upFiles.reduce(function (s, f) { return s + f.size; }, 0))
      : "点击选择或拖入视频文件";
    box.innerHTML = upFiles.map(function (f, i) {
      return '<div class="upf"><span class="upf-name">' + esc(f.name) + "</span>" +
        '<span class="upf-size">' + fmtSize(f.size) + "</span>" +
        (upBusy ? "" : '<button type="button" class="icon-btn upf-del" data-i="' + i + '" title="移除"><svg class="ic"><use href="#i-close"/></svg></button>') +
        "</div>";
    }).join("");
    box.querySelectorAll(".upf-del").forEach(function (el) {
      el.addEventListener("click", function () {
        upFiles.splice(+el.dataset.i, 1);
        renderUpFiles();
      });
    });
  }

  function bindUpload() {
    $("btn-upload").addEventListener("click", function () {
      upErr("");
      $("up-progress").hidden = true;
      $("up-progress").innerHTML = "";
      show("m-upload");
      loadDisk();
    });

    var file = $("up-file"), drop = $("up-drop");
    drop.addEventListener("click", function (e) { if (e.target.id !== "up-file") file.click(); });
    file.addEventListener("change", function () { upAddFiles(this.files); this.value = ""; });
    drop.addEventListener("dragover", function (e) { e.preventDefault(); drop.classList.add("is-over"); });
    drop.addEventListener("dragleave", function () { drop.classList.remove("is-over"); });
    drop.addEventListener("drop", function (e) {
      e.preventDefault();
      drop.classList.remove("is-over");
      upAddFiles(e.dataTransfer.files);
    });

    // 保存位置切换
    $("up-dest").addEventListener("click", function (e) {
      var b = e.target.closest(".seg-btn");
      if (!b) return;
      $("up-dest").querySelectorAll(".seg-btn").forEach(function (x) { x.classList.toggle("is-active", x === b); });
      var existing = b.dataset.dest === "existing";
      $("up-new-wrap").hidden = existing;
      $("up-existing-wrap").hidden = !existing;
      upErr("");
      if (existing && !$("up-pick-list").children.length) upLoadFolders("");
    });
    $("up-cat").addEventListener("click", function (e) {
      var b = e.target.closest(".seg-btn");
      if (!b) return;
      $("up-cat").querySelectorAll(".seg-btn").forEach(function (x) { x.classList.toggle("is-active", x === b); });
    });

    // 并入已有的条目搜索
    var timer;
    $("up-pick-q").addEventListener("input", function () {
      clearTimeout(timer);
      var kw = this.value.trim();
      timer = setTimeout(function () { upLoadFolders(kw); }, 250);
    });
    $("up-picked-clear").addEventListener("click", function () {
      S.upTarget = null;
      $("up-picked").hidden = true;
    });

    $("up-submit").addEventListener("click", startUpload);
  }

  function upLoadFolders(kw) {
    var list = $("up-pick-list");
    list.innerHTML = '<div class="pick-hint">正在加载…</div>';
    fetch(api("/folders?q=" + enc(kw)))
      .then(function (r) { return r.json(); })
      .then(function (items) {
        items = items || [];
        if (!items.length) { list.innerHTML = '<div class="pick-hint">没有匹配的条目</div>'; return; }
        list.innerHTML = items.map(function (f, i) {
          var thumb = f.cover ? '<img src="' + esc(coverURL(f.cover)) + '" alt="" loading="lazy">' : icon("i-film");
          return '<div class="pick-item" data-i="' + i + '">' +
            '<div class="pick-thumb">' + thumb + "</div>" +
            '<div class="pick-body"><div class="pick-name">' + esc(f.title) + "</div>" +
            '<div class="pick-meta">' + catName(f.category) + " · " + f.episodes + " 个文件 · " + esc(f.sizeHuman || "") + "</div></div></div>";
        }).join("");
        list.querySelectorAll(".pick-item").forEach(function (el) {
          el.addEventListener("click", function () {
            var f = items[+el.dataset.i];
            S.upTarget = f;
            $("up-picked-name").textContent = f.title;
            $("up-picked-meta").textContent = catName(f.category) + " · 已有 " + f.episodes + " 个文件 · " + (f.sizeHuman || "");
            $("up-picked").hidden = false;
            list.innerHTML = "";
            $("up-pick-q").value = "";
          });
        });
      })
      .catch(function () { list.innerHTML = '<div class="pick-hint">加载失败</div>'; });
  }

  function loadDisk() {
    fetch(api("/disk"))
      .then(function (r) { return r.json(); })
      .then(function (d) {
        $("up-disk").textContent = d && d.freeH ? "硬盘剩余 " + d.freeH + " / 共 " + d.totalH : "";
      })
      .catch(function () { $("up-disk").textContent = ""; });
  }

  function startUpload() {
    if (upBusy) return;
    if (!upFiles.length) { upErr("请先选择视频文件"); return; }
    var dest = $("up-dest").querySelector(".seg-btn.is-active").dataset.dest;
    var title = $("up-title").value.trim();
    var cat = $("up-cat").querySelector(".seg-btn.is-active").dataset.cat;
    if (dest === "new" && !title) { upErr("请填写保存名称"); return; }
    if (dest === "existing" && !S.upTarget) { upErr("请选择要并入的条目"); return; }

    // 磁盘空间预检：总大小超剩余空间就直接拦下
    var need = upFiles.reduce(function (s, f) { return s + f.size; }, 0);
    upBusy = true;
    renderUpFiles();
    $("up-submit").disabled = true;
    fetch(api("/disk")).then(function (r) { return r.json(); }).then(function (d) {
      if (d && d.free && need > d.free) {
        throw new Error("硬盘空间不足：需要 " + fmtSize(need) + "，仅剩 " + d.freeH);
      }
      runQueue(0, dest, title, cat);
    }).catch(function (e) {
      upErr(String(e.message || "磁盘检查失败"));
      upBusy = false;
      renderUpFiles();
      $("up-submit").disabled = false;
    });
  }

  // 顺序传队列里的第 idx 个
  function runQueue(idx, dest, title, cat) {
    var box = $("up-progress");
    box.hidden = false;
    if (idx >= upFiles.length) {
      upBusy = false;
      $("up-submit").disabled = false;
      upFiles = [];
      renderUpFiles();
      toast("上传完成");
      hide("m-upload");
      loadLibrary();
      return;
    }
    var f = upFiles[idx];
    var row = document.createElement("div");
    row.className = "upp";
    row.innerHTML = '<div class="upp-head"><span class="upp-name">' + esc(f.name) + "</span>" +
      '<span class="upp-pct">0%</span></div>' +
      '<div class="dl-track"><i class="dl-fill" style="width:0%"></i></div>' +
      '<p class="upp-sub">等待中</p>';
    box.appendChild(row);
    var pctEl = row.querySelector(".upp-pct"), fill = row.querySelector(".dl-fill"), sub = row.querySelector(".upp-sub");

    var fd = new FormData();
    // 文本字段必须在文件前 append，后端流式读取靠这个顺序
    fd.append("cat", cat);
    fd.append("dest", dest);
    fd.append("title", title);
    fd.append("targetId", dest === "existing" && S.upTarget ? S.upTarget.id : "");
    fd.append("file", f, f.name);

    var xhr = new XMLHttpRequest();
    upXhr = xhr;
    var t0 = Date.now(), lastLoaded = 0, lastT = t0;
    xhr.upload.onprogress = function (e) {
      if (!e.lengthComputable) return;
      var pct = Math.round(e.loaded * 100 / e.total);
      pctEl.textContent = pct + "%";
      fill.style.width = pct + "%";
      var now = Date.now();
      if (now - lastT > 800) {
        var speed = (e.loaded - lastLoaded) * 1000 / (now - lastT);
        lastLoaded = e.loaded; lastT = now;
        var eta = speed > 0 ? Math.round((e.total - e.loaded) / speed) : 0;
        sub.textContent = fmtSize(e.loaded) + " / " + fmtSize(e.total) + " · " + fmtSize(speed) + "/s · 约剩 " +
          (eta >= 60 ? Math.floor(eta / 60) + " 分 " + (eta % 60) + " 秒" : eta + " 秒");
      }
    };
    xhr.onload = function () {
      upXhr = null;
      if (xhr.status === 200) {
        fill.style.width = "100%";
        pctEl.textContent = "100%";
        sub.textContent = "已完成 · " + fmtSize(f.size);
        runQueue(idx + 1, dest, title, cat);
      } else {
        sub.textContent = "失败：" + xhr.responseText.slice(0, 80);
        row.classList.add("is-err");
        upBusy = false;
        $("up-submit").disabled = false;
        renderUpFiles();
      }
    };
    xhr.onerror = function () {
      upXhr = null;
      sub.textContent = "网络中断，半截文件已自动清理";
      row.classList.add("is-err");
      upBusy = false;
      $("up-submit").disabled = false;
      renderUpFiles();
    };
    xhr.open("POST", api("/upload"));
    xhr.send(fd);
  }

  /* ---------- 文件管理器 ---------- */
  var fsPath = "";        // 当前浏览路径（空 = 硬盘根）
  var fsSel = {};         // 勾选的条目 path -> entry
  var fsData = null;      // 最近一次 list 结果

  function bindFs() {
    $("fs-mkdir").addEventListener("click", function () {
      var name = window.prompt("新建文件夹名称");
      if (!name || !name.trim()) return;
      fetch(api("/fs/mkdir"), {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: fsPath, name: name.trim() })
      }).then(function (r) { return r.json(); }).then(function (d) {
        if (d && d.ok) { toast("已创建"); loadFs(fsPath); }
        else toast("创建失败");
      }).catch(function () { toast("创建失败"); });
    });
    $("fs-refresh").addEventListener("click", function () { loadFs(fsPath); });
    $("fs-rename").addEventListener("click", function () {
      var keys = Object.keys(fsSel);
      if (keys.length !== 1) return;
      var e0 = fsSel[keys[0]];
      var base = e0.isDir ? e0.name : e0.name.replace(/\.[^.]+$/, "");
      var name = window.prompt("新名称", base);
      if (!name || !name.trim() || name.trim() === base) return;
      fetch(api("/fs/rename"), {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: e0.path, name: name.trim() })
      }).then(function (r) { return r.text(); }).then(function (t) {
        try {
          var d = JSON.parse(t);
          if (d.ok) { toast("已改名"); loadFs(fsPath); loadLibrary(); }
          else toast("改名失败");
        } catch (e) { toast(t || "改名失败"); }
      }).catch(function () { toast("改名失败"); });
    });
    $("fs-delete").addEventListener("click", function () { fsDelete(false); });
  }

  function fsDelete(force) {
    var keys = Object.keys(fsSel);
    if (!keys.length) return;
    var list = keys.map(function (k) { return fsSel[k]; });
    if (!force && !window.confirm("确认删除选中的 " + list.length + " 项？删除后无法恢复。")) return;
    // 逐个删，遇到非空目录二次确认
    var i = 0, freed = 0, failed = 0;
    (function next() {
      if (i >= list.length) {
        toast(failed ? "部分删除失败（" + failed + " 项）" : "已删除，释放 " + fmtSize(freed));
        loadFs(fsPath);
        loadLibrary();
        return;
      }
      var e0 = list[i++];
      fetch(api("/fs/delete"), {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: e0.path, force: force })
      }).then(function (r) { return r.text(); }).then(function (t) {
        var d;
        try { d = JSON.parse(t); } catch (err) { failed++; return next(); }
        if (d.needConfirm) {
          if (window.confirm("「" + e0.name + "」不是空文件夹，里面有 " + d.dirFiles + " 个文件共 " + d.dirSizeHuman + "。确认全部删除？")) {
            return fsDeleteOne(e0.path, true, function (ok, fr) {
              if (ok) freed += fr; else failed++;
              next();
            });
          }
          return next();
        }
        if (d.ok) freed += d.freed || 0; else failed++;
        next();
      }).catch(function () { failed++; next(); });
    })();
  }

  function fsDeleteOne(path, force, cb) {
    fetch(api("/fs/delete"), {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: path, force: force })
    }).then(function (r) { return r.json(); })
      .then(function (d) { cb(!!(d && d.ok), (d && d.freed) || 0); })
      .catch(function () { cb(false, 0); });
  }

  function loadFs(path) {
    fsSel = {};
    fsTools();
    fetch(api("/fs/list?path=" + enc(path || "")))
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (d && d.entries) { fsData = d; fsPath = d.path; renderFs(); }
        else toast("目录读取失败");
      })
      .catch(function () { toast("目录读取失败"); });
  }

  function fsTools() {
    var n = Object.keys(fsSel).length;
    $("fs-delete").disabled = !n;
    $("fs-rename").disabled = n !== 1;
  }

  function renderFs() {
    var list = $("fs-list");
    var entries = (fsData && fsData.entries) || [];
    // 面包屑
    var root = fsData.root || "";
    var rel = fsPath === root ? "" : fsPath.slice(root.length).replace(/^[\\/]/, "");
    var crumbs = '<button class="fs-crumb" data-p="">' + icon("i-disk") + "硬盘</button>";
    var acc = "";
    rel.split(/[\\/]/).filter(Boolean).forEach(function (seg) {
      acc += "/" + seg;
      crumbs += '<span class="fs-sep">/</span><button class="fs-crumb" data-p="' + esc(root + acc) + '">' + esc(seg) + "</button>";
    });
    $("fs-crumbs").innerHTML = crumbs;
    $("fs-crumbs").querySelectorAll(".fs-crumb").forEach(function (b) {
      b.addEventListener("click", function () { loadFs(b.dataset.p); });
    });

    if (!entries.length) {
      list.innerHTML = "";
      $("fs-empty").hidden = false;
      return;
    }
    $("fs-empty").hidden = true;
    list.innerHTML = entries.map(function (e, i) {
      var ic = e.isDir ? "i-folder" : (e.video ? "i-film" : "i-list");
      var badges = "";
      if (e.protected) badges += '<span class="chip">受保护</span>';
      if (e.inUse) badges += '<span class="chip chip-brand">下载中</span>';
      var checked = fsSel[e.path] ? " is-sel" : "";
      return '<div class="fs-item' + checked + '" data-i="' + i + '"' + (e.isDir ? " data-dir" : "") + '>' +
        '<label class="fs-check"><input type="checkbox" class="fs-cb" data-i="' + i + '"' + (fsSel[e.path] ? " checked" : "") + "></label>" +
        '<div class="fs-ic">' + icon(ic) + "</div>" +
        '<div class="fs-body"><p class="fs-name">' + esc(e.name) + badges + "</p>" +
        '<p class="fs-meta">' + (e.isDir ? "文件夹" : (e.sizeHuman || "")) + " · " + fmtTime(e.modified) + "</p></div>" +
        (e.isDir ? '<button class="btn btn-ghost btn-sm fs-open" data-i="' + i + '">打开</button>' : "") +
        "</div>";
    }).join("");
    list.querySelectorAll(".fs-cb").forEach(function (cb) {
      cb.addEventListener("change", function () {
        var e = entries[+cb.dataset.i];
        if (cb.checked) fsSel[e.path] = e; else delete fsSel[e.path];
        cb.closest(".fs-item").classList.toggle("is-sel", cb.checked);
        fsTools();
      });
    });
    list.querySelectorAll(".fs-open").forEach(function (b) {
      b.addEventListener("click", function () { loadFs(entries[+b.dataset.i].path); });
    });
    // 点文件夹名字也能进入
    list.querySelectorAll(".fs-item").forEach(function (el) {
      var e = entries[+el.dataset.i];
      if (!e.isDir) return;
      el.querySelector(".fs-name").addEventListener("click", function () { loadFs(e.path); });
    });
  }

  // 管理页 tab 切换到"文件管理"时换内容
  function applyMngCat() {
    var files = S.mngCat === "files";
    $("fs-view").hidden = !files;
    $("mng-filter").hidden = files;
    document.querySelector(".mng-stats").hidden = files;
    document.querySelector(".table-wrap").hidden = files;
    $("mng-empty").hidden = true;
    var cacheSec = document.querySelector(".cache-sec");
    if (cacheSec) cacheSec.hidden = files;
    if (files) {
      if (!fsPath) loadFs("");
      else renderFs();
    } else {
      renderMngTable();
    }
  }

  /* ---------- 启动 ---------- */
  function init() {
    loadPrefs();
    bindSearch();
    bindPlayer();
    bindModals();
    bindManual();
    bindManage();
    bindUpload();
    bindFs();
    bindMe();
    // 底部导航（移动端）
    document.querySelectorAll(".tabbar-btn").forEach(function (b) {
      b.addEventListener("click", function () {
        var v = b.dataset.view;
        if (v === "lib") showLib();
        else if (v === "cache") showCache();
        else if (v === "mng") showMng();
        else if (v === "me") showMe();
      });
    });
    loadLibrary();

    $("tabs").querySelectorAll(".tab").forEach(function (t) {
      t.addEventListener("click", function () {
        $("tabs").querySelectorAll(".tab").forEach(function (x) { x.classList.remove("is-active"); });
        t.classList.add("is-active");
        S.cat = t.dataset.cat;
        loadLibrary();
      });
    });

    $("btn-refresh").addEventListener("click", function () {
      var b = $("btn-refresh");
      b.disabled = true;
      fetch(api("/scan"), { method: "POST" })
        .then(function () {
          setTimeout(function () { loadLibrary(); b.disabled = false; toast("媒体库已刷新"); }, 1200);
        })
        .catch(function () { b.disabled = false; toast("刷新失败"); });
    });

    $("btn-covers").addEventListener("click", startCoverJob);

    // 按 URL hash 恢复视图（刷新后仍停在原页面）
    var hash = location.hash || "";
    var m = /(?:^|[#&])q=([^&]*)/.exec(hash);
    if (m && m[1]) {
      var kw = decodeURIComponent(m[1]);
      $("q").value = kw;
      $("q-clear").hidden = false;
      submitSearchNoPush(kw);
    } else if (hash.indexOf("manage") >= 0) {
      showMng(false);
    }

    $("btn-tasks").addEventListener("click", openTasks);
    S.tasksTimer = setInterval(function () {
      if (!$("m-tasks").hidden) { refreshTasks(); return; }
      fetch(api("/tasks")).then(function (r) { return r.json(); }).then(function (l) {
        var list = l || [];
        var active = list.filter(function (t) {
          return t.status === "running" || t.status === "processing";
        });
        $("task-dot").hidden = active.length === 0;
        // 有活动任务时刷新媒体库，让进度条动起来；
        // 任务刚结束的那一次也刷新一遍，让新入库的条目立即出现
        if (S.view === "lib" && (active.length || S.downloading.length)) {
          refreshDownloadCards(active);
        }
      }).catch(function () {});
    }, 3000);
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();