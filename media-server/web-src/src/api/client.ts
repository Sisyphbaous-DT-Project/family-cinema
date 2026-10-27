import type {
  BgmEpisode, CalendarDay, ResolveResult, Road, RuleInfo, SearchResult, SourceItem, Subject,
} from './types'
import { bangumi } from './bangumi'
import { isOffline } from '../composables/cache'

const BASE = '/api/anime'

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  // 离线时盒子不可达（App 本地服务对这些路径一律 404），直接给友好提示
  if (isOffline()) throw new Error('现在是离线模式，连上家里的网络后才能用')
  const res = await fetch(BASE + path, { signal })
  if (!res.ok) {
    let msg = `请求失败（${res.status}）`
    try {
      const j = await res.json()
      if (j && j.error) msg = j.error
    } catch {
      /* 响应不是 JSON，用默认文案 */
    }
    throw new Error(msg)
  }
  return (await res.json()) as T
}

async function post<T>(path: string, body: unknown): Promise<T> {
  if (isOffline()) throw new Error('现在是离线模式，连上家里的网络后才能用')
  const res = await fetch(BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const data = (await res.json().catch(() => ({}))) as T & { error?: string }
  if (!res.ok || data.error) throw new Error(data.error || `请求失败（${res.status}）`)
  return data
}

const q = encodeURIComponent

export const api = {
  // Bangumi 资料（番剧信息/时间表/搜索）客户端直连，不走盒子（盒子出不了墙，见 bangumi.ts）
  hot: (offset = 0, limit = 24, signal?: AbortSignal): Promise<SearchResult> => bangumi.hot(offset, limit, signal),
  calendar: (signal?: AbortSignal): Promise<CalendarDay[]> => bangumi.calendar(signal),
  search: (kw: string, offset = 0, limit = 24, signal?: AbortSignal): Promise<SearchResult> =>
    bangumi.search(kw, offset, limit, signal),
  subject: (id: number, signal?: AbortSignal): Promise<Subject> => bangumi.subject(id, signal),
  episodes: (id: number, signal?: AbortSignal): Promise<BgmEpisode[]> => bangumi.episodes(id, signal),
  // 以下是国内视频站的事，留在盒子上（盒子直连没问题）
  rules: () => get<RuleInfo[]>('/rules'),
  sourceSearch: (rule: string, kw: string, signal?: AbortSignal) =>
    get<{ items: SourceItem[]; error?: string }>(`/source/search?rule=${q(rule)}&q=${q(kw)}`, signal),
  sourceChapters: (rule: string, src: string, signal?: AbortSignal) =>
    get<{ roads: Road[]; error?: string }>(`/source/chapters?rule=${q(rule)}&src=${q(src)}`, signal),
  resolve: (rule: string, ep: string, ref: string, signal?: AbortSignal) =>
    get<ResolveResult>(`/resolve?rule=${q(rule)}&ep=${q(ep)}&ref=${q(ref)}`, signal),
  download: (p: {
    rule: string
    ref: string
    title: string
    category: 'movie' | 'series'
    folder: string
    targetId: string
    episodes: { name: string; url: string; index: number }[]
  }): Promise<{ ok: boolean; id?: string; queued: number; skipped: number; dir: string }> => post('/download', p),
  /** 重试下载失败的几集；files 为空 = 所有失败的集 */
  retryDownload: (taskId: string, files: string[] = []) =>
    post<{ ok: boolean; id?: string; queued: number; dir?: string; error?: string }>(
      `/download/${encodeURIComponent(taskId)}/retry`, { files },
    ),
  workerStatus: () => get<{ online: number }>('/worker/status'),
}

/** 取标题：优先中文名 */
export const titleOf = (c: { name: string; name_cn: string }) => c.name_cn || c.name
