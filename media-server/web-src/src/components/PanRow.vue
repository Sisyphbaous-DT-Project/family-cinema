<script setup lang="ts">
// 网盘搜索结果的一行：整行点一下 = 复制链接（带提取码）；右边是「打开」或「下载到盒子」（磁力）。
import { computed } from 'vue'
import { whenText, type PanLink } from '../api/pansou'
import Icon from './m3/Icon.vue'

const props = defineProps<{ link: PanLink; type: string; copied?: boolean }>()
const emit = defineEmits<{ (e: 'copy'): void; (e: 'open'): void; (e: 'download'): void }>()

const meta = computed(() => {
  const parts = [whenText(props.link.datetime)]
  if (props.link.password) parts.push('提取码 ' + props.link.password)
  return parts.filter(Boolean).join(' · ')
})
const isMagnet = computed(() => props.type === 'magnet')
const canOpen = computed(() => /^https?:\/\//i.test(props.link.url))
</script>

<template>
  <div v-ripple class="prow" role="button" tabindex="0" @click="emit('copy')" @keydown.enter="emit('copy')">
    <div class="body">
      <div class="t-body-l clamp-2 title">{{ link.note || '（没写标题）' }}</div>
      <div v-if="meta" class="t-body-m muted clamp-1">{{ meta }}</div>
      <div class="t-body-s url clamp-1">{{ link.url }}</div>
    </div>
    <div class="acts">
      <span class="copytag t-label-l" :class="{ done: copied }">
        <Icon :name="copied ? 'check' : 'copy'" :size="18" />{{ copied ? '已复制' : '复制' }}
      </span>
      <button v-if="isMagnet" v-ripple class="actbtn t-label-l" type="button" @click.stop="emit('download')">
        <Icon name="download" :size="18" />下载到盒子
      </button>
      <button v-else-if="canOpen" v-ripple class="actbtn t-label-l" type="button" @click.stop="emit('open')">
        <Icon name="openInNew" :size="18" />打开
      </button>
    </div>
  </div>
</template>

<style scoped>
.prow { display: flex; align-items: center; gap: 10px; padding: 12px 12px 12px 16px; cursor: pointer; color: inherit; }
.body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; }
.title { word-break: break-all; }
.url { color: var(--md-sys-color-outline); word-break: break-all; }
.acts { flex: none; display: flex; flex-direction: column; align-items: stretch; gap: 6px; }
.copytag, .actbtn { display: inline-flex; align-items: center; justify-content: center; gap: 4px; height: 34px; padding: 0 10px; border-radius: 10px; white-space: nowrap; }
.copytag { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); }
.copytag.done { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.actbtn { border: 1px solid var(--md-sys-color-outline-variant); color: var(--md-sys-color-primary); }
</style>
