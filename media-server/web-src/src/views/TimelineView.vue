<script setup lang="ts">
import { computed, onActivated, onMounted, ref } from 'vue'
import { api } from '../api/client'
import type { Card, CalendarDay } from '../api/types'
import { weekName } from '../api/follow'
import FollowStrip from '../components/FollowStrip.vue'
import { airtimeOf, loadAirtimes } from '../composables/airtime'
import { isFollowed, refreshFollows, useFollows } from '../composables/follows'
import TopAppBar from '../components/m3/TopAppBar.vue'
import Chip from '../components/m3/Chip.vue'
import Button from '../components/m3/Button.vue'
import PosterCard from '../components/PosterCard.vue'
import { useScrolled } from '../composables/useScrolled'

defineOptions({ name: 'TimelineView' })

// 星期条紧贴在标题栏下面一起吸顶，渐隐带交给星期条（标题栏的关掉，免得盖住星期条）
const weekEl = ref<HTMLElement | null>(null)
const weekScrolled = useScrolled(weekEl)
const NAMES = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']
const jsDay = new Date().getDay() // 0=周日
const today = jsDay === 0 ? 7 : jsDay
const days = ref<CalendarDay[]>([])
const selected = ref(today)
const loading = ref(true)
const error = ref('')
const airTick = ref(0) // 播出时间查回来后让列表重算
useFollows()
// 回到时间表时刷新一下追番状态（可能刚更新了）
onActivated(() => void refreshFollows())

async function load() {
  loading.value = true
  error.value = ''
  try {
    days.value = await api.calendar()
    void loadAirtimes(days.value.flatMap((d) => d.items.map((c) => c.id))).then(() => airTick.value++)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 当天的番剧：知道几点更新的按时间先后排在前面；其余有评分的在前、评分高的优先
const items = computed(() => {
  void airTick.value
  const d = days.value.find((x) => x.weekday === selected.value)
  // 换算成"这一栏当天的第几分钟"：北京时间跨到第二天凌晨的算 24 点以后（当天最晚播），前一天晚上的算负数
  const minute = (c: Card) => {
    const a = airtimeOf(c.id)
    if (!a?.every) return null
    const [h, m] = a.time.split(':').map(Number)
    const diff = (a.weekday - selected.value + 7) % 7
    return h * 60 + m + (diff === 1 ? 1440 : diff === 6 ? -1440 : 0)
  }
  return [...(d?.items ?? [])].sort((a, b) => {
    const ta = minute(a)
    const tb = minute(b)
    if (ta !== null && tb !== null && ta !== tb) return ta - tb
    if ((ta === null) !== (tb === null)) return ta === null ? 1 : -1
    return (b.score > 0 ? 1 : 0) - (a.score > 0 ? 1 : 0) || b.score - a.score
  })
})

// 卡片副标题："23:00 更新"（北京时间和 Bangumi 的星期对不上时带上星期，比如日本周四 0 点 = 北京周三 23 点）
function subOf(c: Card): string {
  void airTick.value
  const a = airtimeOf(c.id)
  if (a?.every === 7) return (a.weekday !== selected.value ? weekName(a.weekday) + ' ' : '') + a.time + ' 更新'
  return c.date ? '首播 ' + c.date : ''
}
</script>

<template>
  <div class="page tab-page">
    <TopAppBar title="时间表" :fade="false" />
    <FollowStrip />
    <!-- 外层负责吸顶 + 渐隐带；里层负责左右滑（自己能滑的元素会把挂在下面的渐隐带裁掉） -->
    <div ref="weekEl" class="weekwrap fc-edge" :class="{ on: weekScrolled }">
      <div class="weekbar">
        <Chip
          v-for="(n, i) in NAMES"
          :key="n"
          :selected="selected === i + 1"
          @click="selected = i + 1"
        >{{ n }}{{ i + 1 === today ? ' · 今天' : '' }}</Chip>
      </div>
    </div>

    <div class="content">
      <div v-if="loading" class="grid">
        <div v-for="i in 9" :key="i"><div class="skeleton sk" /><div class="skeleton line" /></div>
      </div>
      <div v-else-if="error" class="state">
        <div class="muted">{{ error }}</div>
        <Button variant="tonal" @click="load">再试一次</Button>
      </div>
      <div v-else-if="!items.length" class="state muted">这天没有新番放送</div>
      <TransitionGroup v-else name="list" tag="div" class="grid">
        <PosterCard v-for="c in items" :key="selected + '-' + c.id" :card="c" :sub="subOf(c)" :badge="isFollowed(c.id) ? '已追' : ''" />
      </TransitionGroup>
    </div>
  </div>
</template>

<style scoped>
.weekwrap { position: sticky; top: calc(64px + var(--safe-top)); z-index: 4; background: var(--md-sys-color-surface); }
.weekbar { display: flex; gap: 8px; padding: 4px 16px 12px; overflow-x: auto; scrollbar-width: none; }
.weekbar::-webkit-scrollbar { display: none; }
.content { padding: 4px 16px 16px; max-width: 1400px; margin: 0 auto; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(104px, 1fr)); gap: 16px 12px; }
.sk { aspect-ratio: 2 / 3; }
.line { height: 14px; width: 70%; margin-top: 10px; border-radius: 4px; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 64px 0; text-align: center; }
/* 切换星期时卡片依次淡入上浮 */
.list-enter-active { transition: opacity 300ms var(--md-ease-emphasized-decel), transform 300ms var(--md-ease-emphasized-decel); }
.list-enter-from { opacity: 0; transform: translateY(16px); }
</style>
