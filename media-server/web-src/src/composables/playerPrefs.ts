// 播放器偏好：与旧前端共用同一个 localStorage 键 "player-prefs"、同一套字段名，
// 所以在旧页面里调过的设置，新页面直接继承（反之亦然）。
import { reactive, watch } from 'vue'
import { readJSON, writeJSON } from './storage'

export interface PlayerPrefs {
  /** 倍速 */
  rate: number
  /** 画面适应：contain 原始比例 / cover 填充裁切 / fill 拉伸铺满 */
  fit: 'contain' | 'cover' | 'fill'
  /** 音量 0~1 */
  vol: number
  /** 亮度 %（40-180） */
  bri: number
  /** 对比度 %（50-180） */
  con: number
  /** 饱和度 %（0-200） */
  sat: number
  /** 锐度 0-100（用 contrast 微调模拟，浏览器没有原生锐化滤镜） */
  sharp: number
  /** 快进步长 秒（5-60 步进 5） */
  seek: number
  /** 单集循环 */
  loop: boolean
  /** 自动连播下一集 */
  auto: boolean
  /** 记忆进度（关掉就不读不写观看进度） */
  resume: boolean
  /** 水平镜像 */
  mirror: boolean
}

const DEFAULTS: PlayerPrefs = {
  rate: 1,
  fit: 'contain',
  vol: 1,
  bri: 100,
  con: 100,
  sat: 100,
  sharp: 0,
  seek: 10,
  loop: false,
  auto: true,
  resume: true,
  mirror: false,
}

const KEY = 'player-prefs'

export const playerPrefs = reactive<PlayerPrefs>({ ...DEFAULTS, ...readJSON<Partial<PlayerPrefs>>(KEY, {}) })

watch(playerPrefs, () => writeJSON(KEY, { ...playerPrefs }))

export function resetPlayerPrefs(): void {
  Object.assign(playerPrefs, DEFAULTS)
}

/** 视频滤镜串：亮度/对比度（含锐度微调）/饱和度 */
export function videoFilter(p: PlayerPrefs): string {
  return `brightness(${p.bri / 100}) contrast(${(p.con + p.sharp * 0.25) / 100}) saturate(${p.sat / 100})`
}
