package com.family.cinema

import android.content.Context
import android.util.Log
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

// 热更新：在线时把服务器最新前端（index/app.js/style.css）拉到本地，
// 这样一是离线时能用上次下载的界面，二是配合"只改服务器、App 不重装"的诉求。
// 版本号以 /api/web-manifest 的 version 为准（服务端按文件内容哈希生成）。
class WebUpdateManager(private val ctx: Context) {
    private val webDir: File get() = File(ctx.filesDir, "web").apply { mkdirs() }
    private val manifestFile: File get() = File(webDir, "web-manifest.json")
    private val files = listOf("index.html", "app.js", "style.css")

    fun localWebVersion(): String {
        return try {
            if (manifestFile.exists()) JSONObject(manifestFile.readText()).optString("version") else ""
        } catch (e: Exception) { "" }
    }

    // 在线时调用：比对服务器版本，有更新或本地文件缺失就下载补齐。失败静默（下次启动再试）。
    fun syncFrom(serverBase: String) {
        try {
            val manifestText = httpGet("$serverBase/api/web-manifest") ?: return
            val remoteVer = JSONObject(manifestText).optString("version")
            if (remoteVer.isEmpty()) return
            val versionChanged = remoteVer != localWebVersion()
            // 版本变了，或某个文件本地缺失，都重新下载该文件（防止上次没下全）
            files.forEach { name ->
                val f = File(webDir, name)
                if (versionChanged || !f.exists()) {
                    val body = httpGet("$serverBase/$name") ?: return@forEach
                    f.writeText(body)
                }
            }
            // 全部文件齐了才写 manifest，保证 manifest 和磁盘文件一致
            if (files.all { File(webDir, it).exists() }) {
                manifestFile.writeText(manifestText)
                Log.d("WebUpdate", "前端已更新到 $remoteVer")
            }
        } catch (e: Exception) {
            Log.d("WebUpdate", "同步前端失败: ${e.message}")
        }
    }

    fun hasLocalWeb(): Boolean = File(webDir, "index.html").exists()

    private fun httpGet(url: String): String? {
        return try {
            val conn = URL(url).openConnection() as HttpURLConnection
            conn.connectTimeout = 5000
            conn.readTimeout = 8000
            conn.setRequestProperty("Cache-Control", "no-cache")
            if (conn.responseCode != 200) return null
            conn.inputStream.bufferedReader().readText()
        } catch (e: Exception) { null }
    }
}
