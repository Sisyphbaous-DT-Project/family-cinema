import { computed, ref, watch } from 'vue'
import type { Subtitle } from '../api/local'
import { readJSON, writeJSON } from './storage'

const STORAGE_KEY = 'player-subtitle'

export interface SubtitleOption {
  index: number
  label: string
}

function languageName(code?: string): string {
  const raw = (code ?? '').trim()
  switch (raw.toLowerCase()) {
    case 'chi':
    case 'zho':
    case 'zh':
    case 'chs':
    case 'cht':
    case 'sc':
    case 'tc':
    case 'gb':
    case 'big5':
    case 'zh-cn':
    case 'zh-tw':
    case 'zh-hans':
    case 'zh-hant':
      return '中文'
    case 'eng':
    case 'en':
      return '英文'
    case 'jpn':
    case 'ja':
    case 'jp':
      return '日文'
    case '':
    case 'und':
      return '未标注语言'
    default:
      // 文件名里直接写了「简体」「繁体」「中文」的
      return /[简繁中]/.test(raw) ? '中文' : raw
  }
}

const isChinese = (code?: string) => languageName(code) === '中文'

function defaultIndex(list: Subtitle[], audioLang?: string): number {
  if (isChinese(audioLang)) return -1
  const chinese = list.map((s, index) => ({ s, index })).filter(({ s }) => isChinese(s.lang))
  return (chinese.find(({ s }) => !s.forced) ?? chinese[0])?.index ?? -1
}

/** 记住的是"哪种语言的第几条"，不是列表下标：下一集字幕排列不一样时也能选对语言。 */
interface SavedChoice {
  lang: string
  forced: boolean
  title: string
  nth: number
}

function describe(list: Subtitle[], index: number): SavedChoice {
  const s = list[index]
  const lang = languageName(s.lang)
  const forced = !!s.forced
  const nth = list.slice(0, index + 1).filter((x) => languageName(x.lang) === lang && !!x.forced === forced).length
  return { lang, forced, title: s.title?.trim() ?? '', nth }
}

/** 在这一集的字幕里找回记住的那条；这一集没有这种语言就返回 undefined，交给默认规则。 */
function locate(list: Subtitle[], choice: SavedChoice): number | undefined {
  const sameLang = list.map((s, index) => ({ s, index })).filter(({ s }) => languageName(s.lang) === choice.lang)
  if (!sameLang.length) return undefined
  if (choice.title) {
    const titled = sameLang.find(({ s }) => (s.title?.trim() ?? '') === choice.title && !!s.forced === choice.forced)
    if (titled) return titled.index
  }
  const sameKind = sameLang.filter(({ s }) => !!s.forced === choice.forced)
  const pool = sameKind.length ? sameKind : sameLang
  return pool[Math.min(Math.max(choice.nth, 1), pool.length) - 1].index
}

/** 按作品记住字幕选择（和超分一样），换集后按语言找回同一种字幕。 */
export function useSubtitles(
  subtitles: () => Subtitle[],
  audioLang: () => string | undefined,
  seriesKey: () => string,
) {
  const selectedIndex = ref(-1)
  const selected = computed(() => selectedIndex.value >= 0 ? subtitles()[selectedIndex.value] : undefined)
  const options = computed<SubtitleOption[]>(() => {
    const list = subtitles()
    const totals = new Map<string, number>()
    for (const s of list) {
      const name = languageName(s.lang)
      totals.set(name, (totals.get(name) ?? 0) + 1)
    }
    const seen = new Map<string, number>()
    return list.map((s, index) => {
      const name = languageName(s.lang)
      const n = (seen.get(name) ?? 0) + 1
      seen.set(name, n)
      const numbered = (totals.get(name) ?? 0) > 1 ? `${name}${n}` : name
      const title = s.title?.trim()
      return {
        index,
        label: `${numbered}${title ? ` · ${title}` : ''}${s.forced ? '（强制）' : ''}`,
      }
    })
  })

  watch(() => [subtitles(), audioLang(), seriesKey()] as const, ([list, lang, key]) => {
    const saved = readJSON<Record<string, number | SavedChoice>>(STORAGE_KEY, {})
    const choice = Object.prototype.hasOwnProperty.call(saved, key) ? saved[key] : undefined
    let index: number | undefined
    if (choice === -1) index = -1
    else if (choice && typeof choice === 'object' && typeof choice.lang === 'string') index = locate(list, choice)
    // 旧版本存的是列表下标
    else if (typeof choice === 'number' && Number.isInteger(choice) && choice >= 0 && choice < list.length) index = choice
    selectedIndex.value = index ?? defaultIndex(list, lang)
  }, { immediate: true })

  function set(index: number) {
    const list = subtitles()
    if (index < -1 || index >= list.length) return
    selectedIndex.value = index
    const saved = readJSON<Record<string, number | SavedChoice>>(STORAGE_KEY, {})
    saved[seriesKey()] = index === -1 ? -1 : describe(list, index)
    writeJSON(STORAGE_KEY, saved)
  }

  function reset() {
    const saved = readJSON<Record<string, number | SavedChoice>>(STORAGE_KEY, {})
    delete saved[seriesKey()]
    writeJSON(STORAGE_KEY, saved)
    selectedIndex.value = defaultIndex(subtitles(), audioLang())
  }

  return { selected, selectedIndex, options, set, reset }
}
