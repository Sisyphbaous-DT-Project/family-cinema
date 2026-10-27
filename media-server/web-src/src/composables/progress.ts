// 本地片库的观看进度。键名与旧前端一致（"pos:<文件路径>"），所以旧页面里看到哪了，新页面接着用。
// App 里进度由原生保存（在线离线共用一份），浏览器才用 localStorage。
import { bcall, hasBridge } from './bridge'

interface Prog {
  pos: number
  dur: number
  ts: number
}

const key = (path: string) => 'pos:' + path

export function getProg(path: string): Prog | null {
  if (hasBridge()) {
    const raw = bcall((b) => (b as unknown as { getProg?: (k: string) => string }).getProg?.(key(path)))
    try {
      const o = raw ? JSON.parse(raw) : null
      return o && typeof o.pos === 'number' ? o : null
    } catch {
      return null
    }
  }
  try {
    const raw = localStorage.getItem(key(path))
    if (!raw) return null
    if (raw[0] !== '{') {
      const s = parseFloat(raw) // 更早的旧格式：只存了秒数
      return isNaN(s) ? null : { pos: s, dur: 0, ts: 0 }
    }
    const o = JSON.parse(raw)
    return o && typeof o.pos === 'number' ? o : null
  } catch {
    return null
  }
}

export function setProg(path: string, pos: number, dur: number): void {
  if (hasBridge()) {
    bcall((b) => (b as unknown as { setProg?: (k: string, p: number, d: number) => string }).setProg?.(key(path), pos, dur))
    return
  }
  try {
    localStorage.setItem(key(path), JSON.stringify({ pos, dur: dur || 0, ts: Math.floor(Date.now() / 1000) }))
  } catch {
    /* 存不下就算了 */
  }
}

/** 看了几成（0~1），时长未知记 0 */
export function progRatio(path: string): number {
  const p = getProg(path)
  return p && p.dur ? Math.min(1, p.pos / p.dur) : 0
}

export const isWatched = (path: string) => progRatio(path) >= 0.95
