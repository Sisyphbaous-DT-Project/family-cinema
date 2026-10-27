<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import Hls from 'hls.js/light' // 精简版：去掉字幕/多音轨/DRM，动漫用不到，体积小一半
import { acquireWakeLock, hasBridge, lockLandscape, releaseWakeLock, unlockOrientation } from '../composables/bridge'
import { playerPrefs, videoFilter } from '../composables/playerPrefs'
import { usePlayerGestures } from '../composables/usePlayerGestures'
import { toast } from '../composables/snackbar'
import IconButton from './m3/IconButton.vue'
import Spinner from './m3/Spinner.vue'
import Icon from './m3/Icon.vue'
import PlayerSettings from './player/PlayerSettings.vue'

const props = defineProps<{
  /** 播放地址（盒子播放代理的相对地址）；为空时只显示占位/提示 */
  src?: string
  kind?: 'hls' | 'mp4'
  title?: string
  /** 起播位置（秒） */
  startAt?: number
  /** 正在解析播放地址 */
  resolving?: boolean
  /** 解析/播放失败时的提示文案 */
  message?: string
  hasPrev?: boolean
  hasNext?: boolean
}>()
const emit = defineEmits<{
  (e: 'prev'): void
  (e: 'next'): void
  /** 播放结束：带上当时的位置和时长，方便记满进度 */
  (e: 'ended', pos: number, dur: number): void
  (e: 'progress', pos: number, dur: number): void
  (e: 'fail', msg: string): void
}>()

const stage = ref<HTMLElement>()
const video = ref<HTMLVideoElement>()
const playing = ref(false)
const buffering = ref(false)
const cur = ref(0)
const dur = ref(0)
const buffered = ref(0)
const showControls = ref(true)
const showSpeed = ref(false)
const showSettings = ref(false)
const isFull = ref(false)
const flashText = ref('') // 画面上方一闪而过的小提示（快进几秒、改了倍速）

let hls: Hls | null = null
let hideTimer: ReturnType<typeof setTimeout> | undefined
let lastProgressEmit = 0
let recoverTries = 0
// 起播位置还没跳到位时不上报进度：此时 currentTime 还是 0，上报会把已存的进度冲成 0
let posReady = true

const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2, 3]
// 判定是不是手机不能用窗口宽度：横屏后 innerWidth 变大，判定会翻车，
// 导致"谁把屏幕转横的谁负责转回来"里的解锁被跳过（返回键回竖屏失效的 bug）。
// 有原生桥 = 一定是手机 App；浏览器才看宽度（桌面全屏不转屏）。
const isMobile = () => hasBridge() || window.innerWidth <= 760
// 进全屏时如果是我们把屏幕转横的，记一笔账；退出时按账解锁（幂等，重复解锁无害）
let rotatedByUs = false

// ---------- 装载视频 ----------
function destroy() {
  hls?.destroy()
  hls = null
  const v = video.value
  if (v) {
    v.removeAttribute('src')
    v.load()
  }
  playing.value = false
  cur.value = dur.value = buffered.value = 0
}

function load() {
  destroy()
  playError.value = ''
  recoverTries = 0
  const v = video.value
  if (!v || !props.src) return
  applyPrefs()
  const startAt = playerPrefs.resume && props.startAt && props.startAt > 3 ? props.startAt : 0
  posReady = !startAt
  lastProgressEmit = Date.now() // 起播后 5 秒内不触发定时保存（durationchange 等事件也会走到这里）
  const begin = () => {
    if (startAt) {
      v.addEventListener('seeked', () => (posReady = true), { once: true })
      v.currentTime = startAt
    }
    v.play().catch(() => {
      /* 浏览器不让自动播放时，等用户点一下大播放按钮 */
    })
  }
  if (props.kind === 'mp4' || !Hls.isSupported()) {
    // mp4，或不支持 MSE 的环境（如 iOS 原生 HLS）走原生播放
    v.src = props.src
    v.addEventListener('loadedmetadata', begin, { once: true })
    return
  }
  hls = new Hls({ maxBufferLength: 40, maxMaxBufferLength: 90, enableWorker: true, lowLatencyMode: false })
  hls.on(Hls.Events.MANIFEST_PARSED, begin)
  hls.on(Hls.Events.ERROR, (_e, data) => {
    if (!data.fatal) return
    if (data.type === Hls.ErrorTypes.NETWORK_ERROR && recoverTries < 2) {
      recoverTries++
      hls?.startLoad()
    } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR && recoverTries < 2) {
      recoverTries++
      hls?.recoverMediaError()
    } else {
      playError.value = '视频加载失败，换个线路或片源试试'
      emit('fail', playError.value)
      hls?.destroy()
      hls = null
    }
  })
  hls.loadSource(props.src)
  hls.attachMedia(v)
}

watch(() => props.src, load)

// ---------- 播放偏好应用到 video 元素 ----------
function applyPrefs() {
  const v = video.value
  if (!v) return
  v.playbackRate = playerPrefs.rate
  v.volume = playerPrefs.vol
  v.loop = playerPrefs.loop
  v.style.objectFit = playerPrefs.fit
  v.style.filter = videoFilter(playerPrefs)
  v.style.transform = playerPrefs.mirror ? 'scaleX(-1)' : ''
}
watch(playerPrefs, applyPrefs)

// ---------- 播放状态 ----------
const playError = ref('')

function saveNow() {
  if (dur.value && posReady) emit('progress', cur.value, dur.value)
}

function onTime() {
  const v = video.value
  if (!v) return
  cur.value = v.currentTime
  dur.value = isFinite(v.duration) ? v.duration : 0
  if (v.buffered.length) buffered.value = v.buffered.end(v.buffered.length - 1)
  const now = Date.now()
  if (dur.value && now - lastProgressEmit > 5000) {
    lastProgressEmit = now
    saveNow()
  }
}
function onPause() {
  playing.value = false
  saveNow()
  poke()
}
function onEnded() {
  saveNow()
  emit('ended', cur.value, dur.value)
}
// 切后台/关页时立刻存一次进度，避免最多丢 5 秒
function onPageHide() {
  saveNow()
}

function toggle() {
  const v = video.value
  if (!v || !props.src) return
  if (v.paused) v.play().catch(() => {})
  else v.pause()
}
function seekTo(t: number) {
  const v = video.value
  if (!v || !dur.value) return
  v.currentTime = Math.min(Math.max(0, t), dur.value)
  cur.value = v.currentTime
}
function seekBy(d: number) {
  seekTo((video.value?.currentTime ?? 0) + d)
  flash((d > 0 ? '快进 ' : '快退 ') + Math.abs(d) + ' 秒', 700)
}
let hintTimer: ReturnType<typeof setTimeout> | undefined
function flash(text: string, ms: number) {
  flashText.value = text
  clearTimeout(hintTimer)
  hintTimer = setTimeout(() => (flashText.value = ''), ms)
}

function setRate(r: number) {
  playerPrefs.rate = r
  showSpeed.value = false
}

// ---------- 控制条显隐 ----------
function poke() {
  showControls.value = true
  clearTimeout(hideTimer)
  if (playing.value && !showSpeed.value && !showSettings.value) hideTimer = setTimeout(() => (showControls.value = false), 3000)
}
watch(playing, poke)
watch(showSettings, poke)

// ---------- 手势 ----------
// 长按：先临时 2 倍速，底部浮出倍速条；按住不放把手指拖到条上，手指所在的倍速马上生效，
// 这时松开就一直用这个倍速（和点倍速按钮一样记住）；没拖到条上就松开 = 恢复原来的速度。
const HOLD_RATE = 2
const holdPick = ref<number | null>(null) // 手指正落在倍速条的哪个倍速上（null = 不在条上）
const rateRow = ref<HTMLElement>()
const rateText = (s: number) => (Number.isInteger(s) ? s.toFixed(1) : String(s))

function setLiveRate(r: number) {
  if (video.value) video.value.playbackRate = r
}
function pickFromEvent(e: PointerEvent): number | null {
  const r = rateRow.value?.getBoundingClientRect()
  if (!r || !r.width) return null
  // 条的上方留一点余量；往下和左右都不设限（竖屏拖出播放器下沿、拖过两头也算），老人家手指不用那么准
  if (e.clientY < r.top - 24) return null
  const i = Math.floor(((e.clientX - r.left) / r.width) * SPEEDS.length)
  return SPEEDS[Math.min(SPEEDS.length - 1, Math.max(0, i))]
}

const { holding, onDown, onMove, onUp, onCancel, onLeave, dispose: disposeGestures } = usePlayerGestures({
  isPlaying: () => playing.value,
  onSingleTap: () => {
    if (showSettings.value) return
    if (showControls.value && playing.value) showControls.value = false
    else poke()
  },
  onDoubleTap: toggle,
  onHoldStart: () => {
    // 收起控制条和倍速菜单，底部让给倍速条
    showSpeed.value = false
    showControls.value = false
    clearTimeout(hideTimer)
    holdPick.value = null
    setLiveRate(HOLD_RATE)
  },
  onHoldMove: (e) => {
    const p = pickFromEvent(e)
    if (p === holdPick.value) return
    holdPick.value = p
    setLiveRate(p ?? HOLD_RATE)
  },
  onHoldEnd: (cancelled) => {
    const p = cancelled ? null : holdPick.value
    holdPick.value = null
    if (p == null) {
      setLiveRate(playerPrefs.rate)
      return
    }
    playerPrefs.rate = p
    setLiveRate(p) // 选的和原来一样时 watch 不触发，这里直接设
    flash(`已改成 ${p} 倍速`, 1200)
  },
})
function onStageMove(e: PointerEvent) {
  onMove(e)
  if (!holding.value) poke()
}
// 长按拖动时不让页面跟着滚（竖屏时往下拖会滚动页面，长按就被系统打断了）
function blockScroll(e: TouchEvent) {
  if (holding.value && e.cancelable) e.preventDefault()
}

// ---------- 进度条拖动 ----------
const bar = ref<HTMLElement>()
const dragging = ref(false)
const dragPos = ref(0)
function posFromEvent(e: PointerEvent) {
  const r = bar.value!.getBoundingClientRect()
  return Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)) * dur.value
}
function barDown(e: PointerEvent) {
  if (!dur.value) return
  dragging.value = true
  dragPos.value = posFromEvent(e)
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
}
function barMove(e: PointerEvent) {
  if (dragging.value) dragPos.value = posFromEvent(e)
}
function barUp() {
  if (!dragging.value) return
  dragging.value = false
  seekTo(dragPos.value)
}
const shownPos = computed(() => (dragging.value ? dragPos.value : cur.value))
const pct = (v: number) => (dur.value ? Math.min(100, (v / dur.value) * 100) : 0) + '%'

// ---------- 全屏 ----------
type FsEl = HTMLElement & { webkitRequestFullscreen?: () => void }
function toggleFull() {
  const el = stage.value as FsEl | undefined
  if (!el) return
  const doc = document as Document & { webkitFullscreenElement?: Element; webkitExitFullscreen?: () => void }
  if (document.fullscreenElement || doc.webkitFullscreenElement) {
    ;(document.exitFullscreen || doc.webkitExitFullscreen)?.call(document)
    return
  }
  const req = el.requestFullscreen || el.webkitRequestFullscreen
  if (req) {
    Promise.resolve(req.call(el)).catch(() => {})
    if (isMobile()) {
      lockLandscape()
      rotatedByUs = true // 我们转的屏，退出时负责转回来
    }
  }
}
function onFsChange() {
  const doc = document as Document & { webkitFullscreenElement?: Element }
  isFull.value = !!(document.fullscreenElement || doc.webkitFullscreenElement)
  if (!isFull.value && rotatedByUs) {
    unlockOrientation()
    rotatedByUs = false
  }
  poke()
}

// ---------- 画中画 ----------
function togglePip() {
  const v = video.value
  if (!v) return
  const doc = document as Document & { pictureInPictureElement?: Element }
  if (doc.pictureInPictureElement) {
    ;(document as unknown as { exitPictureInPicture: () => Promise<void> }).exitPictureInPicture().catch(() => {})
  } else {
    ;(v as unknown as { requestPictureInPicture: () => Promise<void> }).requestPictureInPicture().catch(() => {
      toast('无法进入画中画')
    })
  }
}

// ---------- 键盘（桌面）----------
function stepRate(dir: number) {
  const i = SPEEDS.indexOf(playerPrefs.rate)
  const next = SPEEDS[Math.min(SPEEDS.length - 1, Math.max(0, (i < 0 ? 2 : i) + dir))]
  playerPrefs.rate = next
  toast('播放速度 ' + next + '×')
}
function onKey(e: KeyboardEvent) {
  if ((e.target as HTMLElement)?.closest('input,textarea,select')) return
  if (!props.src) return
  if (showSettings.value) return // 面板开着时不响应快捷键，避免拖滑块时乱动
  const v = video.value
  if (e.key === ' ') { e.preventDefault(); toggle() }
  else if (e.key === 'ArrowLeft') { e.preventDefault(); seekBy(-playerPrefs.seek) }
  else if (e.key === 'ArrowRight') { e.preventDefault(); seekBy(playerPrefs.seek) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); playerPrefs.vol = Math.min(1, Math.round((playerPrefs.vol + 0.05) * 100) / 100) }
  else if (e.key === 'ArrowDown') { e.preventDefault(); playerPrefs.vol = Math.max(0, Math.round((playerPrefs.vol - 0.05) * 100) / 100) }
  else if (e.key === 'f' || e.key === 'F') toggleFull()
  else if (e.key === 'm' || e.key === 'M') { if (v) { v.muted = !v.muted; toast(v.muted ? '已静音' : '已取消静音') } }
  else if (e.key === '[') stepRate(-1)
  else if (e.key === ']') stepRate(1)
}

onMounted(() => {
  document.addEventListener('fullscreenchange', onFsChange)
  document.addEventListener('webkitfullscreenchange', onFsChange)
  window.addEventListener('keydown', onKey)
  document.addEventListener('visibilitychange', onPageHide)
  window.addEventListener('pagehide', onPageHide)
  stage.value?.addEventListener('touchmove', blockScroll, { passive: false })
  acquireWakeLock() // 播放页打开期间保持屏幕常亮（暂停也算），离开页面立即恢复
  load()
})
// 一点返回就先存一次进度：卸载时那次保存要等返回动画放完才执行，片库页早就算完"接着看"了，会差最后几秒
onBeforeRouteLeave(() => {
  saveNow()
})

onBeforeUnmount(() => {
  document.removeEventListener('fullscreenchange', onFsChange)
  document.removeEventListener('webkitfullscreenchange', onFsChange)
  window.removeEventListener('keydown', onKey)
  document.removeEventListener('visibilitychange', onPageHide)
  window.removeEventListener('pagehide', onPageHide)
  stage.value?.removeEventListener('touchmove', blockScroll)
  clearTimeout(hideTimer)
  clearTimeout(hintTimer)
  disposeGestures()
  saveNow()
  destroy()
  releaseWakeLock()
  if (document.fullscreenElement) document.exitFullscreen?.().catch(() => {})
  if (rotatedByUs) {
    unlockOrientation()
    rotatedByUs = false
  }
})

function fmt(s: number) {
  if (!isFinite(s) || s < 0) s = 0
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = Math.floor(s % 60)
  const mm = String(m).padStart(h ? 2 : 1, '0')
  return (h ? h + ':' : '') + mm + ':' + String(sec).padStart(2, '0')
}

const overlayMsg = computed(() => playError.value || props.message || '')
const controlsVisible = computed(() => showControls.value || !playing.value || showSpeed.value || showSettings.value)
</script>

<template>
  <div
    ref="stage"
    class="stage"
    :class="{ full: isFull, idle: playing && !controlsVisible }"
    @pointermove="onStageMove"
    @pointerdown="onDown"
    @pointerup="onUp"
    @pointercancel="onCancel"
    @pointerleave="onLeave"
  >
    <video ref="video" class="video" playsinline webkit-playsinline preload="auto"
      @play="playing = true" @playing="playing = true; buffering = false" @pause="onPause"
      @waiting="buffering = true" @canplay="buffering = false" @seeking="buffering = true" @seeked="buffering = false"
      @timeupdate="onTime" @durationchange="onTime" @progress="onTime" @ended="onEnded"
      @contextmenu.prevent />

    <!-- 解析中 / 缓冲中 -->
    <div v-if="resolving || (buffering && src && !overlayMsg)" class="center-state"><Spinner :size="48" /></div>

    <!-- 失败或提示 -->
    <div v-if="overlayMsg && !resolving" class="center-state msg">
      <Icon name="error" :size="32" />
      <div class="t-body-m">{{ overlayMsg }}</div>
    </div>

    <!-- 没有播放地址时的占位 -->
    <div v-if="!src && !resolving && !overlayMsg" class="center-state hint t-body-m">选择下方的片源和集数开始播放</div>

    <!-- 长按倍速、快进快退、改倍速的提示 -->
    <div v-if="holding" class="toast t-label-l">{{ holdPick == null ? `${HOLD_RATE} 倍速播放中` : `松开手指，就一直用 ${holdPick} 倍速` }}</div>
    <div v-else-if="flashText" class="toast t-label-l">{{ flashText }}</div>

    <!-- 长按时底部的倍速条：手指拖到哪个倍速上松开，就固定用哪个 -->
    <Transition name="strip">
      <div v-if="holding" class="ratestrip">
        <div class="rs-tip t-label-m">拖到想要的倍速上松开手指，就一直用这个倍速</div>
        <div ref="rateRow" class="rs-row">
          <span
            v-for="s in SPEEDS"
            :key="s"
            class="rs-chip"
            :class="{ on: s === (holdPick ?? HOLD_RATE), pick: s === holdPick }"
          >{{ rateText(s) }}</span>
        </div>
      </div>
    </Transition>

    <!-- 控制层 -->
    <div class="ctrl top" :class="{ show: controlsVisible }">
      <IconButton v-if="isFull" icon="back" label="退出全屏" @click="toggleFull" />
      <div class="t-title-m clamp-1 grow">{{ title }}</div>
      <IconButton icon="tune" label="播放设置" @click="showSettings = !showSettings" />
    </div>

    <button v-if="src && !playing && !resolving && !overlayMsg && !buffering" class="bigplay" type="button" aria-label="播放" @click.stop="toggle">
      <Icon name="play" :size="40" />
    </button>

    <div class="ctrl bottom" :class="{ show: controlsVisible }">
      <div
        ref="bar"
        class="seek"
        @pointerdown.stop="barDown"
        @pointermove="barMove"
        @pointerup.stop="barUp"
        @pointercancel="barUp"
      >
        <div class="track">
          <i class="buf" :style="{ width: pct(buffered) }" />
          <i class="played" :style="{ width: pct(shownPos) }" />
        </div>
        <i class="thumb" :class="{ drag: dragging }" :style="{ left: pct(shownPos) }" />
      </div>
      <div class="row">
        <IconButton :icon="playing ? 'pause' : 'play'" :label="playing ? '暂停' : '播放'" @click.stop="toggle" />
        <IconButton icon="rewind" :label="`快退 ${playerPrefs.seek} 秒`" @click.stop="seekBy(-playerPrefs.seek)" />
        <IconButton icon="forward" :label="`快进 ${playerPrefs.seek} 秒`" @click.stop="seekBy(playerPrefs.seek)" />
        <IconButton v-if="hasPrev" class="epnav" icon="skipPrevious" label="上一集" @click.stop="emit('prev')" />
        <IconButton v-if="hasNext" class="epnav" icon="skipNext" label="下一集" @click.stop="emit('next')" />
        <span class="time t-label-m">{{ fmt(shownPos) }} / {{ fmt(dur) }}</span>
        <span class="grow" />
        <div class="speedwrap">
          <button v-ripple class="speedbtn t-label-l" type="button" @click.stop="showSpeed = !showSpeed">{{ playerPrefs.rate }}x</button>
          <Transition name="pop">
            <div v-if="showSpeed" class="speedmenu" @pointerup.stop>
              <button
                v-for="s in SPEEDS"
                :key="s"
                v-ripple
                class="t-label-l"
                :class="{ on: s === playerPrefs.rate }"
                type="button"
                @click.stop="setRate(s)"
              >{{ s }}x</button>
            </div>
          </Transition>
        </div>
        <IconButton :icon="isFull ? 'fullscreenExit' : 'fullscreen'" :label="isFull ? '退出全屏' : '全屏'" @click.stop="toggleFull" />
      </div>
    </div>

    <!-- 播放设置面板：渲染在 stage 内部，全屏时才能看见 -->
    <!-- 播放设置：不全屏时搬到页面最上层，悬浮在屏幕正中间（播放器框太小，关在里面会被切掉）；
         全屏时必须留在 .stage 里（全屏只显示 .stage 里的东西），悬浮在画面右边 -->
    <Teleport to="body" :disabled="isFull">
      <PlayerSettings v-if="showSettings" :floating="!isFull" @close="showSettings = false" @pip="togglePip" />
    </Teleport>
  </div>
</template>

<style scoped>
.stage { position: relative; width: 100%; aspect-ratio: 16 / 9; background: #000; color: #fff; overflow: hidden; touch-action: manipulation; user-select: none; -webkit-user-select: none; container-type: inline-size; }
.stage.full { aspect-ratio: auto; height: 100%; }
.stage.idle { cursor: none; }
.video { position: absolute; inset: 0; width: 100%; height: 100%; background: #000; object-fit: contain; }
.grow { flex: 1; min-width: 0; }

.center-state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; pointer-events: none; text-align: center; padding: 0 24px; }
.center-state .m3-spinner { color: #fff; }
.center-state.msg { color: #ffdad6; }
.center-state.hint { color: rgb(255 255 255 / 0.7); }
/* 左右各留 12px、内容多宽就多宽、居中（left:50% 的写法最多只有一半宽，长提示会被挤成两行） */
.toast { position: absolute; top: 16%; left: 12px; right: 12px; width: fit-content; margin: 0 auto; padding: 6px 14px; border-radius: 16px; background: rgb(0 0 0 / 0.6); pointer-events: none; text-align: center; }

/* 长按倍速条：贴底部的悬浮圆角条，全屏时不超过 520 宽、居中 */
.ratestrip { position: absolute; left: 10px; right: 10px; bottom: 10px; max-width: 520px; margin: 0 auto; padding: 6px 6px 6px; border-radius: 16px; background: rgb(0 0 0 / 0.62); backdrop-filter: blur(6px); -webkit-backdrop-filter: blur(6px); pointer-events: none; text-align: center; }
.rs-tip { padding: 0 6px 6px; color: rgb(255 255 255 / 0.85); }
.rs-row { display: flex; gap: 2px; }
.rs-chip { flex: 1 1 0; min-width: 0; height: 38px; display: flex; align-items: center; justify-content: center; border-radius: 10px; font-size: 14px; font-weight: 500; font-variant-numeric: tabular-nums; white-space: nowrap; color: rgb(255 255 255 / 0.85); transition: background-color var(--md-dur-short) linear, transform var(--md-dur-short) var(--md-ease-standard); }
.rs-chip.on { box-shadow: inset 0 0 0 1.5px rgb(255 255 255 / 0.7); color: #fff; }
.rs-chip.pick { background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); box-shadow: none; transform: scale(1.06); }
.strip-enter-active { transition: opacity 150ms linear, transform 250ms var(--md-ease-emphasized-decel); }
.strip-leave-active { transition: opacity 120ms linear; }
.strip-enter-from { opacity: 0; transform: translateY(12px); }
.strip-leave-to { opacity: 0; }

.bigplay { position: absolute; left: 50%; top: 50%; width: 72px; height: 72px; margin: -36px 0 0 -36px; border-radius: 50%; background: rgb(0 0 0 / 0.5); color: #fff; display: flex; align-items: center; justify-content: center; backdrop-filter: blur(4px); }

.ctrl { position: absolute; left: 0; right: 0; opacity: 0; transition: opacity var(--md-dur-medium) var(--md-ease-standard); pointer-events: none; }
.ctrl.show { opacity: 1; pointer-events: auto; }
.ctrl.top { top: 0; display: flex; align-items: center; padding: 4px 12px 24px; background: linear-gradient(rgb(0 0 0 / 0.65), transparent); }
.ctrl.bottom { bottom: 0; padding: 24px 8px 0; background: linear-gradient(transparent, rgb(0 0 0 / 0.7)); }
.ctrl :deep(.m3-iconbtn) { color: #fff; width: 44px; height: 44px; }

.seek { position: relative; height: 24px; margin: 0 8px; touch-action: none; cursor: pointer; }
.track { position: absolute; left: 0; right: 0; top: 10px; height: 4px; border-radius: 2px; background: rgb(255 255 255 / 0.3); overflow: hidden; }
.track i { position: absolute; left: 0; top: 0; bottom: 0; }
.buf { background: rgb(255 255 255 / 0.4); }
.played { background: var(--md-sys-color-primary-container); }
.thumb { position: absolute; top: 12px; width: 14px; height: 14px; margin: -7px 0 0 -7px; border-radius: 50%; background: var(--md-sys-color-primary-container); box-shadow: 0 0 0 0 rgb(255 255 255 / 0.3); transition: transform var(--md-dur-short) var(--md-ease-standard), box-shadow var(--md-dur-short) linear; }
.thumb.drag { transform: scale(1.5); box-shadow: 0 0 0 8px rgb(255 255 255 / 0.25); }

.row { display: flex; align-items: center; gap: 0; }
/* 播放器窄（竖着拿手机、播放器是圆角卡片）时一排放不下：收起"上一集/下一集"（下面就是选集），按钮也收小一点；
   横屏全屏时播放器够宽，自动恢复 */
@container (max-width: 400px) {
  .row .epnav { display: none; }
  .ctrl :deep(.m3-iconbtn) { width: 40px; height: 40px; }
  .speedbtn { padding: 0 8px; }
}
.time { margin-left: 4px; font-variant-numeric: tabular-nums; opacity: 0.9; white-space: nowrap; }
.speedwrap { position: relative; }
.speedbtn { height: 36px; padding: 0 12px; border-radius: var(--md-shape-full); color: #fff; }
.speedmenu { position: absolute; right: 0; bottom: 44px; display: flex; flex-direction: column; padding: 8px 0; border-radius: var(--md-shape-md); background: rgb(30 30 30 / 0.94); box-shadow: var(--md-elev-3); transform-origin: bottom right; }
.speedmenu button { height: 40px; min-width: 88px; padding: 0 20px; color: #fff; text-align: left; }
.speedmenu button.on { color: var(--md-sys-color-primary-container); }
.pop-enter-active { transition: opacity 150ms linear, transform 250ms var(--md-ease-emphasized-decel); }
.pop-leave-active { transition: opacity 100ms linear; }
.pop-enter-from { opacity: 0; transform: scale(0.8); }
.pop-leave-to { opacity: 0; }
</style>
