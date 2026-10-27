package com.family.cinema

import android.content.Context
import android.net.wifi.WifiManager
import android.util.Log
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress

// 局域网自动发现：向约定端口广播探测串，盒子应答后解析出服务器地址。
// 这样盒子 IP 变化（路由器重拨 / DHCP 重分配）时 App 也能自动找到，不用手填。
object Discovery {
    private const val PORT = 45222
    private const val MAGIC = "FAMILY_CINEMA_DISCOVER"
    private const val PREFIX = "FAMILY_CINEMA"

    // 同步发现，超时 timeoutMs 毫秒。成功返回 "http://host:port"，失败返回 null。
    fun discover(ctx: Context, timeoutMs: Int = 1500): String? {
        var socket: DatagramSocket? = null
        var lock: WifiManager.MulticastLock? = null
        return try {
            val wm = ctx.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
            lock = wm.createMulticastLock("family-cinema").apply { setReferenceCounted(true); acquire() }
            socket = DatagramSocket().apply { broadcast = true; soTimeout = timeoutMs }
            val data = MAGIC.toByteArray()
            socket.send(DatagramPacket(data, data.size, InetAddress.getByName("255.255.255.255"), PORT))
            val buf = ByteArray(256)
            val resp = DatagramPacket(buf, buf.size)
            socket.receive(resp)
            val text = String(resp.data, 0, resp.length)
            // 应答格式 FAMILY_CINEMA|port|webVersion
            if (text.startsWith(PREFIX)) {
                val port = text.split("|").getOrNull(1) ?: "8088"
                val host = resp.address.hostAddress ?: return null
                "http://$host:$port"
            } else null
        } catch (e: Exception) {
            Log.d("Discovery", "未发现服务器: ${e.message}")
            null
        } finally {
            try { lock?.release() } catch (e: Exception) {}
            try { socket?.close() } catch (e: Exception) {}
        }
    }
}
