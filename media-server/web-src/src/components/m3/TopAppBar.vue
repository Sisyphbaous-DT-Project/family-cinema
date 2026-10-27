<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useScrolled } from '../../composables/useScrolled'
import IconButton from './IconButton.vue'

const props = withDefaults(
  defineProps<{
    title: string
    back?: boolean
    /** 不吸顶（放在别的吸顶容器里时用，渐隐带由那个容器自己负责） */
    flat?: boolean
    /** 下边缘的渐隐带；正下方还有别的吸顶栏（如时间表的星期条）时关掉，交给下面那条 */
    fade?: boolean
  }>(),
  { fade: true },
)
const router = useRouter()
const bar = ref<HTMLElement>()
// 标题栏始终是页面底色（和手机状态栏一整块，不再划一下就变灰）；划过后下边缘出现渐隐带
const scrolled = useScrolled(bar)

function goBack() {
  if (window.history.length > 1) router.back()
  else router.replace('/')
}
</script>

<template>
  <header ref="bar" class="appbar" :class="{ flat, 'fc-edge': props.fade && !flat, on: scrolled }">
    <IconButton v-if="back" icon="back" label="返回" @click="goBack" />
    <h1 class="title t-title-l clamp-1">{{ title }}</h1>
    <div class="actions"><slot name="actions" /></div>
  </header>
</template>

<style scoped>
.appbar {
  position: sticky;
  top: 0;
  z-index: 5;
  display: flex;
  align-items: center;
  gap: 4px;
  height: calc(64px + var(--safe-top));
  padding: var(--safe-top) 4px 0 16px;
  background: var(--md-sys-color-surface);
}
.appbar:has(.m3-iconbtn:first-child) { padding-left: 4px; }
.appbar.flat { position: relative; }
.title { margin: 0; flex: 1; min-width: 0; color: var(--md-sys-color-on-surface); }
.actions { display: flex; align-items: center; }
</style>
