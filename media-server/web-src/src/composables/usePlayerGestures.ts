// 播放器手势：单击显隐控制条、双击播放/暂停、长按临时加速（长按时手指拖到底部倍速条上松开 = 固定那个倍速）。
// 从 VideoPlayer.vue 拆出来的，免得主文件越来越长。倍速条长什么样、手指落在哪个倍速上由 VideoPlayer 自己算。
import { ref } from 'vue'

export interface GestureHandlers {
  /** 正在播放（长按倍速只在播放时生效） */
  isPlaying: () => boolean
  /** 单击（300ms 内没有第二击才触发）：显隐控制条 */
  onSingleTap: () => void
  /** 双击（画面任意位置）：播放/暂停 */
  onDoubleTap: () => void
  /** 长按开始 */
  onHoldStart: () => void
  /** 长按中手指移动（离开按下的位置超过一小段才开始回调，手抖不算） */
  onHoldMove: (e: PointerEvent) => void
  /** 长按结束：cancelled 为真表示被系统打断（来电、切后台等），不算松手选定 */
  onHoldEnd: (cancelled: boolean) => void
}

const HOLD_MS = 500
const TAP_GAP = 300
const SLOP = 10 // 手指移动超过这么多像素才算"拖动"

// 按在这些控件上不算点画面，也不触发长按（按住播放键不该变 2 倍速）
const isControl = (t: EventTarget | null) => !!(t as HTMLElement | null)?.closest?.('button, .seek, .speedmenu')

export function usePlayerGestures(h: GestureHandlers) {
  const holding = ref(false)
  let tapTimer: ReturnType<typeof setTimeout> | undefined
  let pressTimer: ReturnType<typeof setTimeout> | undefined
  let lastTap = 0
  let pressing = false
  let moved = false
  let downX = 0
  let downY = 0
  let pressId = -1
  let pressEl: HTMLElement | null = null

  function onDown(e: PointerEvent) {
    if (e.button > 0) return // 鼠标右键/中键不管
    clearTimeout(pressTimer)
    pressing = true
    moved = false
    downX = e.clientX
    downY = e.clientY
    pressId = e.pointerId
    pressEl = e.currentTarget as HTMLElement
    if (isControl(e.target)) return
    pressTimer = setTimeout(() => {
      if (!h.isPlaying()) return
      holding.value = true
      // 抓住这根手指：拖出播放器范围（竖屏往下拖过了头）也继续收到移动和松开
      try {
        pressEl?.setPointerCapture(pressId)
      } catch {
        /* 手指已经抬起 */
      }
      h.onHoldStart()
    }, HOLD_MS)
  }

  function onMove(e: PointerEvent) {
    if (!pressing || e.pointerId !== pressId) return
    if (!moved && Math.hypot(e.clientX - downX, e.clientY - downY) > SLOP) {
      moved = true
      // 还没到长按时间就挪动了：是在滑/拖，不算长按
      if (!holding.value) clearTimeout(pressTimer)
    }
    if (holding.value && moved) h.onHoldMove(e)
  }

  /** 结束这次按压；返回 true 表示刚结束的是长按 */
  function finish(cancelled: boolean): boolean {
    pressing = false
    clearTimeout(pressTimer)
    if (!holding.value) return false
    holding.value = false
    h.onHoldEnd(cancelled)
    return true
  }

  function onUp(e: PointerEvent) {
    const wasPressing = pressing
    const wasMoved = moved
    // 长按刚结束、拖动过、或点在按钮上，都不算点击
    if (finish(false) || !wasPressing || wasMoved || isControl(e.target)) return
    const now = Date.now()
    if (now - lastTap < TAP_GAP) {
      clearTimeout(tapTimer)
      lastTap = 0
      h.onDoubleTap()
      return
    }
    lastTap = now
    tapTimer = setTimeout(() => h.onSingleTap(), TAP_GAP)
  }

  function onCancel() {
    finish(true)
  }

  // 长按时手指被抓住了，正常不会触发离开；这里只是兜底（没抓住时鼠标拖出去松开，播放器收不到松开）
  function onLeave() {
    finish(true)
  }

  function dispose() {
    clearTimeout(tapTimer)
    clearTimeout(pressTimer)
  }

  return { holding, onDown, onMove, onUp, onCancel, onLeave, dispose }
}
