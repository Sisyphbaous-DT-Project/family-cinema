import { onBeforeUnmount, onMounted, ref, watch, nextTick, type Ref } from 'vue'
import { motionOK } from './motion'

/**
 * 长文字的展开/收起：收起时只露 lines 行，展开、收起时高度平滑变化（不再一下子弹开）。
 * 文字本来就不超过 lines 行时 overflowing 为 false，页面不用显示「展开」按钮。
 */
export function useExpand(el: Ref<HTMLElement | null>, lines: number) {
  const expanded = ref(false)
  const overflowing = ref(false)
  let collapsedH = 0

  function measure() {
    const e = el.value
    if (!e) return
    const lh = parseFloat(getComputedStyle(e).lineHeight) || 20
    collapsedH = Math.round(lh * lines)
    overflowing.value = e.scrollHeight > collapsedH + 2
    if (!expanded.value) e.style.maxHeight = overflowing.value ? collapsedH + 'px' : ''
  }

  function toggle() {
    const e = el.value
    if (!e) return
    expanded.value = !expanded.value
    e.style.transition = motionOK() ? 'max-height 560ms var(--ease-soft)' : ''
    if (expanded.value) {
      e.style.maxHeight = e.scrollHeight + 'px'
      // 展开到位后放开限制，旋转屏幕等宽度变化时文字不会被截
      e.addEventListener('transitionend', () => expanded.value && (e.style.maxHeight = 'none'), { once: true })
      if (!motionOK()) e.style.maxHeight = 'none'
    } else {
      // 从"不限高"收起要先定一个起点高度，过渡才有起点
      e.style.maxHeight = e.scrollHeight + 'px'
      void e.offsetHeight
      e.style.maxHeight = collapsedH + 'px'
    }
  }

  watch(el, (e) => e && nextTick(measure), { flush: 'post' })
  onMounted(() => window.addEventListener('resize', measure))
  onBeforeUnmount(() => window.removeEventListener('resize', measure))
  return { expanded, overflowing, toggle }
}
