<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import type { Card } from '../api/types'
import { titleOf } from '../api/client'
import { rememberCover } from '../composables/motion'
import Icon from './m3/Icon.vue'

const props = defineProps<{
  card: Card
  /** 副标题，如"更新至 第3集"；缺省显示开播日期 */
  sub?: string
  /** 左上角小标签，如"已追" */
  badge?: string
}>()
const loaded = ref(false)
const failed = ref(false)
const img = computed(() => props.card.images.common || props.card.images.large)
const sub = computed(() => props.sub ?? (props.card.date ? props.card.date.slice(0, 4) + ' 年' : ''))
// 记下这张封面：点进详情页时数据还没到，先用它画海报（换页时海报直接飞过去落在上面）
watchEffect(() => img.value && rememberCover(props.card.id, img.value, titleOf(props.card)))
</script>

<template>
  <router-link v-ripple class="poster" :to="`/subject/${card.id}`">
    <div class="cover" :data-hero="`subject:${card.id}`">
      <div v-if="!loaded" class="skeleton fill" />
      <img v-if="img && !failed" :src="img" :alt="titleOf(card)" loading="lazy" decoding="async" :class="{ show: loaded }" @load="loaded = true" @error="failed = true" />
      <div v-if="failed || !img" class="noimg t-label-m">暂无封面</div>
      <span v-if="badge" class="badge t-label-m"><Icon name="favorite" :size="12" />{{ badge }}</span>
      <span v-if="card.score > 0" class="score t-label-m"><Icon name="star" :size="12" />{{ card.score.toFixed(1) }}</span>
    </div>
    <div class="meta">
      <div class="name t-title-s clamp-2">{{ titleOf(card) }}</div>
      <div v-if="sub" class="sub t-body-s muted clamp-1">{{ sub }}</div>
    </div>
  </router-link>
</template>

<style scoped>
.poster { display: block; border-radius: 14px; color: inherit; padding-bottom: 4px; }
.cover { position: relative; aspect-ratio: 2 / 3; border-radius: 14px; overflow: hidden; background: var(--md-sys-color-surface-container-high); box-shadow: var(--fc-float); }
.fill { position: absolute; inset: 0; border-radius: 0; }
/* 封面加载好时：淡入的同时从微微放大落回原样 */
img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; opacity: 0; transform: scale(1.06); transition: opacity 420ms ease, transform 700ms var(--ease-out-expo); }
img.show { opacity: 1; transform: none; }
.noimg { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--md-sys-color-on-surface-variant); }
.score {
  position: absolute;
  right: 6px;
  top: 6px;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px 6px;
  border-radius: var(--md-shape-full);
  background: rgb(0 0 0 / 0.55);
  color: #fff;
  backdrop-filter: blur(4px);
}
.badge {
  position: absolute;
  left: 6px;
  top: 6px;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px 7px;
  border-radius: var(--md-shape-full);
  background: var(--md-sys-color-primary);
  color: var(--md-sys-color-on-primary);
}
.meta { padding: 8px 4px 0; }
.name { color: var(--md-sys-color-on-surface); }
.sub { margin-top: 2px; }
</style>
