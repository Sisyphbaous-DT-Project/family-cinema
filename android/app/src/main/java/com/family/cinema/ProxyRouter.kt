package com.family.cinema

import android.content.SharedPreferences
import android.webkit.WebResourceResponse
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.InetSocketAddress
import java.net.Proxy
import java.net.URL

// Bangumi 网络选路：bgm.tv 被墙，App 访问它有两条路——
//   1. 直连：手机梯子开 VPN 模式时，系统层接管全部流量，直连就通（最常用）
//   2. 本地代理：梯子只开"仅代理模式"时，把流量送进 127.0.0.1:端口
// 自动选路 = 先探测直连，不通再探测代理，都不行就报错。
// 探测结果缓存 10 分钟；请求中途失败会作废缓存并重探测一次。
class ProxyRouter(private val prefs: SharedPreferences) {

    enum class Route { DIRECT, PROXY, DEAD }

    private val lock = Any()
    @Volatile private var route: Route? = null
    @Volatile private var routeAt = 0L

    private fun auto(): Boolean = prefs.getBoolean("bgm_auto", true)
    private fun proxyAddr(): String = prefs.getString("bgm_proxy_addr", "127.0.0.1:7890") ?: "127.0.0.1:7890"

    fun configJson(): String = JSONObject()
        .put("auto", auto())
        .put("addr", proxyAddr())
        .toString()

    fun saveConfig(autoOn: Boolean, addr: String) {
        prefs.edit()
            .putBoolean("bgm_auto", autoOn)
            .putString("bgm_proxy_addr", addr.ifBlank { "127.0.0.1:7890" })
            .apply()
        synchronized(lock) { route = null } // 配置变了，重新选路
    }

    /** 主动探测（设置页"测试连接"按钮用），强制重新选路 */
    fun probeJson(): String {
        val r = synchronized(lock) { selectRoute(force = true) }
        return JSONObject().put("route", when (r) {
            Route.DIRECT -> "direct"
            Route.PROXY -> "proxy"
            Route.DEAD -> "dead"
        }).toString()
    }

    // 选路：缓存 10 分钟内直接用；过期或强制时重新探测。auto 关闭 = 永远直连。
    private fun selectRoute(force: Boolean = false): Route {
        if (!auto()) return Route.DIRECT
        val cached = route
        if (!force && cached != null && System.currentTimeMillis() - routeAt < 10 * 60 * 1000) return cached
        val r = when {
            tryOpen("https://api.bgm.tv/calendar", useProxy = false, timeoutMs = 4000) != null -> Route.DIRECT
            tryOpen("https://api.bgm.tv/calendar", useProxy = true, timeoutMs = 4000) != null -> Route.PROXY
            else -> Route.DEAD
        }
        route = r
        routeAt = System.currentTimeMillis()
        return r
    }

    private fun invalidate() {
        synchronized(lock) { route = null }
    }

    // 开连接：第二参决定是否走本地代理。地址格式 host:port，拆不开就不用代理。
    private fun open(url: String, useProxy: Boolean): HttpURLConnection {
        val u = URL(url)
        if (!useProxy) return u.openConnection() as HttpURLConnection
        val parts = proxyAddr().trim().split(":")
        if (parts.size != 2) return u.openConnection() as HttpURLConnection
        val port = parts[1].toIntOrNull() ?: return u.openConnection() as HttpURLConnection
        val p = Proxy(Proxy.Type.HTTP, InetSocketAddress(parts[0].trim(), port))
        return u.openConnection(p) as HttpURLConnection
    }

    // 探测用：能连上并拿到响应码就算通（bgm.tv 只认 GET/POST，这里发个 GET）
    private fun tryOpen(url: String, useProxy: Boolean, timeoutMs: Int): Int? {
        return try {
            val conn = open(url, useProxy)
            conn.connectTimeout = timeoutMs
            conn.readTimeout = timeoutMs
            conn.instanceFollowRedirects = true
            conn.responseCode.also { conn.disconnect() }
        } catch (e: Exception) {
            null
        }
    }

    /** 给 JS 桥的同步请求：返回 {"status":..,"body":".."} 或 {"error":".."} */
    fun requestJson(method: String, url: String, body: String): String {
        // 最多两轮：当前路由失败就重探测换路再试一次
        repeat(2) { attempt ->
            val r = synchronized(lock) { selectRoute(force = attempt > 0) }
            if (r == Route.DEAD) {
                return JSONObject().put("error", "连不上 Bangumi：直连和本地代理都不通，请检查梯子是否开启").toString()
            }
            try {
                return JSONObject()
                    .put("status", 200)
                    .put("body", execute(method, url, body, r == Route.PROXY))
                    .toString()
            } catch (e: RouteException) {
                return JSONObject().put("error", e.message).toString() // HTTP 层错误，换路也没用
            } catch (e: Exception) {
                invalidate() // 网络层错误，可能是路由失效，下一轮重探测
            }
        }
        return JSONObject().put("error", "网络不稳定，请稍后再试").toString()
    }

    private class RouteException(msg: String) : Exception(msg)

    private fun execute(method: String, url: String, body: String, useProxy: Boolean): String {
        val conn = open(url, useProxy)
        conn.connectTimeout = 10000
        conn.readTimeout = 15000
        conn.requestMethod = method
        conn.setRequestProperty("Accept", "application/json")
        // Bangumi 要求可识别的 UA，WebView 的 UA 太长太花，统一用自己的
        conn.setRequestProperty("User-Agent", "FamilyCinema/1.0 (android-app)")
        if (body.isNotEmpty()) {
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
        }
        val code = conn.responseCode
        val stream = if (code in 200..299) conn.inputStream else conn.errorStream
        val text = stream?.readBytes()?.toString(Charsets.UTF_8) ?: ""
        conn.disconnect()
        if (code !in 200..299) throw RouteException("Bangumi 返回 $code")
        return text
    }

    /** 给 WebView 拦截器用：转发图片等 GET 资源（封面图域名为 lain.bgm.tv） */
    fun interceptGet(url: String): WebResourceResponse? {
        val r = synchronized(lock) { selectRoute() }
        if (r == Route.DEAD) return null // 交给 WebView 自己撞墙，反正也出不去
        return try {
            val conn = open(url, r == Route.PROXY)
            conn.connectTimeout = 10000
            conn.readTimeout = 15000
            conn.instanceFollowRedirects = true
            val code = conn.responseCode
            if (code !in 200..299) { conn.disconnect(); return null }
            val mime = conn.contentType?.substringBefore(';')?.trim() ?: "image/jpeg"
            val resp = WebResourceResponse(mime, null, conn.inputStream)
            resp.responseHeaders = mapOf("Access-Control-Allow-Origin" to "*")
            resp
        } catch (e: Exception) {
            null
        }
    }
}
