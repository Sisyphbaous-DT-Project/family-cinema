package com.family.cinema

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

// 一个缓存项（一集或一部电影）
data class CacheItem(
    val itemId: String,   // 所属条目（片名）
    val epId: String,     // 集标识（用服务器端文件路径，全局唯一；也是进度的 key）
    val title: String,    // 片名
    val epTitle: String,  // 集标题
    val path: String,     // 服务器端路径（用于在线时比对"已缓存"角标）
    val size: Long,       // 文件总大小（字节）
    var done: Boolean,    // 是否下载完成
    var downloaded: Long, // 已下载字节（算进度用）
    var speedBps: Long = 0,       // 当前下载速度（字节/秒），完成或停止时归 0
    val category: String = "movie", // movie | series（旧缓存没这字段时按 path 里的目录名推断）
    val url: String = "",          // 下载地址：进程被杀后重建任务的凭据（旧缓存没有，靠前端重拼）
    val coverUrl: String = "",     // 封面地址，同上
    var queued: Boolean = false,   // 在队列里等着、还没轮到（只在内存里，进程没了队列也没了，不落盘）
    var userPaused: Boolean = false // 用户手动暂停的：App 重开时不自动续下
)

// 缓存清单：读写 filesDir/cache/manifest.json，是 Bridge 查询缓存的唯一数据源
class CacheStore(private val ctx: Context) {

    companion object {
        // 单例：MainActivity(Bridge)、DownloadService、LocalHttpServer 共享同一份内存缓存。
        // 否则下载服务写的进度，Bridge 这边的实例看不到（缓存视图会显示"已缓存 0 个"）。
        @Volatile private var instance: CacheStore? = null
        fun get(ctx: Context): CacheStore =
            instance ?: synchronized(this) {
                instance ?: CacheStore(ctx.applicationContext).also { instance = it }
            }
    }

    private val dir: File get() = File(ctx.filesDir, "cache").apply { mkdirs() }
    private val manifestFile: File get() = File(dir, "manifest.json")
    private val items = LinkedHashMap<String, CacheItem>() // epId -> item，保序

    init { load() }

    @Synchronized private fun load() {
        items.clear()
        if (!manifestFile.exists()) return
        try {
            val arr = JSONArray(manifestFile.readText())
            for (i in 0 until arr.length()) {
                val o = arr.getJSONObject(i)
                val it = CacheItem(
                    itemId = o.optString("itemId"),
                    epId = o.optString("epId"),
                    title = o.optString("title"),
                    epTitle = o.optString("epTitle"),
                    path = o.optString("path"),
                    size = o.optLong("size"),
                    done = o.optBoolean("done", false),
                    downloaded = o.optLong("downloaded", 0),
                    speedBps = o.optLong("speed", 0),
                    // 旧缓存没有 category 字段：按路径里的媒体目录名推断
                    category = o.optString("category").ifEmpty {
                        if (o.optString("path").contains("/番剧/")) "series" else "movie"
                    },
                    url = o.optString("url"),
                    coverUrl = o.optString("coverUrl"),
                    userPaused = o.optBoolean("userPaused", false)
                )
                items[it.epId] = it
            }
        } catch (e: Exception) { /* 清单损坏就当空库 */ }
    }

    @Synchronized private fun save() {
        val arr = JSONArray()
        items.values.forEach {
            arr.put(JSONObject().apply {
                put("itemId", it.itemId); put("epId", it.epId); put("title", it.title)
                put("epTitle", it.epTitle); put("path", it.path); put("size", it.size)
                put("done", it.done); put("downloaded", it.downloaded)
                put("speed", it.speedBps); put("category", it.category)
                put("url", it.url); put("coverUrl", it.coverUrl)
                put("userPaused", it.userPaused)
            })
        }
        try { manifestFile.writeText(arr.toString()) } catch (e: Exception) {}
    }

    @Synchronized fun upsert(item: CacheItem) { items[item.epId] = item; save() }

    // 加入下载队列时立刻登记"排队中"：以前要等轮到才写进清单，排在后面的集界面上看不见，
    // 网页一刷新、App 一重开就当成"没缓存"。
    // alreadyActive = 这一集在加入前就已经有任务在队列里/在跑：
    //   这时只有"刚被手动暂停"（新任务会排在后面接着下）才改成排队中，否则保持原样（它可能正在下）
    @Synchronized fun markQueued(item: CacheItem, alreadyActive: Boolean) {
        val old = items[item.epId]
        if (old == null) {
            item.queued = true
            items[item.epId] = item
        } else {
            if (old.done) return
            if (alreadyActive && !old.userPaused) return
            old.queued = true
            old.userPaused = false
            old.speedBps = 0
        }
        save()
    }

    @Synchronized fun updateProgress(epId: String, downloaded: Long, done: Boolean, speedBps: Long = 0) {
        items[epId]?.let { it.downloaded = downloaded; it.done = done; it.speedBps = speedBps; save() }
    }

    // 用户点了暂停：立刻把速度归零、移出"排队中"（下载线程要等读到下一块数据才会发现暂停，
    // 这之前界面再读清单，不能还看到"正在下载"），并记下是手动暂停的，App 重开时不自动续下
    @Synchronized fun markUserPaused(epId: String) {
        items[epId]?.let {
            if (it.done) return
            it.speedBps = 0; it.queued = false; it.userPaused = true
            save()
        }
    }

    @Synchronized fun remove(epId: String) {
        items.remove(epId)
        fileFor(epId).delete()
        coverFileFor(epId).delete()
        save()
    }

    // 清尸：进程被杀是猝死，来不及把 speed 归零，界面会拿旧速度假装还在下载。
    // App 启动时把未完成项的 speed 和"排队中"清掉，界面才能正确显示"已暂停"。
    // 下载服务还活着（只是界面重建）时，正在队列里/在跑的任务不能动。
    @Synchronized fun resetStalled() {
        var changed = false
        items.values.forEach {
            if (it.done || DownloadControl.isActive(it.epId)) return@forEach
            if (it.speedBps != 0L || it.queued) { it.speedBps = 0; it.queued = false; changed = true }
        }
        if (changed) save()
    }

    // 待恢复的下载任务：没下完、存了下载地址、不是用户手动暂停的（旧缓存没地址，由前端"继续"按钮重拼）
    @Synchronized fun pendingItems(): List<CacheItem> =
        items.values.filter { !it.done && it.url.isNotEmpty() && !it.userPaused }

    @Synchronized fun all(): List<CacheItem> = items.values.toList()

    // 缓存文件名用 epId 的哈希，避免中文/斜杠等非法文件名字符
    fun fileFor(epId: String): File = File(dir, Integer.toHexString(epId.hashCode()) + ".video")

    // 对应的封面文件（下载视频时一并存，缓存页/离线首页显示海报用）
    fun coverFileFor(epId: String): File = File(dir, Integer.toHexString(epId.hashCode()) + ".cover")

    fun listJson(): String {
        val arr = JSONArray()
        all().forEach {
            arr.put(JSONObject().apply {
                put("itemId", it.itemId); put("epId", it.epId); put("title", it.title)
                put("epTitle", it.epTitle); put("path", it.path); put("size", it.size)
                put("done", it.done); put("speed", it.speedBps); put("category", it.category)
                put("progress", if (it.size > 0) it.downloaded.toDouble() / it.size else 0.0)
                // 1.4 起：排队中 / 手动暂停（网页靠这两个字段区分，旧版 App 没有）
                put("queued", it.queued); put("paused", it.userPaused)
            })
        }
        return arr.toString()
    }

    fun sizeJson(): String {
        var total = 0L
        all().forEach { total += if (it.done) it.size else it.downloaded }
        return JSONObject().apply {
            put("total", total); put("count", all().count { it.done })
        }.toString()
    }

    // 离线模式：把已完成的缓存项拼成媒体库结构（每个片名一个条目，各缓存项为集）
    fun localLibraryJson(): String {
        val grouped = all().filter { it.done }.groupBy { it.itemId }
        val arr = JSONArray()
        grouped.forEach { (itemId, eps) ->
            val first = eps.first()
            val epArr = JSONArray()
            eps.forEachIndexed { idx, it ->
                epArr.put(JSONObject().apply {
                    put("index", idx + 1); put("title", it.epTitle); put("filename", it.epTitle)
                    put("path", it.epId); put("size", it.size)
                })
            }
            arr.put(JSONObject().apply {
                put("id", "cache:$itemId"); put("category", first.category); put("title", first.title)
                put("rawTitle", first.title); put("isDir", true); put("episodes", epArr)
                put("size", eps.sumOf { it.size }); put("modified", 0)
                // 本地封面存在时才给（没有的话前端走图标占位；cover 值就是 epId，
                // 前端会请求 /api/cover?path=epId，由 LocalHttpServer 返回本地封面文件）
                if (coverFileFor(first.epId).exists()) put("cover", first.epId)
            })
        }
        return JSONObject().apply { put("items", arr) }.toString()
    }
}
