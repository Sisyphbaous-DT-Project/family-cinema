<script setup lang="ts">
import { ref } from 'vue'
import type { Card } from '../api/types'
import { useSentinel } from '../composables/usePagedCards'
import PosterCard from './PosterCard.vue'
import Spinner from './m3/Spinner.vue'
import Button from './m3/Button.vue'

// 番剧卡片网格：首屏骨架屏、滑到底自动加载、失败重试、"没有更多了"
const props = defineProps<{
  cards: Card[]
  loading: boolean
  error: string
  done: boolean
  /** 卡片副标题生成函数 */
  sub?: (c: Card) => string
  emptyText?: string
}>()
const emit = defineEmits<{ (e: 'more'): void }>()

const sentinel = ref<HTMLElement>()
useSentinel(sentinel, () => emit('more'))
</script>

<template>
  <div class="cardgrid">
    <div v-if="cards.length" class="grid">
      <!-- 依次浮上来；翻页加载的那一批从头错开（每页 24 个） -->
      <PosterCard
        v-for="(c, i) in cards"
        :key="c.id"
        v-reveal="i % 24"
        :card="c"
        :sub="sub ? sub(c) : undefined"
      />
    </div>
    <div v-else-if="loading" class="grid">
      <div v-for="i in 12" :key="i" class="sk">
        <div class="skeleton sk-cover" />
        <div class="skeleton sk-line" />
      </div>
    </div>
    <div v-else-if="!error && done" class="state muted">{{ props.emptyText ?? '什么也没找到' }}</div>

    <div v-if="error" class="state">
      <div class="muted">{{ error }}</div>
      <Button variant="tonal" @click="emit('more')">再试一次</Button>
    </div>
    <div ref="sentinel" class="tail">
      <Spinner v-if="loading && cards.length" :size="28" />
      <span v-else-if="done && cards.length" class="muted t-body-s">没有更多了</span>
    </div>
  </div>
</template>

<style scoped>
.cardgrid { padding: 0 16px 16px; max-width: 1400px; margin: 0 auto; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(104px, 1fr)); gap: 16px 12px; }
.sk-cover { aspect-ratio: 2 / 3; }
.sk-line { height: 14px; margin-top: 10px; width: 70%; border-radius: 4px; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 48px 0; text-align: center; }
.tail { display: flex; justify-content: center; align-items: center; min-height: 48px; padding: 8px; }
</style>
