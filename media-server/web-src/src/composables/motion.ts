/*
 * 换页"一镜到底"：用浏览器自带的视图过渡（View Transitions）。
 * 换页前浏览器给旧画面拍一张照，换好新页面再拍一张，在两张之间补出中间画面；
 * 标了同一个 data-hero 的元素（列表里的海报 ↔ 详情页的海报 ↔ 播放器里的海报）会从旧位置飞到新位置。
 *
 * - 元素用 data-hero="键" 标记，一个元素可以有多个键，用 | 隔开；data-hero-p 越大越优先当落点
 * - 往里走：从被点的那个元素起飞；往回退：飞回当初起飞的那个元素（找不到就找页面上看得见的同键元素）
 * - 新页面要等数据才能画出落点时，在 setup 里调 holdTransition(promise)，最多等 HOLD_MAX 毫秒
 * - 不支持的浏览器（老手机内核）或系统开了"减少动画"：不走这里，App.vue 的 <Transition> 用 CSS 动画兜底
 */
import { nextTick } from 'vue'
import type { RouteLocationNormalized, Router } from 'vue-router'

/** 带一点回弹的弹簧（阻尼比 0.7，过冲约 4.6%），与 motion.css 的 --ease-spring 一致 */
export const EASE_SPRING =
  'linear(0, 0.042, 0.144, 0.278, 0.42, 0.558, 0.681, 0.786, 0.871, 0.935, 0.982, 1.014, 1.033, 1.043, 1.046, 1.044, 1.04, 1.034, 1.027, 1.021, 1.015, 1.01, 1.006, 1.003, 1.001, 0.999, 0.999, 0.998, 0.998, 0.998, 0.998, 0.998, 1)'

const reducedMotion = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false

/** 支持 linear() 缓动就用弹簧，否则退回一条带轻微过冲的贝塞尔曲线 */
export const springEase = (() => {
  try {
    return CSS.supports('transition-timing-function', 'linear(0, 1)') ? EASE_SPRING : 'cubic-bezier(0.34, 1.36, 0.64, 1)'
  } catch {
    return 'cubic-bezier(0.34, 1.36, 0.64, 1)'
  }
})()

// 开发时 ?novt=1 假装成老内核，测 CSS 兜底动画
const forceFallback = import.meta.env.DEV && new URLSearchParams(location.search).has('novt')

/** 换页是否由视图过渡接管（启动时定一次）；为 false 时走 CSS 兜底动画 */
export const useViewTransition =
  typeof document !== 'undefined' && typeof document.startViewTransition === 'function' && !reducedMotion() && !forceFallback

/** 给页面里的小动作用：系统开了"减少动画"就不放 */
export const motionOK = () => !reducedMotion()

const HOLD_MAX = 320 // 新页面最多让换页等这么久（毫秒），等不到就先换，落点出来后自然淡入
const IMG_WAIT = 160 // 落点是图片时等它解码好的上限

type Kind = 'forward' | 'back' | 'tab-next' | 'tab-prev' | 'fade'

/** 哪些页面有"主角"：键和元素上 data-hero 的写法一致 */
function heroKeyOf(r: RouteLocationNormalized): string {
  switch (r.name) {
    case 'subject': return `subject:${r.params.id}`
    case 'play': return `play:${r.params.id}`
    case 'local': return `local:${r.params.id}`
    case 'search': return 'search'
    case 'pansou': return 'pansou'
    default: return ''
  }
}

export function transitionKind(to: RouteLocationNormalized, from: RouteLocationNormalized): Kind {
  if (to.meta.depth > from.meta.depth) return 'forward'
  if (to.meta.depth < from.meta.depth) return 'back'
  if (to.meta.tab && from.meta.tab) return (to.meta.order ?? 0) >= (from.meta.order ?? 0) ? 'tab-next' : 'tab-prev'
  return 'fade'
}

// ---------- 找主角 ----------
function keysOf(el: HTMLElement): string[] {
  return (el.dataset.hero ?? '').split('|')
}

function onScreen(el: Element): boolean {
  const r = el.getBoundingClientRect()
  return r.width > 0 && r.height > 0 && r.bottom > 0 && r.right > 0 && r.top < window.innerHeight && r.left < window.innerWidth
}

/** 页面上看得见的同键元素，优先级高的在前 */
function candidates(key: string): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('[data-hero]')]
    .filter((el) => keysOf(el).includes(key) && onScreen(el))
    .sort((a, b) => Number(b.dataset.heroP ?? 0) - Number(a.dataset.heroP ?? 0))
}

// 最近一次点到的元素：同一个片子在页面上出现两次时（如"接着看"和海报墙），从被点的那个起飞
let lastTap: Element | null = null
// 往里走时记下起飞的元素，返回时飞回它（KeepAlive 的页面元素还是同一个）
const takeoff = new Map<string, WeakRef<HTMLElement>>()

function pickSource(key: string, kind: Kind): HTMLElement | null {
  const list = candidates(key)
  if (kind === 'forward') {
    const tapped = lastTap?.closest<HTMLElement>('[data-hero]')
    if (tapped && list.includes(tapped)) return tapped
  }
  return list[0] ?? null
}

function pickTarget(key: string, kind: Kind): HTMLElement | null {
  if (kind === 'back') {
    const el = takeoff.get(key)?.deref()
    if (el?.isConnected && onScreen(el)) return el
  }
  return candidates(key)[0] ?? null
}

async function imageReady(el: HTMLElement) {
  const img = el instanceof HTMLImageElement ? el : el.querySelector('img')
  if (!img?.src) return
  await Promise.race([img.decode().catch(() => {}), sleep(IMG_WAIT)])
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

// ---------- 新页面的"等我一下" ----------
let capturing = false
let holds: Promise<unknown>[] = []

/** 新页面的落点要等数据（如番剧详情、片库条目）时调用；只在换页动画准备期间生效 */
export function holdTransition(p: Promise<unknown>) {
  if (capturing) holds.push(p.catch(() => {}))
}

// ---------- 封面提示：详情页/播放页的数据还没到时，先用列表卡片上已经加载好的封面 ----------
const coverHints = new Map<number, { img: string; title: string }>()

export function rememberCover(id: number, img: string, title: string) {
  if (img) coverHints.set(id, { img, title })
}

export function coverHint(id: number) {
  return coverHints.get(id) ?? null
}

// ---------- 接到路由上 ----------
export function installViewTransitions(router: Router) {
  if (!useViewTransition) return
  const root = document.documentElement
  const tap = (e: Event) => (lastTap = e.target instanceof Element ? e.target : null)
  document.addEventListener('pointerdown', tap, { capture: true, passive: true })
  document.addEventListener('keydown', (e) => e.key === 'Enter' && tap(e), { capture: true, passive: true })

  // 等"这一次"换页真正落定（afterEach 对取消/失败的导航也会调用）
  let navWaiters: (() => void)[] = []
  let seq = 0
  router.afterEach(() => {
    const w = navWaiters
    navWaiters = []
    w.forEach((r) => r())
  })

  router.beforeResolve((to, from) => {
    if (!from.matched.length || to.fullPath === from.fullPath) return // 首次打开、原地刷新不放动画
    const kind = transitionKind(to, from)
    const key = kind === 'forward' ? heroKeyOf(to) : kind === 'back' ? heroKeyOf(from) : ''
    const source = key ? pickSource(key, kind) : null
    const named: HTMLElement[] = []
    const name = (el: HTMLElement) => {
      el.style.viewTransitionName = 'hero'
      named.push(el)
    }

    const mine = ++seq
    return new Promise<void>((proceed) => {
      root.dataset.vt = kind
      if (source) {
        name(source)
        root.dataset.vtHero = key.split(':')[0]
        if (kind === 'forward') takeoff.set(key, new WeakRef(source))
      }
      const landed = new Promise<void>((r) => {
        navWaiters.push(r)
        setTimeout(r, 800) // 保险：导航卡住也不让画面一直定格
      })
      let vt: ViewTransition
      try {
        vt = document.startViewTransition(async () => {
          capturing = true
          holds = []
          proceed()
          await landed
          await nextTick()
          if (holds.length) await Promise.race([Promise.all(holds), sleep(HOLD_MAX)])
          capturing = false
          await nextTick()
          // 起飞的元素已经跟着旧页面下场了（KeepAlive 的会留着），摘掉名字，免得和落点重名
          source?.style.removeProperty('view-transition-name')
          const target = source ? pickTarget(key, kind) : null
          if (target) {
            name(target)
            await imageReady(target)
          }
        })
      } catch {
        capturing = false
        proceed()
        delete root.dataset.vt
        delete root.dataset.vtHero
        return
      }
      vt.finished.finally(() => {
        named.forEach((el) => el.style.removeProperty('view-transition-name'))
        // 动画期间又开始了下一次换页时，标记归下一次管
        if (seq === mine) {
          delete root.dataset.vt
          delete root.dataset.vtHero
        }
      })
    })
  })
}
