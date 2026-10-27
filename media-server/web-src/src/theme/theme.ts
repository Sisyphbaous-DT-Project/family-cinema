// Material 3 动态配色：从"种子色"生成亮/暗两套颜色，写成 :root 上的 CSS 变量。
import { reactive, watch } from 'vue'
import { argbFromHex, Hct, hexFromArgb, themeFromSourceColor, TonalPalette } from '@material/material-color-utilities'
import { readJSON, writeJSON } from '../composables/storage'
import { bcall } from '../composables/bridge'

export type ThemeMode = 'auto' | 'light' | 'dark'

/** 预设种子色（第一个是默认色，接近 Kazumi 的绿色） */
export const SEED_PRESETS: { name: string; hex: string }[] = [
  { name: '青草绿', hex: '#4caf50' },
  { name: '天空蓝', hex: '#2196f3' },
  { name: '樱花粉', hex: '#e91e63' },
  { name: '葡萄紫', hex: '#9c27b0' },
  { name: '落日橙', hex: '#ff9800' },
  { name: '湖水青', hex: '#009688' },
  { name: '靛青', hex: '#3f51b5' },
  { name: '朱砂红', hex: '#f44336' },
]

const KEY = 'fc.theme'

export const themeState = reactive<{ mode: ThemeMode; seed: string }>({
  mode: 'auto',
  seed: SEED_PRESETS[0].hex,
})

const kebab = (s: string) => s.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase())

/** 按 M3 规范的色调(tone)取值生成整套颜色 */
export function buildTokens(seedHex: string, dark: boolean): Record<string, string> {
  const argb = argbFromHex(seedHex)
  const { palettes: base } = themeFromSourceColor(argb)
  // M3 默认把主色饱和度抬到至少 48，绿色在屏幕上又亮又刺眼（选中的整块、进度条尤其明显）。
  // 这里只借用种子色的色相，饱和度压到 30 以内，得到柔和的灰绿/豆沙色系；中性色和错误色沿用默认。
  const hct = Hct.fromInt(argb)
  const chroma = Math.min(30, hct.chroma)
  const p = {
    primary: TonalPalette.fromHueAndChroma(hct.hue, chroma),
    secondary: TonalPalette.fromHueAndChroma(hct.hue, Math.min(12, chroma / 2)),
    tertiary: TonalPalette.fromHueAndChroma(hct.hue + 60, Math.min(22, chroma)),
    neutral: base.neutral,
    neutralVariant: base.neutralVariant,
    error: base.error,
  }
  const t = (pal: typeof p.primary, tone: number) => hexFromArgb(pal.tone(tone))
  const n = p.neutral
  const nv = p.neutralVariant
  const role = (pal: typeof p.primary) =>
    dark
      ? { main: t(pal, 80), on: t(pal, 20), container: t(pal, 30), onContainer: t(pal, 90) }
      : { main: t(pal, 40), on: t(pal, 100), container: t(pal, 90), onContainer: t(pal, 10) }
  const pr = role(p.primary)
  const se = role(p.secondary)
  const te = role(p.tertiary)
  const er = role(p.error)
  const v = {
    primary: pr.main, onPrimary: pr.on, primaryContainer: pr.container, onPrimaryContainer: pr.onContainer,
    secondary: se.main, onSecondary: se.on, secondaryContainer: se.container, onSecondaryContainer: se.onContainer,
    tertiary: te.main, onTertiary: te.on, tertiaryContainer: te.container, onTertiaryContainer: te.onContainer,
    error: er.main, onError: er.on, errorContainer: er.container, onErrorContainer: er.onContainer,
    surface: t(n, dark ? 6 : 98),
    onSurface: t(n, dark ? 90 : 10),
    surfaceVariant: t(nv, dark ? 30 : 90),
    onSurfaceVariant: t(nv, dark ? 80 : 30),
    outline: t(nv, dark ? 60 : 50),
    outlineVariant: t(nv, dark ? 30 : 80),
    surfaceContainerLowest: t(n, dark ? 4 : 100),
    surfaceContainerLow: t(n, dark ? 10 : 96),
    surfaceContainer: t(n, dark ? 12 : 94),
    surfaceContainerHigh: t(n, dark ? 17 : 92),
    surfaceContainerHighest: t(n, dark ? 22 : 90),
    inverseSurface: t(n, dark ? 90 : 20),
    inverseOnSurface: t(n, dark ? 20 : 95),
    inversePrimary: t(p.primary, dark ? 40 : 80),
    scrim: '#000000',
  }
  const out: Record<string, string> = {}
  for (const [k, val] of Object.entries(v)) out['--md-sys-color-' + kebab(k)] = val
  // 悬浮卡片（全站统一的"一块一块浮起来"的模块样式，见 base.css 的 .fc-card）：
  // 浅色 = 白卡片 + 很淡的阴影；深色 = 比背景亮一档的卡片 + 深一点的阴影（深色背景上阴影本来就不显）
  out['--fc-card'] = dark ? v.surfaceContainer : v.surfaceContainerLowest
  out['--fc-float'] = dark
    ? '0 2px 4px rgb(0 0 0 / 0.35), 0 8px 24px rgb(0 0 0 / 0.35)'
    : '0 1px 3px rgb(0 0 0 / 0.05), 0 6px 20px rgb(0 0 0 / 0.07)'
  return out
}

let mq: MediaQueryList | null = null

function isDark(): boolean {
  if (themeState.mode === 'dark') return true
  if (themeState.mode === 'light') return false
  return !!mq && mq.matches
}

/** 把当前主题写进 :root，并同步浏览器地址栏颜色 */
export function applyTheme(): void {
  const dark = isDark()
  const tokens = buildTokens(themeState.seed, dark)
  const root = document.documentElement
  for (const [k, val] of Object.entries(tokens)) root.style.setProperty(k, val)
  root.style.colorScheme = dark ? 'dark' : 'light'
  const meta = document.querySelector('meta[name="theme-color"]')
  if (meta) meta.setAttribute('content', tokens['--md-sys-color-surface'])
  // 手机 App 里：状态栏、底部手势条跟着页面底色走，和标题栏连成一整块（App 1.5 起才有这个桥接口，旧版静默跳过）
  bcall((b) => b.setSystemBars?.(tokens['--md-sys-color-surface'], dark))
}

export function initTheme(): void {
  const saved = readJSON<{ mode?: ThemeMode; seed?: string }>(KEY, {})
  if (saved.mode === 'light' || saved.mode === 'dark' || saved.mode === 'auto') themeState.mode = saved.mode
  if (saved.seed && /^#[0-9a-f]{6}$/i.test(saved.seed)) themeState.seed = saved.seed
  mq = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null
  mq?.addEventListener?.('change', () => themeState.mode === 'auto' && applyTheme())
  applyTheme()
  watch(themeState, () => {
    writeJSON(KEY, { mode: themeState.mode, seed: themeState.seed })
    applyTheme()
  })
}
