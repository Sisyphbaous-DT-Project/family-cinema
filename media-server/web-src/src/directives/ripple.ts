import type { Directive } from 'vue'

// v-ripple：按下时从触点扩散一圈水波纹（Material 触摸反馈）。
// 元素自动加上 m3-press 类（状态层 + 裁剪），颜色取元素的 currentColor。
export const ripple: Directive<HTMLElement> = {
  mounted(el) {
    el.classList.add('m3-press')
    el.addEventListener('pointerdown', (e: PointerEvent) => {
      if (e.button !== 0 && e.pointerType === 'mouse') return
      if (el.hasAttribute('disabled')) return
      const r = el.getBoundingClientRect()
      const size = Math.hypot(Math.max(e.clientX - r.left, r.right - e.clientX), Math.max(e.clientY - r.top, r.bottom - e.clientY)) * 2
      const dot = document.createElement('span')
      dot.className = 'm3-ripple'
      dot.style.width = dot.style.height = size + 'px'
      dot.style.left = e.clientX - r.left - size / 2 + 'px'
      dot.style.top = e.clientY - r.top - size / 2 + 'px'
      el.appendChild(dot)
      dot.addEventListener('animationend', () => dot.remove())
    })
  },
}
