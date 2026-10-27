import { reactive } from 'vue'

// 全局 Snackbar：任何地方 toast('文案') 即可，由 App.vue 里的 Snackbar 组件渲染
export const snackbar = reactive<{ text: string; show: boolean; id: number }>({ text: '', show: false, id: 0 })

let timer: ReturnType<typeof setTimeout> | undefined

export function toast(text: string, ms = 3200): void {
  snackbar.text = text
  snackbar.show = true
  snackbar.id++
  clearTimeout(timer)
  timer = setTimeout(() => (snackbar.show = false), ms)
}
