import { computed, reactive, ref, type Ref } from 'vue'
import { api } from '../api/client'
import type { Road, RuleInfo, SourceItem, Subject } from '../api/types'
import { isRuleEnabled } from './settings'
import { rankItems, searchTitles, seasonOf, titleSeason } from './useSources'
import { readJSON, writeJSON } from './storage'
import { playerPrefs } from './playerPrefs'
import { toast } from './snackbar'

export interface SourceState {
  rule: RuleInfo
  status: 'loading' | 'done' | 'error'
  items: SourceItem[]
  error: string
  /** 只搜到季数对不上的（比如要第二季、站上只有第一季） */
  offSeason?: boolean
}

/** 每部番记住看到哪（片源、线路、集、秒） */
interface Watch {
  rule: string
  src: string
  road: number
  ep: number
  pos: number
}

const LAST_RULE_KEY = 'fc.lastRule'
const errMsg = (e: unknown, fb: string) => (e instanceof Error ? e.message : fb)

/**
 * 播放页的核心流程：
 * 搜索各片源 → 选一个源和作品 → 取线路/选集 → 点某集解析播放地址 → 交给播放器。
 */
export function usePlayback(subjectId: number, subject: Ref<Subject | null>) {
  const sources = ref<SourceState[]>([])
  const sel = reactive({ rule: '', item: 0, road: 0, ep: 0 })
  const roads = ref<Road[]>([])
  const roadsLoading = ref(false)
  const roadsError = ref('')
  const play = reactive({ src: '', kind: 'hls' as 'hls' | 'mp4', startAt: 0, resolving: false, message: '', via: '' })

  const WATCH_KEY = `fc.watch.${subjectId}`
  let saved = readJSON<Watch | null>(WATCH_KEY, null)
  const lastRule = readJSON<string>(LAST_RULE_KEY, '')
  let userPicked = false
  let searchEpoch = 0
  let chaptersSeq = 0
  let resolveSeq = 0

  const currentSource = computed(() => sources.value.find((s) => s.rule.name === sel.rule))
  const currentItem = computed(() => currentSource.value?.items[sel.item])
  const currentRoad = computed(() => roads.value[sel.road])
  const currentEp = computed(() => currentRoad.value?.episodes[sel.ep])
  const hasNext = computed(() => !!currentRoad.value && sel.ep + 1 < currentRoad.value.episodes.length)
  const hasPrev = computed(() => sel.ep > 0)
  const searching = computed(() => sources.value.some((s) => s.status === 'loading'))
  const nothingFound = computed(
    () => sources.value.length > 0 && !searching.value && sources.value.every((s) => !s.items.length),
  )

  // ---------- 搜索各片源 ----------
  async function searchAll(titles: string[]) {
    const epoch = ++searchEpoch
    let rules: RuleInfo[] = []
    try {
      rules = (await api.rules()).filter((r) => !r.unsupported && isRuleEnabled(r.name))
    } catch (e) {
      toast('获取片源列表失败：' + errMsg(e, ''))
    }
    if (epoch !== searchEpoch) return
    sources.value = rules.map((rule) => ({ rule, status: 'loading', items: [], error: '' }))
    // 最多同时搜 4 个站，别把盒子和站点都压垮
    let next = 0
    const worker = async () => {
      while (epoch === searchEpoch && next < sources.value.length) {
        const st = sources.value[next++]
        await searchOne(st, titles, epoch)
      }
    }
    await Promise.all(Array.from({ length: Math.min(4, sources.value.length) }, worker))
    // 所有站都只有季数对不上的：退而求其次，选第一个搜到东西的
    if (epoch === searchEpoch && !sel.rule) {
      const st = sources.value.find((x) => x.items.length)
      if (st) chooseRule(st.rule.name, false)
    }
  }

  async function searchOne(st: SourceState, titles: string[], epoch: number) {
    const season = titleSeason(titles)
    try {
      for (const t of titles) {
        const r = await api.sourceSearch(st.rule.name, t)
        if (epoch !== searchEpoch) return
        if (r.error) st.error = r.error
        if (r.items.length) {
          const ranked = rankItems(r.items, titles)
          st.error = ''
          if (seasonOf(ranked[0].name) === season) {
            st.items = ranked
            st.offSeason = false
            break
          }
          // 季数对不上（比如只搜到第一季）：先记着，换下一个关键词再试
          if (!st.items.length) {
            st.items = ranked
            st.offSeason = true
          }
        }
      }
      st.status = !st.items.length && st.error ? 'error' : 'done'
    } catch (e) {
      st.status = 'error'
      st.error = errMsg(e, '搜索失败')
    }
    if (epoch === searchEpoch) onSourceDone(st)
  }

  // 第一个搜到结果（且季数对得上）的源自动选中；如果之前用过的源也搜到了，且用户还没自己选，就换成它
  function onSourceDone(st: SourceState) {
    if (!st.items.length || st.offSeason) return // 季数对不上的站不自动选
    if (!sel.rule || (!userPicked && st.rule.name === lastRule && sel.rule !== lastRule)) {
      chooseRule(st.rule.name, false)
    }
  }

  /** 首次进入：用 Bangumi 的名字搜 */
  function start() {
    if (!subject.value) return
    return searchAll(searchTitles(subject.value.name_cn, subject.value.name))
  }

  /** 手动换关键词重搜 */
  function searchManual(kw: string) {
    const k = kw.trim()
    if (!k) return
    userPicked = false
    sel.rule = ''
    roads.value = []
    resetPlay()
    return searchAll([k])
  }

  // ---------- 选源 / 选作品 ----------
  function chooseRule(name: string, byUser: boolean) {
    if (byUser) userPicked = true
    const st = sources.value.find((s) => s.rule.name === name)
    if (!st || !st.items.length) return
    sel.rule = name
    sel.item = saved && saved.rule === name ? Math.max(0, st.items.findIndex((i) => i.src === saved!.src)) : 0
    return loadChapters()
  }

  function chooseItem(i: number) {
    userPicked = true
    sel.item = i
    saved = null
    return loadChapters()
  }

  async function loadChapters() {
    const item = currentItem.value
    if (!item) return
    const my = ++chaptersSeq
    roadsLoading.value = true
    roadsError.value = ''
    roads.value = []
    sel.road = sel.ep = 0
    resetPlay()
    try {
      const r = await api.sourceChapters(sel.rule, item.src)
      if (my !== chaptersSeq) return
      if (r.error) throw new Error(r.error)
      roads.value = r.roads
      if (!r.roads.length) {
        roadsError.value = '这个片源里没有找到剧集，换个片源或作品试试'
        return
      }
      // 第一次进来恢复上次看到的位置
      let ri = 0
      let ei = 0
      let pos = 0
      const w = playerPrefs.resume ? saved : null
      if (w && w.rule === sel.rule && w.src === item.src && r.roads[w.road]?.episodes[w.ep]) {
        ri = w.road
        ei = w.ep
        pos = w.pos
      }
      saved = null
      playEpisode(ri, ei, pos)
    } catch (e) {
      if (my === chaptersSeq) roadsError.value = errMsg(e, '获取剧集失败')
    } finally {
      if (my === chaptersSeq) roadsLoading.value = false
    }
  }

  function chooseRoad(i: number) {
    sel.road = i
    const eps = roads.value[i]?.episodes ?? []
    if (sel.ep >= eps.length) sel.ep = 0
    playEpisode(i, sel.ep, 0)
  }

  // ---------- 解析并播放 ----------
  function resetPlay() {
    resolveSeq++
    play.src = ''
    play.message = ''
    play.resolving = false
    play.via = ''
  }

  async function playEpisode(ri: number, ei: number, pos = 0) {
    const ep = roads.value[ri]?.episodes[ei]
    const item = currentItem.value
    if (!ep || !item) return
    sel.road = ri
    sel.ep = ei
    const my = ++resolveSeq
    play.src = ''
    play.message = ''
    play.startAt = pos
    play.resolving = true
    try {
      const r = await api.resolve(sel.rule, ep.url, item.src)
      if (my !== resolveSeq) return
      if (r.status === 'ok' && r.playUrl) {
        play.kind = r.kind ?? 'hls'
        play.via = r.via ?? ''
        play.src = r.playUrl
        writeJSON(LAST_RULE_KEY, sel.rule)
      } else {
        play.message = r.reason || (r.status === 'need-worker' ? '这个站需要手机 App 帮忙解析，请先打开手机 App' : '解析失败，换个片源试试')
      }
    } catch (e) {
      if (my === resolveSeq) play.message = errMsg(e, '解析失败')
    } finally {
      if (my === resolveSeq) play.resolving = false
    }
  }

  const next = () => hasNext.value && playEpisode(sel.road, sel.ep + 1)
  const prev = () => hasPrev.value && playEpisode(sel.road, sel.ep - 1)

  /** 播放器每隔几秒上报进度：记住看到哪 */
  function saveProgress(pos: number, dur: number) {
    if (!playerPrefs.resume) return
    const item = currentItem.value
    if (!item || !sel.rule) return
    const w: Watch = {
      rule: sel.rule,
      src: item.src,
      road: sel.road,
      ep: sel.ep,
      pos: dur > 0 && pos > dur * 0.95 ? 0 : Math.floor(pos),
    }
    writeJSON(WATCH_KEY, w)
  }

  /** 看完了：标记本集已看完（pos=0，下次从头），连播与否由调用方按设置决定 */
  function markEnded() {
    if (!playerPrefs.resume) return
    const item = currentItem.value
    if (!item || !sel.rule) return
    const w: Watch = { rule: sel.rule, src: item.src, road: sel.road, ep: sel.ep, pos: 0 }
    writeJSON(WATCH_KEY, w)
  }

  return {
    sources, sel, roads, roadsLoading, roadsError, play,
    currentSource, currentItem, currentRoad, currentEp,
    hasNext, hasPrev, searching, nothingFound,
    start, searchManual, chooseRule, chooseItem, chooseRoad, playEpisode, next, prev, saveProgress, markEnded,
  }
}
