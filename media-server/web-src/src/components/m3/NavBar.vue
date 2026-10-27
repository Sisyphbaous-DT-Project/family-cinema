<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'
import Icon from './Icon.vue'

defineProps<{ items: { path: string; label: string; icon: string }[] }>()
const route = useRoute()
const router = useRouter()

function go(path: string) {
  if (route.path !== path) router.push(path)
}
</script>

<template>
  <nav class="navbar" aria-label="主导航">
    <button
      v-for="it in items"
      :key="it.path"
      class="item"
      :class="{ active: route.path === it.path }"
      type="button"
      @click="go(it.path)"
    >
      <span v-ripple class="pill"><Icon :name="it.icon" :size="24" /></span>
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
}
.item {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  color: var(--md-sys-color-on-surface-variant);
}
/* 选中的那一项：圆角矩形底块，从中心向两侧展开 */
.pill {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 30px;
  border-radius: 12px;
  isolation: isolate;
}
.pill::before {
  content: "";
  position: absolute;
  inset: 0;
  border-radius: inherit;
  background: var(--md-sys-color-secondary-container);
  transform: scaleX(0.3);
  opacity: 0;
  transition: transform var(--md-dur-medium) var(--md-ease-emphasized), opacity 100ms linear;
  z-index: -2;
}
.item.active { color: var(--md-sys-color-on-surface); }
.item.active .pill { color: var(--md-sys-color-on-secondary-container); }
.item.active .pill::before { transform: scaleX(1); opacity: 1; }
.item.active .label { font-weight: 700; }

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
