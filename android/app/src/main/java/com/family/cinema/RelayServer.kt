package com.family.cinema

import android.util.Log
import fi.iki.elonen.NanoHTTPD
import java.io.ByteArrayInputStream
import java.io.FilterInputStream
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.URL
import java.util.Base64
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap

// 手机代取：解析助手开启时才监听，凭一小时有效的随机令牌转发公网视频流。
class RelayServer : NanoHTTPD("0.0.0.0", 0) {
    private val tickets = ConcurrentHashMap<String, Long>()

    @Volatile var port = -1
        private set

    fun startRelay(): Int {
        if (port >= 0) return port
        return try {
            start()
            port = listeningPort
            port
        } catch (e: Exception) {
            Log.w("RelayServer", "启动失败", e)
            -1
        }
    }

    fun stopRelay() {
        stop()
        tickets.clear()
        port = -1
    }

    fun newTicket(): String {
        val now = System.currentTimeMillis()
        tickets.entries.removeAll { it.value <= now }
        return UUID.randomUUID().toString().also { tickets[it] = now + TICKET_TTL }
    }

    override fun serve(session: IHTTPSession): Response {
        if (session.uri != "/relay") return text(Response.Status.NOT_FOUND, "not found")
        if (session.method != Method.GET) return text(Response.Status.METHOD_NOT_ALLOWED, "method not allowed")
        val token = session.headers["x-relay-token"] ?: ""
        val expires = tickets[token] ?: return text(Response.Status.FORBIDDEN, "代取令牌无效")
        if (expires <= System.currentTimeMillis()) {
            tickets.remove(token)
            return text(Response.Status.FORBIDDEN, "代取令牌已过期")
        }
        val encoded = session.parameters["u"]?.firstOrNull() ?: return text(Response.Status.BAD_REQUEST, "缺少视频地址")
        val raw = try {
            String(Base64.getUrlDecoder().decode(encoded), Charsets.UTF_8)
        } catch (e: Exception) {
            return text(Response.Status.BAD_REQUEST, "视频地址编码不对")
        }
        val target = try {
            URL(raw)
        } catch (e: Exception) {
            return text(Response.Status.BAD_REQUEST, "视频地址不对")
        }
        if ((target.protocol != "http" && target.protocol != "https") || !isPublicHost(target.host)) {
            return text(Response.Status.FORBIDDEN, "只允许公网视频地址")
        }
        return fetch(target, session)
    }

    private fun isPublicHost(host: String): Boolean {
        if (host.equals("localhost", ignoreCase = true)) return false
        return try {
            val addrs = InetAddress.getAllByName(host)
            addrs.isNotEmpty() && addrs.all {
                !it.isAnyLocalAddress && !it.isLoopbackAddress && !it.isLinkLocalAddress &&
                    !it.isSiteLocalAddress && !it.isMulticastAddress
            }
        } catch (e: Exception) {
            false
        }
    }

    private fun fetch(target: URL, session: IHTTPSession): Response {
        var conn: HttpURLConnection? = null
        try {
            val c = target.openConnection() as HttpURLConnection
            conn = c
            c.connectTimeout = 15000
            c.readTimeout = 60000
            c.instanceFollowRedirects = true
            c.setRequestProperty("Accept-Encoding", "identity")
            for ((incoming, outgoing) in FORWARD_HEADERS) {
                session.headers[incoming]?.let { c.setRequestProperty(outgoing, it) }
            }
            val code = c.responseCode
            val status = Response.Status.lookup(code) ?: Response.Status.INTERNAL_ERROR
            val source = if (code in 200..299) c.inputStream else c.errorStream ?: ByteArrayInputStream(ByteArray(0))
            val stream = object : FilterInputStream(source) {
                override fun close() {
                    try { super.close() } finally { c.disconnect() }
                }
            }
            val mime = c.contentType?.substringBefore(';')?.trim().orEmpty().ifEmpty { "application/octet-stream" }
            val length = c.contentLengthLong
            val response = if (length >= 0) {
                NanoHTTPD.newFixedLengthResponse(status, mime, stream, length)
            } else {
                NanoHTTPD.newChunkedResponse(status, mime, stream)
            }
            for (name in RESPONSE_HEADERS) {
                c.getHeaderField(name)?.let { response.addHeader(name, it) }
            }
            response.addHeader("X-Upstream-Final-URL", c.url.toString())
            return response
        } catch (e: Exception) {
            conn?.disconnect()
            Log.d("RelayServer", "代取 ${target.host} 失败: ${e.message}")
            return text(Response.Status.INTERNAL_ERROR, "手机代取失败：${e.message}")
        }
    }

    private fun text(status: Response.Status, message: String): Response =
        NanoHTTPD.newFixedLengthResponse(status, "text/plain; charset=utf-8", message)

    companion object {
        private const val TICKET_TTL = 60 * 60 * 1000L
        private val FORWARD_HEADERS = mapOf(
            "range" to "Range",
            "referer" to "Referer",
            "user-agent" to "User-Agent",
            "cookie" to "Cookie",
            "accept" to "Accept",
            "origin" to "Origin",
            "accept-language" to "Accept-Language",
            "x-requested-with" to "X-Requested-With",
            "sec-fetch-dest" to "Sec-Fetch-Dest",
            "sec-fetch-mode" to "Sec-Fetch-Mode",
            "sec-fetch-site" to "Sec-Fetch-Site",
            "sec-fetch-user" to "Sec-Fetch-User",
            "dnt" to "DNT",
            "cache-control" to "Cache-Control",
            "pragma" to "Pragma",
        )
        private val RESPONSE_HEADERS = listOf("Content-Range", "Accept-Ranges", "ETag", "Last-Modified", "Cache-Control")
    }
}
