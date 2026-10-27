import type { SourceItem } from '../api/types'

/** 归一化标题用于比较：去掉空白、标点、符号并转小写 */
export function norm(s: string): string {
  return s.toLowerCase().replace(/[\s\p{P}\p{S}]/gu, '')
}

/**
 * 去掉 Bangumi 中文名里片源站通常不带的部分：～副标题～、「第N季/期/部」。
 * 例："无职转生 第三季 ～到了异世界就拿出真本事～" → "无职转生"
 */
export function simplifyTitle(s: string): string {
  return s
    .replace(/[～~].*?[～~]/g, ' ')
    .replace(/第[一二三四五六七八九十0-9]+[季期部]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
}

/** 搜索时依次尝试的关键词（去重、去空） */
export function searchTitles(nameCN: string, name: string): string[] {
  const out: string[] = []
  for (const t of [nameCN, simplifyTitle(nameCN), name, simplifyTitle(name)]) {
    if (t && !out.includes(t)) out.push(t)
  }
  return out
}

const CN_NUM: Record<string, number> = { 一: 1, 二: 2, 三: 3, 四: 4, 五: 5, 六: 6, 七: 7, 八: 8, 九: 9, 十: 10 }
const ROMAN: Record<string, number> = { Ⅱ: 2, Ⅲ: 3, Ⅳ: 4, Ⅴ: 5, Ⅵ: 6, II: 2, III: 3, IV: 4 }

/**
 * 从名字里认第几季，认不出算第 1 季（和盒子 follow/check.go 的 seasonOf 同一套）。
 * 例："转生就是剑 第二季" → 2，"転生したら剣でしたII" → 2，"Re:Zero 3rd Season" → 3，"转生成为魔剑2" → 2
 */
export function seasonOf(name: string): number {
  const s = name.trim()
  let m = s.match(/第\s*([一二三四五六七八九十0-9]+)\s*[季期部]/)
  if (m) {
    if (/^\d+$/.test(m[1])) return Number(m[1])
    let n = 0
    for (const ch of m[1]) n = ch === '十' ? Math.max(n, 1) * 10 : n + (CN_NUM[ch] ?? 0)
    if (n > 0) return n
  }
  m = s.match(/(\d+)(?:st|nd|rd|th)\s*season|season\s*(\d+)|\bs(\d+)\b/i)
  if (m) {
    const n = Number(m[1] ?? m[2] ?? m[3])
    if (n > 0) return n
  }
  m = s.match(/(Ⅱ|Ⅲ|Ⅳ|Ⅴ|Ⅵ)(?:\s|$)/) ?? s.match(/(?:[^A-Za-z]|^)(II|III|IV)\s*$/)
  if (m) return ROMAN[m[1]]
  m = s.match(/(?:[^0-9a-zA-Z]|^)([2-9])\s*$/)
  return m ? Number(m[1]) : 1
}

/** 番剧标题是第几季（几个标题里取最大的，去掉"第二季"的简化标题算 1） */
export const titleSeason = (titles: string[]) => Math.max(1, ...titles.map(seasonOf))

/**
 * 把搜索结果按与番剧标题的接近程度排序：完全相同 > 互相包含 > 其它，同档保持原顺序；
 * 季数对不上的排最后（去掉"第二季"再比时，第一季的名字会和它一模一样）
 */
export function rankItems(items: SourceItem[], titles: string[]): SourceItem[] {
  const ts = titles.map(norm).filter(Boolean)
  const season = titleSeason(titles)
  const score = (it: SourceItem) => {
    if (seasonOf(it.name) !== season) return -1
    const n = norm(it.name)
    if (ts.some((t) => n === t)) return 2
    if (ts.some((t) => n.includes(t) || t.includes(n))) return 1
    return 0
  }
  return items
    .map((it, i) => ({ it, i, s: score(it) }))
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .map((x) => x.it)
}
