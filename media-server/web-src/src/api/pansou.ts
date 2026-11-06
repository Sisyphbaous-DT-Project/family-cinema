// 网盘搜索由主服务转发到配置的盘搜；老版本主服务仍按原来的 8888 端口连接。
import { ref } from 'vue'
import { isOffline } from '../composables/cache'

export const pansouEnabled = ref(true)
let legacy = false
let featuresPromise: Promise<boolean> | undefined

export function loadPansouFeatures(): Promise<boolean> {
  if (isOffline()) return Promise.resolve(false)
  featuresPromise ??= (async () => {
    const ctrl = new AbortController()
    const timer = setTimeout(() => ctrl.abort(), 5000)
    try {
      const res = await fetch('/api/features', { signal: ctrl.signal, cache: 'no-store' })
      if (res.status === 404) {
        legacy = true
        return pansouEnabled.value
      }
      if (!res.ok) throw new Error('读取功能状态失败')
      const data = await res.json() as { pansou?: boolean }
      if (typeof data.pansou !== 'boolean') {
        legacy = true
        return pansouEnabled.value
      }
      pansouEnabled.value = data.pansou
      legacy = false
    } catch {
      // 短暂断网不能把新服务误认成旧版，下次访问继续重试。
      featuresPromise = undefined
    } finally {
      clearTimeout(timer)
    }
    return pansouEnabled.value
  })()
  return featuresPromise
}

function searchURL(): string {
  if (!legacy) return '/api/pansou/search'
  if (import.meta.env.DEV) return '/pansou-api/api/search'
  return `${location.protocol}//${location.hostname}:8888/api/search`
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
  if (!await loadPansouFeatures()) throw new Error('当前未安装盘搜，可在一键程序中勾选安装')
  let res: Response
  try {
    res = await fetch(searchURL(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kw, res: 'merge', src: 'plugin' }),
      signal,
    })
  } catch (e) {
    if ((e as Error)?.name === 'AbortError') throw e
    throw new Error('网盘搜索暂时无法连接，请检查盘搜是否正在运行')
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
