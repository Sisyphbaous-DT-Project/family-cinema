// 离线缓存中心：离线判断、App 缓存清单、每一集的实时缓存状态。
// 逻辑与旧前端（web-legacy/app.js）一致：isOffline 只在 App 内成立；缓存标记只认下载完成的。
import { onBeforeUnmount, onMounted, reactive } from 'vue'
import { bcall, bjson, hasBridge } from './bridge'

export interface CachedEp {
  itemId: string
  epId: string
  title: string
  epTitle: string
  path: string
  size: number
  done?: boolean
  speed?: number
  category?: string
  progress?: number
  queued?: boolean // App 1.4 起：在队列里等着、还没轮到
  paused?: boolean // App 1.4 起：用户手动暂停的
}

/** 离线：App 检测不到服务器时为 true；浏览器里永远 false */
export const isOffline = (): boolean => hasBridge() && bcall((b) => b.isOnline?.()) === '0'

/** 缓存清单（原始数组，含未完成项） */
export function listCached(): CachedEp[] {
  return bjson((b) => b.listCached?.(), [] as CachedEp[]) ?? []
}

export function cacheSize(): { total: number; count: number } {
  return bjson((b) => b.getCacheSize?.(), { total: 0, count: 0 })
}

// ---------- 每一集的实时缓存状态 ----------
// 几个必须知道的事实（来自 DownloadService.kt）：
//  1. 下载服务是单线程排队的。App 1.4 起一加入队列就写进清单（queued=true），轮到了才变成下载中；
//     1.3 及以前排在后面的集要等轮到才会出现在清单里，所以前端还自己记一份"已请求"（pending）兜底。
//  2. 一集刚开始下载的头一秒速度是 0，和"暂停"长得一样，要给一小段缓冲，否则进度条会闪成"已暂停"。
//     App 1.4 起手动暂停有明确标记（paused=true），不用靠速度猜。
//  3. App 1.2 及以前下载线程没有取消接口：删除只对"已下完"的安全；1.3 起有暂停/取消（见 canControlDownload）。

export type CacheState =
  | { kind: 'none' }
  | { kind: 'queued' }
  | { kind: 'running'; pct: number; speed: number }
  | { kind: 'paused'; pct: number }
  | { kind: 'done' }

const NONE: CacheState = { kind: 'none' }
const RUN_GRACE = 4000 // 新出现或刚有进展的未完成项，4 秒内都算"下载中"
const PENDING_TTL = 8000 // 已请求但整整这么久没有任何下载在动，就当没请求成（比如服务没起来）
const POLL_MS = 1000

const live = reactive({
  states: new Map<string, CacheState>(), // 路径/epId → 状态
  pending: new Map<string, number>(), // 路径/epId → 请求时间
  list: [] as CachedEp[], // 最近一次读到的清单（「我的缓存」页用，和状态同一拍刷新）
})

/** 最近一次读到的缓存清单（响应式，随 refreshCacheMarks 刷新） */
export const cachedList = (): CachedEp[] => live.list

interface Track {
  progress: number
  lastMove: number
}
const tracks = new Map<string, Track>() // epId → 进展记录（不需要响应式）
let rawByKey = new Map<string, CachedEp>()
let idleSince = 0
let watchers = 0
let timer: ReturnType<typeof setInterval> | undefined

/** 某一集（传服务器路径或 epId）当前的缓存状态 */
export function cacheState(path: string): CacheState {
  return live.states.get(path) ?? NONE
}

/** 已缓存（下载完成） */
export const isCached = (path: string): boolean => cacheState(path).kind === 'done'

/** 重新读一遍清单并算出每一集的状态；有下载在动时会自动每秒再来一次（见 watchCache） */
export function refreshCacheMarks(): void {
  if (!hasBridge()) return
  const now = Date.now()
  const states = new Map<string, CacheState>()
  const raws = new Map<string, CachedEp>()
  const seen = new Set<string>()
  const list = listCached()
  let anyQueued = false

  for (const c of list) {
    const keys = [c.path, c.epId].filter(Boolean)
    seen.add(c.epId)
    const wasPending = keys.some((k) => live.pending.has(k))
    keys.forEach((k) => live.pending.delete(k)) // 已经出现在清单里，不再算"排队中"
    const pct = Math.round((c.progress || 0) * 100)

    let st: CacheState
    if (c.done !== false) {
      tracks.delete(c.epId)
      st = { kind: 'done' }
    } else if (c.queued) {
      // 排队中：记成"刚有动静"，轮到它开始下载的头一秒（速度还是 0）也不会被误判成暂停
      anyQueued = true
      tracks.set(c.epId, { progress: c.progress || 0, lastMove: now })
      st = { kind: 'queued' }
    } else if (c.paused) {
      tracks.set(c.epId, { progress: c.progress || 0, lastMove: 0 })
      st = { kind: 'paused', pct }
    } else {
      const p = c.progress || 0
      const speed = c.speed || 0
      const t = tracks.get(c.epId)
      if (!t) {
        // 第一次见到：在跑（有速度）或是我们刚请求的才算下载中，否则是以前遗留的暂停项
        tracks.set(c.epId, { progress: p, lastMove: speed > 0 || wasPending ? now : now - RUN_GRACE })
      } else {
        if (speed > 0 || p > t.progress || wasPending) t.lastMove = now
        t.progress = p
      }
      const moving = now - tracks.get(c.epId)!.lastMove < RUN_GRACE
      st = moving ? { kind: 'running', pct, speed } : { kind: 'paused', pct }
    }
    keys.forEach((k) => {
      states.set(k, st)
      raws.set(k, c)
    })
  }
  for (const k of [...tracks.keys()]) if (!seen.has(k)) tracks.delete(k) // 被删掉的

  // 有没有任何下载在动：全都停了很久、请求还没出现，说明那些请求没成功，别一直显示"排队中"
  const active = [...tracks.values()].some((t) => now - t.lastMove < RUN_GRACE)
  if (active) idleSince = 0
  else if (!idleSince) idleSince = now
  if (idleSince && now - idleSince > PENDING_TTL) {
    for (const [k, since] of [...live.pending]) if (now - since > PENDING_TTL) live.pending.delete(k)
  }
  for (const k of live.pending.keys()) if (!states.has(k)) states.set(k, { kind: 'queued' })

  rawByKey = raws
  live.states = states
  live.list = list
  syncTimer(active || anyQueued || live.pending.size > 0)
}

function syncTimer(need: boolean): void {
  if (need && watchers > 0 && !timer) timer = setInterval(refreshCacheMarks, POLL_MS)
  else if ((!need || watchers <= 0) && timer) {
    clearInterval(timer)
    timer = undefined
  }
}

/** 开始盯着缓存状态（有下载在动时每秒自动刷新）；返回停止函数 */
export function watchCache(): () => void {
  watchers++
  refreshCacheMarks()
  let released = false
  return () => {
    if (released) return
    released = true
    watchers = Math.max(0, watchers - 1)
    if (watchers === 0) syncTimer(false)
  }
}

/** 页面里用：挂载时开始盯，卸载时停 */
export function useCacheLive(): void {
  let stop: (() => void) | undefined
  onMounted(() => (stop = watchCache()))
  onBeforeUnmount(() => stop?.())
}

// 从后台切回来时立刻刷新一次（切走期间下载完成了，页面要马上反映）
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && watchers > 0) refreshCacheMarks()
})

/**
 * 把某一集加入缓存（参数约定与旧版 cacheEpisode 完全一致）。
 * 只在"App 内且在线"时调用方该出现入口，这里不再重复判断。
 */
export function downloadEpisode(args: {
  itemTitle: string
  epPath: string
  epSize: number
  epTitle: string
  coverAbsUrl: string
  category: string
}): void {
  // 已缓存/下载中/排队中/暂停的都不能再发起：下载服务会再开一个线程去写同一个文件
  if (cacheState(args.epPath).kind !== 'none') return
  const absUrl = location.origin + `/api/stream?path=${encodeURIComponent(args.epPath)}`
  bcall((b) =>
    b.downloadVideo?.(
      absUrl,
      args.itemTitle,
      args.itemTitle,
      args.epPath,
      String(args.epSize || 0),
      args.coverAbsUrl,
      args.category || 'movie',
      args.epTitle || args.itemTitle,
    ),
  )
  live.pending.set(args.epPath, Date.now())
  refreshCacheMarks()
}

/** 继续下载暂停的缓存：断点续传会认出已下载的部分 */
export function resumeCache(c: CachedEp): void {
  const k = c.epId
  const kind = cacheState(k).kind
  if (kind === 'running' || kind === 'queued') return
  const url = location.origin + `/api/stream?path=${encodeURIComponent(c.path || c.epId)}`
  bcall((b) =>
    b.downloadVideo?.(url, c.title, c.itemId, c.epId, String(c.size || 0), '', c.category || 'movie', c.epTitle || c.title),
  )
  live.pending.set(k, Date.now())
  refreshCacheMarks()
}

/** 按路径继续缓存（详情页用，不用自己拿清单） */
export function resumeByPath(path: string): void {
  const c = rawByKey.get(path)
  if (c) resumeCache(c)
}

/**
 * 暂停下载：保留已下载的部分，之后用"继续"断点续传。需要 App 1.3+，调用前先判断 canControlDownload()。
 * 原生已把清单里的速度立刻归零，这里再让"最近有进展"的缓冲失效，界面马上就显示"已暂停"。
 */
export function pauseCache(path: string): void {
  bcall((b) => b.pauseDownload?.(path))
  const t = tracks.get(path)
  if (t) t.lastMove = 0
  live.pending.delete(path)
  refreshCacheMarks()
}

/**
 * 取消下载：停下来并删掉已下载的部分（排队中的也行，轮到它时会被跳过）。
 * 旧 App 没有 cancelDownload，退回只删清单（对已暂停的是安全的）。
 */
export function cancelCache(path: string): void {
  bcall((b) => {
    if (b.cancelDownload) b.cancelDownload(path)
    else b.deleteCached?.(path)
  })
  live.pending.delete(path)
  tracks.delete(path)
  refreshCacheMarks()
}

export function deleteCache(epId: string): void {
  bcall((b) => b.deleteCached?.(epId))
  live.pending.delete(epId)
  tracks.delete(epId)
  refreshCacheMarks()
}

/** 缓存视频的本地播放地址（离线也能播），没有返回空串 */
export const playLocalUrl = (epId: string): string => bcall((b) => b.playLocalUrl?.(epId)) ?? ''

/** 缓存封面的本地地址（离线也能显示海报），没有返回空串 */
export const cachedCoverUrl = (epId: string): string => bcall((b) => b.coverUrl?.(epId)) ?? ''

// ---------- 离线片库（App 本地服务只认 /api/stream 和 /api/cover，所以片库必须走桥） ----------

interface OfflineLibraryItem {
  id: string // "cache:<itemId>"
  category: string
  title: string
  rawTitle: string
  isDir: boolean
  episodes: { index: number; title: string; filename: string; path: string; size: number }[]
  size: number
  modified: number
  cover?: string
}

export function offlineLibrary(): OfflineLibraryItem[] {
  const d = bjson((b) => b.getLocalLibrary?.(), { items: [] as OfflineLibraryItem[] })
  return d?.items ?? []
}
