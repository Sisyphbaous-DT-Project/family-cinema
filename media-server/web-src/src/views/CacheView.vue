<script setup lang="ts">
// 我的缓存（App 专属）：缓存清单 + 每一集的状态（已缓存/下载中/排队中/已暂停）+ 播放/暂停/继续/取消/删除。
// 状态和详情页共用同一个状态中心（cache.ts）：有下载在动或有排队时每秒自动刷新，全停了就不刷（省电）。
// App 1.4 起排队中的集一加入队列就在清单里，这里能直接看到；旧版 App 要轮到了才出现。
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  cachedCoverUrl,
  cachedList,
  cacheState,
  cancelCache,
  deleteCache,
  isOffline,
  pauseCache,
  resumeCache,
  useCacheLive,
  type CachedEp,
} from '../composables/cache'
import { canControlDownload, hasBridge } from '../composables/bridge'
import { fmtSize, shortEpTitle } from '../api/local'
import { toast } from '../composables/snackbar'
import TopAppBar from '../components/m3/TopAppBar.vue'
import Button from '../components/m3/Button.vue'
import Icon from '../components/m3/Icon.vue'
import BottomSheet from '../components/m3/BottomSheet.vue'

const router = useRouter()
const inApp = hasBridge()
const ctl = canControlDownload() // App 1.3 起才能暂停/取消下载中、排队中的缓存
const toDelete = ref<CachedEp | null>(null)

useCacheLive()

const items = computed(() => cachedList())
const kindOf = (c: CachedEp) => cacheState(c.epId).kind
const pct = (c: CachedEp) => Math.round((c.progress || 0) * 100)

// 汇总（和原生 getCacheSize 同一算法：下完的按文件大小，没下完的按已下载的部分）
const sizeText = computed(() => {
  let total = 0
  let done = 0
  let busy = 0
  for (const c of items.value) {
    const k = kindOf(c)
    if (k === 'done') done++
    else if (k === 'running' || k === 'queued') busy++
    total += c.done !== false ? c.size || 0 : Math.round((c.progress || 0) * (c.size || 0))
  }
  return `已缓存 ${done} 个` + (busy ? ` · 正在缓存 ${busy} 个` : '') + ` · 占用 ${fmtSize(total)}`
})

// 老缓存条目里存的集标题可能是整个文件名，只显示"第N集"
const epName = (c: CachedEp) => shortEpTitle(c.epTitle || '')

function statusText(c: CachedEp): string {
  const s = cacheState(c.epId)
  if (s.kind === 'queued') return '正在排队中，等前面的缓存完就会开始下载'
  if (s.kind === 'running') return `正在下载 ${s.pct}%` + (s.speed ? ` · ${fmtSize(s.speed)}/s` : '')
  if (s.kind === 'paused') return `已暂停（下载了 ${s.pct}%）`
  return '已缓存' + (c.size ? ' · ' + fmtSize(c.size) : '')
}

function play(c: CachedEp) {
  // 详情页的选集列表 = 同一片名下已下载完成的集（顺序与原生离线片库一致），
  // 带上下标才能直接播用户点的这一集，而不是详情页自己挑的"上次看到的那集"
  const ep = items.value.filter((x) => x.itemId === c.itemId && x.done !== false).findIndex((x) => x.epId === c.epId)
  router.push(`/local/${encodeURIComponent('cache:' + c.itemId)}` + (ep >= 0 ? `?ep=${ep}` : ''))
}

function resume(c: CachedEp) {
  if (isOffline()) {
    toast('现在连不上家里的盒子，没法接着下载')
    return
  }
  resumeCache(c)
  toast('接着缓存：' + (epName(c) || c.title))
}

function pause(c: CachedEp) {
  pauseCache(c.epId)
  toast('已暂停：' + (epName(c) || c.title))
}

/** 排队中的取消：还没开始下载，没有数据可丢，不用确认 */
function cancelQueued(c: CachedEp) {
  cancelCache(c.epId)
  toast('已取消排队：' + (epName(c) || c.title))
}

// 没下完的叫"取消"（停下来并清掉已下载的部分），下完的叫"删除"
function confirmDelete() {
  const c = toDelete.value
  if (!c) return
  const unfinished = kindOf(c) !== 'done'
  if (unfinished) cancelCache(c.epId)
  else deleteCache(c.epId)
  toDelete.value = null
  toast(unfinished ? '已取消缓存' : '已删除缓存')
}
const delUnfinished = computed(() => !!toDelete.value && kindOf(toDelete.value) !== 'done')
</script>

<template>
  <div class="page">
    <TopAppBar title="我的缓存" back />

    <div v-if="!inApp" class="state">
      <Icon name="download" :size="48" />
      <div class="t-title-m">缓存功能要在手机 App 里用</div>
      <div class="muted t-body-m">电脑浏览器里没法把片子缓存到手机</div>
    </div>

    <template v-else>
      <div class="meta t-body-m muted">{{ sizeText }}</div>

      <div v-if="!items.length" class="fc-card state">
        <Icon name="download" :size="48" />
        <div class="t-title-m">手机里还没有缓存的片子</div>
        <div class="muted t-body-m">在片子的选集下面点「缓存视频」，没网也能看</div>
      </div>

      <ul class="list">
        <li v-for="c in items" :key="c.epId" class="fc-card row" :class="'k-' + kindOf(c)">
          <div class="poster">
            <img v-if="cachedCoverUrl(c.epId)" :src="cachedCoverUrl(c.epId)" :alt="c.title" loading="lazy" />
            <Icon v-else :name="kindOf(c) === 'done' ? 'tv' : 'download'" :size="24" />
          </div>
          <div class="body">
            <div class="t-body-l clamp-1">{{ c.title }}<template v-if="epName(c)"> · {{ epName(c) }}</template></div>
            <div class="t-body-m status">{{ statusText(c) }}</div>
            <div v-if="kindOf(c) !== 'done'" class="pbar" :class="{ wait: kindOf(c) === 'queued' }">
              <i :class="{ paused: kindOf(c) !== 'running' }" :style="{ width: pct(c) + '%' }" />
            </div>
            <div class="acts">
              <template v-if="kindOf(c) === 'done'">
                <Button variant="filled" small @click="play(c)">播放</Button>
                <Button variant="text" small danger @click="toDelete = c">删除</Button>
              </template>
              <template v-else-if="kindOf(c) === 'running'">
                <!-- 下载中的暂停/取消需要 App 1.3；旧 App 上中途删除会留下垃圾，所以不显示 -->
                <Button v-if="ctl" variant="filled" small @click="pause(c)">暂停</Button>
                <Button v-if="ctl" variant="text" small danger @click="toDelete = c">取消</Button>
              </template>
              <template v-else-if="kindOf(c) === 'queued'">
                <Button v-if="ctl" variant="text" small danger @click="cancelQueued(c)">取消排队</Button>
              </template>
              <template v-else>
                <Button variant="filled" small @click="resume(c)">继续</Button>
                <Button variant="text" small danger @click="toDelete = c">取消</Button>
              </template>
            </div>
          </div>
        </li>
      </ul>
    </template>

    <BottomSheet :model-value="!!toDelete" :title="delUnfinished ? '取消缓存' : '删除缓存'" @update:model-value="toDelete = null">
      <p class="del-text t-body-l">
        {{ delUnfinished ? '不再缓存' : '删除' }}「{{ toDelete?.title }} · {{ toDelete ? epName(toDelete) : '' }}」{{ delUnfinished ? '？' : '的缓存？' }}已下载的数据会被清除，需要重新下载。
      </p>
      <div class="del-acts">
        <Button variant="text" @click="toDelete = null">{{ delUnfinished ? '先不了' : '先不删' }}</Button>
        <Button variant="filled" danger @click="confirmDelete">{{ delUnfinished ? '不缓存了' : '删除' }}</Button>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.meta { max-width: 720px; margin: 0 auto; padding: 4px 22px 10px; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; margin: 16px; padding: 56px 20px; text-align: center; color: var(--md-sys-color-on-surface-variant); }
.list { list-style: none; margin: 0; padding: 0 16px 24px; max-width: 720px; margin: 0 auto; display: flex; flex-direction: column; gap: 12px; }
.row { display: flex; gap: 14px; padding: 14px; }
.poster { width: 64px; aspect-ratio: 2 / 3; flex-shrink: 0; border-radius: 10px; overflow: hidden; background: var(--md-sys-color-surface-container-high); display: flex; align-items: center; justify-content: center; color: var(--md-sys-color-on-surface-variant); }
.poster img { width: 100%; height: 100%; object-fit: cover; }
.body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.status { color: var(--md-sys-color-on-surface-variant); }
.k-done .status { color: var(--md-sys-color-primary); }
.k-running .status { color: var(--md-sys-color-tertiary); }
.pbar { height: 4px; border-radius: 2px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.pbar.wait { opacity: 0.5; }
.pbar i { display: block; height: 100%; border-radius: 2px; background: var(--md-sys-color-primary); transition: width var(--md-dur-medium) linear; }
.pbar i.paused { background: var(--md-sys-color-outline); }
.acts { display: flex; gap: 4px; margin-top: 4px; }
.del-text { margin: 0 0 8px; color: var(--md-sys-color-on-surface-variant); }
.del-acts { display: flex; justify-content: flex-end; gap: 8px; }
</style>
