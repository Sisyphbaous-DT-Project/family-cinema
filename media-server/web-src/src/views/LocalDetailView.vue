<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { epLabel, fetchItem, fmtSize, streamURL, type LocalItem } from '../api/local'
import { getProg, isWatched, setProg } from '../composables/progress'
import {
  cacheState,
  cancelCache,
  deleteCache,
  isOffline,
  pauseCache,
  playLocalUrl,
  refreshCacheMarks,
  useCacheLive,
  type CacheState,
} from '../composables/cache'
import { cacheLocalEp } from '../composables/localCache'
import { canControlDownload, hasBridge } from '../composables/bridge'
import { playerPrefs } from '../composables/playerPrefs'
import { useScrolled } from '../composables/useScrolled'
import { toast } from '../composables/snackbar'
import TopAppBar from '../components/m3/TopAppBar.vue'
import Button from '../components/m3/Button.vue'
import Icon from '../components/m3/Icon.vue'
import BottomSheet from '../components/m3/BottomSheet.vue'
import VideoPlayer from '../components/VideoPlayer.vue'
import LocalEpisodes from '../components/LocalEpisodes.vue'

// 本地片库的详情 + 播放：上面播放器，下面选集；进度按文件路径记（与旧前端共用同一份）
// 也服务"我的缓存"里点进来的条目：id 带 cache: 前缀时走桥取数据、用本地地址播放（离线也能看）
// 选集区（每集缓存状态、缓存/删除多选）在 LocalEpisodes 组件里；这里只管播放和"当前这一集"的操作条
const route = useRoute()
const router = useRouter()
const id = decodeURIComponent(String(route.params.id))
const isCacheItem = id.startsWith('cache:')
const item = ref<LocalItem | null>(null)
const loading = ref(true)
const error = ref('')
const cur = ref(-1)
const startAt = ref(0)
const tick = ref(0) // 观看进度变化后让列表刷新

const inApp = hasBridge()
// 顶部吸顶区（标题 + 播放器）：划过后下边缘出现渐隐带，内容不会被一刀切断
const stickEl = ref<HTMLElement | null>(null)
const stickScrolled = useScrolled(stickEl)
// 新增缓存的入口只在"App 内且在线且不是缓存条目"时出现
const canCache = inApp && !isOffline() && !isCacheItem
// App 1.3 起才能暂停/取消下载（界面热更新比 App 本体先到，旧 App 上这些按钮不能显示）
const ctl = canControlDownload()

// 缓存状态每秒自动刷新（有下载在动/有排队时），缓存完不用重进页面
useCacheLive()

async function load() {
  loading.value = true
  error.value = ''
  try {
    const it = await fetchItem(id)
    if (!it) throw new Error('这部片子已经不在盒子里了')
    item.value = it
    refreshCacheMarks()
    // 从"继续观看"/缓存页进来的：指定了集数就直接播那集
    const qEp = Number(route.query.ep)
    if (Number.isInteger(qEp) && qEp >= 0 && qEp < it.episodes.length) {
      play(qEp)
      return
    }
    // 默认接着上次看的：先找没看完且有进度的最近一集，否则第一个没看完的，否则第一集
    const eps = it.episodes
    let pick = -1
    let latest = 0
    eps.forEach((e, i) => {
      const p = getProg(e.path)
      if (p && p.pos > 5 && !isWatched(e.path) && p.ts >= latest) {
        latest = p.ts
        pick = i
      }
    })
    if (pick < 0) pick = Math.max(0, eps.findIndex((e) => !isWatched(e.path)))
    if (eps.length) play(pick)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}
onMounted(load)

function play(i: number) {
  const ep = item.value?.episodes[i]
  if (!ep) return
  const p = playerPrefs.resume ? getProg(ep.path) : null
  startAt.value = p && !isWatched(ep.path) ? p.pos : 0
  cur.value = i
}

const ep = computed(() => (item.value && cur.value >= 0 ? item.value.episodes[cur.value] : undefined))
const hasNext = computed(() => !!item.value && cur.value + 1 < item.value.episodes.length)
const hasPrev = computed(() => cur.value > 0)

/** 集名：只写"第N集"，不显示文件名 */
function labelOf(i: number): string {
  const it = item.value
  const e = it?.episodes[i]
  return it && e ? epLabel(e, i, it.episodes.length, isCacheItem) : ''
}

// 缓存条目走 App 本地服务播（离线可播）；取不到再退回盒子地址
const epSrc = computed(() => {
  if (!ep.value) return ''
  if (isCacheItem) return playLocalUrl(ep.value.path) || streamURL(ep.value.path)
  return streamURL(ep.value.path)
})

function onProgress(pos: number, dur: number) {
  if (!ep.value || !playerPrefs.resume) return
  setProg(ep.value.path, pos, dur)
  tick.value++
}

function onEnded(pos: number, dur: number) {
  // 看完记满进度（选集上打勾用），再决定要不要连播
  if (ep.value && playerPrefs.resume && dur) {
    setProg(ep.value.path, dur, dur)
    tick.value++
  }
  if (playerPrefs.auto) next()
}
function next() {
  if (hasNext.value) play(cur.value + 1)
}

// ---------- 当前这一集：缓存 / 暂停 / 继续 / 取消 / 删除 ----------
function cacheCur() {
  const it = item.value
  if (it && cacheLocalEp(it, cur.value, isCacheItem)) toast('已加入缓存：' + labelOf(cur.value))
}
function resumeCur() {
  const it = item.value
  if (it && cacheLocalEp(it, cur.value, isCacheItem)) toast('接着缓存：' + labelOf(cur.value))
}
function pauseCur() {
  if (!ep.value) return
  pauseCache(ep.value.path)
  toast('已暂停：' + labelOf(cur.value))
}
/** 排队中的取消：还没开始下载，没有数据可丢，不用确认 */
function cancelQueuedCur() {
  if (!ep.value) return
  cancelCache(ep.value.path)
  toast('已取消排队：' + labelOf(cur.value))
}

// 同一个确认弹层：delete = 删除已缓存好的；cancel = 放弃下载中/暂停的（已下载的部分会被清掉）
const delIdx = ref(-1)
const delMode = ref<'delete' | 'cancel'>('delete')
function askDelete(mode: 'delete' | 'cancel') {
  delMode.value = mode
  delIdx.value = cur.value
}
function confirmDelete() {
  const i = delIdx.value
  const e = item.value?.episodes[i]
  delIdx.value = -1
  if (!e) return
  if (delMode.value === 'cancel') {
    cancelCache(e.path)
    toast('已取消缓存：' + labelOf(i))
    return
  }
  deleteCache(e.path)
  toast('已删除缓存：' + labelOf(i))
  onRemoved([e.path])
}

/** 缓存条目的选集本身就是"已缓存的集"，删掉的要从列表里拿掉；正在播的被删了就停下来 */
function onRemoved(paths: string[]) {
  const it = item.value
  if (!it || !isCacheItem) return
  const curPath = ep.value?.path
  it.episodes = it.episodes.filter((e) => !paths.includes(e.path))
  if (!it.episodes.length) {
    router.back()
    return
  }
  cur.value = curPath ? it.episodes.findIndex((e) => e.path === curPath) : -1
}

// ---------- 显示用的衍生数据 ----------
const counts = computed(() => {
  let done = 0
  let busy = 0
  for (const e of item.value?.episodes ?? []) {
    const k = cacheState(e.path).kind
    if (k === 'done') done++
    else if (k === 'running' || k === 'queued') busy++
  }
  return { done, busy }
})

// 片名下面一行：几集 + 手机里缓存了几集（文件大小老人家看不懂，不写在这里）
const summary = computed(() => {
  const it = item.value
  if (!it) return ''
  const n = it.episodes.length
  if (isCacheItem) return n > 1 ? `手机里缓存了 ${n} 集` : '已缓存在手机里'
  const parts: string[] = [n > 1 ? `共 ${n} 集` : it.category === 'series' ? '1 集' : '电影']
  if (inApp) {
    if (counts.value.done) parts.push(n > 1 ? `已缓存 ${counts.value.done} 集` : '已缓存')
    if (counts.value.busy) parts.push(n > 1 ? `正在缓存 ${counts.value.busy} 集` : '正在缓存')
  }
  return parts.join(' · ')
})

const curState = computed<CacheState>(() => (ep.value ? cacheState(ep.value.path) : { kind: 'none' }))
const curLine = computed(() => {
  const e = ep.value
  if (!e) return ''
  const s = curState.value
  if (s.kind === 'queued') return '正在排队，等前面的缓存完就会开始下载'
  if (s.kind === 'running') return `正在下载 ${s.pct}%` + (s.speed ? `，每秒 ${fmtSize(s.speed)}` : '')
  if (s.kind === 'paused') return `已暂停（下载了 ${s.pct}%）`
  if (s.kind === 'done') return '已缓存到手机，没网也能看'
  return canCache ? `还没缓存。缓存到手机后没网也能看（要占 ${fmtSize(e.size)}）` : `大小 ${fmtSize(e.size)}`
})
const curPct = computed(() => (curState.value.kind === 'running' || curState.value.kind === 'paused' ? curState.value.pct : 0))
const showBar = computed(() => ['queued', 'running', 'paused'].includes(curState.value.kind))

const playerTitle = computed(() =>
  [item.value?.title, item.value && item.value.episodes.length > 1 ? labelOf(cur.value) : ''].filter(Boolean).join(' · '),
)
</script>

<template>
  <div class="page">
    <div class="layout">
      <div class="left">
        <div ref="stickEl" class="stick fc-edge" :class="{ on: stickScrolled }">
          <TopAppBar :title="item?.title ?? ''" back flat />
          <!-- 播放器：圆角矩形，左右和下面的卡片对齐（全屏时播放器整个提到最上层铺满，不受这层圆角影响） -->
          <div class="playerbox">
            <VideoPlayer
              :src="epSrc"
              kind="mp4"
              :series-key="`local:${id}`"
              :title="playerTitle"
              :start-at="startAt"
              :message="error"
              :has-prev="hasPrev"
              :has-next="hasNext"
              @prev="play(cur - 1)"
              @next="next"
              @ended="onEnded"
              @progress="onProgress"
            />
          </div>
        </div>
      </div>

      <div class="right">
        <div v-if="loading" class="pad"><div v-for="i in 4" :key="i" class="skeleton row-sk" /></div>

        <div v-else-if="error" class="state">
          <Icon name="error" :size="40" />
          <div class="muted">{{ error }}</div>
          <Button variant="tonal" @click="load">再试一次</Button>
        </div>

        <section v-else-if="item" class="pad">
          <!-- 信息卡：片名 + 当前这一集的缓存（缓存 / 进度条 / 暂停 / 删除） -->
          <div class="fc-card info">
            <h2 class="t-title-l name">{{ item.title }}</h2>
            <div class="t-body-m muted">{{ summary }}</div>
            <template v-if="inApp && ep">
              <div class="divider" />
              <div class="cb-top">
                <div class="grow">
                  <div class="t-title-m">{{ item.episodes.length > 1 ? '正在看：' + labelOf(cur) : '缓存到手机' }}</div>
                  <div class="t-body-m muted">{{ curLine }}</div>
                </div>
                <div class="cb-acts">
                  <Button v-if="curState.kind === 'none' && canCache" variant="tonal" small @click="cacheCur">
                    <Icon name="download" :size="18" />{{ item.episodes.length > 1 ? '缓存这一集' : '缓存这部' }}
                  </Button>
                  <template v-else-if="curState.kind === 'queued'">
                    <Button v-if="ctl" variant="text" small danger @click="cancelQueuedCur">取消排队</Button>
                  </template>
                  <template v-else-if="curState.kind === 'running'">
                    <Button v-if="ctl" variant="tonal" small @click="pauseCur"><Icon name="pause" :size="18" />暂停</Button>
                    <Button v-if="ctl" variant="text" small danger @click="askDelete('cancel')">取消</Button>
                  </template>
                  <template v-else-if="curState.kind === 'paused'">
                    <Button v-if="canCache" variant="tonal" small @click="resumeCur">继续</Button>
                    <Button variant="text" small danger @click="askDelete('cancel')">取消</Button>
                  </template>
                  <!-- 和下面选集区的批量「删除缓存」区分开：这里只删正在看的这一集（会先弹确认） -->
                  <Button v-else-if="curState.kind === 'done'" variant="text" small danger @click="askDelete('delete')">
                    <Icon name="delete" :size="18" />{{ item.episodes.length > 1 ? '删除这一集' : '删除这部' }}
                  </Button>
                </div>
              </div>
              <div v-if="!ctl && (curState.kind === 'running' || curState.kind === 'queued')" class="t-body-s muted">
                更新 App 到最新版后，这里可以暂停和取消缓存
              </div>
              <div v-if="showBar" class="cbar" :class="{ wait: curState.kind === 'queued', paused: curState.kind === 'paused' }">
                <i :style="{ width: curPct + '%' }" />
              </div>
            </template>
          </div>

          <LocalEpisodes
            v-if="item.episodes.length > 1"
            :item="item"
            :cur="cur"
            :tick="tick"
            :can-cache="canCache"
            :from-cache="isCacheItem"
            @play="play"
            @removed="onRemoved"
          />
        </section>
      </div>
    </div>

    <BottomSheet :model-value="delIdx >= 0" :title="delMode === 'cancel' ? '取消缓存' : '删除缓存'" @update:model-value="delIdx = -1">
      <p v-if="delMode === 'cancel'" class="del-text t-body-l">
        不再缓存「{{ item?.title }}{{ item && item.episodes.length > 1 ? ' · ' + labelOf(delIdx) : '' }}」？已经下载的部分会被清除，需要时可以重新缓存。
      </p>
      <p v-else class="del-text t-body-l">
        删除「{{ item?.title }}{{ item && item.episodes.length > 1 ? ' · ' + labelOf(delIdx) : '' }}」的缓存？手机上已下载的文件会被清除，需要时可以重新缓存。
      </p>
      <div class="del-acts">
        <Button variant="text" @click="delIdx = -1">{{ delMode === 'cancel' ? '先不了' : '先不删' }}</Button>
        <Button variant="filled" danger @click="confirmDelete">{{ delMode === 'cancel' ? '不缓存了' : '删除' }}</Button>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.layout { max-width: 1400px; margin: 0 auto; }
.stick { position: sticky; top: 0; z-index: 6; background: var(--md-sys-color-surface); }
.playerbox { margin: 0 16px 4px; border-radius: var(--fc-radius); overflow: hidden; isolation: isolate; box-shadow: var(--fc-float); background: #000; }
.pad { padding: 16px; display: flex; flex-direction: column; gap: 12px; }
.grow { flex: 1; min-width: 0; }
.name { margin: 0; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 48px 16px; color: var(--md-sys-color-on-surface-variant); text-align: center; }
.row-sk { height: 56px; margin-bottom: 8px; }

/* 信息卡：片名 + 当前这一集的缓存 */
.info { display: flex; flex-direction: column; gap: 4px; padding: 16px; }
.divider { height: 1px; margin: 10px 0 6px; background: var(--md-sys-color-surface-container-high); }
.cb-top { display: flex; align-items: center; gap: 12px; }
.info .cbar { margin-top: 8px; }
.cb-acts { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.cbar { height: 6px; border-radius: 3px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.cbar i { display: block; height: 100%; border-radius: 3px; background: var(--md-sys-color-tertiary); transition: width 1s linear; }
.cbar.paused i { background: var(--md-sys-color-outline); }
.cbar.wait { opacity: 0.5; }

.del-text { margin: 0 0 8px; color: var(--md-sys-color-on-surface-variant); }
.del-acts { display: flex; justify-content: flex-end; gap: 8px; }
@media (min-width: 960px) {
  .layout { display: grid; grid-template-columns: minmax(0, 1fr) 400px; gap: 16px; padding: 0 16px; align-items: start; }
  .playerbox { margin: 0 0 4px; }
  .right { position: sticky; top: 0; max-height: 100vh; overflow-y: auto; }
}
</style>
