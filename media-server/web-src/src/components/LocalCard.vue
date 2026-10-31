<script setup lang="ts">
import { computed, ref } from 'vue'
import { coverURL, type LocalItem } from '../api/local'
import { progRatio } from '../composables/progress'
import Icon from './m3/Icon.vue'

// tick：观看进度变了由片库页 +1（进度不是响应式的，靠它让海报底部的进度条重算）
const props = defineProps<{ item: LocalItem; tick?: number }>()
const loaded = ref(false)
const failed = ref(false)
const cover = computed(() => (props.item.cover ? coverURL(props.item.cover) : ''))
const epCount = computed(() => props.item.episodes.length)
// 整部的进度 = 各集进度的平均（和旧前端一致）
const ratio = computed(() => {
  void props.tick
  const eps = props.item.episodes
  if (!eps.length) return 0
  return eps.reduce((s, e) => s + progRatio(e.path), 0) / eps.length
})
// 卡片下面一行：剧写"共 N 集"；单个文件的在电影电视剧里写"电影"，在番剧里写"1 集"（文件大小老人家看不懂，不写）
const sub = computed(() => (epCount.value > 1 ? `共 ${epCount.value} 集` : props.item.category === 'series' ? '1 集' : '电影'))
</script>

<template>
  <router-link v-ripple class="lc" :to="`/local/${encodeURIComponent(item.id)}`">
    <div class="cover" :data-hero="`local:${item.id}`">
      <div v-if="cover && !loaded && !failed" class="skeleton fill" />
      <img v-if="cover && !failed" :src="cover" :alt="item.title" loading="lazy" decoding="async" :class="{ show: loaded }" @load="loaded = true" @error="failed = true" />
      <div v-if="!cover || failed" class="ph">
        <Icon name="tv" :size="32" />
        <span class="t-label-m clamp-2">{{ item.title }}</span>
      </div>
      <i v-if="ratio > 0" class="bar"><i :style="{ width: Math.round(ratio * 100) + '%' }" /></i>
    </div>
    <div class="meta">
      <div class="name t-title-m clamp-2">{{ item.title }}</div>
      <div class="sub t-body-m muted clamp-1">{{ sub }}</div>
    </div>
  </router-link>
</template>

<style scoped>
.lc { display: block; border-radius: 14px; color: inherit; padding-bottom: 4px; }
.cover { position: relative; aspect-ratio: 2 / 3; border-radius: 14px; overflow: hidden; background: var(--md-sys-color-surface-container-high); box-shadow: var(--fc-float); }
.fill { position: absolute; inset: 0; border-radius: 0; }
/* 封面加载好时：淡入的同时从微微放大落回原样 */
img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; opacity: 0; transform: scale(1.06); transition: opacity 420ms ease, transform 700ms var(--ease-out-expo); }
img.show { opacity: 1; transform: none; }
.ph { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; padding: 12px; text-align: center; background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }
.bar { position: absolute; left: 0; right: 0; bottom: 0; height: 4px; background: rgb(0 0 0 / 0.35); }
.bar i { display: block; height: 100%; background: var(--md-sys-color-primary-container); }
.meta { padding: 8px 4px 0; }
.name { color: var(--md-sys-color-on-surface); }
.sub { margin-top: 2px; }
</style>
