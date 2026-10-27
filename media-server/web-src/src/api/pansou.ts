// 网盘搜索：调盒子上独立运行的盘搜（PanSou，8888 端口，见 AGENTS §18）。
// 它允许跨域，前端直接按"当前访问的盒子地址 + 8888"去调；开发时走 vite 代理 /pansou-api。
// 盒子没梯子，只搜插件（各资源网站），不搜 Telegram 频道。
import { isOffline } from '../composables/cache'

const PANSOU_PORT = 8888

function base(): string {
  if (import.meta.env.DEV) return '/pansou-api'
  return `${location.protocol}//${location.hostname}:${PANSOU_PORT}`
}

export interface PanLink {
  url: string
  password: string
  /** 资源标题（盘搜叫 note） */
  note: string
  datetime: string
  /** 来源，如 plugin:lingjisp */
  source: string
}

export interface PanResult {
  total: number
  /** 按网盘类型分组（key 见 PAN_TYPES），已去重、"other/others" 已合并 */
  byType: Record<string, PanLink[]>
}

/** 网盘类型：显示顺序按常用程度排；name 用在分组标题，short 用在筛选标签 */
export const PAN_TYPES: { key: string; name: string; short: string }[] = [
  { key: 'quark', name: '夸克网盘', short: '夸克' },
  { key: 'baidu', name: '百度网盘', short: '百度' },
  { key: 'aliyun', name: '阿里云盘', short: '阿里' },
  { key: 'xunlei', name: '迅雷云盘', short: '迅雷' },
  { key: 'uc', name: 'UC 网盘', short: 'UC' },
  { key: '123', name: '123 云盘', short: '123' },
  { key: 'tianyi', name: '天翼云盘', short: '天翼' },
  { key: '115', name: '115 网盘', short: '115' },
  { key: 'mobile', name: '移动云盘', short: '移动' },
  { key: 'pikpak', name: 'PikPak', short: 'PikPak' },
  { key: 'guangya', name: '光鸭云盘', short: '光鸭' },
  { key: 'magnet', name: '磁力链接', short: '磁力' },
  { key: 'ed2k', name: '电驴链接', short: '电驴' },
  { key: 'others', name: '其他链接', short: '其他' },
]

const KNOWN = new Set(PAN_TYPES.map((t) => t.key))

export async function panSearch(kw: string, signal?: AbortSignal): Promise<PanResult> {
  if (isOffline()) throw new Error('没连上家里的网络，现在用不了网盘搜索')
  let res: Response
  try {
    res = await fetch(base() + '/api/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kw, res: 'merge', src: 'plugin' }),
      signal,
    })
  } catch (e) {
    if ((e as Error)?.name === 'AbortError') throw e
    throw new Error('网盘搜索暂时用不了，等一会儿再试试（盒子上的盘搜服务没开着）')
  }
  if (!res.ok) throw new Error('网盘搜索出错了，等一会儿再试试')
  const body = (await res.json()) as { code?: number; message?: string; data?: { total?: number; merged_by_type?: Record<string, PanLink[]> } }
  if (body.code && body.code !== 0) throw new Error(body.message || '网盘搜索出错了')
  const raw = body.data?.merged_by_type ?? {}
  const byType: Record<string, PanLink[]> = {}
  const seen = new Set<string>()
  for (const [k, list] of Object.entries(raw)) {
    const key = KNOWN.has(k) ? k : 'others' // other/others 和不认识的类型都算"其他"
    for (const l of list ?? []) {
      if (!l?.url || seen.has(l.url)) continue
      seen.add(l.url)
      ;(byType[key] ||= []).push({
        url: l.url,
        password: l.password || '',
        note: (l.note || '').trim(),
        datetime: l.datetime || '',
        source: l.source || '',
      })
    }
  }
  return { total: seen.size, byType }
}

/** 复制给用户的文字：链接 + 提取码（网盘 App 打开时会自动识别剪贴板里的链接） */
export function copyTextOf(l: PanLink): string {
  return l.password ? `${l.url} 提取码：${l.password}` : l.url
}

/** 分享时间写成大白话；盘搜给不出时间的是 0001 年，不显示 */
export function whenText(iso: string): string {
  const t = Date.parse(iso)
  if (!t || new Date(t).getFullYear() < 2000) return ''
  const days = Math.floor((Date.now() - t) / 86400000)
  if (days < 1) return '今天分享'
  if (days < 30) return `${days} 天前分享`
  if (days < 365) return `${Math.floor(days / 30)} 个月前分享`
  return `${new Date(t).getFullYear()} 年分享`
}
