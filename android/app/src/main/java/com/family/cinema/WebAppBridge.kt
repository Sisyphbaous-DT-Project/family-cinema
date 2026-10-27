package com.family.cinema

import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.net.Uri
import android.os.Build
import android.webkit.JavascriptInterface
import androidx.core.app.NotificationManagerCompat
import org.json.JSONObject

// 前端（JS）调原生的桥。每个 @JavascriptInterface 方法对应前端 Bridge.xxx。
// 接口一次设计宽，尽量减少"加原生能力必须重装 APK"的情况。
class WebAppBridge(
    private val ctx: Context,
    private val store: CacheStore,
    private val server: LocalHttpServer,
    private val updateMgr: WebUpdateManager,
    private val prefs: SharedPreferences,
    private val isOnlineCheck: () -> Boolean,
    private val onOrientation: (Boolean) -> Unit,
    private val onKeepScreenOn: (Boolean) -> Unit,
    private val onSystemBars: (Int, Boolean) -> Unit,
    private val sniffWorker: SniffWorker,
    private val proxyRouter: ProxyRouter
) {
    // ---- Bangumi 网络：bgm.tv 被墙，前端对它的请求全部从这里走（自动选路见 ProxyRouter） ----
    // method/path/body → 同步返回 {"status":200,"body":"..."} 或 {"error":"..."}
    @JavascriptInterface
    fun bgmRequest(method: String, path: String, body: String): String =
        proxyRouter.requestJson(method, "https://api.bgm.tv$path", body)

    @JavascriptInterface
    fun getBgmProxy(): String = proxyRouter.configJson()

    @JavascriptInterface
    fun setBgmProxy(auto: Boolean, addr: String) { proxyRouter.saveConfig(auto, addr) }

    // 设置页"测试连接"按钮：强制重新选路，返回 {"route":"direct|proxy|dead"}
    @JavascriptInterface
    fun bgmProbe(): String = proxyRouter.probeJson()
    // 解析助手：让这台手机替盒子/电脑嗅探视频地址（详见 SniffWorker）
    // 开/关解析助手：开 = 拉起前台服务（由服务持有助手，退到后台也不断）；关 = 停助手并停服务
    @JavascriptInterface
    fun startSniffWorker(): String { SniffService.start(ctx); return "1" }

    @JavascriptInterface
    fun stopSniffWorker(): String { sniffWorker.stop(); SniffService.stop(ctx); return "1" }

    @JavascriptInterface
    fun isSniffWorkerRunning(): String = if (sniffWorker.isRunning) "1" else "0"

    @JavascriptInterface
    fun getVersion(): String = JSONObject().apply {
        put("app", BuildConfig.VERSION_NAME)
        put("code", BuildConfig.VERSION_CODE)   // 应用内更新比对用：盒子上的 code 比我大就提示更新
        put("web", updateMgr.localWebVersion())
    }.toString()

    // 应用内更新：从盒子下载 APK 并跳系统安装器。
    // 安卓规定最后一步必须由用户在系统弹窗里亲手点"安装"，我们能做到"点更新条→自动下载→自动弹安装"。
    // 在 JS 桥线程同步执行（几 MB 内网秒下），返回 {"ok":true} 或 {"error":"..."}
    @JavascriptInterface
    fun downloadAndInstallApk(url: String): String {
        val f = java.io.File(ctx.cacheDir, "app-update/app.apk")
        return try {
            f.parentFile?.mkdirs()
            val conn = java.net.URL(url).openConnection() as java.net.HttpURLConnection
            conn.connectTimeout = 10000
            conn.readTimeout = 60000
            if (conn.responseCode != 200) return JSONObject().put("error", "下载失败（${conn.responseCode}）").toString()
            conn.inputStream.use { inp -> f.outputStream().use { out -> inp.copyTo(out) } }
            conn.disconnect()
            val uri = androidx.core.content.FileProvider.getUriForFile(ctx, "com.family.cinema.fileprovider", f)
            val i = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, "application/vnd.android.package-archive")
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            ctx.startActivity(i)
            JSONObject().put("ok", true).toString()
        } catch (e: Exception) {
            JSONObject().put("error", "更新失败：${e.message}").toString()
        }
    }

    @JavascriptInterface
    fun isOnline(): String = if (isOnlineCheck()) "1" else "0"

    @JavascriptInterface
    fun getServerAddr(): String = prefs.getString("server_addr", "") ?: ""

    @JavascriptInterface
    fun setServerAddr(addr: String) { prefs.edit().putString("server_addr", addr.trim()).apply() }

    @JavascriptInterface
    fun discoverServer(): String {
        val found = Discovery.discover(ctx)
        if (found != null) setServerAddr(found)
        return found ?: ""
    }

    // 前端把服务器端的流地址交给原生去下载（JS 下不了大文件）
    @JavascriptInterface
    fun downloadVideo(url: String, title: String, itemId: String, epId: String, size: String, coverUrl: String, category: String, epTitle: String): String {
        val i = Intent(ctx, DownloadService::class.java).apply {
            putExtra("url", url); putExtra("title", title); putExtra("itemId", itemId)
            // epTitle 是"第1集"这类集标题，之前误填了片名，导致离线只显示片名不显示集数
            putExtra("epId", epId); putExtra("epTitle", epTitle.ifEmpty { title }); putExtra("size", size)
            putExtra("coverUrl", coverUrl); putExtra("category", category)
        }
        if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
        return "1"
    }

    // 播放时由原生转横屏。网页的 screen.orientation.lock 在用户开了系统旋转锁定时无效，
    // 只有 Activity 层的 requestedOrientation 才掰得动系统设置。
    @JavascriptInterface
    fun setOrientation(orientation: String) {
        onOrientation(orientation == "landscape")
    }

    // 播放器开着就保持屏幕常亮（看视频不熄屏），关掉播放器即恢复系统息屏。
    // 网页的 Wake Lock API 在 http 内网（非 secure context）不可用，只能靠原生 flag。
    @JavascriptInterface
    fun setKeepScreenOn(on: Boolean) {
        onKeepScreenOn(on)
    }

    // 状态栏、底部手势条跟着网页的页面底色走（网页换主题色/切深浅色时调用），和标题栏连成一整块。
    // 记下来，下次打开 App 时网页还没加载出来就先刷成这个颜色，不会先闪一下亮白。
    @JavascriptInterface
    fun setSystemBars(color: String, dark: Boolean) {
        val c = try { android.graphics.Color.parseColor(color) } catch (e: Exception) { return }
        prefs.edit().putInt("bar_color", c).putBoolean("bar_dark", dark).apply()
        onSystemBars(c, dark)
    }

    @JavascriptInterface
    fun listCached(): String = store.listJson()

    // 暂停下载：保留已下载的部分，之后用 downloadVideo 再发起同一集就是断点续传。
    // 只对"在队列里或正在下"的有效；已经停着的也把清单里的速度归零，界面才不会误以为还在下。
    @JavascriptInterface
    fun pauseDownload(epId: String): String {
        DownloadControl.signal(epId, DownloadControl.PAUSE)
        store.markUserPaused(epId) // 记下是手动暂停的：App 重开时不会自己接着下
        return "1"
    }

    // 取消下载：停下来，并删掉已下载的部分和清单项（排队中的也能取消，轮到它时会被跳过）。
    @JavascriptInterface
    fun cancelDownload(epId: String): String {
        DownloadControl.signal(epId, DownloadControl.CANCEL) // 先让下载线程停手并断开连接
        store.remove(epId)                                    // 再删文件和清单，界面立刻看不到它
        return "1"
    }

    // 删除缓存：已缓存好的直接删；还在下载/排队的先取消（以前只删清单，下载线程还在往已删的文件里写）
    @JavascriptInterface
    fun deleteCached(epId: String): String = cancelDownload(epId)

    @JavascriptInterface
    fun getCacheSize(): String = store.sizeJson()

    // 离线模式的数据源：把已缓存内容拼成媒体库结构给前端渲染
    @JavascriptInterface
    fun getLocalLibrary(): String = store.localLibraryJson()

    // 播放本地缓存：返回本地 HTTP server 的地址（支持拖动进度条）
    @JavascriptInterface
    fun playLocalUrl(epId: String): String {
        val f = store.fileFor(epId)
        return if (f.exists()) server.baseUrl() + "/cache/" + f.name else ""
    }

    // 缓存项的本地封面地址（没有就返回空，前端退回图标占位）
    @JavascriptInterface
    fun coverUrl(epId: String): String {
        val f = store.coverFileFor(epId)
        return if (f.exists()) server.baseUrl() + "/cache/" + f.name else ""
    }

    // ---- 观看进度：原生存 SharedPreferences，不按网址隔离 ----
    // 这样在线（盒子 IP）和离线（本地地址）读到的是同一份进度，观看记录才连续。
    @JavascriptInterface
    fun getProg(key: String): String = prefs.getString("prog:$key", "") ?: ""

    @JavascriptInterface
    fun setProg(key: String, pos: Double, dur: Double): String {
        prefs.edit().putString("prog:$key", JSONObject().apply {
            put("pos", pos); put("dur", dur); put("ts", System.currentTimeMillis() / 1000)
        }.toString()).apply()
        return "1"
    }

    @JavascriptInterface
    fun clearProg(key: String): String {
        prefs.edit().remove("prog:$key").apply()
        return "1"
    }

    // ---- 后台保活状态：给"我的"页面显示体检结果和修复入口 ----
    @JavascriptInterface
    fun getBackgroundStatus(): String = JSONObject().apply {
        put("battery", BatteryHelper.isUnrestricted(ctx))          // 是否已进电池白名单
        put("notify", NotificationManagerCompat.from(ctx).areNotificationsEnabled())
    }.toString()

    // 弹系统的"允许后台运行"对话框
    @JavascriptInterface
    fun requestBatteryWhitelist(): String { BatteryHelper.requestWhitelist(ctx); return "1" }

    // 打开本应用的系统设置页（通知开关、自启动、省电策略都在里面）
    @JavascriptInterface
    fun openAppSettings(): String { BatteryHelper.openSettings(ctx); return "1" }

    // ---- 追番提醒（1.8）：有追番时定时去盒子取更新消息，见 FollowWatcher ----
    @JavascriptInterface
    fun setFollowWatch(on: Boolean) = FollowWatcher.setEnabled(ctx, on)

    @JavascriptInterface
    fun getFollowNotifyStatus(): String = FollowWatcher.status(ctx)

    @JavascriptInterface
    fun testFollowNotify(): String =
        if (FollowWatcher.notifyUpdate(ctx, 0, "测试", "第1集", "片源站")) "ok" else "denied"

    @JavascriptInterface
    fun openAutostartSettings(): String { FollowWatcher.openAutostart(ctx); return "1" }

    @JavascriptInterface
    fun openExternal(url: String) {
        try {
            ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        } catch (e: Exception) {}
    }
}
