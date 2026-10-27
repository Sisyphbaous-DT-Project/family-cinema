package com.family.cinema

import android.annotation.SuppressLint
import android.content.Context
import android.content.SharedPreferences
import android.graphics.Bitmap
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.webkit.CookieManager
import android.webkit.JsResult
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

// 解析助手：盒子静态解析不出视频地址时，会把"播放页"派给在线的手机。
// 这里用一个看不见的 WebView 打开播放页，拦截它发出的 m3u8/mp4 请求，把真实地址回报给盒子。
// 和 Kazumi 的做法一致（原生层拦截网络请求，不受网页跨域限制）。
// 只要 App 开着、开关打开，就持续向盒子长轮询取任务；电脑浏览器上点播放也能借用这台手机。
class SniffWorker(ctx: Context, private val prefs: SharedPreferences) {
    private val app = ctx.applicationContext
    private val main = Handler(Looper.getMainLooper())
    @Volatile private var running = false
    private var loopThread: Thread? = null
    private var webView: WebView? = null // 只在主线程访问
    private val relay = RelayServer()

    private val deviceId: String = prefs.getString("device_id", null) ?: UUID.randomUUID().toString().also {
        prefs.edit().putString("device_id", it).apply()
    }

    // score：3=路径以 .m3u8 结尾，2=路径以 .mp4/.flv 结尾，1=只是带 Range 头的媒体请求（地址里没有后缀）
    private class Hit(val url: String, val headers: Map<String, String>, val score: Int)

    private class Active(val job: JSONObject) {
        val hits = java.util.Collections.synchronizedList(mutableListOf<Hit>())
        val latch = CountDownLatch(1)
        @Volatile var finished = false
        @Volatile var pickScheduled = false
        @Volatile var result: Hit? = null
        @Volatile var cookie: String? = null
    }

    @Volatile private var active: Active? = null
    @Volatile private var currentUA: String = DESKTOP_UA // WebView 的方法只能在主线程调，UA 另存一份给后台线程用

    val isRunning: Boolean get() = running

    fun start() {
        if (running) return
        running = true
        relay.startRelay()
        prefs.edit().putBoolean("sniff_enabled", true).apply()
        loopThread = Thread { loop() }.apply { isDaemon = true; name = "sniff-worker"; start() }
    }

    fun stop() {
        running = false
        relay.stopRelay()
        prefs.edit().putBoolean("sniff_enabled", false).apply()
        loopThread?.interrupt()
        main.post { webView?.destroy(); webView = null }
    }

    /** Activity 销毁时调用：停掉线程但不改用户的开关设置 */
    fun release() {
        running = false
        relay.stopRelay()
        loopThread?.interrupt()
        main.post { webView?.destroy(); webView = null }
    }

    // ---------- 向盒子长轮询取任务 ----------
    private fun loop() {
        while (running) {
            val base = prefs.getString("server_addr", "") ?: ""
            if (base.isEmpty()) { sleepQuiet(3000); continue }
            try {
                val conn = URL("$base/api/anime/worker/poll?id=$deviceId").openConnection() as HttpURLConnection
                conn.connectTimeout = 5000
                conn.readTimeout = 35000 // 盒子最长 25 秒才会回复 204
                if (conn.responseCode == 200) {
                    val job = JSONObject(conn.inputStream.bufferedReader().readText())
                    report(base, runJob(job))
                } else {
                    conn.inputStream?.close()
                }
            } catch (e: InterruptedException) {
                return
            } catch (e: Exception) {
                Log.d("SniffWorker", "轮询失败: ${e.message}")
                sleepQuiet(3000)
            }
        }
    }

    private fun sleepQuiet(ms: Long) {
        try { Thread.sleep(ms) } catch (e: InterruptedException) { /* 被 stop 打断 */ }
    }

    private fun report(base: String, res: JSONObject) {
        try {
            val conn = URL("$base/api/anime/worker/result").openConnection() as HttpURLConnection
            conn.requestMethod = "POST"
            conn.connectTimeout = 5000
            conn.readTimeout = 8000
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(res.toString().toByteArray()) }
            conn.responseCode
            conn.inputStream?.close()
        } catch (e: Exception) {
            Log.d("SniffWorker", "回报失败: ${e.message}")
        }
    }

    // ---------- 执行一个嗅探任务 ----------
    private fun runJob(job: JSONObject): JSONObject {
        val a = Active(job)
        val timeout = job.optLong("timeoutMs", 15000).coerceIn(5000, 30000)
        active = a
        main.post { startOnMain(a, timeout) }
        a.latch.await(timeout + 3000, TimeUnit.MILLISECONDS)
        a.finished = true
        active = null
        main.post { webView?.let { it.stopLoading(); it.loadUrl("about:blank") } }

        val res = JSONObject().put("jobId", job.optString("id"))
        val hit = a.result
        if (hit == null) {
            return res.put("ok", false).put("error", "手机没有嗅探到视频地址（可能需要验证码或页面结构特殊）")
        }
        // 其余命中的地址作为备选一并回报：主地址盒子读不到时（广告、盒子连不上的 CDN）盒子依次试它们
        val alts = JSONArray()
        synchronized(a.hits) { a.hits.toList() }
            .filter { it.url != hit.url }
            .distinctBy { it.url }
            .sortedByDescending { it.score }
            .take(3)
            .forEach { alts.put(candidateJson(it, CookieManager.getInstance().getCookie(it.url))) }
        val main = candidateJson(hit, a.cookie)
        if (relay.port > 0) {
            res.put("relayPort", relay.port).put("relayToken", relay.newTicket())
        }
        return res.put("ok", true).put("url", main.get("url")).put("kind", main.get("kind"))
            .put("headers", main.get("headers")).put("alts", alts)
    }

    private fun candidateJson(hit: Hit, cookie: String?): JSONObject {
        val headers = JSONObject()
        for ((key, value) in hit.headers) {
            when {
                key.equals("Referer", true) -> headers.put("Referer", value)
                key.equals("User-Agent", true) -> headers.put("User-Agent", value)
                key.lowercase() in FORWARD_HIT_HEADERS -> headers.put(key, value)
            }
        }
        if (!headers.has("User-Agent")) headers.put("User-Agent", currentUA)
        cookie?.let { headers.put("Cookie", it) }
        return JSONObject().put("url", hit.url)
            .put("kind", if (hit.score == 3) "hls" else "mp4")
            .put("headers", headers)
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun createWebView(): WebView {
        val wv = WebView(app)
        // 不挂在界面上的 WebView 默认是 0×0，播放器按容器尺寸初始化会失败：给它一个桌面视口大小
        wv.measure(
            android.view.View.MeasureSpec.makeMeasureSpec(1280, android.view.View.MeasureSpec.EXACTLY),
            android.view.View.MeasureSpec.makeMeasureSpec(720, android.view.View.MeasureSpec.EXACTLY)
        )
        wv.layout(0, 0, 1280, 720)
        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            mediaPlaybackRequiresUserGesture = false
            mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
            blockNetworkImage = true            // 嗅探不需要图片，省流量加快加载
            loadsImagesAutomatically = false
            setSupportMultipleWindows(false)
            javaScriptCanOpenWindowsAutomatically = false
        }
        wv.webViewClient = object : WebViewClient() {
            override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
                // 这个 WebView 看不见，别让页面里的视频出声
                view.evaluateJavascript(MUTE_JS, null)
            }

            override fun onPageFinished(view: WebView, url: String?) {
                view.evaluateJavascript(POKE_JS, null)
            }

            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                val a = active
                if (a != null && !a.finished) {
                    val u = request.url.toString()
                    val hdrs = request.requestHeaders ?: emptyMap()
                    val score = if (request.isForMainFrame) 0 else mediaScore(u, hdrs)
                    if (score > 0 && !isAd(u)) {
                        Log.d("SniffWorker", "命中 score=$score range=${hdrs.keys.any { it.equals("Range", true) }} $u")
                        a.hits.add(Hit(u, hdrs, score))
                        // 第一个命中后再等一小会儿，优先挑 m3u8、避开页面先加载的片头小广告。
                        // 先到的是 mp4 时多等几秒，给真正的 m3u8 留出出现的时间；m3u8 一到就只再等 1.2 秒。
                        if (score == 3) {
                            a.pickScheduled = true
                            main.postDelayed({ pick(a) }, 1200)
                        } else if (!a.pickScheduled) {
                            a.pickScheduled = true
                            main.postDelayed({ pick(a) }, 3500)
                        }
                    }
                }
                return null // 一律放行，让页面照常加载
            }
        }
        wv.webChromeClient = object : WebChromeClient() {
            // 页面里的 alert/confirm 会卡住脚本，直接放行
            override fun onJsAlert(view: WebView?, url: String?, message: String?, result: JsResult): Boolean { result.confirm(); return true }
            override fun onJsConfirm(view: WebView?, url: String?, message: String?, result: JsResult): Boolean { result.cancel(); return true }
        }
        return wv
    }

    private fun startOnMain(a: Active, timeout: Long) {
        val wv = webView ?: createWebView().also { webView = it }
        val ua = a.job.optString("ua")
        currentUA = if (ua.isNotEmpty()) ua else DESKTOP_UA
        wv.settings.userAgentString = currentUA
        val headers = HashMap<String, String>()
        val referer = a.job.optString("referer")
        if (referer.isNotEmpty()) headers["Referer"] = referer
        wv.loadUrl(a.job.optString("pageUrl"), headers)
        // 一些播放器要点一下才开始加载视频，隔一会儿就"戳"一次页面
        for (delay in longArrayOf(1500, 3500, 6500, 10000)) {
            main.postDelayed({ if (!a.finished) webView?.evaluateJavascript(POKE_JS, null) }, delay)
        }
        main.postDelayed({ pick(a) }, timeout) // 超时兜底：没有命中就以失败收场
    }

    // 从已有的命中里挑最合适的一个并结束任务
    private fun pick(a: Active) {
        if (a.finished) return
        val hits = synchronized(a.hits) { a.hits.toList() }
        val best = hits.maxByOrNull { it.score } // 同分取先到的
        a.finished = true
        a.result = best
        if (best != null) a.cookie = CookieManager.getInstance().getCookie(best.url)
        a.latch.countDown()
    }

    companion object {
        private val FORWARD_HIT_HEADERS = setOf(
            "origin", "accept", "accept-language", "x-requested-with",
            "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "sec-fetch-user",
            "dnt", "cache-control", "pragma"
        )
        private const val DESKTOP_UA =
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
        // 路径（不含参数）以这些结尾的一定不是要找的视频：网页、脚本、图片、音频、HLS 分片
        private val NOT_VIDEO_PATH = Regex(
            "\\.(html?|php\\d?|aspx?|jsp|js|css|json|xml|txt|png|jpe?g|gif|webp|ico|svg|woff2?|ttf|" +
                "mp3|m4a|aac|wav|ogg|ts|m4s)$", RegexOption.IGNORE_CASE
        )
        private val AD_WORDS = listOf(
            "doubleclick", "googlesyndication", "/ads/", "/ad/", "adserver", "advert", "preroll", "/adv/",
            "adposter" // aafun 播放器的片头广告视频
        )

        // 判断一个请求像不像"视频"。只看路径里的后缀，不看参数里的——
        // 否则 player.html?url=xxx.m3u8 这种播放器网页会被误当成视频。
        // 后缀不在路径里的（云存储下载链接等）靠 Range 头判断：只有 <video> 取流才会带 Range。
        private fun mediaScore(url: String, headers: Map<String, String>): Int {
            val path = (android.net.Uri.parse(url).path ?: "").lowercase()
            if (NOT_VIDEO_PATH.containsMatchIn(path)) return 0
            if (path.endsWith(".m3u8")) return 3
            if (path.endsWith(".mp4") || path.endsWith(".flv")) return 2
            return if (headers.keys.any { it.equals("Range", true) }) 1 else 0
        }
        private fun isAd(url: String): Boolean {
            val l = url.lowercase()
            return AD_WORDS.any { l.contains(it) }
        }

        // 让页面里所有 <video>/<audio> 的 play() 先静音
        private const val MUTE_JS =
            "(function(){try{var p=HTMLMediaElement.prototype.play;if(p.__fc)return;" +
                "HTMLMediaElement.prototype.play=function(){try{this.muted=true;this.volume=0}catch(e){}return p.apply(this,arguments)};" +
                "HTMLMediaElement.prototype.play.__fc=1}catch(e){}})();"

        // 尝试起播：对 <video> 调 play，并点击常见播放器的"开始播放"按钮（含同源 iframe 里的）
        private const val POKE_JS =
            "(function(){try{" +
                "var sels=['.vjs-big-play-button','.dplayer-play-icon','.xgplayer-start','.art-state','.plyr__control--overlaid'," +
                "'.jw-icon-display','[class*=\"play-btn\"]','[class*=\"playBtn\"]','[class*=\"start-btn\"]'];" +
                "function go(d){try{var v=d.querySelector('video');if(v){v.muted=true;var r=v.play();if(r&&r.catch)r.catch(function(){});}" +
                "sels.forEach(function(s){var e=d.querySelector(s);if(e&&e.click)e.click();});" +
                "[].forEach.call(d.querySelectorAll('iframe'),function(f){try{if(f.contentDocument)go(f.contentDocument)}catch(e){}});}catch(e){}}" +
                "go(document);}catch(e){}})();"
    }
}
