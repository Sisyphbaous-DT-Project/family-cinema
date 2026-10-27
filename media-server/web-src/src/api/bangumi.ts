// 客户端直连 Bangumi。
// 为什么不让盒子中转：bgm.tv 被墙（DNS 污染 + SNI 重置），盒子出不去；
// 而电脑浏览器会走系统代理、手机 App 由原生层自动选路（直连→本地代理→报错），
// 所以 Bangumi 的数据全部改为客户端自己取，盒子只管国内视频站和播放代理。
// 浏览器直连已实测可行：api.bgm.tv 返回 Access-Control-Allow-Origin: *（含 POST 预检）。
import type { BgmEpisode, CalendarDay, Card, Images, SearchResult, Subject } from './types'
import { isOffline } from '../composables/cache'

const BGM = 'https://api.bgm.tv'
const OFFLINE_MSG = '现在是离线模式，连上家里的网络后才能用'

// App 原生桥提供的请求通道（自动代理在原生层完成，见 ProxyRouter.kt）
interface BgmBridge {
  bgmRequest?: (method: string, path: string, body: string) => string
}

function bridge(): BgmBridge | null {
  return (window as unknown as { AndroidBridge?: BgmBridge }).AndroidBridge ?? null
}

export class BgmError extends Error {}

/** 统一请求入口：App 走桥（同步返回 JSON 字符串），浏览器走 fetch */
async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  if (isOffline()) throw new BgmError(OFFLINE_MSG)
  const payload = body === undefined ? '' : JSON.stringify(body)
  const b = bridge()
  if (b?.bgmRequest) {
    let res: { status?: number; body?: string; error?: string }
    try {
      res = JSON.parse(b.bgmRequest(method, path, payload))
    } catch {
      throw new BgmError('App 网络通道异常')
    }
    if (res.error) throw new BgmError(res.error)
    if (res.status !== 200) throw new BgmError(`Bangumi 返回 ${res.status}`)
    try {
      return JSON.parse(res.body ?? '') as T
    } catch {
      throw new BgmError('Bangumi 返回的数据无法解析')
    }
  }
  let res: Response
  try {
    res = await fetch(BGM + path, {
      method,
      signal,
      headers: payload ? { 'Content-Type': 'application/json' } : undefined,
      body: payload || undefined,
    })
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new BgmError('连不上 Bangumi：请确认本机的代理/VPN 已开启')
  }
  if (!res.ok) throw new BgmError(`Bangumi 返回 ${res.status}`)
  return (await res.json()) as T
}

// ---- 上游原始结构 → 前端类型（与后端 internal/anime/bangumi/models.go 同一套映射） ----

interface RawRating {
  rank?: number
  total?: number
  score?: number
}

interface RawCard {
  id: number
  name?: string
  name_cn?: string
  summary?: string
  air_date?: string
  date?: string
  eps?: number
  total_episodes?: number
  rating?: RawRating
  images?: Images
}

const emptyImages: Images = { large: '', common: '', medium: '', small: '', grid: '' }

function cardOf(r: RawCard): Card {
  return {
    id: r.id,
    name: r.name ?? '',
    name_cn: r.name_cn ?? '',
    date: r.date || r.air_date || '',
    summary: r.summary ?? '',
    eps: r.eps || r.total_episodes || 0,
    score: r.rating?.score ?? 0,
    rank: r.rating?.rank ?? 0,
    images: r.images ?? emptyImages,
  }
}

// infobox 的 value 可能是字符串，也可能是 [{k,v}] 数组
function infoValue(v: unknown): string {
  if (typeof v === 'string') return v
  if (Array.isArray(v)) {
    return v
      .map((it) => (it && typeof it === 'object' ? String((it as { v?: string }).v ?? '') : ''))
      .filter(Boolean)
      .join('、')
  }
  return ''
}

function subjectOf(r: RawCard & { platform?: string; tags?: { name: string; count: number }[]; infobox?: { key: string; value: unknown }[] }): Subject {
  const c = cardOf(r)
  const tags = [...(r.tags ?? [])].sort((a, b) => b.count - a.count).slice(0, 12)
  return {
    ...c,
    platform: r.platform ?? '',
    tags,
    infobox: (r.infobox ?? [])
      .map((it) => ({ key: it.key, value: infoValue(it.value) }))
      .filter((it) => it.value !== ''),
    rating: { rank: r.rating?.rank ?? 0, total: r.rating?.total ?? 0, score: r.rating?.score ?? 0 },
  }
}

/** 热门榜时间窗：上一季度开头 ~ 当前季度结尾（与后端 SeasonWindow 一致） */
function seasonWindow(now = new Date()): [string, string] {
  const q = Math.floor(now.getMonth() / 3) // 0..3
  const start = Date.UTC(now.getUTCFullYear(), q * 3, 1)
  const fmt = (ms: number) => new Date(ms).toISOString().slice(0, 10)
  // 按日历月加减（对齐 Go 的 AddDate），不能用固定天数
  const from = new Date(start)
  from.setUTCMonth(from.getUTCMonth() - 3)
  const to = new Date(start)
  to.setUTCMonth(to.getUTCMonth() + 3)
  return [fmt(from.getTime()), fmt(to.getTime())]
}

interface RawSearchPage {
  total?: number
  offset?: number
  data?: RawCard[]
}

async function searchSubjects(body: unknown, limit: number, offset: number, signal?: AbortSignal): Promise<SearchResult> {
  const raw = await request<RawSearchPage>('POST', `/v0/search/subjects?limit=${limit}&offset=${offset}`, body, signal)
  return { total: raw.total ?? 0, offset: raw.offset ?? 0, items: (raw.data ?? []).map(cardOf) }
}

export const bangumi = {
  /** 每周放送日历 */
  async calendar(signal?: AbortSignal): Promise<CalendarDay[]> {
    const raw = await request<{ weekday: { id: number }; items: RawCard[] }[]>('GET', '/calendar', undefined, signal)
    return raw.map((d) => ({ weekday: d.weekday.id, items: (d.items ?? []).map(cardOf) }))
  },

  /** 按关键词搜动画（type=2），按匹配度排序 */
  search(kw: string, offset = 0, limit = 24, signal?: AbortSignal): Promise<SearchResult> {
    return searchSubjects({ keyword: kw, sort: 'match', filter: { type: [2] } }, limit, offset, signal)
  },

  /** 近两季热门番剧（按热度排序） */
  hot(offset = 0, limit = 24, signal?: AbortSignal): Promise<SearchResult> {
    const [from, to] = seasonWindow()
    return searchSubjects(
      { keyword: '', sort: 'heat', filter: { type: [2], air_date: [`>=${from}`, `<${to}`] } },
      limit,
      offset,
      signal,
    )
  },

  /** 番剧详情 */
  async subject(id: number, signal?: AbortSignal): Promise<Subject> {
    const raw = await request<Parameters<typeof subjectOf>[0]>('GET', `/v0/subjects/${id}`, undefined, signal)
    return subjectOf(raw)
  },

  /** 本篇剧集列表（上游每页最多 100，最多翻 5 页，与后端一致） */
  async episodes(id: number, signal?: AbortSignal): Promise<BgmEpisode[]> {
    const all: BgmEpisode[] = []
    for (let offset = 0; offset < 500; offset += 100) {
      const page = await request<{ total?: number; data?: BgmEpisode[] }>(
        'GET',
        `/v0/episodes?subject_id=${id}&type=0&limit=100&offset=${offset}`,
        undefined,
        signal,
      )
      const items = page.data ?? []
      all.push(...items)
      if (all.length >= (page.total ?? 0) || items.length === 0) break
    }
    return all
  },
}
