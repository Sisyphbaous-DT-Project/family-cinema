<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, titleOf } from '../api/client'
import { peekSubject } from '../api/bangumi'
import { coverHint, holdTransition } from '../composables/motion'
import { fmtSize } from '../api/local'
import type { Subject } from '../api/types'
import { usePlayback } from '../composables/usePlayback'
import { useAnimeDownloads } from '../composables/animeDownloads'
import { playerPrefs } from '../composables/playerPrefs'
import { useScrolled } from '../composables/useScrolled'
import { useFitCells } from '../composables/useFitCells'
import TopAppBar from '../components/m3/TopAppBar.vue'
import Tabs from '../components/m3/Tabs.vue'
import Chip from '../components/m3/Chip.vue'
import Button from '../components/m3/Button.vue'
import Icon from '../components/m3/Icon.vue'
import BottomSheet from '../components/m3/BottomSheet.vue'
import VideoPlayer from '../components/VideoPlayer.vue'
import AnimeDownloadSheet from '../components/AnimeDownloadSheet.vue'
import FollowButton from '../components/FollowButton.vue'
import { useAirtime } from '../composables/airtime'
import { reportSource } from '../composables/follows'
import { useEpisodeSizes } from '../composables/episodeSizes'

const route = useRoute()
const id = Number(route.params.id)
// 从详情页过来时番剧资料已经在手，第一帧就能画出标题和播放器里的封面；没有的话先用列表卡片上的封面
const peeked = peekSubject(id)
const hint = coverHint(id)
const subject = ref<Subject | null>(peeked)
const subjectP = api.subject(id)
if (!peeked && !hint) holdTransition(subjectP)
const loadError = ref('')
const tab = ref(0)
// 「选源 / 简介」切换时内容顺着方向滑过去
const tabSlide = ref('slide-next')
watch(tab, (n, o) => (tabSlide.value = n > o ? 'slide-next' : 'slide-prev'))
const sheet = ref(false)
const kw = ref('')
const pb = usePlayback(id, subject)
const dl = useAnimeDownloads(pb)
// 顶部吸顶区（标题 + 播放器）：划过后下边缘出现渐隐带，内容不会被一刀切断
const stickEl = ref<HTMLElement | null>(null)
const stickScrolled = useScrolled(stickEl)

onMounted(async () => {
  try {
    subject.value = await subjectP
    kw.value = titleOf(subject.value)
    pb.start()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : '加载失败'
  }
})

const title = computed(() => (subject.value ? titleOf(subject.value) : hint?.title ?? ''))
const cover = computed(() => subject.value?.images.large || subject.value?.images.common || hint?.img || '')
const playerTitle = computed(() => [title.value, pb.currentEp.value?.name].filter(Boolean).join(' · '))
const itemCount = computed(() => pb.currentSource.value?.items.length ?? 0)
const { line: airLine, cn: airCn } = useAirtime(id, subject)

// 追番按钮带上正在看的片源；真的播起来了也告诉盒子（追着的番以后就按这个站查更新）
const followSource = computed(() =>
  pb.sel.rule && pb.currentItem.value ? { rule: pb.sel.rule, src: pb.currentItem.value.src } : null,
)
watch(
  () => pb.play.src,
  (src) => {
    if (src && followSource.value) reportSource(id, followSource.value.rule, followSource.value.src)
  },
)

// 下载模式里点集数只打勾；退出后才恢复点击播放
const dlMode = ref(false)
const dlSelected = reactive(new Set<number>())
const dlSheet = ref(false)
const allOn = computed(() => {
  const n = pb.currentRoad.value?.episodes.length ?? 0
  return n > 0 && dlSelected.size === n
})
const sz = useEpisodeSizes(pb, dlMode, () => [...dlSelected])
const pickedEpisodes = computed(() =>
  (pb.currentRoad.value?.episodes ?? []).flatMap((e, i) =>
    dlSelected.has(i) ? [{ name: e.name, url: e.url, index: i }] : [],
  ),
)

// 勾选圈和状态字都占宽度，切换下载模式或状态变化后重新量格子
const epsEl = ref<HTMLElement | null>(null)
const cellMin = useFitCells(epsEl, { base: 72, deps: [() => pb.currentRoad.value, dlMode, dl.stateSignature, sz.signature] })

function startDownload() {
  dlSelected.clear()
  dlMode.value = true
}
function exitDownload() {
  dlSelected.clear()
  dlMode.value = false
  dlSheet.value = false
}
function onQueued() {
  exitDownload()
  dl.refreshTasks()
}
function onEpisode(i: number) {
  if (!dlMode.value) {
    pb.playEpisode(pb.sel.road, i)
    return
  }
  if (dlSelected.has(i)) dlSelected.delete(i)
  else dlSelected.add(i)
}
function toggleAll() {
  const n = pb.currentRoad.value?.episodes.length ?? 0
  const shouldClear = allOn.value
  dlSelected.clear()
  if (!shouldClear) for (let i = 0; i < n; i++) dlSelected.add(i)
}
watch(() => pb.currentRoad.value, exitDownload)

function badge(st: { status: string; items: unknown[] }) {
  return st.status === 'done' && st.items.length ? st.items.length : ''
}
function pickItem(i: number) {
  sheet.value = false
  pb.chooseItem(i)
}
function speedText(kbs: number): string {
  return kbs > 0 ? `每秒 ${fmtSize(kbs * 1024)}` : ''
}

// 看完一集：先标记看完，再按"自动连播"设置决定要不要接着放
function onEnded() {
  pb.markEnded()
  if (playerPrefs.auto) pb.next()
}
</script>

<template>
  <div class="page">
    <div class="layout">
      <div class="left">
        <div ref="stickEl" class="stick fc-edge" :class="{ on: stickScrolled }">
          <TopAppBar :title="title" back flat>
            <template v-if="subject" #actions><FollowButton :subject="subject" :source="followSource" small /></template>
          </TopAppBar>
          <!-- 播放器：圆角矩形，左右和下面的卡片对齐（全屏时播放器整个提到最上层铺满，不受这层圆角影响） -->
          <div class="playerbox" :data-hero="`play:${id}`" data-hero-p="1">
            <VideoPlayer
              :src="pb.play.src"
              :kind="pb.play.kind"
              :series-key="`anime:${id}`"
              :cover="cover"
              :hero-key="`play:${id}`"
              :title="playerTitle"
              :start-at="pb.play.startAt"
              :resolving="pb.play.resolving"
              :message="pb.play.message"
              :has-prev="pb.hasPrev.value"
              :has-next="pb.hasNext.value"
              @prev="pb.prev"
              @next="pb.next"
              @ended="onEnded"
              @progress="pb.saveProgress"
            />
          </div>
        </div>
      </div>

      <div class="right">
        <Tabs v-model="tab" :tabs="['选源', '简介']" />

        <Transition :name="tabSlide" mode="out-in">
          <div v-if="loadError" class="state">
            <div class="muted">{{ loadError }}</div>
          </div>

          <section v-else-if="tab === 0" class="src">
            <!-- 片源站 -->
            <div class="label t-label-l muted">片源 <span v-if="pb.searching.value">· 搜索中…</span></div>
            <div class="chips">
              <Chip
                v-for="st in pb.sources.value"
                :key="st.rule.name"
                :selected="pb.sel.rule === st.rule.name"
                :loading="st.status === 'loading'"
                :disabled="st.status !== 'loading' && !st.items.length"
                :badge="badge(st)"
                :title="st.error"
                @click="pb.chooseRule(st.rule.name, true)"
              >{{ st.rule.name }}</Chip>
            </div>

            <!-- 该片源搜到的作品（多个时可切换） -->
            <button v-if="pb.currentItem.value" v-ripple class="item" type="button" :disabled="itemCount < 2" @click="sheet = true">
              <span class="grow">
                <span class="t-label-m muted">匹配作品</span>
                <span class="t-body-m clamp-1 name">{{ pb.currentItem.value.name }}</span>
              </span>
              <Icon v-if="itemCount > 1" name="expandMore" />
            </button>

            <!-- 线路 -->
            <template v-if="pb.roads.value.length > 1">
              <div class="label t-label-l muted">线路</div>
              <div class="chips">
                <Chip
                  v-for="(r, i) in pb.roads.value"
                  :key="i"
                  :selected="pb.sel.road === i"
                  @click="pb.chooseRoad(i)"
                >{{ r.name }}</Chip>
              </div>
            </template>

            <!-- 选集 / 多选下载到盒子 -->
            <template v-if="pb.currentRoad.value">
              <div v-if="dl.activeTask.value" class="dlstatus">
                <div class="dltop t-body-m">
                  <span class="grow">{{ dl.activeTask.value.stage || '排队中，前面的下完就开始' }}</span>
                  <span>{{ Math.round(dl.activeTask.value.progress || 0) }}%</span>
                </div>
                <div class="dlbar"><i :style="{ width: Math.min(100, dl.activeTask.value.progress || 0) + '%' }" /></div>
                <div class="t-body-s muted">
                  已完成 {{ dl.doneCount.value }} / {{ dl.activeTask.value.animeFiles?.length ?? 0 }} 集
                  <span v-if="speedText(dl.activeTask.value.speedKBs)"> · {{ speedText(dl.activeTask.value.speedKBs) }}</span>
                </div>
              </div>

              <div class="label t-label-l muted headrow">
                <span>{{ dlMode ? '选择要下载的集' : `选集 · 共 ${pb.currentRoad.value.episodes.length} 集` }}</span>
                <Button v-if="!dlMode" variant="tonal" small @click="startDownload"><Icon name="download" :size="20" />下载到盒子</Button>
              </div>
              <div v-if="dlMode" class="dlactions">
                <Button variant="outlined" small @click="toggleAll">{{ allOn ? '全不选' : '全选整部' }}</Button>
                <Button variant="text" small @click="exitDownload">退出</Button>
                <span class="t-body-s muted dlhint">点集数打勾</span>
              </div>
              <div v-if="dlMode && sz.needPhone.value" class="t-body-s muted">这个站要打开手机 App 才量得出大小，不影响下载</div>
              <div ref="epsEl" class="eps" :style="{ '--cell-min': cellMin + 'px' }">
                <button
                  v-for="(e, i) in pb.currentRoad.value.episodes"
                  :key="i"
                  v-ripple
                  class="ep t-label-l"
                  :class="{
                    on: !dlMode && pb.sel.ep === i,
                    picked: dlMode && dlSelected.has(i),
                    filling: ['running', 'merging'].includes(dl.episodeState(i).kind),
                    failed: dl.episodeState(i).kind === 'error',
                  }"
                  :style="{ '--p': dl.episodeState(i).pct + '%' }"
                  type="button"
                  @click="onEpisode(i)"
                >
                  <span v-if="dlMode" class="tick" :class="{ on: dlSelected.has(i) }"><Icon v-if="dlSelected.has(i)" name="check" :size="16" /></span>
                  <span class="lab">
                    {{ e.name }}<span v-if="dl.episodeState(i).text" class="st" :class="'st-' + dl.episodeState(i).kind">（{{ dl.episodeState(i).text }}）</span>
                    <template v-if="dlMode"><br /><span class="sz t-label-s" :class="{ guess: sz.sizeOf(i).guess }">{{ sz.sizeOf(i).text }}</span></template>
                  </span>
                </button>
              </div>
              <Transition name="bar-rise">
                <div v-if="dlMode" class="selbar">
                  <div class="grow">
                    <div class="t-body-l">
                      已选 <b>{{ pickedEpisodes.length }}</b> 集<template v-if="sz.totalText.value"> · <b :class="{ bad: sz.tooBig.value }">{{ sz.totalText.value }}</b></template>
                    </div>
                    <div class="t-body-s" :class="sz.tooBig.value ? 'bad' : 'muted'">
                      <template v-if="sz.total.value.pending && pickedEpisodes.length">还有 {{ sz.total.value.pending }} 集在算 · </template>
                      <template v-if="sz.disk.value">{{ sz.tooBig.value ? '空间不够了，' : '' }}盒子还剩 {{ sz.disk.value.freeH }}</template>
                    </div>
                  </div>
                  <Button :disabled="!pickedEpisodes.length" @click="dlSheet = true">下一步</Button>
                </div>
            </Transition>
          </template>

          <div v-else-if="pb.roadsLoading.value" class="eps">
            <div v-for="i in 12" :key="i" class="skeleton ep-sk" />
          </div>
          <div v-else-if="pb.roadsError.value" class="note">
            <Icon name="error" />
            <span class="t-body-m">{{ pb.roadsError.value }}</span>
          </div>
          <div v-else-if="pb.nothingFound.value" class="note">
            <Icon name="info" />
            <span class="t-body-m">所有片源都没有找到这部番，试试下面换个关键词搜索</span>
          </div>

          <!-- 手动搜索 -->
          <form class="manual" @submit.prevent="pb.searchManual(kw)">
            <input v-model="kw" class="t-body-m" type="search" placeholder="没找到？换个关键词搜索片源" />
            <Button variant="tonal">搜索</Button>
          </form>
        </section>

        <section v-else-if="subject" class="intro">
          <h2 class="t-title-l">{{ title }}</h2>
          <div v-if="subject.name_cn && subject.name !== subject.name_cn" class="t-body-m muted">{{ subject.name }}</div>
          <div v-if="airLine" class="air t-body-m">
            <Icon name="schedule" :size="18" />
            <span>{{ airLine }}<span v-if="airCn" class="muted"> · {{ airCn }}</span></span>
          </div>
          <p class="t-body-m summary">{{ subject.summary || '暂无简介' }}</p>
        </section>
        </Transition>
      </div>
    </div>

    <BottomSheet v-model="sheet" title="选择作品">
      <button
        v-for="(it, i) in pb.currentSource.value?.items ?? []"
        :key="it.src"
        v-ripple
        class="opt t-body-l"
        :class="{ on: i === pb.sel.item }"
        type="button"
        @click="pickItem(i)"
      >
        <span class="grow">{{ it.name }}</span>
        <Icon v-if="i === pb.sel.item" name="check" />
      </button>
    </BottomSheet>

    <AnimeDownloadSheet
      v-model="dlSheet"
      :rule="pb.sel.rule"
      :source-ref="pb.currentItem.value?.src ?? ''"
      :title="title"
      :episodes="pickedEpisodes"
      :size-text="sz.totalText.value"
      :disk-free="sz.disk.value?.freeH ?? ''"
      :too-big="sz.tooBig.value"
      @done="onQueued"
    />
  </div>
</template>

<style scoped>
.layout { max-width: 1400px; margin: 0 auto; }
.headrow { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.dlactions { display: flex; align-items: center; gap: 8px; }
.dlhint { margin-left: auto; }
.dlstatus { display: flex; flex-direction: column; gap: 8px; padding: 14px 16px; border-radius: var(--fc-radius); background: var(--md-sys-color-tertiary-container); color: var(--md-sys-color-on-tertiary-container); box-shadow: var(--fc-float); }
.dltop { display: flex; align-items: flex-start; gap: 12px; }
.dltop > span:last-child { flex: none; }
.dlbar { height: 6px; overflow: hidden; border-radius: 3px; background: var(--md-sys-color-surface-container-highest); }
.dlbar i { display: block; height: 100%; border-radius: 3px; background: var(--md-sys-color-primary); transition: width var(--md-dur-long) var(--md-ease-standard); }
.ep.picked { background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); box-shadow: inset 0 0 0 2px var(--md-sys-color-primary); }
.ep .lab { text-align: center; }
.ep .sz { color: var(--md-sys-color-on-surface-variant); }
.ep .sz.guess { opacity: 0.7; }
.ep.picked .sz { color: inherit; }
.bad { color: var(--md-sys-color-error, #ba1a1a); }
.air { display: flex; align-items: flex-start; gap: 6px; margin-top: 8px; color: var(--md-sys-color-primary); font-weight: 600; }
.air > .m3-icon { flex: none; margin-top: 2px; }
.air .muted { font-weight: 400; }
.tick { flex: none; box-sizing: border-box; display: flex; align-items: center; justify-content: center; width: 20px; height: 20px; border: 2px solid var(--md-sys-color-outline); border-radius: 50%; }
.tick.on { border-color: var(--md-sys-color-primary); background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.selbar { position: sticky; bottom: calc(12px + env(safe-area-inset-bottom)); z-index: 5; display: flex; align-items: center; gap: 12px; margin-top: 4px; padding: 10px 10px 10px 18px; border-radius: var(--fc-radius); background: var(--fc-card); box-shadow: 0 2px 6px rgb(0 0 0 / 0.08), 0 8px 28px rgb(0 0 0 / 0.14); }
.stick { position: sticky; top: 0; z-index: 6; background: var(--md-sys-color-surface); }
.playerbox { margin: 0 16px 4px; border-radius: var(--fc-radius); overflow: hidden; isolation: isolate; box-shadow: var(--fc-float); background: #000; }
.state { padding: 48px 16px; text-align: center; }
.src, .intro { padding: 12px 16px 32px; display: flex; flex-direction: column; gap: 12px; }
.label { margin-top: 4px; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; }
.item { display: flex; align-items: center; gap: 8px; padding: 12px 12px 12px 16px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); text-align: left; }
.item:disabled { opacity: 1; }
.item .grow { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.name { color: var(--md-sys-color-on-surface); }
/* 每格最小宽度由 useFitCells 按集名实际宽度算（--cell-min），最多压到一行两个，再长的名字省略号 */
.eps { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(var(--cell-min, 72px), calc(50% - 4px)), 1fr)); gap: 8px; }
.ep { --p: 0%; position: relative; display: flex; align-items: center; justify-content: center; gap: 5px; min-height: 44px; padding: 4px 6px; border-radius: var(--md-shape-md); background: var(--md-sys-color-surface-container-high); color: var(--md-sys-color-on-surface); overflow: hidden; white-space: nowrap; transition: background-color var(--md-dur-medium) var(--md-ease-standard); }
.ep .lab { overflow: hidden; text-overflow: ellipsis; }
.ep.on { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.ep.filling { background-image: linear-gradient(90deg, var(--md-sys-color-tertiary-container) var(--p), transparent var(--p)); }
.ep.failed { box-shadow: inset 0 0 0 1px var(--md-sys-color-error, #ba1a1a); }
.st-done { color: var(--md-sys-color-primary); }
.st-running, .st-merging { color: var(--md-sys-color-tertiary); }
.st-queued, .st-resolving { color: var(--md-sys-color-on-surface-variant); }
.st-error { color: var(--md-sys-color-error, #ba1a1a); }
.ep-sk { height: 40px; border-radius: var(--md-shape-md); }
.note { display: flex; gap: 12px; align-items: center; padding: 16px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); color: var(--md-sys-color-on-surface-variant); }
.manual { display: flex; gap: 8px; align-items: center; margin-top: 8px; }
.manual input { flex: 1; min-width: 0; height: 48px; padding: 0 16px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: transparent; outline: none; }
.manual input:focus { border-color: var(--md-sys-color-primary); box-shadow: 0 0 0 1px var(--md-sys-color-primary); }
.intro h2 { margin: 0; }
.summary { white-space: pre-line; color: var(--md-sys-color-on-surface-variant); margin: 8px 0 0; }
.opt { display: flex; align-items: center; width: 100%; padding: 14px 8px; border-radius: var(--md-shape-md); text-align: left; }
.opt.on { color: var(--md-sys-color-primary); }
.grow { flex: 1; min-width: 0; }

/* 宽屏：左边播放器，右边选源面板（和 Kazumi 桌面端一致） */
@media (min-width: 960px) {
  .layout { display: grid; grid-template-columns: minmax(0, 1fr) 400px; gap: 16px; padding: 0 16px; align-items: start; }
  .playerbox { margin: 0 0 4px; }
  .right { position: sticky; top: 0; max-height: 100vh; overflow-y: auto; }
}
</style>
