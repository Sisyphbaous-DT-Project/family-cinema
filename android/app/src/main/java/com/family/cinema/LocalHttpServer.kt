package com.family.cinema

import android.content.Context
import fi.iki.elonen.NanoHTTPD
import java.io.File
import java.io.FileInputStream

// 本地 HTTP server（仅 127.0.0.1，本机访问），两个用途：
//  - /cache/*  → filesDir/cache（已缓存的视频、封面，支持 Range 拖动进度条）
//  - 其他       → filesDir/web（离线时的前端界面文件）
// 统一走 HTTP 而不是 file://，避开 WebView 对 file:// 的 ERR_ACCESS_DENIED 限制。
class LocalHttpServer(ctx: Context, port: Int = 0) : NanoHTTPD("127.0.0.1", port) {

    private val cacheDir: File = File(ctx.filesDir, "cache")
    private val webDir: File = File(ctx.filesDir, "web")
    private val store = CacheStore.get(ctx) // 单例，和 Bridge / 下载服务是同一份

    override fun serve(session: IHTTPSession): Response {
        val uri = session.uri
        return when {
            // 离线时前端还在用"找服务器"的半截地址（/api/stream、/api/cover），
            // 这里也认这两个接口：按 path（就是 epId）去本地缓存找文件返回。
            // 这样前端在线离线走同一套地址，一行不用改。
            uri == "/api/stream" -> serveCacheFile(store.fileFor(session.parms["path"] ?: ""), session)
            uri == "/api/cover" -> serveCacheFile(store.coverFileFor(session.parms["path"] ?: ""), session)
            uri.startsWith("/cache/") -> serveFromDir(cacheDir, uri.removePrefix("/cache/"), session)
            else -> {
                // 离线时的前端界面文件
                serveFromDir(webDir, uri.trimStart('/').ifEmpty { "index.html" }, session)
            }
        }
    }

    private fun serveCacheFile(file: File, session: IHTTPSession): Response {
        if (!file.exists() || !file.isFile) {
            return NanoHTTPD.newFixedLengthResponse(Response.Status.NOT_FOUND, "text/plain", "not found")
        }
        return serveFile(session.headers, file)
    }

    private fun serveFromDir(dir: File, name: String, session: IHTTPSession): Response {
        val file = File(dir, name)
        // 防路径穿越：必须落在目标目录内
        if (!file.exists() || !file.isFile || !file.canonicalPath.startsWith(dir.canonicalPath)) {
            return NanoHTTPD.newFixedLengthResponse(Response.Status.NOT_FOUND, "text/plain", "not found")
        }
        return serveFile(session.headers, file)
    }

    private fun mimeFor(name: String): String = when {
        name.endsWith(".html") -> "text/html; charset=utf-8"
        name.endsWith(".js") -> "application/javascript"
        name.endsWith(".css") -> "text/css"
        name.endsWith(".cover") || name.endsWith(".jpg") -> "image/jpeg"
        name.endsWith(".png") -> "image/png"
        else -> "video/mp4"
    }

    private fun serveFile(header: Map<String, String>, file: File): Response {
        val mime = mimeFor(file.name)
        val etag = Integer.toHexString((file.absolutePath + file.lastModified() + file.length()).hashCode())
        val range = header["range"]
        var startFrom = 0L
        var endAt = -1L
        if (range != null && range.startsWith("bytes=")) {
            val r = range.substring(6).split("-")
            try { if (r[0].isNotEmpty()) startFrom = r[0].toLong() } catch (e: Exception) {}
            try { if (r.size > 1 && r[1].isNotEmpty()) endAt = r[1].toLong() } catch (e: Exception) {}
        }
        val fileLen = file.length()
        if (range == null && etag == header["if-none-match"]) {
            return NanoHTTPD.newFixedLengthResponse(Response.Status.NOT_MODIFIED, mime, "")
                .apply { addHeader("ETag", etag) }
        }
        return try {
            if (range != null && startFrom >= 0) {
                if (endAt < 0 || endAt >= fileLen) endAt = fileLen - 1
                val len = endAt - startFrom + 1
                val fis = FileInputStream(file)
                fis.skip(startFrom)
                NanoHTTPD.newFixedLengthResponse(Response.Status.PARTIAL_CONTENT, mime, fis, len).apply {
                    addHeader("Content-Range", "bytes $startFrom-$endAt/$fileLen")
                    addHeader("Accept-Ranges", "bytes")
                    addHeader("ETag", etag)
                }
            } else {
                NanoHTTPD.newFixedLengthResponse(Response.Status.OK, mime, FileInputStream(file), fileLen).apply {
                    addHeader("Accept-Ranges", "bytes")
                    addHeader("ETag", etag)
                }
            }
        } catch (e: Exception) {
            NanoHTTPD.newFixedLengthResponse(Response.Status.INTERNAL_ERROR, "text/plain", "error")
        }
    }

    fun baseUrl(): String = "http://127.0.0.1:$listeningPort"
}
