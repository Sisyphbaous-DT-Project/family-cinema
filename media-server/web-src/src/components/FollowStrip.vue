<script setup lang="ts">
import { computed, watchEffect } from 'vue'
import { weekName, type FollowItem } from '../api/follow'
import { freshlyUpdated, useFollows } from '../composables/follows'
import { rememberCover } from '../composables/motion'
import Icon from './m3/Icon.vue'

// 时间表最上面一排：我追的番（刚更新的排前面，其余按下次更新时间）
const { items } = useFollows()
// 点进播放页时番剧数据还没到，播放器先用这张封面（换页时封面直接飞进播放器）
watchEffect(() => items.value.forEach((f) => f.cover && rememberCover(f.id, f.cover, f.title)))

const list = computed(() =>
  [...items.value].sort((a, b) => {
    const fa = freshlyUpdated(a) ? 0 : 1
    const fb = freshlyUpdated(b) ? 0 : 1
    if (fa !== fb) return fa - fb
    const na = a.air?.next ? Date.parse(a.air.next) : Infinity
    const nb = b.air?.next ? Date.parse(b.air.next) : Infinity
    return na - nb
  }),
)

const dayStart = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()

/** "今天 23:00 更新" / "明天 …" / "周三 …" */
function whenText(f: FollowItem): string {
  const a = f.air
  if (!a) return ''
  if (a.ended) return a.every > 0 ? '已完结' : ''
  if (!a.next) return ''
  const diff = Math.round((dayStart(new Date(a.next)) - dayStart(new Date())) / 86400e3)
  const day = diff === 0 ? '今天' : diff === 1 ? '明天' : weekName(a.weekday)
  return `${day} ${a.time} 更新`
}

function status(f: FollowItem): { text: string; hot: boolean } {
  if (freshlyUpdated(f)) return { text: `${f.latest || '新的一集'}更新了，可以看了`, hot: true }
  if (!f.baseline) return { text: f.checkErr ? '还没在片源站找到这部番' : '正在找片源…', hot: false }
  return { text: f.latest ? `已更新到${f.latest}` : `已有 ${f.count} 集`, hot: false }
}
</script>

<template>
  <section v-if="list.length" class="strip">
    <h2 class="t-title-s sec">我追的番 · {{ list.length }} 部</h2>
    <div class="row">
      <router-link v-for="f in list" :key="f.id" v-ripple class="fcard" :to="`/play/${f.id}`">
        <img v-if="f.cover" class="cv" :src="f.cover" alt="" loading="lazy" decoding="async" :data-hero="`play:${f.id}`" />
        <div v-else class="cv" />
        <div class="info">
          <div class="t-title-s clamp-2 name">{{ f.title }}</div>
          <div v-if="whenText(f)" class="when t-body-s"><Icon name="schedule" :size="14" />{{ whenText(f) }}</div>
          <div class="st t-body-s" :class="{ hot: status(f).hot }">{{ status(f).text }}</div>
        </div>
      </router-link>
    </div>
  </section>
</template>

<style scoped>
.strip { padding-bottom: 4px; }
.sec { margin: 4px 22px 10px; color: var(--md-sys-color-primary); }
.row { display: flex; gap: 12px; padding: 0 16px 12px; overflow-x: auto; scrollbar-width: none; }
.row::-webkit-scrollbar { display: none; }
.fcard { flex: none; display: flex; gap: 12px; width: 264px; max-width: 80vw; padding: 10px; border-radius: var(--fc-radius); background: var(--fc-card); box-shadow: var(--fc-float); color: inherit; }
.cv { flex: none; width: 64px; aspect-ratio: 2 / 3; border-radius: 10px; object-fit: cover; background: var(--md-sys-color-surface-container-high); }
.info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.name { color: var(--md-sys-color-on-surface); }
.when { display: flex; align-items: center; gap: 4px; color: var(--md-sys-color-on-surface-variant); }
.st { color: var(--md-sys-color-on-surface-variant); }
.st.hot { color: var(--md-sys-color-primary); font-weight: 700; }
</style>
