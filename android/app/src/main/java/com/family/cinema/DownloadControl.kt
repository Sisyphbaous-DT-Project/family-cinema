package com.family.cinema

import java.net.HttpURLConnection

// 下载的暂停 / 取消控制台。
// 下载服务是单线程队列，线程一旦开始读数据就没有现成的办法让它停，所以用两招配合：
//   1. 打标记：下载循环每读一块数据、重试等待的每个小片段，都会看一眼有没有指令
//   2. 断开连接：线程卡在 read() 上（网络很慢或卡住）时，从别的线程 disconnect，
//      read 立刻抛异常醒过来，再去看标记，不用干等 20 秒超时
// 只有"在队列里或正在下"的任务才认指令；已经停了的任务不会被留下过期指令误伤。
object DownloadControl {
    const val NONE = 0
    const val PAUSE = 1   // 暂停：保留已下载的部分，之后可以断点续传
    const val CANCEL = 2  // 取消：停下来并删掉已下载的部分

    private val lock = Any()
    private val active = HashMap<String, Int>()                 // epId -> 在队列里/正在跑的任务数
    private val flags = HashMap<String, Int>()                  // epId -> 待处理的指令
    private val conns = HashMap<String, HttpURLConnection>()    // epId -> 正在用的连接

    /** 任务入队时调用（同一集可能被重复点，所以计数） */
    fun enter(epId: String) = synchronized(lock) { active[epId] = (active[epId] ?: 0) + 1; Unit }

    /** 任务结束（不管成功失败）时调用；全部结束后顺手清掉没人处理的过期指令 */
    fun leave(epId: String) = synchronized(lock) {
        val n = (active[epId] ?: 0) - 1
        if (n <= 0) {
            active.remove(epId)
            flags.remove(epId)
        } else {
            active[epId] = n
        }
        Unit
    }

    fun isActive(epId: String): Boolean = synchronized(lock) { active.containsKey(epId) }

    /**
     * 发指令。返回 false 表示这一集当前没有任务在队列里或在跑（没必要发，也不会留下过期指令）。
     * 取消优先于暂停：已经标了取消，再来暂停不覆盖。
     */
    fun signal(epId: String, mode: Int): Boolean {
        val conn: HttpURLConnection?
        synchronized(lock) {
            if (!active.containsKey(epId)) return false
            flags[epId] = maxOf(flags[epId] ?: NONE, mode)
            conn = conns[epId]
        }
        // 断开放到独立小线程：有的 HTTP 实现里 disconnect() 要等另一个线程的 read() 返回才能拿到锁，
        // 同步调用会把调用方（JS 桥线程，也就是网页的点击）一起卡住，最长一个读超时。
        // 标记已经打上了，就算断不开，下载线程读到下一块数据时也会自己停下。
        if (conn != null) {
            Thread { try { conn.disconnect() } catch (e: Exception) { /* 断不断得开都不影响标记生效 */ } }.start()
        }
        return true
    }

    /** 只看不取 */
    fun peek(epId: String): Int = synchronized(lock) { flags[epId] ?: NONE }

    /** 取走指令（取走后就算处理了） */
    fun take(epId: String): Int = synchronized(lock) { flags.remove(epId) ?: NONE }

    fun attach(epId: String, conn: HttpURLConnection) = synchronized(lock) { conns[epId] = conn; Unit }

    fun detach(epId: String) = synchronized(lock) { conns.remove(epId); Unit }
}
