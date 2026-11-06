import { reactive, watch } from 'vue'
import { argbFromHex, Hct, hexFromArgb, themeFromSourceColor, TonalPalette } from '@material/material-color-utilities'

export type ThemeMode = 'auto' | 'light' | 'dark'
export const theme = reactive<{ mode: ThemeMode; dark: boolean }>({ mode: 'auto', dark: false })
const media = window.matchMedia('(prefers-color-scheme: dark)')

// 与家庭影院网页保持相同的绿色种子、柔和色度和 Material 3 色调。
function applyTheme(): void {
  const dark = theme.mode === 'dark' || (theme.mode === 'auto' && media.matches)
  const seed = argbFromHex('#4caf50')
  const base = themeFromSourceColor(seed).palettes
  const hct = Hct.fromInt(seed)
  const chroma = Math.min(30, hct.chroma)
  const primary = TonalPalette.fromHueAndChroma(hct.hue, chroma)
  const secondary = TonalPalette.fromHueAndChroma(hct.hue, Math.min(12, chroma / 2))
  const tertiary = TonalPalette.fromHueAndChroma(hct.hue + 60, Math.min(22, chroma))
  const tone = (palette: TonalPalette, value: number) => hexFromArgb(palette.tone(value))
  const colors: Record<string, string> = {}
  for (const [name, palette] of Object.entries({ primary, secondary, tertiary, error: base.error })) {
    colors[name] = tone(palette, dark ? 80 : 40)
    colors[`on-${name}`] = tone(palette, dark ? 20 : 100)
    colors[`${name}-container`] = tone(palette, dark ? 30 : 90)
    colors[`on-${name}-container`] = tone(palette, dark ? 90 : 10)
  }
  const n = base.neutral
  const nv = base.neutralVariant
  Object.assign(colors, {
    surface: tone(n, dark ? 6 : 98),
    'on-surface': tone(n, dark ? 90 : 10),
    'on-surface-variant': tone(nv, dark ? 80 : 30),
    'surface-container-lowest': tone(n, dark ? 4 : 100),
    'surface-container-low': tone(n, dark ? 10 : 96),
    'surface-container': tone(n, dark ? 12 : 94),
    'surface-container-high': tone(n, dark ? 17 : 92),
    'surface-container-highest': tone(n, dark ? 22 : 90),
    outline: tone(nv, dark ? 60 : 50),
    'outline-variant': tone(nv, dark ? 30 : 80),
    'inverse-surface': tone(n, dark ? 90 : 20),
    'inverse-on-surface': tone(n, dark ? 20 : 95),
  })
  for (const [name, value] of Object.entries(colors)) {
    document.documentElement.style.setProperty(`--md-sys-color-${name}`, value)
  }
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  theme.dark = dark
}

export function initTheme(): void {
  try {
    const saved = localStorage.getItem('family-cinema.setup.theme')
    if (saved === 'auto' || saved === 'light' || saved === 'dark') theme.mode = saved
  } catch { /* 内嵌页面不保证提供持久化存储，主题仍可在本次会话切换。 */ }
  applyTheme()
  media.addEventListener('change', applyTheme)
  watch(() => theme.mode, () => {
    applyTheme()
    try { localStorage.setItem('family-cinema.setup.theme', theme.mode) } catch { }
  })
}
