package com.family.cinema

import android.annotation.SuppressLint
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.webkit.WebChromeClient
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity
import kotlin.concurrent.thread

class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView
    private lateinit var store: CacheStore
    private lateinit var server: LocalHttpServer
    private lateinit var updateMgr: WebUpdateManager
    private lateinit var sniffWorker: SniffWorker
    private lateinit var prefs: android.content.SharedPreferences
    private lateinit var proxyRouter: ProxyRouter
    @Volatile private var online = false
    @Volatile private var serverAddr = ""
    // 点追番通知进来时要打开的页面（如 /play/412008），页面加载时带上
    @Volatile private var pendingRoute: String? = null

    companion object {
        private const val REQ_NOTIF = 1001
        const val EXTRA_ROUTE = "route"
    }

    // 全屏视频（前端 requestFullscreen）用的覆盖层
    private var customView: View? = null
    private var customCallback: WebChromeClient.CustomViewCallback? = null

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        prefs = getSharedPreferences("app", MODE_PRIVATE)
        pendingRoute = routeOf(intent)
        store = CacheStore.get(this)
        // 上次进程猝死会留下"假下载中"状态（speed 没归零），先清尸再渲染
        store.resetStalled()
        server = LocalHttpServer(this).apply { start() }
        updateMgr = WebUpdateManager(this)
        sniffWorker = SniffHost.get(applicationContext, prefs)
        proxyRouter = ProxyRouter(prefs)

        webView = findViewById(R.id.webview)
        // 先按上次网页告诉我们的页面底色刷状态栏和 WebView 底色（网页加载出来前那一瞬间也不闪白）
        if (prefs.contains("bar_color")) {
            val c = prefs.getInt("bar_color", 0)
            applySystemBars(c, prefs.getBoolean("bar_dark", false))
            webView.setBackgroundColor(c)
        }
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true                    // localStorage：观看进度记忆
            mediaPlaybackRequiresUserGesture = false  // 允许脚本直接起播
            cacheMode = WebSettings.LOAD_DEFAULT
            mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW // 内网 http 图片/视频
            useWideViewPort = true                      // 让 viewport meta 生效（自适应屏幕宽度）
            loadWithOverviewMode = true                 // 按屏幕宽度布局，不自作主张缩放
            setSupportZoom(false)                       // 禁止双指放大缩小（要像 App，不能像看图）
            builtInZoomControls = false
            displayZoomControls = false
        }
        // 拦截 Bangumi 封面图（lain.bgm.tv 被墙）走自动选路；
        // 注意 shouldInterceptRequest 拿不到 POST 请求体，所以 API 请求走桥（bgmRequest），这里只管 GET 图片
        webView.webViewClient = object : WebViewClient() {
            override fun shouldInterceptRequest(
                view: WebView?,
                request: android.webkit.WebResourceRequest?
            ): android.webkit.WebResourceResponse? {
                val u = request?.url ?: return null
                if (request.method == "GET" && u.host == "lain.bgm.tv") {
                    return proxyRouter.interceptGet(u.toString())
                }
                return null
            }
        }
        webView.webChromeClient = object : WebChromeClient() {
            override fun onShowCustomView(view: View?, callback: CustomViewCallback?) {
                if (customView != null) { callback?.onCustomViewHidden(); return }
                customView = view; customCallback = callback
                addContentView(view, ViewGroup.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
                webView.visibility = View.GONE
            }
            override fun onHideCustomView() {
                (customView?.parent as? ViewGroup)?.removeView(customView)
                customView = null
                webView.visibility = View.VISIBLE
                customCallback?.onCustomViewHidden(); customCallback = null
            }
        }
        webView.addJavascriptInterface(
            WebAppBridge(
                ctx = this,
                store = store,
                server = server,
                updateMgr = updateMgr,
                prefs = prefs,
                isOnlineCheck = { online },
                onOrientation = { landscape ->
                    // 由 Activity 层转横屏，用户开了系统旋转锁定也照样生效；
                    // 同时进/退沉浸式全屏（隐藏状态栏，否则播放时顶上一条白边很碍眼）
                    runOnUiThread {
                        requestedOrientation = if (landscape)
                            android.content.pm.ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE
                        else
                            android.content.pm.ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
                        applyImmersive(landscape)
                    }
                },
                onKeepScreenOn = { on ->
                    // FLAG_KEEP_SCREEN_ON 只在本 Activity 前台时生效，退后台/锁屏不会耗电，
                    // 重复 addFlags 幂等，无需自己做状态去重
                    runOnUiThread {
                        if (on) window.addFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                        else window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                    }
                },
                onSystemBars = { color, dark -> runOnUiThread { applySystemBars(color, dark) } },
                sniffWorker = sniffWorker,
                proxyRouter = proxyRouter
            ),
            "AndroidBridge"
        )
        ensureBackgroundPermissions()
        boot()
    }

    // 后台缓存要两样东西，都得用户点头：
    //   1. 通知权限（安卓 13+）—— 前台服务的进度通知，没有的话看不到进度，
    //      而且部分 OEM 见通知被拒会更激进地杀后台
    //   2. 电池优化白名单 —— HyperOS/MIUI 默认会掐锁屏后的后台任务
    private fun ensureBackgroundPermissions() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) !=
            android.content.pm.PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(android.Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIF)
        } else {
            askBatteryWhitelist()
        }
    }

    // 通知权限弹窗关掉后再问电池白名单，两个系统弹窗不叠在一起
    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, results: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, results)
        if (requestCode == REQ_NOTIF) askBatteryWhitelist()
    }

    // 只在没进白名单时弹一次系统对话框，用户拒了就不再骚扰（"我的"页面留了手动入口）
    private fun askBatteryWhitelist() {
        if (Build.VERSION.SDK_INT < 23) return
        if (BatteryHelper.isUnrestricted(this)) return
        if (prefs.getBoolean("battery_asked", false)) return
        prefs.edit().putBoolean("battery_asked", true).apply()
        BatteryHelper.requestWhitelist(this)
    }

    // 启动：后台发现服务器 → 在线则热更新前端 → 决定加载在线地址还是本地缓存页
    private fun boot() {
        thread {
            val addr = Discovery.discover(this) ?: prefs.getString("server_addr", "") ?: ""
            if (addr.isNotEmpty()) prefs.edit().putString("server_addr", addr).apply()
            serverAddr = addr
            online = addr.isNotEmpty() && probe(addr)
            if (online) {
                updateMgr.syncFrom(addr)
                resumePendingDownloads()   // 上次没下完的接着下（离线时不恢复，免得空转重试）
            }
            runOnUiThread { loadContent() }
            FollowWatcher.ensureScheduled(this) // 追番提醒的闹钟（被系统清掉过就补上）
            // 解析助手默认开启：手机开着 App，电脑上点播放也能借它解析需要浏览器的片源站
            if (prefs.getBoolean("sniff_enabled", true)) SniffService.start(this)
        }
    }

    // 把清单里没下完的任务重新丢给下载服务。服务端支持 Range，已下载的部分不白费。
    // 手动暂停的不续（pendingItems 已排除）；下载服务还活着时已在队列里/在跑的也不重复丢
    private fun resumePendingDownloads() {
        store.pendingItems().filter { !DownloadControl.isActive(it.epId) }.forEach {
            val i = Intent(this, DownloadService::class.java).apply {
                putExtra("url", it.url); putExtra("title", it.title); putExtra("itemId", it.itemId)
                putExtra("epId", it.epId); putExtra("epTitle", it.epTitle)
                putExtra("size", it.size.toString())
                putExtra("coverUrl", it.coverUrl); putExtra("category", it.category)
            }
            if (Build.VERSION.SDK_INT >= 26) startForegroundService(i) else startService(i)
        }
    }

    private fun loadContent() {
        if (online && serverAddr.isNotEmpty()) {
            val route = pendingRoute
            pendingRoute = null
            // 在线：直接读服务器，永远是最新（这就是热更新）；点通知进来的直接到那一页
            webView.loadUrl("$serverAddr/" + if (route != null) "#$route" else "")
        } else if (updateMgr.hasLocalWeb()) {
            webView.loadUrl(server.baseUrl() + "/index.html") // 离线：本地HTTP加载缓存的界面（避开 file:// 限制）
        } else {
            webView.loadDataWithBaseURL(null,
                "<body style='font-family:sans-serif;padding:28px;color:#333'>" +
                    "<h3>未连接到服务器</h3><p>请连接家庭内网后重新打开。</p></body>",
                "text/html", "utf-8", null)
        }
    }

    // 状态栏、底部手势条刷成网页的页面底色；深色模式用白色图标（浅色背景上用深色图标）
    private fun applySystemBars(color: Int, dark: Boolean) {
        window.statusBarColor = color
        window.navigationBarColor = color
        val ctrl = androidx.core.view.WindowCompat.getInsetsController(window, window.decorView)
        ctrl.isAppearanceLightStatusBars = !dark
        ctrl.isAppearanceLightNavigationBars = !dark
    }

    // 沉浸式全屏：播放时隐藏状态栏和导航栏（顶上那条白边很碍眼），退出播放时恢复。
    // 网页层没有隐藏系统栏的权限，只能由原生做。
    private fun applyImmersive(on: Boolean) {
        val ctrl = androidx.core.view.WindowCompat.getInsetsController(window, window.decorView)
        if (on) {
            androidx.core.view.WindowCompat.setDecorFitsSystemWindows(window, false)
            ctrl.hide(androidx.core.view.WindowInsetsCompat.Type.systemBars())
            // 从边缘滑动可临时唤出系统栏，不会打断播放
            ctrl.systemBarsBehavior =
                androidx.core.view.WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        } else {
            androidx.core.view.WindowCompat.setDecorFitsSystemWindows(window, true)
            ctrl.show(androidx.core.view.WindowInsetsCompat.Type.systemBars())
        }
    }

    private fun probe(addr: String): Boolean {
        return try {
            val conn = java.net.URL("$addr/api/library").openConnection() as java.net.HttpURLConnection
            conn.connectTimeout = 4000
            conn.readTimeout = 4000
            conn.responseCode == 200
        } catch (e: Exception) { false }
    }

    // 只认我们自己发的通知带来的页面路径（/play/数字），别的一律不跳
    private fun routeOf(i: Intent?): String? =
        i?.getStringExtra(EXTRA_ROUTE)?.takeIf { Regex("^/play/\\d+$").matches(it) }

    // App 已经开着时点通知：直接切到那一页
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        val route = routeOf(intent) ?: return
        if (online && serverAddr.isNotEmpty()) {
            webView.evaluateJavascript("location.hash = " + org.json.JSONObject.quote("#$route"), null)
        } else {
            pendingRoute = route
        }
    }

    override fun onBackPressed() {
        when {
            customView != null -> (webView.webChromeClient as? WebChromeClient)?.onHideCustomView()
            webView.canGoBack() -> webView.goBack()
            else -> super.onBackPressed()
        }
    }

    override fun onDestroy() {
        // 解析助手由前台服务持有，界面销毁不影响它
        try { server.stop() } catch (e: Exception) {}
        super.onDestroy()
    }
}
