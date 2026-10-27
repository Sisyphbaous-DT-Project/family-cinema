import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import type { Card, SearchResult } from '../api/types'

/** 分页的番剧卡片列表（热门/搜索共用）：loadMore 追加下一页，reset 清空重来 */
export function usePagedCards(fetchPage: (offset: number) => Promise<SearchResult>) {
  const cards = ref<Card[]>([])
  const loading = ref(false)
  const error = ref('')
  const done = ref(false)
  const total = ref(0)
  let offset = 0
  let epoch = 0

  async function loadMore() {
    if (loading.value || done.value) return
    loading.value = true
    error.value = ''
    const my = epoch
    try {
      const res = await fetchPage(offset)
      if (my !== epoch) return // 期间被 reset 了，丢弃过期结果
      const seen = new Set(cards.value.map((c) => c.id))
      cards.value.push(...res.items.filter((c) => !seen.has(c.id)))
      offset += res.items.length
      total.value = res.total
      if (res.items.length === 0 || offset >= res.total) done.value = true
    } catch (e) {
      if (my === epoch) error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
      if (my === epoch) loading.value = false
    }
  }

  function reset() {
    epoch++
    cards.value = []
    offset = 0
    total.value = 0
    done.value = false
    error.value = ''
    loading.value = false
  }

  return { cards, loading, error, done, total, loadMore, reset }
}

/** 哨兵元素进入视口（提前 400px）时触发回调，用来做"滑到底自动加载更多" */
export function useSentinel(el: Ref<HTMLElement | undefined>, onVisible: () => void) {
  let io: IntersectionObserver | null = null
  onMounted(() => {
    if (!el.value || !('IntersectionObserver' in window)) return
    io = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && onVisible(), { rootMargin: '400px' })
    io.observe(el.value)
  })
  onBeforeUnmount(() => io?.disconnect())
}
