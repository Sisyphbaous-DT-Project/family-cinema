// 本地片库的观看进度。键名与旧前端一致（"pos:<进度键>"），进度键通常是原文件路径，转码后也不会变。
// App 里进度由原生保存（在线离线共用一份），浏览器才用 localStorage。
import { bcall, hasBridge } from './bridge'

interface Prog {
  pos: number
  dur: number
  ts: number
}

const key = (path: string) => 'pos:' + path

// 转换过的片子：在线时用原片路径记进度，缓存到手机里的却是新 MP4 的路径。
// 在线时记下两者的对应关系，两个键一起写、读的时候取最新的一份，在线和离线看的进度就能接上。
const mirrors = new Map<string, string>()

export function linkProgress(progressKey: string, path: string): void {
  if (progressKey && path && progressKey !== path) mirrors.set(progressKey, path)
}

function readProg(path: string): Prog | null {
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

function writeProg(path: string, pos: number, dur: number): void {
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

export function getProg(path: string): Prog | null {
  const own = readProg(path)
  const mirror = mirrors.get(path)
  if (!mirror) return own
  const other = readProg(mirror)
  const newest = !other ? own : !own ? other : (other.ts || 0) > (own.ts || 0) ? other : own
  // 两边不一样就把新的那份抄给另一边（比如转换前看到一半，之后才缓存到手机）
  if (newest) {
    const stale = newest === own ? other : own
    if (!stale || Math.abs(stale.pos - newest.pos) > 1) writeProg(newest === own ? mirror : path, newest.pos, newest.dur)
  }
  return newest
}

export function setProg(path: string, pos: number, dur: number): void {
  writeProg(path, pos, dur)
  const mirror = mirrors.get(path)
  if (mirror) writeProg(mirror, pos, dur)
}

/** 看了几成（0~1），时长未知记 0 */
export function progRatio(path: string): number {
  const p = getProg(path)
  return p && p.dur ? Math.min(1, p.pos / p.dur) : 0
}

export const isWatched = (path: string) => progRatio(path) >= 0.95
