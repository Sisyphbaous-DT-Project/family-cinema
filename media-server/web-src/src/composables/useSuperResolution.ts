import { ref, watch, type Ref } from 'vue'
import {
  ANIME4K_HIGHEREND_MODE_A_FAST,
  ANIME4K_LOWEREND_MODE_A_FAST,
  VideoUpscaler,
} from 'anime4k.js'
import { readJSON, writeJSON } from './storage'
import { toast } from './snackbar'

export type SuperResolutionMode = 'off' | 'efficiency' | 'quality'

const STORAGE_KEY = 'player-super-resolution'
const PRESETS = {
  efficiency: ANIME4K_LOWEREND_MODE_A_FAST,
  quality: ANIME4K_HIGHEREND_MODE_A_FAST,
}
const LABELS: Record<SuperResolutionMode, string> = {
  off: '关闭',
  efficiency: '效率',
  quality: '质量',
}

function savedMode(seriesKey: string): SuperResolutionMode {
  const modes = readJSON<Record<string, SuperResolutionMode>>(STORAGE_KEY, {})
  const mode = modes[seriesKey]
  return mode === 'efficiency' || mode === 'quality' ? mode : 'off'
}

export function useSuperResolution(
  video: Ref<HTMLVideoElement | undefined>,
  canvas: Ref<HTMLCanvasElement | undefined>,
  seriesKey: () => string,
) {
  const mode = ref<SuperResolutionMode>('off')
  const active = ref(false)
  let engine: VideoUpscaler | null = null

  function stop() {
    engine?.detachVideo()
    engine = null
    active.value = false
    if (canvas.value) canvas.value.style.visibility = 'hidden'
  }

  function fail(message: string): false {
    stop()
    toast(message)
    return false
  }

  function sync(): boolean {
    const v = video.value
    const c = canvas.value
    stop()
    if (mode.value === 'off' || !v || !c || v.readyState < 2 || !v.videoWidth) return false

    try {
      c.style.visibility = 'hidden'
      engine = new VideoUpscaler(PRESETS[mode.value])
      engine.attachVideo(v, c)
      engine.start()
      // start() 会同步画第一帧；没画出来通常是缺少浮点纹理扩展或跨域读取被拦。
      if (c.style.visibility !== 'visible') throw new Error('WebGL renderer did not produce a frame')
      active.value = true
      if (v.paused || document.hidden) pause()
      return true
    } catch {
      return fail('这个设备或视频无法开启超分，已退回普通画质')
    }
  }

  function pause() {
    if (!engine || !active.value) return
    engine.stop()
    // 暂停后不再计算新帧，但保留最后一张处理后的画面。
    if (canvas.value) canvas.value.style.visibility = 'visible'
  }

  function resume() {
    if (!engine || !active.value || document.hidden) return
    try {
      engine.start()
    } catch {
      fail('超分已中断，已退回普通画质')
    }
  }

  function setMode(next: SuperResolutionMode) {
    mode.value = next
    const modes = readJSON<Record<string, SuperResolutionMode>>(STORAGE_KEY, {})
    modes[seriesKey()] = next
    writeJSON(STORAGE_KEY, modes)
    const ok = sync()
    if (next === 'off') toast('已关闭超分')
    else if (ok) toast(`已开启超分 · ${LABELS[next]}档`)
  }

  function contextLost() {
    fail('超分画面已中断，已退回普通画质')
  }

  watch(seriesKey, () => {
    stop()
    mode.value = savedMode(seriesKey())
  }, { immediate: true })

  return { mode, active, setMode, sync, pause, resume, stop, contextLost }
}
