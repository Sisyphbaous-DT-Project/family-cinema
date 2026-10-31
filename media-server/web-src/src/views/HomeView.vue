<script setup lang="ts">
import { onMounted } from 'vue'
import { api } from '../api/client'
import { usePagedCards } from '../composables/usePagedCards'
import TopAppBar from '../components/m3/TopAppBar.vue'
import IconButton from '../components/m3/IconButton.vue'
import CardGrid from '../components/CardGrid.vue'

defineOptions({ name: 'HomeView' })

// 近两季按热度排序的番剧；滑到底继续加载
const list = usePagedCards((offset) => api.hot(offset, 24))
onMounted(() => list.loadMore())

const sub = (c: { score: number; date: string }) => (c.date ? c.date.slice(0, 7) : '')
</script>

<template>
  <div class="page tab-page">
    <TopAppBar title="推荐">
      <template #actions>
        <IconButton icon="search" label="搜索" data-hero="search" @click="$router.push('/search')" />
      </template>
    </TopAppBar>
    <CardGrid
      :cards="list.cards.value"
      :loading="list.loading.value"
      :error="list.error.value"
      :done="list.done.value"
      :sub="sub"
      @more="list.loadMore"
    />
  </div>
</template>
