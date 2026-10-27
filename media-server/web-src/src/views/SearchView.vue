<script setup lang="ts">
import { nextTick, onActivated, ref, watch } from 'vue'
import { api } from '../api/client'
import { usePagedCards } from '../composables/usePagedCards'
import { readJSON, writeJSON } from '../composables/storage'
import { searchEpoch } from '../navState'
import { useScrolled } from '../composables/useScrolled'
import IconButton from '../components/m3/IconButton.vue'
import Chip from '../components/m3/Chip.vue'
import CardGrid from '../components/CardGrid.vue'

defineOptions({ name: 'SearchView' })

const HIST_KEY = 'fc.searchHistory'
// 吸顶的搜索栏：划过后下边缘出现渐隐带
const barEl = ref<HTMLElement | null>(null)
const barScrolled = useScrolled(barEl)
const input = ref<HTMLInputElement>()
const q = ref('')
const submitted = ref('')
const history = ref<string[]>(readJSON<string[]>(HIST_KEY, []))
const list = usePagedCards((offset) => api.search(submitted.value, offset, 24))

function submit(kw?: string) {
  const k = (kw ?? q.value).trim()
  if (!k) return
  q.value = k
  submitted.value = k
  history.value = [k, ...history.value.filter((x) => x !== k)].slice(0, 12)
  writeJSON(HIST_KEY, history.value)
  list.reset()
  list.loadMore()
  input.value?.blur()
}

function clearAll() {
  q.value = ''
  submitted.value = ''
  list.reset()
  nextTick(() => input.value?.focus())
}

function clearHistory() {
  history.value = []
  writeJSON(HIST_KEY, [])
}

// 从一级页面重新进入时清空上次的搜索
watch(searchEpoch, clearAll)
onActivated(() => {
  if (!submitted.value) nextTick(() => input.value?.focus())
})
</script>

<template>
  <div class="page">
    <header ref="barEl" class="bar fc-edge" :class="{ on: barScrolled }">
      <IconButton icon="back" label="返回" @click="$router.back()" />
      <form class="field" @submit.prevent="submit()">
        <input
          ref="input"
          v-model="q"
          class="t-body-l"
          type="search"
          enterkeyhint="search"
          placeholder="搜索番剧名称"
          autocomplete="off"
        />
        <IconButton v-if="q" icon="close" label="清空" :size="20" @click="clearAll" />
        <IconButton icon="search" label="搜索" @click="submit()" />
      </form>
    </header>

    <section v-if="!submitted" class="history">
      <div v-if="history.length" class="hhead">
        <span class="t-title-s muted">搜索历史</span>
        <button v-ripple class="clear t-label-m" type="button" @click="clearHistory">清空</button>
      </div>
      <div class="chips">
        <Chip v-for="h in history" :key="h" @click="submit(h)">{{ h }}</Chip>
      </div>
      <p v-if="!history.length" class="hint muted">输入番剧名称，如「孤独摇滚」</p>
    </section>

    <CardGrid
      v-else
      :cards="list.cards.value"
      :loading="list.loading.value"
      :error="list.error.value"
      :done="list.done.value"
      :sub="(c) => (c.date ? c.date.slice(0, 4) + ' 年' : '')"
      :empty-text="`没有找到「${submitted}」`"
      @more="list.loadMore"
    />
  </div>
</template>

<style scoped>
.bar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 4px; padding: calc(8px + var(--safe-top)) 8px 8px 4px; background: var(--md-sys-color-surface); }
.field {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  height: 56px;
  padding: 0 4px 0 20px;
  border-radius: 16px;
  background: var(--fc-card);
  box-shadow: var(--fc-float);
  transition: background-color var(--md-dur-medium) var(--md-ease-standard);
}
.field:focus-within { box-shadow: var(--fc-float), 0 0 0 2px var(--md-sys-color-primary); }
input { flex: 1; min-width: 0; height: 100%; border: 0; outline: 0; background: transparent; }
input::placeholder { color: var(--md-sys-color-on-surface-variant); }
input::-webkit-search-cancel-button { display: none; }
.history { padding: 8px 16px; max-width: 900px; margin: 0 auto; }
.hhead { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.clear { color: var(--md-sys-color-primary); padding: 6px 10px; border-radius: var(--md-shape-md); }
.chips { display: flex; flex-wrap: wrap; gap: 8px; }
.hint { text-align: center; padding: 64px 0; }
</style>
