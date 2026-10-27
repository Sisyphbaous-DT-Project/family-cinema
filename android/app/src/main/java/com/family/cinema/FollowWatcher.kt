package com.family.cinema

import android.app.AlarmManager
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import kotlin.concurrent.thread

// 追番提醒：盒子发现追的番在片源站出了新的一集，会记一条"更新消息"（带编号）；
// 手机每隔二三十分钟去盒子取一次没取过的消息，在通知栏提醒，点一下直接打开这部番。
// 盒子只在家里连得上：人在外面时取不到，回家连上 WiFi 后按编号补发，不会漏。
// 定时用系统闹钟（setAndAllowWhileIdle，省电模式下会被系统合并延后，但不会不来），
// 不常驻后台；手机重启、App 更新后由 FollowBootReceiver 重新定上。
object FollowWatcher {
    private const val TAG = "FollowWatcher"
    private const val INTERVAL = 20 * 60 * 1000L
    private const val CH_ID = "follow"
    private const val REQ_ALARM = 7101

    // prefs 键
    private const val K_ON = "follow_watch"
    private const val K_AFTER = "follow_after"          // 已经取过的最新消息编号；-1 = 还没取过
    private const val K_LAST = "follow_last_check"
    private const val K_ERR = "follow_last_error"

    private fun prefs(ctx: Context): SharedPreferences = ctx.getSharedPreferences("app", Context.MODE_PRIVATE)

    fun isEnabled(ctx: Context) = prefs(ctx).getBoolean(K_ON, false)

    // 网页告诉我们有没有在追的番。刚打开、或很久没取过时马上取一次（回家打开 App 就能看到补发的提醒）
    fun setEnabled(ctx: Context, on: Boolean) {
        val p = prefs(ctx)
        val was = p.getBoolean(K_ON, false)
        p.edit().putBoolean(K_ON, on).apply()
        if (!on) {
            alarm(ctx)?.cancel(pending(ctx))
            return
        }
        val stale = System.currentTimeMillis() - p.getLong(K_LAST, 0) > INTERVAL
        if (!was || stale) schedule(ctx, 5_000)
    }

    fun ensureScheduled(ctx: Context) {
        if (isEnabled(ctx)) schedule(ctx, 60_000)
    }

    fun schedule(ctx: Context, delayMs: Long = INTERVAL) {
        val am = alarm(ctx) ?: return
        val at = System.currentTimeMillis() + delayMs
        try {
            if (Build.VERSION.SDK_INT >= 23) am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, at, pending(ctx))
            else am.set(AlarmManager.RTC_WAKEUP, at, pending(ctx))
        } catch (e: Exception) {
            Log.w(TAG, "定闹钟失败: ${e.message}")
        }
    }

    fun status(ctx: Context): String {
        val p = prefs(ctx)
        return JSONObject().apply {
            put("notify", NotificationManagerCompat.from(ctx).areNotificationsEnabled())
            put("battery", BatteryHelper.isUnrestricted(ctx))
            put("watching", p.getBoolean(K_ON, false))
            put("lastCheck", p.getLong(K_LAST, 0))
            put("lastError", p.getString(K_ERR, "") ?: "")
        }.toString()
    }

    private fun alarm(ctx: Context) = ctx.getSystemService(Context.ALARM_SERVICE) as? AlarmManager

    private fun pending(ctx: Context): PendingIntent = PendingIntent.getBroadcast(
        ctx, REQ_ALARM, Intent(ctx, FollowAlarmReceiver::class.java),
        PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)

    // 去盒子取一次消息（后台线程里调）。返回 false = 盒子说已经没有在追的番了，不用再定时
    fun poll(ctx: Context): Boolean {
        val p = prefs(ctx)
        val after = p.getLong(K_AFTER, -1)
        var base = p.getString("server_addr", "") ?: ""
        var res = if (base.isNotEmpty()) fetch(base, after) else null
        if (res == null) {
            // 盒子换了 IP：在家里 WiFi 下广播找一下
            Discovery.discover(ctx)?.let {
                base = it
                p.edit().putString("server_addr", it).apply()
                res = fetch(base, after)
            }
        }
        val now = System.currentTimeMillis()
        val r = res
        if (r == null) {
            p.edit().putLong(K_LAST, now).putString(K_ERR, "连不上盒子").apply()
            return true
        }
        val latest = r.optLong("latest", 0)
        if (after >= 0 && latest >= after) {
            val evs = r.optJSONArray("events")
            for (i in 0 until (evs?.length() ?: 0)) {
                val e = evs!!.getJSONObject(i)
                notifyUpdate(ctx, e.optInt("subjectId"), e.optString("title"), e.optString("ep"), e.optString("site"))
            }
        }
        // 第一次来（或盒子的消息记录被清过）只记编号，不补发以前的旧消息
        p.edit().putLong(K_AFTER, latest).putLong(K_LAST, now).putString(K_ERR, "").apply()
        return r.optInt("follows", 1) > 0
    }

    private fun fetch(base: String, after: Long): JSONObject? = try {
        val conn = URL("$base/api/anime/follows/events?after=$after").openConnection() as HttpURLConnection
        conn.connectTimeout = 5000
        conn.readTimeout = 8000
        try {
            if (conn.responseCode != 200) null
            else JSONObject(conn.inputStream.bufferedReader().readText())
        } finally {
            conn.disconnect()
        }
    } catch (e: Exception) {
        Log.d(TAG, "取更新消息失败: ${e.message}")
        null
    }

    private fun ensureChannel(ctx: Context) {
        if (Build.VERSION.SDK_INT < 26) return
        val ch = NotificationChannel(CH_ID, "追番更新", NotificationManager.IMPORTANCE_HIGH).apply {
            description = "追的番在片源站出了新的一集时提醒"
        }
        (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).createNotificationChannel(ch)
    }

    // 同一部番只留最新一条（通知编号 = 番剧编号）
    fun notifyUpdate(ctx: Context, subjectId: Int, title: String, ep: String, site: String): Boolean {
        val nm = NotificationManagerCompat.from(ctx)
        if (!nm.areNotificationsEnabled()) return false
        ensureChannel(ctx)
        val open = Intent(ctx, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP
            if (subjectId > 0) putExtra(MainActivity.EXTRA_ROUTE, "/play/$subjectId")
        }
        val pi = PendingIntent.getActivity(ctx, subjectId, open,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val text = listOf(ep.ifEmpty { "新的一集" }, if (site.isNotEmpty()) "$site 已经能看了" else "已经能看了")
            .joinToString(" · ")
        val n = NotificationCompat.Builder(ctx, CH_ID)
            .setSmallIcon(R.drawable.ic_stat_follow)
            .setContentTitle("《$title》更新了")
            .setContentText(text)
            .setContentIntent(pi)
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_RECOMMENDATION)
            .build()
        return try {
            nm.notify(9000 + subjectId % 100000, n)
            true
        } catch (e: SecurityException) {
            false
        }
    }

    // 小米/红米的"自启动"管理页（不开的话手机重启后闹钟定不上）；找不到就打开应用详情页
    fun openAutostart(ctx: Context) {
        val candidates = listOf(
            Intent().setComponent(ComponentName("com.miui.securitycenter", "com.miui.permcenter.autostart.AutoStartManagementActivity")),
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).setData(Uri.parse("package:" + ctx.packageName)),
        )
        for (i in candidates) {
            try {
                ctx.startActivity(i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                return
            } catch (e: Exception) {
            }
        }
    }
}

// 闹钟到点：去盒子取一次消息，再定下一次
class FollowAlarmReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (!FollowWatcher.isEnabled(ctx)) return
        val app = ctx.applicationContext
        val pending = goAsync()
        thread {
            try {
                if (FollowWatcher.poll(app)) FollowWatcher.schedule(app)
                else FollowWatcher.setEnabled(app, false)
            } finally {
                pending.finish()
            }
        }
    }
}

// 手机重启 / App 更新后闹钟会被系统清掉，重新定上
class FollowBootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        FollowWatcher.ensureScheduled(ctx.applicationContext)
    }
}
