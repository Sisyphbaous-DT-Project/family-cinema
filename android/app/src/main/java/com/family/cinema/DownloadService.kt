package com.family.cinema

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.net.wifi.WifiManager
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import androidx.core.app.NotificationCompat
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

// 前台下载服务：断点续传把视频下载进缓存目录，通知栏显示进度。
// 服务端 /api/stream 支持 Range，所以中断后能接着下，不用从头再来。
//
// 后台保活三件套：
//   1. 前台服务 + dataSync 类型 —— 回桌面/切别的 App 不会被系统回收
//   2. PARTIAL_WAKE_LOCK    —— 锁屏后 CPU 不进 Doze，下载线程不被挂起
//   3. WIFI_MODE_FULL_HIGH_PERF —— 锁屏后 WiFi 网卡不进省电，速度不掉
// 另外网络中断会自动退避重试，不用用户手动再点一次。
class DownloadService : Service() {

    companion object {
        const val CH_ID = "download"
        const val NOTIF_ID = 1
        private const val MAX_RETRY = 6                          // 单集最多重试次数
        private const val LOCK_TIMEOUT = 6L * 60 * 60 * 1000     // 持锁兜底超时，防止泄漏耗电
    }

    // 单次尝试的结果：成功 / 可重试（网络问题）/ 致命（资源不存在，重试无意义）
    // STOPPED = 用户点了暂停（保留已下载部分，不重试）；CANCELLED = 用户点了取消（删掉已下载部分）
    private enum class R { DONE, RETRY, FATAL, STOPPED, CANCELLED }

    private lateinit var store: CacheStore
    private val pool = Executors.newSingleThreadExecutor() // 单线程排队，避免抢占带宽
    private val queued = AtomicInteger(0)                  // 队列里还没干完的任务数
    private var wakeLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null

    override fun onCreate() {
        super.onCreate()
        store = CacheStore.get(this)
        createChannel()
        acquireLocks()
    }

    // 锁屏后系统会让 CPU 进 Doze、WiFi 网卡进省电模式，下载会卡死或掉到几十 KB/s。
    // 服务活着就等于有下载任务在跑，所以在这儿持锁、onDestroy 释放，不会泄漏。
    private fun acquireLocks() {
        try {
            val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
            wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "family-cinema:download").apply {
                setReferenceCounted(false)
                acquire(LOCK_TIMEOUT)   // 带超时兜底，万一漏了释放也不会一直耗电
            }
        } catch (e: Exception) { /* 拿不到锁也要能下，只是锁屏可能被限速 */ }
        try {
            val wm = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
            wifiLock = wm.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "family-cinema:download").apply {
                setReferenceCounted(false)
                acquire()
            }
        } catch (e: Exception) {}
    }

    private fun releaseLocks() {
        try { wakeLock?.let { if (it.isHeld) it.release() } } catch (e: Exception) {}
        try { wifiLock?.let { if (it.isHeld) it.release() } } catch (e: Exception) {}
        wakeLock = null
        wifiLock = null
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val url = intent?.getStringExtra("url")
        if (url == null) {
            // 没活儿可干；只有队列也空的时候才收工，否则别打断正在下的任务
            if (queued.get() == 0) stopSelf(startId)
            return START_NOT_STICKY
        }
        val title = intent.getStringExtra("title") ?: ""
        val itemId = intent.getStringExtra("itemId") ?: title
        val epId = intent.getStringExtra("epId") ?: url
        val epTitle = intent.getStringExtra("epTitle") ?: title
        val size = intent.getStringExtra("size")?.toLongOrNull() ?: 0L
        val coverUrl = intent.getStringExtra("coverUrl") ?: ""
        val category = intent.getStringExtra("category") ?: "movie"

        val n = queued.incrementAndGet()
        // 清单里立刻登记"排队中"，界面马上能看到（以前要等轮到才写进清单）
        val already = DownloadControl.isActive(epId)
        val file = store.fileFor(epId)
        store.markQueued(CacheItem(
            itemId = itemId, epId = epId, title = title, epTitle = epTitle, path = epId,
            size = size, done = false, downloaded = if (file.exists()) file.length() else 0L,
            category = category, url = url, coverUrl = coverUrl), already)
        DownloadControl.enter(epId) // 登记"在队列里/正在跑"，暂停和取消才认得出这个任务
        // 前台通知必须在 onStartCommand 里尽快挂上（系统硬要求），否则服务会被判违规杀掉
        startForeground(NOTIF_ID, buildNotification(
            if (n > 1) "排队中 · 共 $n 个任务" else "准备下载", "$title $epTitle", -1))

        pool.execute {
            try {
                download(url, title, itemId, epId, epTitle, size, coverUrl, category)
            } finally {
                queued.decrementAndGet()
                DownloadControl.leave(epId)
                // 关键：必须用带 startId 的 stopSelf。无参版本会不管队列直接销毁服务，
                // 导致"整部缓存"只下完第一集，剩下的任务全被 shutdownNow 静默丢弃。
                stopSelf(startId)
            }
        }
        // 被系统杀掉后重启时重新投递 Intent（START_STICKY 拿到的 intent 是 null，参数全丢）
        return START_REDELIVER_INTENT
    }

    // 带自动重试的下载。锁屏时网络抖一下就断，之前直接标记失败要用户手动再点，
    // 现在退避重试，等网络回来自己接着下。
    private fun download(url: String, title: String, itemId: String, epId: String,
                         epTitle: String, size: Long, coverUrl: String, category: String) {
        // 排队等候期间就被暂停/取消了：这一集还没开始，直接跳过
        // （取消时清单项和文件已经由桥接口删掉；暂停的话桥接口已把清单项标成手动暂停）
        if (DownloadControl.take(epId) != DownloadControl.NONE) return
        val file = store.fileFor(epId)
        // url / coverUrl 一并落盘：进程被杀后靠它们重建下载任务（断点续传接着下）。
        // 新建的清单项 queued/userPaused 都是 false：轮到了，从"排队中"变成"下载中"
        store.upsert(CacheItem(
            itemId = itemId, epId = epId, title = title, epTitle = epTitle, path = epId,
            size = size, done = false, downloaded = if (file.exists()) file.length() else 0L,
            speedBps = 0, category = category, url = url, coverUrl = coverUrl))

        var attempt = 0
        while (true) {
            when (attemptOnce(url, epId, size, file, title, epTitle)) {
                R.DONE -> {
                    if (coverUrl.isNotEmpty()) downloadCover(coverUrl, epId)
                    notify(buildNotification("下载完成", "$title $epTitle", 100))
                    return
                }
                R.FATAL -> {
                    markPaused(epId, file)
                    notify(buildNotification("下载失败 · 资源不可用", "$title $epTitle", 0))
                    return
                }
                R.STOPPED -> {
                    // 用户点了暂停：已下载的部分留着，不重试，之后"继续"会断点续传
                    markPaused(epId, file)
                    notify(buildNotification("已暂停", "$title $epTitle", -1))
                    return
                }
                R.CANCELLED -> {
                    // 用户点了取消：连文件带清单项一起删掉
                    store.remove(epId)
                    notify(buildNotification("已取消", "$title $epTitle", -1))
                    return
                }
                R.RETRY -> {
                    attempt++
                    markPaused(epId, file)   // 进度落盘，界面显示停在这儿
                    if (attempt > MAX_RETRY) {
                        notify(buildNotification("下载失败 · 已重试 $MAX_RETRY 次", "$title $epTitle", 0))
                        return
                    }
                    // 退避等待 5s→10s→20s→30s→30s…，给网络恢复留时间
                    val wait = (5000L shl (attempt - 1)).coerceAtMost(30000L)
                    notify(buildNotification(
                        "网络中断 · ${wait / 1000}秒后重试（第 $attempt 次）", "$title $epTitle", -1))
                    // 等待期间也要能响应暂停/取消，所以不能一觉睡到底
                    when (waitOrStop(epId, wait)) {
                        R.STOPPED -> {
                            notify(buildNotification("已暂停", "$title $epTitle", -1))
                            return
                        }
                        R.CANCELLED -> {
                            store.remove(epId)
                            notify(buildNotification("已取消", "$title $epTitle", -1))
                            return
                        }
                        R.FATAL -> return // 线程被中断（服务被销毁），进度已落盘，下次启动再续
                        else -> { /* 等够了，进入下一次尝试 */ }
                    }
                }
            }
        }
    }

    // 单次尝试。返回 DONE/RETRY/FATAL 交给上层决定要不要再来一遍。
    private fun attemptOnce(url: String, epId: String, size: Long, file: File,
                            title: String, epTitle: String): R {
        val stopAtStart = controlled(epId) // 开始前（或重试间隙）就被暂停/取消了
        if (stopAtStart != null) return stopAtStart
        val existing = if (file.exists()) file.length() else 0L
        // 已经齐了（比如上次断在最后一刻）就别再连了
        if (size > 0 && existing >= size) {
            store.updateProgress(epId, existing, true, 0)
            return R.DONE
        }
        var conn: HttpURLConnection? = null
        try {
            conn = URL(url).openConnection() as HttpURLConnection
            DownloadControl.attach(epId, conn) // 让暂停/取消能从别的线程把这条连接断开，不用等它自己超时
            val stopAfterConnect = controlled(epId)
            if (stopAfterConnect != null) return stopAfterConnect
            conn.connectTimeout = 8000
            conn.readTimeout = 20000
            if (existing > 0) conn.setRequestProperty("Range", "bytes=$existing-")
            val code = conn.responseCode
            // 4xx 是资源不存在或没权限，重试多少次结果都一样，直接判死
            if (code in 400..499) return R.FATAL
            if (code != 200 && code != 206) return R.RETRY
            val total = if (size > 0) size
                else conn.getHeaderField("Content-Range")?.substringAfter("/")?.toLongOrNull()
                    ?: conn.contentLengthLong.let { if (it > 0) it + existing else 0L }

            conn.inputStream.use { input ->
                FileOutputStream(file, existing > 0).use { out ->
                    val buf = ByteArray(64 * 1024)
                    var downloaded = existing
                    var lastTick = System.currentTimeMillis()
                    var lastBytes = existing
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        // 暂停/取消：这一块不写了，已经写下的部分由上层决定留（暂停）还是删（取消）
                        val stop = controlled(epId)
                        if (stop != null) return stop
                        out.write(buf, 0, n)
                        downloaded += n
                        // 服务被销毁时线程会被 interrupt，此时停手保留进度，下次续传
                        if (Thread.currentThread().isInterrupted) {
                            store.updateProgress(epId, downloaded, false, 0)
                            return R.RETRY
                        }
                        val now = System.currentTimeMillis()
                        // 每秒记一次进度和速度（原来每个 64KB 缓冲都写一次清单，太频繁）
                        if (now - lastTick > 1000) {
                            val speed = (downloaded - lastBytes) * 1000 / (now - lastTick)
                            lastBytes = downloaded
                            lastTick = now
                            store.updateProgress(epId, downloaded, false, speed)
                            if (total > 0) {
                                val pct = (downloaded * 100 / total).toInt()
                                notify(buildNotification(
                                    "下载中 $pct%   ${fmtSpeed(speed)}", "$title $epTitle", pct))
                            }
                        }
                    }
                    out.flush()
                    // 流正常结束但字节数不够 → 下了半截，当可重试处理（断点续传接着下）
                    if (total > 0 && downloaded < total) {
                        store.updateProgress(epId, downloaded, false, 0)
                        return R.RETRY
                    }
                    store.updateProgress(epId, downloaded, true, 0)
                }
            }
            return R.DONE
        } catch (e: Exception) {
            // 用户暂停/取消时我们自己把连接断开了，read 会因此抛异常——这不是网络故障，别当成要重试
            return controlled(epId) ?: R.RETRY
        } finally {
            DownloadControl.detach(epId)
            try { conn?.disconnect() } catch (e: Exception) {}
        }
    }

    // 取走并翻译这一集的暂停/取消指令；没有指令返回 null
    private fun controlled(epId: String): R? = when (DownloadControl.take(epId)) {
        DownloadControl.PAUSE -> R.STOPPED
        DownloadControl.CANCEL -> R.CANCELLED
        else -> null
    }

    // 重试前的退避等待：每 250ms 看一眼有没有暂停/取消。
    // 返回 null = 等够了；STOPPED/CANCELLED = 用户的指令；FATAL = 线程被中断（服务销毁）
    private fun waitOrStop(epId: String, ms: Long): R? {
        var left = ms
        while (left > 0) {
            val stop = controlled(epId)
            if (stop != null) return stop
            try { Thread.sleep(250) } catch (e: InterruptedException) { return R.FATAL }
            left -= 250
        }
        return controlled(epId)
    }

    // 保留已下载部分并标记未完成，下次重试断点续传
    private fun markPaused(epId: String, file: File) {
        store.updateProgress(epId, if (file.exists()) file.length() else 0L, false, 0)
    }

    // 顺带把封面存到本地，缓存页才能显示海报（在线离线都有）
    private fun downloadCover(coverUrl: String, epId: String) {
        try {
            val conn = URL(coverUrl).openConnection() as HttpURLConnection
            conn.connectTimeout = 5000
            conn.readTimeout = 8000
            if (conn.responseCode == 200) {
                store.coverFileFor(epId).writeBytes(conn.inputStream.readBytes())
            }
        } catch (e: Exception) { /* 封面失败不影响视频下载 */ }
    }

    private fun fmtSpeed(bps: Long): String = when {
        bps <= 0 -> ""
        bps >= 1024 * 1024 -> String.format("%.1f MB/s", bps / 1048576.0)
        else -> String.format("%.0f KB/s", bps / 1024.0)
    }

    private fun createChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            val ch = NotificationChannel(CH_ID, "视频缓存", NotificationManager.IMPORTANCE_LOW)
            (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).createNotificationChannel(ch)
        }
    }

    private fun buildNotification(title: String, text: String, progress: Int): Notification {
        // 点通知回到 App
        val pi = android.app.PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            android.app.PendingIntent.FLAG_UPDATE_CURRENT or android.app.PendingIntent.FLAG_IMMUTABLE)
        val b = NotificationCompat.Builder(this, CH_ID)
            .setContentTitle(title).setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_download)
            .setContentIntent(pi)
            .setOnlyAlertOnce(true)
        if (progress in 0..99) { b.setOngoing(true); b.setProgress(100, progress, progress <= 0) }
        return b.build()
    }

    private fun notify(n: Notification) {
        try {
            (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).notify(NOTIF_ID, n)
        } catch (e: Exception) { /* 用户关了通知权限也不能影响下载本身 */ }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        pool.shutdownNow()
        releaseLocks()
        super.onDestroy()
    }
}
