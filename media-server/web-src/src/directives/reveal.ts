import type { Directive } from 'vue'
import { motionOK } from '../composables/motion'

// v-reveal="序号"：卡片、列表行第一次出现时依次浮上来（样式见 styles/motion.css 的 .reveal）。
// 放完就摘掉：KeepAlive 的页面切回来时元素会重新挂到页面上，CSS 动画会从头再放一遍，
// 返回时海报要落回的那张卡片就成了透明的，所以只放这一次。
export const reveal: Directive<HTMLElement, number | undefined> = {
  mounted(el, { value }) {
    if (!motionOK()) return
    el.style.setProperty('--i', String(value ?? 0))
    el.classList.add('reveal')
    const done = (e: AnimationEvent) => {
      if (e.target !== el) return
      el.classList.remove('reveal')
      el.removeEventListener('animationend', done)
    }
    el.addEventListener('animationend', done)
  },
}
