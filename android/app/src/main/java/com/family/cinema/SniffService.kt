package com.family.cinema

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat

// 整个进程共用一个解析助手：界面（Activity）被划掉后，前台服务继续持有它，手机放兜里也能接任务。
object SniffHost {
    @Volatile private var worker: SniffWorker? = null

    fun get(ctx: Context, prefs: SharedPreferences): SniffWorker =
        worker ?: synchronized(this) {
            worker ?: SniffWorker(ctx.applicationContext, prefs).also { worker = it }
        }
}

// 解析助手的前台服务：挂一条常驻通知，系统就不会在 App 退到后台后冻结/杀掉它。
// 只在"解析助手"开关打开时运行；关掉开关即停止。
class SniffService : Service() {

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        createChannel()
        val n = buildNotification()
        // 通知必须在 onStartCommand 里尽快挂上（系统硬要求）
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(NOTIF_ID, n)
        }
        val prefs = getSharedPreferences("app", MODE_PRIVATE)
        SniffHost.get(this, prefs).start()
        return START_STICKY // 进程被杀后系统会自动拉起服务，助手跟着恢复
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun createChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            val ch = NotificationChannel(CH_ID, "解析助手", NotificationManager.IMPORTANCE_MIN)
            (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).createNotificationChannel(ch)
        }
    }

    private fun buildNotification(): Notification {
        val pi = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        return NotificationCompat.Builder(this, CH_ID)
            .setContentTitle("解析助手运行中")
            .setContentText("帮助盒子解析视频地址，必要时为播放和下载代取视频流")
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentIntent(pi)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .build()
    }

    companion object {
        private const val CH_ID = "sniff_worker"
        private const val NOTIF_ID = 4202

        fun start(ctx: Context) {
            val i = Intent(ctx, SniffService::class.java)
            try {
                if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
            } catch (e: Exception) {
                // 极少数情况下系统不允许从后台拉起前台服务：退回只在界面存活期间工作
                SniffHost.get(ctx, ctx.getSharedPreferences("app", Context.MODE_PRIVATE)).start()
            }
        }

        fun stop(ctx: Context) {
            ctx.stopService(Intent(ctx, SniffService::class.java))
        }
    }
}
