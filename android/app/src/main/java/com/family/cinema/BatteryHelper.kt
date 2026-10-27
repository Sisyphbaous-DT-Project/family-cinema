package com.family.cinema

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings

// 电池优化白名单。前台服务 + WakeLock 只是让系统"正常情况下"不动我们，
// 但 HyperOS/MIUI 这类系统的省电策略会额外掐后台网络，必须进白名单才稳。
// 这东西只能由用户点头，代码只负责检测状态和把用户送到对应设置页。
object BatteryHelper {

    fun isUnrestricted(ctx: Context): Boolean {
        if (Build.VERSION.SDK_INT < 23) return true
        return try {
            val pm = ctx.getSystemService(Context.POWER_SERVICE) as PowerManager
            pm.isIgnoringBatteryOptimizations(ctx.packageName)
        } catch (e: Exception) { false }
    }

    // 直接弹系统的"允许后台运行"对话框，用户点一下就完事，不用自己翻设置
    fun requestWhitelist(ctx: Context) {
        if (Build.VERSION.SDK_INT < 23) return
        try {
            ctx.startActivity(
                Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
                    .setData(Uri.parse("package:" + ctx.packageName))
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        } catch (e: Exception) {
            openSettings(ctx)   // 个别机型没有这个 Action，退回设置页
        }
    }

    // 兜底：打开本应用的系统设置页，用户手动改省电策略
    fun openSettings(ctx: Context) {
        try {
            ctx.startActivity(
                Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                    .setData(Uri.parse("package:" + ctx.packageName))
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        } catch (e: Exception) {}
    }
}
