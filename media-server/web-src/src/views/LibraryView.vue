<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { coverURL, epLabel, fetchLibrary, fmtSize, rescan, type DownloadingCard, type LocalCategory, type LocalItem } from '../api/local'
import { api } from '../api/client'
import { toast } from '../composables/snackbar'
import { useServerTasks } from '../composables/serverTasks'
import { isOffline, refreshCacheMarks } from '../composables/cache'
import { hasBridge } from '../composables/bridge'
import { appUpdate, startAppUpdate } from '../composables/appUpdate'
import { getProg } from '../composables/progress'
import TopAppBar from '../components/m3/TopAppBar.vue'
import IconButton from '../components/m3/IconButton.vue'
import Button from '../components/m3/Button.vue'
import Icon from '../components/m3/Icon.vue'
import LocalCard from '../components/LocalCard.vue'

defineOptions({ name: 'LibraryView' })

// 同一个页面服务两个标签：电影电视剧（/）和番剧（/series），互不混杂
const route = useRoute()
const cat = route.meta.cat as LocalCategory
const title = cat === 'movie' ? '电影电视剧' : '番剧'

// 电影电视剧页顶上的搜索框：去网上的网盘里找片子（盘搜），结果在 /pansou 页
const router = useRouter()
const panQ = ref('')
const panInput = ref<HTMLInputElement>()
function panSearch() {
  const k = panQ.value.trim()
  if (!k) {
    panInput.value?.focus()
    return
  }
  panInput.value?.blur()
  router.push({ path: '/pansou', query: { q: k } })
}

const items = ref<LocalItem[]>([])
const downloading = ref<DownloadingCard[]>([])
const loading = ref(true)
const error = ref('')
const offline = ref(isOffline())
let loadedAt = 0

async function load(silent = false) {
  if (!silent) loading.value = true
  error.value = ''
  try {
    const r = await fetchLibrary(cat)
    items.value = r.items
    downloading.value = r.downloading
    offline.value = isOffline()
    loadedAt = Date.now()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function refresh() {
  toast('正在刷新片子列表…', 1500)
  try {
    await rescan()
    // 扫描在后台跑，稍等片刻再取最新结果
    setTimeout(() => load(true), 1500)
  } catch {
    toast('刷新没成功，等一会儿再试试')
  }
}

// 观看进度存在原生/localStorage 里，不是响应式的：靠 progTick 让"接着看"和海报上的进度条重算
const progTick = ref(0)
let tickTimer: ReturnType<typeof setTimeout> | undefined
function refreshProgress() {
  progTick.value++
  // 播放页离开时会再存一次进度，保险起见稍等再算一遍（返回动画约 0.3 秒）
  clearTimeout(tickTimer)
  tickTimer = setTimeout(() => progTick.value++, 700)
}
// 从后台切回 App 时也重算（比如在通知栏/别的 App 待了一会儿）
function onVisible() {
  if (!document.hidden) refreshProgress()
}

// 下载卡片共用任务状态中心；任务结束时顺手刷新片库，让下好的片子马上出现
const { tasks: serverTasks, refreshTasks } = useServerTasks()
let activeBefore = -1
watch(serverTasks, (list) => {
  const cards = list
    .filter((t) => t.category === cat && (t.status === 'running' || t.status === 'processing'))
    .map((t) => ({
      taskId: t.id,
      title: t.name,
      category: cat,
      progress: t.progress,
      speedKBs: t.speedKBs,
      status: t.status,
      stage: t.stage,
      cover: t.cover,
      animeFiles: t.animeFiles,
      files: t.files,
      animeFailedFiles: t.animeFailedFiles,
      animeFailures: t.animeFailures,
      animeActive: t.animeActive,
    }))
  downloading.value = cards
  if (activeBefore >= 0 && cards.length < activeBefore) load(true)
  activeBefore = cards.length
}, { immediate: true })

onMounted(() => {
  refreshCacheMarks()
  load()
  document.addEventListener('visibilitychange', onVisible)
})
onBeforeUnmount(() => {
  clearTimeout(tickTimer)
  document.removeEventListener('visibilitychange', onVisible)
})
// 从详情页/缓存页返回时：马上重算"接着看"和进度条，"已缓存"标记也刷新；片子列表刚加载过就不重复请求
onActivated(() => {
  refreshCacheMarks()
  refreshProgress()
  if (Date.now() - loadedAt > 15000) load(true)
})

const empty = computed(() => !loading.value && !error.value && !items.value.length && !downloading.value.length)
const pct = (p: number) => Math.max(0, Math.min(100, Math.round(p || 0)))

/** 盒子下载任务的状态，写成大白话 */
function dlText(d: DownloadingCard): string {
  const speed = d.speedKBs > 0 ? ` · 每秒 ${fmtSize(d.speedKBs * 1024)}` : ''
  const total = d.animeFiles?.length ?? 0
  if (total > 0) {
    const files = new Set(d.files ?? [])
    const done = d.animeFiles!.filter((f) => files.has(f)).length
    const failed = d.animeFailedFiles?.length ?? 0
    const remaining = Math.max(0, total - done - failed)
    const failure = failed ? ` · 失败 ${failed} 集` : ''
    return `已完成 ${done}/${total} 集 · 还剩 ${remaining} 集${failure} · 总进度 ${pct(d.progress)}%${speed}`
  }
  if (d.status === 'processing') return d.stage || '下载好了，正在整理…'
  return `已下载 ${pct(d.progress)}%` + (d.speedKBs > 0 ? `，每秒 ${fmtSize(d.speedKBs * 1024)}` : '')
}

function activeText(a: NonNullable<DownloadingCard['animeActive']>[number]): string {
  switch (a.state) {
    case 'merging': return '正在拼成完整视频'
    case 'mergewait': return '已下完，等着拼接'
    case 'resolving': return '正在找视频地址'
    case 'phone': return '等手机帮忙找地址'
    default: return '下载中'
  }
}

// 失败的那一集：点"重试"让盒子再下一遍（任务还在跑就直接接到它的队列后面）
const retrying = ref('')
async function retryEp(d: DownloadingCard, file: string) {
  const key = d.taskId + '|' + file
  if (retrying.value) return
  retrying.value = key
  try {
    const r = await api.retryDownload(d.taskId, [file])
    if (!r.ok) throw new Error(r.error || '重试没成功')
    toast(`已重新排上${file.replace(/\.mp4$/i, '')}，下载完会出现在片库里`, 4000)
    refreshTasks()
  } catch (e) {
    toast(e instanceof Error ? e.message : '重试没成功，再点一次')
  } finally {
    retrying.value = ''
  }
}

/** 看到哪儿了：用"多少分钟"说，比百分比好懂 */
function watchedText(sec: number): string {
  if (sec < 60) return '刚开始看'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  return h ? `看到 ${h} 小时 ${m} 分钟` : `看到 ${m} 分钟`
}

/* 接着看：一部片子只留一条（最近看的那一集），按最近观看排前 4 部。
   那一集没看完 → 从看到的地方接着放；已经看完了 → 直接给下一集；整部看完了就不再出现 */
interface ContinueEntry {
  item: LocalItem
  epIndex: number
  sub: string
  ratio: number
  ts: number
}
const MIN_WATCH = 10 // 看满 10 秒才记（以前要看够 2%，一集 45 分钟得看快 1 分钟）
const continueList = computed<ContinueEntry[]>(() => {
  void progTick.value // 进度变了靠它触发重算
  const list: ContinueEntry[] = []
  for (const it of items.value) {
    const eps = it.episodes
    const fromCache = it.id.startsWith('cache:')
    const name = (i: number) => epLabel(eps[i], i, eps.length, fromCache) // 集名只写"第N集"
    // 找这部里最近看过的那一集
    let last = -1
    let lastP: { pos: number; dur: number; ts: number } | null = null
    eps.forEach((ep, ei) => {
      const p = getProg(ep.path)
      if (p && p.dur && p.pos >= MIN_WATCH && (!lastP || (p.ts || 0) >= (lastP.ts || 0))) {
        last = ei
        lastP = p
      }
    })
    const p = lastP as { pos: number; dur: number; ts: number } | null
    if (!p) continue
    const ratio = p.pos / p.dur
    if (ratio < 0.95) {
      // 没看完：单部电影不写集数
      const head = eps.length > 1 ? name(last) + ' · ' : ''
      list.push({ item: it, epIndex: last, sub: head + watchedText(p.pos), ratio, ts: p.ts || 0 })
    } else if (last + 1 < eps.length) {
      list.push({ item: it, epIndex: last + 1, sub: `看完了${name(last)} · 接着看${name(last + 1)}`, ratio: 0, ts: p.ts || 0 })
    }
  }
  return list.sort((a, b) => b.ts - a.ts).slice(0, 4)
})
</script>

<template>
  <div class="page tab-page">
    <TopAppBar :title="title">
      <template #actions>
        <!-- 有新版本时常驻提示，点一下就更新（顶栏地方不够，这时先把刷新按钮让出来） -->
        <Button v-if="appUpdate.available" variant="filled" small class="topbtn" :disabled="appUpdate.busy" @click="startAppUpdate">
          有新版本
        </Button>
        <Button v-if="hasBridge()" variant="tonal" small class="topbtn" @click="$router.push('/cache')">
          <Icon name="download" :size="18" />我的缓存
        </Button>
        <IconButton v-if="!offline && !appUpdate.available" icon="refresh" label="刷新片子列表" @click="refresh" />
      </template>
    </TopAppBar>

    <div class="wrap">
      <form v-if="cat === 'movie' && !offline" class="pansearch" @submit.prevent="panSearch">
        <Icon name="search" :size="22" class="psicon" />
        <input
          ref="panInput"
          v-model="panQ"
          class="t-body-l"
          type="search"
          enterkeyhint="search"
          placeholder="搜片名，去网上的网盘里找"
          autocomplete="off"
        />
        <Button variant="filled" small class="psbtn" @click="panSearch">搜索</Button>
      </form>

      <div v-if="offline" class="fc-card offbanner t-body-l">
        <Icon name="cloudOff" :size="22" />
        <span>没连上家里的网络，现在只能看手机里缓存好的片子</span>
      </div>

      <!-- 接着看：上次没看完的，点一下接着播 -->
      <section v-if="continueList.length">
        <h2 class="t-title-m fc-sec">接着看</h2>
        <div class="fc-card list">
          <router-link
            v-for="c in continueList"
            :key="c.item.id + ':' + c.epIndex"
            v-ripple
            class="crow"
            :to="`/local/${encodeURIComponent(c.item.id)}?ep=${c.epIndex}`"
          >
            <div class="cthumb">
              <img v-if="c.item.cover" :src="coverURL(c.item.cover)" :alt="c.item.title" loading="lazy" decoding="async" />
              <Icon v-else name="tv" :size="22" />
            </div>
            <div class="grow">
              <div class="t-title-m clamp-1">{{ c.item.title }}</div>
              <div class="t-body-m muted clamp-1">{{ c.sub }}</div>
              <i v-if="c.ratio > 0" class="cprog"><i :style="{ width: Math.round(c.ratio * 100) + '%' }" /></i>
            </div>
            <span class="cplay"><Icon name="play" :size="24" /></span>
          </router-link>
        </div>
      </section>

      <!-- 盒子正在下载的（下载完会自动出现在下面的列表里） -->
      <section v-if="downloading.length">
        <h2 class="t-title-m fc-sec">盒子正在下载</h2>
        <div class="fc-card list">
          <div v-for="d in downloading" :key="d.taskId" class="drow">
            <div class="t-body-l clamp-1">{{ d.title }}</div>
            <div class="progress"><i :style="{ width: pct(d.progress) + '%' }" /></div>
            <div class="t-body-m muted">{{ dlText(d) }}</div>
            <div v-if="d.animeActive?.length || d.animeFailures?.length" class="deps">
              <div v-for="a in d.animeActive" :key="a.file" class="dep">
                <div class="dehead">
                  <span class="t-body-m">{{ a.file.replace(/\.mp4$/i, '') }}</span>
                  <span class="t-body-s destate">
                    {{ activeText(a) }}<template v-if="a.state === 'running'"> · {{ pct(a.progress) }}%</template>
                  </span>
                </div>
                <div v-if="a.state === 'running'" class="deprogress"><i :style="{ width: pct(a.progress) + '%' }" /></div>
              </div>
              <div v-for="f in d.animeFailures" :key="f.file" class="dep fail">
                <div class="dehead">
                  <span class="t-body-m">{{ f.file.replace(/\.mp4$/i, '') }}</span>
                  <span class="fright">
                    <span class="t-body-s destate bad">失败</span>
                    <button
                      type="button"
                      class="why"
                      :title="f.message"
                      :aria-label="'失败原因：' + f.message"
                      @click="toast(f.message, 8000)"
                    >
                      <Icon name="help" :size="18" />
                    </button>
                    <Button
                      small
                      variant="tonal"
                      :disabled="retrying === d.taskId + '|' + f.file"
                      @click="retryEp(d, f.file)"
                    >
                      {{ retrying === d.taskId + '|' + f.file ? '重试中…' : '重试' }}
                    </Button>
                  </span>
                </div>
              </div>
            </div>
            <div v-else-if="d.animeFiles?.length && d.stage" class="t-body-s muted">{{ d.stage }}</div>
          </div>
        </div>
      </section>

      <div v-if="loading" class="grid top">
        <div v-for="i in 9" :key="i"><div class="skeleton sk" /><div class="skeleton line" /></div>
      </div>

      <div v-else-if="error" class="fc-card state">
        <Icon name="error" :size="40" />
        <div class="t-body-l">{{ error }}</div>
        <Button variant="tonal" @click="load()">再试一次</Button>
      </div>

      <div v-else-if="empty" class="fc-card state">
        <Icon :name="offline ? 'cloudOff' : 'tv'" :size="48" />
        <template v-if="offline">
          <div class="t-title-m">手机里还没有缓存的片子</div>
          <div class="muted t-body-m">连上家里的 WiFi 后，在片子里点「缓存视频」，以后没网也能看</div>
        </template>
        <template v-else>
          <div class="t-title-m">盒子里还没有{{ title }}</div>
          <div class="muted t-body-m">想添加片子，可以到「设置」里的「片库管理」下载或上传</div>
          <Button variant="tonal" @click="$router.push('/settings')">去设置</Button>
        </template>
      </div>

      <section v-else-if="items.length">
        <h2 class="t-title-m fc-sec">全部 · 共 {{ items.length }} 部</h2>
        <div class="grid">
          <LocalCard v-for="it in items" :key="it.id" :item="it" :tick="progTick" />
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.wrap { padding: 0 16px 24px; max-width: 1400px; margin: 0 auto; }
.topbtn { margin-right: 6px; }

/* 网盘搜索框：和番剧搜索页同款的悬浮圆角框 */
.pansearch { display: flex; align-items: center; gap: 8px; height: 56px; max-width: 720px; margin: 4px 0 8px; padding: 0 8px 0 16px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); }
.pansearch:focus-within { box-shadow: var(--fc-float), 0 0 0 2px var(--md-sys-color-primary); }
.pansearch input { flex: 1; min-width: 0; height: 100%; border: 0; outline: 0; background: transparent; color: var(--md-sys-color-on-surface); }
.pansearch input::placeholder { color: var(--md-sys-color-on-surface-variant); }
.pansearch input::-webkit-search-cancel-button { display: none; }
.psicon { flex: none; color: var(--md-sys-color-on-surface-variant); }
.psbtn { flex: none; }
.offbanner { display: flex; align-items: center; gap: 12px; margin-top: 4px; padding: 14px 16px; color: var(--md-sys-color-on-surface-variant); }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(104px, 1fr)); gap: 18px 12px; }
.grid.top { margin-top: 12px; }
.sk { aspect-ratio: 2 / 3; }
.line { height: 14px; width: 70%; margin-top: 10px; border-radius: 4px; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; margin-top: 16px; padding: 48px 20px; text-align: center; color: var(--md-sys-color-on-surface-variant); }

/* 卡片里的一行一行 */
.list { overflow: hidden; }
.crow, .drow { display: flex; padding: 12px 14px; color: inherit; }
.crow + .crow, .drow + .drow { border-top: 1px solid var(--md-sys-color-surface-container-high); }
.grow { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }

/* 接着看：小海报 + 片名 + 看到哪儿 + 播放键 */
.crow { align-items: center; gap: 14px; }
.cthumb { flex: none; width: 52px; aspect-ratio: 2 / 3; border-radius: 10px; overflow: hidden; display: flex; align-items: center; justify-content: center; background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }
.cthumb img { width: 100%; height: 100%; object-fit: cover; }
.cprog { display: block; height: 4px; margin-top: 6px; border-radius: 2px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.cprog i { display: block; height: 100%; border-radius: 2px; background: var(--md-sys-color-primary); }
.cplay { flex: none; display: flex; align-items: center; justify-content: center; width: 44px; height: 44px; border-radius: 14px; background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }

/* 盒子正在下载 */
.drow { flex-direction: column; gap: 6px; }
.progress { height: 6px; border-radius: 3px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.progress i { display: block; height: 100%; border-radius: 3px; background: var(--md-sys-color-primary); transition: width var(--md-dur-long) var(--md-ease-standard); }
.deps { display: flex; flex-direction: column; gap: 8px; margin-top: 4px; }
.dep { padding: 9px 10px; border-radius: 12px; background: var(--md-sys-color-surface-container-low); }
.dehead { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.destate { flex: none; color: var(--md-sys-color-on-surface-variant); }
.deprogress { height: 4px; margin-top: 7px; border-radius: 2px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.deprogress i { display: block; height: 100%; border-radius: 2px; background: var(--md-sys-color-primary); transition: width var(--md-dur-long) var(--md-ease-standard); }
.dep.fail { background: var(--md-sys-color-surface-container); box-shadow: inset 0 0 0 1px var(--md-sys-color-error-container); }
.destate.bad { color: var(--md-sys-color-error); }
.fright { flex: none; display: flex; align-items: center; gap: 6px; }
.why { display: flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--md-sys-color-on-surface-variant); cursor: pointer; }
.why:hover { background: var(--md-sys-color-surface-container-highest); }
</style>
