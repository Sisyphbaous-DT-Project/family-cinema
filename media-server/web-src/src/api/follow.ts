// 追番、播出时间、下载前量大小：对应后端 /api/anime/follows、/airtime、/size
import { isOffline } from '../composables/cache'

const BASE = '/api/anime'
const OFFLINE = '现在是离线模式，连上家里的网络后才能用'

/** 播出时间（北京时间） */
export interface AirView {
  weekday: number // 1=周一 … 7=周日
  time: string // "23:00"
  every: number // 每几天更新一次；0 = 只播一次
  begin: string
  next?: string
  ended?: boolean
  cn?: { site: string; weekday: number; time: string } // 国内正版平台上线时间
}

export interface FollowItem {
  id: number
  title: string
  name?: string
  cover?: string
  addedAt: string
  rule?: string
  src?: string
  srcName?: string
  pickedBy?: 'user' | 'auto'
  count: number // 片源站现在有几集
  latest?: string // "第5集"
  baseline: boolean
  checkedAt?: string
  updatedAt?: string // 最近一次发现更新
  checkErr?: string
  air?: AirView
}

export interface SizeResult {
  status: 'ok' | 'need-worker' | 'fail'
  bytes: number
  seconds?: number
  exact: boolean
  reason?: string
}

async function call<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  if (isOffline()) throw new Error(OFFLINE)
  const res = await fetch(BASE + path, {
    method,
    signal,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  const data = (await res.json().catch(() => ({}))) as T & { error?: string }
  if (!res.ok) throw new Error((data as { error?: string }).error || `请求失败（${res.status}）`)
  return data
}

const q = encodeURIComponent

export const followApi = {
  list: () => call<{ items: FollowItem[]; latestEvent: number }>('GET', '/follows'),
  add: (b: { id: number; title: string; name?: string; cover?: string; rule?: string; src?: string }) =>
    call<FollowItem>('POST', '/follows', b),
  remove: (id: number) => call<{ ok: boolean }>('DELETE', `/follows/${id}`),
  setSource: (id: number, rule: string, src: string) => call<{ ok: boolean }>('POST', `/follows/${id}/source`, { rule, src }),
  /** 查不到的不在结果里 */
  airtime: (ids: number[], signal?: AbortSignal) =>
    call<Record<string, AirView>>('GET', `/airtime?ids=${ids.join(',')}`, undefined, signal),
  size: (rule: string, ep: string, ref: string, signal?: AbortSignal) =>
    call<SizeResult>('GET', `/size?rule=${q(rule)}&ep=${q(ep)}&ref=${q(ref)}`, undefined, signal),
}

const WEEK = ['', '周一', '周二', '周三', '周四', '周五', '周六', '周日']
export const weekName = (d: number) => WEEK[d] ?? ''

/** "每周三 23:00 更新"；只播一次的写首播日期 */
export function airText(a: AirView): string {
  if (a.every === 7) return `每${weekName(a.weekday)} ${a.time} 更新`
  if (a.every === 1) return `每天 ${a.time} 更新`
  if (a.every > 0) return `每 ${a.every} 天更新一次（${a.time}）`
  const d = new Date(a.begin)
  return `${d.getMonth() + 1}月${d.getDate()}日 ${a.time} 首播`
}

/** "B站 周四 00:30 上线" */
export function cnText(a: AirView): string {
  return a.cn ? `${a.cn.site} ${weekName(a.cn.weekday)} ${a.cn.time} 上线` : ''
}
