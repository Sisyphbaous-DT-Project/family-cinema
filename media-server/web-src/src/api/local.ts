// 盒子本地片库（你保存在硬盘里的电影电视剧、番剧）。接口沿用旧后端：/api/library、/api/cover、/api/stream。
// 离线（或 App 内网络抖动）时回退到原生桥的离线片库（只含已缓存内容）。
import { hasBridge } from '../composables/bridge'
import { isOffline, offlineLibrary } from '../composables/cache'
import type { Task } from './manage'

export type LocalCategory = 'movie' | 'series'

export interface LocalEpisode {
  index: number
  title: string
  filename: string
  path: string
  size: number
}

export interface LocalItem {
  id: string
  category: LocalCategory
  title: string
  rawTitle: string
  isDir: boolean
  episodes: LocalEpisode[]
  size: number
  modified: number
  cover?: string
}

export interface DownloadingCard {
  taskId: string
  title: string
  category: LocalCategory
  progress: number
  speedKBs: number
  status: string
  stage?: string
  cover?: string
  animeFiles?: string[]
  files?: string[]
  animeFailedFiles?: string[]
  animeFailures?: { file: string; message: string }[]
  animeActive?: Task['animeActive']
}

interface LibraryResponse {
  items: LocalItem[] | null
  downloading: DownloadingCard[] | null
}

const enc = encodeURIComponent

export const coverURL = (path: string) => `/api/cover?path=${enc(path)}`
export const streamURL = (path: string) => `/api/stream?path=${enc(path)}`

/** 离线片库（桥里的已缓存内容），按分类过滤；条目 id 形如 cache:<itemId> */
function offlineItems(cat?: LocalCategory): LocalItem[] {
  const all = offlineLibrary() as LocalItem[]
  return (cat ? all.filter((it) => it.category === cat) : all).map((it) => ({ ...it, episodes: it.episodes ?? [] }))
}

// 拉过的片库条目记在内存里：点进详情页时第一帧就能画出片名、封面和选集，再在后台刷新
const known = new Map<string, LocalItem>()
const remember = (items: LocalItem[]) => items.forEach((it) => known.set(it.id, it))

/** 手头已有的条目（片库页拉过的、或缓存条目），没有返回 null */
export function peekItem(id: string): LocalItem | null {
  if (id.startsWith('cache:') || isOffline()) return offlineItems().find((x) => x.id === id) ?? null
  return known.get(id) ?? null
}

export async function fetchLibrary(cat: LocalCategory): Promise<{ items: LocalItem[]; downloading: DownloadingCard[] }> {
  if (isOffline()) return { items: offlineItems(cat), downloading: [] }
  try {
    const res = await fetch(`/api/library?cat=${cat}`)
    if (!res.ok) throw new Error(`加载片库失败（${res.status}）`)
    const j = (await res.json()) as LibraryResponse
    const items = (j.items ?? []).map((it) => ({ ...it, episodes: it.episodes ?? [] }))
    items.sort((a, b) => b.modified - a.modified)
    remember(items)
    return { items, downloading: j.downloading ?? [] }
  } catch (e) {
    // App 里网络抖动时降级到已缓存，而不是只报错
    if (hasBridge()) return { items: offlineItems(cat), downloading: [] }
    throw e
  }
}

export async function fetchItem(id: string): Promise<LocalItem | null> {
  // 缓存条目（cache: 前缀）直接从桥里取
  if (id.startsWith('cache:')) {
    return offlineItems().find((x) => x.id === id) ?? null
  }
  if (isOffline()) return offlineItems().find((x) => x.id === id) ?? null
  try {
    // 旧后端没有按 id 取单个条目的接口，从全库里找
    const res = await fetch('/api/library')
    if (!res.ok) throw new Error(`加载片库失败（${res.status}）`)
    const j = (await res.json()) as LibraryResponse
    const items = (j.items ?? []).map((x) => ({ ...x, episodes: x.episodes ?? [] }))
    remember(items)
    return items.find((x) => x.id === id) ?? null
  } catch (e) {
    if (hasBridge()) return offlineItems().find((x) => x.id === id) ?? null
    throw e
  }
}

export async function rescan(): Promise<void> {
  await fetch('/api/scan', { method: 'POST' })
}

// ---------- 集名整理 ----------
// 后端给番剧的集名是"第1集"，但连续剧（放在电影电视剧里）没有集名，前端以前会拿整个文件名来顶，
// 例如 "01.1080p.HD国语中字无水印[电影港www.dygangs.me].mp4"。这里统一整理成"第N集"。
const EP_NUM_RES = [/第\s*(\d{1,4})\s*[集话話]/, /[[【(](\d{1,3})[\]】)]/, /^(\d{1,3})(?=[\s._-])/]

function episodeNumber(s: string): number | null {
  for (const re of EP_NUM_RES) {
    const m = s.match(re)
    if (m) return Number(m[1])
  }
  return null
}

/** 只有一段文字（比如缓存清单里存的集标题，老数据可能是整个文件名）时的缩短：短的原样，长的认编号 */
export function shortEpTitle(raw: string): string {
  const t = (raw || '').trim()
  if (t.length <= 12) return t
  const n = episodeNumber(t)
  return n !== null ? `第${n}集` : t.slice(0, 12) + '…'
}

/**
 * 选集上显示的集名。
 * i/total：这一集在列表里的位置和总数；fromCache：来自离线缓存清单（序号只是缓存里的先后，不是真集数，不能拿来当集号）。
 */
export function epLabel(
  e: { title?: string; filename?: string; index?: number },
  i: number,
  total: number,
  fromCache = false,
): string {
  const t = (e.title || '').trim()
  if (t && t.length <= 12) return t // 后端给的"第1集""版本 1"
  if (!fromCache && total === 1) return '正片' // 单文件电影，别把文件名当集名
  const n = episodeNumber(t) ?? episodeNumber(e.filename || '')
  if (n !== null) return `第${n}集`
  if (!fromCache && e.index && e.index > 0) return `第${e.index}集`
  return total === 1 ? '正片' : `第${i + 1}集`
}

export function fmtSize(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(0) + ' MB'
  return Math.max(1, Math.round(n / 1024)) + ' KB'
}
