<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { motionOK, springEase } from '../../composables/motion'
import Icon from './Icon.vue'

const props = defineProps<{ items: { path: string; label: string; icon: string }[] }>()
const route = useRoute()
const router = useRouter()

function go(path: string) {
  if (route.path !== path) router.push(path)
}

// 选中块只有一块：换标签时从旧按钮滑到新按钮，路上被拉长一点再缩回去（像果冻追过去）
const nav = ref<HTMLElement>()
const ind = ref<HTMLElement>()
const pills = ref<HTMLElement[]>([])
let last: { x: number; y: number } | null = null

function place(animate: boolean) {
  const el = ind.value
  const navEl = nav.value
  const pill = pills.value[props.items.findIndex((it) => it.path === route.path)]
  if (!el || !navEl) return
  if (!pill) {
    // 二级页面：导航栏藏起来了，下次回来直接出现在对应位置
    el.style.opacity = '0'
    last = null
    return
  }
  const n = navEl.getBoundingClientRect()
  const p = pill.getBoundingClientRect()
  const x = p.left - n.left - navEl.clientLeft
  const y = p.top - n.top - navEl.clientTop
  el.style.width = p.width + 'px'
  el.style.height = p.height + 'px'
  el.style.transform = `translate(${x}px, ${y}px)`
  el.style.opacity = '1'
  if (animate && last && motionOK() && (last.x !== x || last.y !== y)) {
    const dx = x - last.x
    const dy = y - last.y
    const horiz = Math.abs(dx) >= Math.abs(dy)
    const pull = 1 + Math.min(Math.abs(horiz ? dx : dy) / (horiz ? p.width : p.height), 2) * 0.3
    el.animate(
      [
        { transform: `translate(${last.x}px, ${last.y}px)` },
        { transform: `translate(${last.x + dx * 0.45}px, ${last.y + dy * 0.45}px) ${horiz ? `scaleX(${pull})` : `scaleY(${pull})`}`, offset: 0.3 },
        { transform: `translate(${x}px, ${y}px)` },
      ],
      { duration: 620, easing: springEase },
    )
  }
  last = { x, y }
}

watch(() => route.path, () => nextTick(() => place(true)))
let ro: ResizeObserver | undefined
onMounted(() => {
  place(false)
  // 手机竖横屏切换、底部栏 ↔ 侧边栏、字体加载完按钮变高时重新对位（不放动画）
  ro = new ResizeObserver(() => place(false))
  if (nav.value) ro.observe(nav.value)
  pills.value.forEach((p) => p.parentElement && ro?.observe(p.parentElement))
  document.fonts?.ready.then(() => place(false))
})
onBeforeUnmount(() => ro?.disconnect())
</script>

<template>
  <nav ref="nav" class="navbar" aria-label="主导航">
    <i ref="ind" class="ind" aria-hidden="true" />
    <button
      v-for="it in items"
      :key="it.path"
      class="item"
      :class="{ active: route.path === it.path }"
      type="button"
      @click="go(it.path)"
    >
      <span ref="pills" v-ripple class="pill"><Icon :name="it.icon" :size="24" /></span>
      <span class="label t-label-m">{{ it.label }}</span>
    </button>
  </nav>
</template>

<style scoped>
/* 手机：底部导航栏——悬浮的圆角矩形（四周留空、带淡阴影，页面内容从它后面滑过） */
.navbar {
  position: absolute;
  left: 12px;
  right: 12px;
  bottom: calc(var(--nav-gap) + var(--safe-bottom));
  z-index: 10;
  display: flex;
  justify-content: space-around;
  max-width: 560px;
  height: var(--nav-h);
  margin: 0 auto;
  padding: 8px 4px;
  border-radius: 24px;
  background: var(--fc-card);
  box-shadow: var(--fc-float);
  /* 换页时单独成一层，不跟着页面推拉（见 styles/motion.css） */
  view-transition-name: navbar;
}
/* 选中块：只有一块，位置由脚本算 */
.ind {
  position: absolute;
  left: 0;
  top: 0;
  z-index: 0;
  border-radius: 12px;
  background: var(--md-sys-color-secondary-container);
  opacity: 0;
  pointer-events: none;
  transition: opacity 150ms linear;
}
.item {
  position: relative;
  z-index: 1;
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  color: var(--md-sys-color-on-surface-variant);
}
/* 图标的位置（选中块就叠在它下面） */
.pill {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 30px;
  border-radius: 12px;
  transition: color 200ms ease;
}
.item.active { color: var(--md-sys-color-on-surface); }
.item.active .pill { color: var(--md-sys-color-on-secondary-container); }
.item.active .pill :deep(.m3-icon) { animation: icon-pop 520ms var(--ease-spring); }
.item.active .label { font-weight: 700; }
@keyframes icon-pop { 0% { transform: scale(0.72); } }

/* 平板/桌面：左侧导航栏，同样是悬浮的圆角矩形 */
@media (min-width: 840px) {
  .navbar {
    top: 12px;
    bottom: 12px;
    left: 12px;
    right: auto;
    width: var(--rail-w);
    max-width: none;
    height: auto;
    margin: 0;
    flex-direction: column;
    justify-content: flex-start;
    gap: 16px;
    padding: 32px 0 16px;
  }
  .item { flex: none; gap: 4px; }
}
</style>
