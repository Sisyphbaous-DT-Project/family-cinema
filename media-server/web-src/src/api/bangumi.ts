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
  bgmRequestAsync?: (id: string, method: string, path: string, body: string) => void
}

type BgmWindow = Window & {
  AndroidBridge?: BgmBridge
  __familyCinemaBgmResult?: (id: string, result: string) => void
}

function bridge(): BgmBridge | null {
  return (window as BgmWindow).AndroidBridge ?? null
}

export class BgmError extends Error {}

const BRIDGE_TIMEOUT = 90_000
const bridgePrefix = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
let bridgeSequence = 0
const bridgePending = new Map<string, (result: string) => void>()

function parseBridgeResponse<T>(text: string): T {
  let res: { status?: number; body?: string; error?: string }
  try {
    res = JSON.parse(text)
    if (!res || typeof res !== 'object') throw new Error()
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

function requestAsyncBridge(b: BgmBridge, method: string, path: string, payload: string, signal?: AbortSignal): Promise<string> {
  const w = window as BgmWindow
  w.__familyCinemaBgmResult = (id, result) => bridgePending.get(id)?.(result)
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('请求已取消', 'AbortError'))
      return
    }
    const id = `${bridgePrefix}-${++bridgeSequence}`
    let timer: ReturnType<typeof setTimeout>
    const cleanup = () => {
      bridgePending.delete(id)
      clearTimeout(timer)
      signal?.removeEventListener('abort', onAbort)
    }
    const onAbort = () => {
      cleanup()
      reject(new DOMException('请求已取消', 'AbortError'))
    }
    bridgePending.set(id, (result) => {
      cleanup()
      resolve(result)
    })
    timer = setTimeout(() => {
      cleanup()
      reject(new BgmError('App 网络请求超时，请稍后再试'))
    }, BRIDGE_TIMEOUT)
    signal?.addEventListener('abort', onAbort, { once: true })
    try {
      b.bgmRequestAsync!(id, method, path, payload)
    } catch {
      cleanup()
      reject(new BgmError('App 网络通道异常'))
    }
  })
}

// 番剧详情记 10 分钟：详情页 → 播放页 → 返回详情页都用同一份，页面一打开就有内容（换页动画能直接落在海报上）
const SUBJECT_TTL = 10 * 60_000
const subjectMemo = new Map<number, { at: number; p: Promise<Subject>; v?: Subject }>()

/** 已经取到手的番剧详情（没有或过期了返回 null），给页面第一帧直接画出来用 */
export function peekSubject(id: number): Subject | null {
  const hit = subjectMemo.get(id)
  return hit && Date.now() - hit.at < SUBJECT_TTL ? hit.v ?? null : null
}

/** 统一请求入口：新 App 走异步桥，旧 App 回退同步桥，浏览器走 fetch */
async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  if (isOffline()) throw new BgmError(OFFLINE_MSG)
  if (signal?.aborted) throw new DOMException('请求已取消', 'AbortError')
  const payload = body === undefined ? '' : JSON.stringify(body)
  const b = bridge()
  if (b?.bgmRequestAsync) {
    return parseBridgeResponse<T>(await requestAsyncBridge(b, method, path, payload, signal))
  }
  if (b?.bgmRequest) {
    let result: string
    try {
      result = b.bgmRequest(method, path, payload)
    } catch {
      throw new BgmError('App 网络通道异常')
    }
    return parseBridgeResponse<T>(result)
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
  subject(id: number, signal?: AbortSignal): Promise<Subject> {
    const load = () => request<Parameters<typeof subjectOf>[0]>('GET', `/v0/subjects/${id}`, undefined, signal).then(subjectOf)
    if (signal) return load()
    const hit = subjectMemo.get(id)
    if (hit && Date.now() - hit.at < SUBJECT_TTL) return hit.p
    const entry: { at: number; p: Promise<Subject>; v?: Subject } = { at: Date.now(), p: load() }
    subjectMemo.set(id, entry)
    entry.p.then(
      (v) => (entry.v = v),
      () => subjectMemo.get(id) === entry && subjectMemo.delete(id), // 失败的不记，下次重新取
    )
    return entry.p
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
