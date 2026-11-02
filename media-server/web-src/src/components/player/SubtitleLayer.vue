<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { PgsRenderer } from 'libpgs'
import workerCode from 'libpgs/dist/libpgs.worker.js?raw'
import { streamURL, type Subtitle } from '../../api/local'

const props = defineProps<{
  video?: HTMLVideoElement
  subtitle?: Subtitle
  fit?: 'contain' | 'cover' | 'fill'
  // 控制条出现时字幕往上让一让，免得被进度条和按钮盖住
  lift?: boolean
}>()
const emit = defineEmits<{ (e: 'error', message: string): void }>()

const pgsHost = ref<HTMLDivElement>()
let pgsCanvas: HTMLCanvasElement | null = null
const cues = ref<string[]>([])
let track: HTMLTrackElement | null = null
let onCues: (() => void) | null = null
let renderer: PgsRenderer | null = null
let workerUrl = ''
let generation = 0

const absoluteURL = (path: string) => new URL(streamURL(path), location.href).href

function updateCues() {
  const active = track?.track.activeCues
  // getCueAsHTML 会把 <i> 这类标记和 &amp; 这类转义解析掉，只留下该显示的文字
  cues.value = active ? Array.from(active).map((cue) => (cue as VTTCue).getCueAsHTML().textContent ?? '').filter(Boolean) : []
}

function cleanup() {
  generation++
  cues.value = []
  if (track) {
    if (onCues) {
      track.track.removeEventListener('cuechange', onCues)
      track.removeEventListener('load', onCues)
    }
    track.onerror = null
    track.track.mode = 'disabled'
    track.remove()
  }
  track = null
  onCues = null
  renderer?.dispose()
  renderer = null
  if (workerUrl) URL.revokeObjectURL(workerUrl)
  workerUrl = ''
  // 画布交给 libpgs 后不能再拿来画，也不能再交一次：整块扔掉，下次换新的
  pgsCanvas?.remove()
  pgsCanvas = null
}

async function startPgs(video: HTMLVideoElement, subtitle: Subtitle, seq: number) {
  const host = pgsHost.value
  if (!host) return
  const canvas = document.createElement('canvas')
  Object.assign(canvas.style, { position: 'absolute', inset: '0', width: '100%', height: '100%', pointerEvents: 'none', objectFit: props.fit ?? 'contain' })
  host.appendChild(canvas)
  pgsCanvas = canvas
  try {
    workerUrl = URL.createObjectURL(new Blob([workerCode], { type: 'text/javascript' }))
    renderer = new PgsRenderer({
      workerUrl,
      video,
      canvas,
      subUrl: absoluteURL(subtitle.path),
      aspectRatio: props.fit ?? 'contain',
    })
    await renderer.ready
  } catch {
    if (seq !== generation) return
    renderer?.dispose()
    renderer = null
    if (workerUrl) URL.revokeObjectURL(workerUrl)
    workerUrl = ''
    emit('error', '这条图片字幕没能加载，可以换一条或关闭字幕')
  }
}

function sync() {
  cleanup()
  const { video, subtitle } = props
  if (!video || !subtitle) return
  const seq = generation
  if (subtitle.kind === 'pgs') {
    void startPgs(video, subtitle, seq)
    return
  }

  const el = document.createElement('track')
  el.kind = 'subtitles'
  el.label = subtitle.title || subtitle.lang || '字幕'
  el.srclang = subtitle.lang || ''
  el.src = absoluteURL(subtitle.path)
  onCues = updateCues
  el.track.addEventListener('cuechange', onCues)
  el.addEventListener('load', onCues)
  el.onerror = () => {
    if (seq === generation) emit('error', '这条文字字幕没能加载，可以换一条或关闭字幕')
  }
  track = el
  video.appendChild(el)
  el.track.mode = 'hidden'
  updateCues()
}

watch(() => [props.video, props.subtitle?.path, props.subtitle?.kind] as const, sync, { immediate: true, flush: 'post' })
watch(() => props.fit, (fit) => {
  if (pgsCanvas) pgsCanvas.style.objectFit = fit ?? 'contain'
  if (renderer) renderer.aspectRatio = fit ?? 'contain'
})
onBeforeUnmount(cleanup)
</script>

<template>
  <div class="subtitle-layer" :class="{ lift }" aria-hidden="true">
    <div v-if="subtitle?.kind === 'text' && cues.length" class="text-subs">
      <div v-for="(cue, i) in cues" :key="i" class="cue">{{ cue }}</div>
    </div>
    <div v-show="subtitle?.kind === 'pgs'" ref="pgsHost" class="pgs-subs" />
  </div>
</template>

<style scoped>
.subtitle-layer { position: absolute; inset: 0; z-index: 2; pointer-events: none; transition: transform var(--md-dur-medium) var(--md-ease-standard); }
.subtitle-layer.lift { transform: translateY(-68px); }
.text-subs { position: absolute; left: 6%; right: 6%; bottom: 12%; display: flex; flex-direction: column; align-items: center; gap: 4px; text-align: center; }
.cue { max-width: 100%; white-space: pre-wrap; color: #fff; font-size: clamp(14px, 3.2vw, 32px); font-size: clamp(14px, 3.2cqi, 32px); font-weight: 500; line-height: 1.35; -webkit-text-stroke: 0.6px #000; text-shadow: 0 1px 2px #000, 1px 0 2px #000, -1px 0 2px #000; }
.pgs-subs { position: absolute; inset: 0; width: 100%; height: 100%; pointer-events: none; }
</style>
