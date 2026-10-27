<script setup lang="ts">
// 网盘搜索结果页（从「电影电视剧」页的搜索框进来，/pansou?q=片名）。
// 盘搜是"先回快的、慢的网站后台接着搜"：第一次大约 10 秒出结果，之后隔几秒再要两次，结果会变多。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { copyTextOf, panSearch, PAN_TYPES, type PanLink, type PanResult } from '../api/pansou'
import { coverURL, fetchLibrary, type LocalItem } from '../api/local'
import { btDownload } from '../api/manage'
import { bcall, hasBridge } from '../composables/bridge'
import { copyText } from '../composables/clipboard'
import { toast } from '../composables/snackbar'
import { readJSON, writeJSON } from '../composables/storage'
import { useScrolled } from '../composables/useScrolled'
import IconButton from '../components/m3/IconButton.vue'
import Button from '../components/m3/Button.vue'
import Chip from '../components/m3/Chip.vue'
import Icon from '../components/m3/Icon.vue'
import Spinner from '../components/m3/Spinner.vue'
import BottomSheet from '../components/m3/BottomSheet.vue'
import PanRow from '../components/PanRow.vue'

const route = useRoute()
const router = useRouter()

const HIST_KEY = 'fc.panHistory'
const PREVIEW = 3 // "全部"里每种网盘先露几条
const PAGE = 30 // 单看一种网盘时，一次显示多少条

const barEl = ref<HTMLElement | null>(null)
const barScrolled = useScrolled(barEl)
const input = ref<HTMLInputElement>()
const q = ref(String(route.query.q || ''))
const kw = ref('') // 正在显示结果的关键词
const history = ref<string[]>(readJSON<string[]>(HIST_KEY, []))

const loading = ref(false)
const more = ref(false) // 第一批结果出来后，还在等慢的网站
const error = ref('')
const result = ref<PanResult | null>(null)
const local = ref<LocalItem[]>([])
const filter = ref('all')
const shown = ref(PAGE)
const copiedUrl = ref('')

// ---------- 搜索 ----------
let seq = 0
let ctrl: AbortController | null = null
let waitTimer: ReturnType<typeof setTimeout> | undefined
function cancel() {
  seq++
  ctrl?.abort()
  ctrl = null
  clearTimeout(waitTimer)
}
const sleep = (ms: number) => new Promise<void>((r) => (waitTimer = setTimeout(r, ms)))

async function run(k: string) {
  cancel()
  const my = seq
  ctrl = new AbortController()
  kw.value = k
  loading.value = true
  more.value = false
  error.value = ''
  result.value = null
  filter.value = 'all'
  shown.value = PAGE
  copiedUrl.value = ''
  findLocal(k)
  try {
    const r = await panSearch(k, ctrl.signal)
    if (my !== seq) return
    result.value = r
  } catch (e) {
    if (my !== seq || (e as Error)?.name === 'AbortError') return
    error.value = e instanceof Error ? e.message : '搜索出错了'
    return
  } finally {
    if (my === seq) loading.value = false
  }
  // 慢的网站还在后台搜：过一会儿再要一次，结果会变多（盘搜自己有缓存，再要很快）
  more.value = true
  for (const wait of [5000, 8000]) {
    await sleep(wait)
    if (my !== seq) return
    try {
      const r = await panSearch(k, ctrl?.signal)
      if (my !== seq) return
      if (r.total >= (result.value?.total ?? 0)) result.value = r
    } catch {
      break
    }
  }
  if (my === seq) more.value = false
}

// 盒子里已经有的片子，放在最前面（不用再去网上找了）
async function findLocal(k: string) {
  local.value = []
  const norm = (s: string) => s.toLowerCase().replace(/[\s·・:：,，.。!！?？\-—_'"“”‘’()（）[\]【】]/g, '')
  const key = norm(k)
  if (!key) return
  const my = seq
  const lists = await Promise.all((['movie', 'series'] as const).map((c) => fetchLibrary(c).then((r) => r.items).catch(() => [] as LocalItem[])))
  if (my !== seq) return
  local.value = lists.flat().filter((it) => !it.id.startsWith('cache:') && norm(it.title).includes(key)).slice(0, 5)
}

function submit(k?: string) {
  const v = (k ?? q.value).trim()
  if (!v) return
  q.value = v
  input.value?.blur()
  history.value = [v, ...history.value.filter((x) => x !== v)].slice(0, 10)
  writeJSON(HIST_KEY, history.value)
  // 地址里记下关键词（返回时还在），换词用 replace，返回键直接回片库
  if (route.query.q !== v) router.replace({ path: '/pansou', query: { q: v } })
  else run(v)
}
watch(
  () => route.query.q,
  (v) => {
    if (route.path === '/pansou' && typeof v === 'string' && v.trim()) run(v.trim())
  },
)

function clearInput() {
  q.value = ''
  nextTick(() => input.value?.focus())
}
function clearHistory() {
  history.value = []
  writeJSON(HIST_KEY, [])
}

onMounted(() => {
  if (q.value.trim()) submit(q.value)
  else nextTick(() => input.value?.focus())
})
onBeforeUnmount(cancel)

// ---------- 结果 ----------
const types = computed(() => PAN_TYPES.filter((t) => result.value?.byType[t.key]?.length))
const typeName = (key: string) => PAN_TYPES.find((t) => t.key === key)?.name ?? '网盘'
const curList = computed(() => (filter.value === 'all' ? [] : (result.value?.byType[filter.value] ?? [])))

function pick(key: string) {
  filter.value = key
  shown.value = PAGE
  // 回到筛选标签的位置，免得停在长列表中间
  nextTick(() => document.querySelector('.pan-chips')?.scrollIntoView({ block: 'start', behavior: 'smooth' }))
}

function copy(l: PanLink, type: string) {
  if (!copyText(copyTextOf(l))) {
    toast('复制没成功，可以长按链接文字手动复制')
    return
  }
  copiedUrl.value = l.url
  if (type === 'magnet' || type === 'ed2k') toast('已复制下载链接', 2500)
  else toast(`已复制${l.password ? '链接和提取码' : '链接'}，打开${typeName(type)} App 会自动认出来`, 3500)
}

function open(l: PanLink) {
  // 有提取码就先复制上，复制不成功也把提取码说出来
  if (l.password) {
    const ok = copyText(l.password)
    toast(ok ? `提取码 ${l.password} 已复制，要输入时粘贴就行` : `提取码是 ${l.password}，要输入时用得上`, 5000)
  }
  if (hasBridge()) bcall((b) => b.openExternal?.(l.url))
  else window.open(l.url, '_blank', 'noopener')
}

// ---------- 磁力链接交给盒子下载 ----------
const dlLink = ref<PanLink | null>(null)
const dlName = ref('')
const dlCat = ref<'movie' | 'series'>('movie')
const dlBusy = ref(false)
function askDownload(l: PanLink) {
  dlLink.value = l
  dlName.value = kw.value
  dlCat.value = 'movie'
}
async function doDownload() {
  const name = dlName.value.trim()
  if (!name || !dlLink.value) return
  dlBusy.value = true
  try {
    const task = await btDownload(dlLink.value.url, name, dlCat.value)
    if (!task?.id) throw new Error('盒子没接下这个任务')
    dlLink.value = null
    toast(`盒子开始下载了，下好会出现在「${dlCat.value === 'movie' ? '电影电视剧' : '番剧'}」里`, 4000)
  } catch (e) {
    toast(e instanceof Error ? e.message : '下载没开始，等一会儿再试试')
  } finally {
    dlBusy.value = false
  }
}
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
          placeholder="输入电影、电视剧的名字"
          autocomplete="off"
        />
        <IconButton v-if="q" icon="close" label="清空" :size="20" @click="clearInput" />
        <IconButton icon="search" label="搜索" @click="submit()" />
      </form>
    </header>

    <div class="wrap">
      <!-- 还没搜：搜索历史 + 说明 -->
      <template v-if="!kw">
        <section v-if="history.length">
          <div class="hhead">
            <span class="t-title-s muted">搜过的</span>
            <button v-ripple class="clear t-label-m" type="button" @click="clearHistory">清空</button>
          </div>
          <div class="chips"><Chip v-for="h in history" :key="h" @click="submit(h)">{{ h }}</Chip></div>
        </section>
        <div class="fc-card tip t-body-m">
          在网上的网盘里找片子：搜到后点一下就能复制链接，再打开对应的网盘 App（夸克、百度等）就能看或者存下来。磁力链接还能直接让盒子下载。
        </div>
      </template>

      <template v-else>
        <!-- 盒子里已经有了 -->
        <section v-if="local.length">
          <h2 class="t-title-m fc-sec">盒子里已经有了</h2>
          <div class="fc-card list">
            <router-link v-for="it in local" :key="it.id" v-ripple class="lrow" :to="`/local/${encodeURIComponent(it.id)}`">
              <div class="lthumb">
                <img v-if="it.cover" :src="coverURL(it.cover)" :alt="it.title" loading="lazy" decoding="async" />
                <Icon v-else name="tv" :size="20" />
              </div>
              <div class="grow t-title-m clamp-1">{{ it.title }}</div>
              <span class="lplay"><Icon name="play" :size="22" /></span>
            </router-link>
          </div>
        </section>

        <div v-if="loading" class="fc-card state">
          <Spinner :size="36" />
          <div class="t-body-l">正在网上找「{{ kw }}」</div>
          <div class="t-body-m muted">要找几十个网站，大概要等 10 秒</div>
        </div>

        <div v-else-if="error" class="fc-card state">
          <Icon name="error" :size="40" />
          <div class="t-body-l">{{ error }}</div>
          <Button variant="tonal" @click="run(kw)">再试一次</Button>
        </div>

        <div v-else-if="result && !result.total && more" class="fc-card state">
          <Spinner :size="36" />
          <div class="t-body-l">暂时还没找到「{{ kw }}」</div>
          <div class="t-body-m muted">还有几个慢一点的网站在找，再等几秒</div>
        </div>

        <div v-else-if="result && !result.total" class="fc-card state">
          <Icon name="search" :size="44" />
          <div class="t-body-l">没搜到「{{ kw }}」</div>
          <div class="t-body-m muted">换个说法试试，比如只写片名，不写年份、第几季</div>
        </div>

        <template v-else-if="result">
          <div class="summary t-body-m muted">
            <span>找到 {{ result.total }} 个链接，点一下就复制</span>
            <span v-if="more" class="moreing"><Spinner :size="14" />还在找更多</span>
          </div>

          <div class="pan-chips chips scroll">
            <Chip :selected="filter === 'all'" @click="pick('all')">全部</Chip>
            <Chip v-for="t in types" :key="t.key" :selected="filter === t.key" :badge="result.byType[t.key].length" @click="pick(t.key)">
              {{ t.short }}
            </Chip>
          </div>

          <!-- 全部：每种网盘一组，先露几条 -->
          <template v-if="filter === 'all'">
            <section v-for="t in types" :key="t.key">
              <h2 class="t-title-m fc-sec">{{ t.name }} · {{ result.byType[t.key].length }} 条</h2>
              <div class="fc-card list">
                <PanRow
                  v-for="l in result.byType[t.key].slice(0, PREVIEW)"
                  :key="l.url"
                  :link="l"
                  :type="t.key"
                  :copied="copiedUrl === l.url"
                  @copy="copy(l, t.key)"
                  @open="open(l)"
                  @download="askDownload(l)"
                />
                <button v-if="result.byType[t.key].length > PREVIEW" v-ripple class="morebtn t-label-l" type="button" @click="pick(t.key)">
                  看全部 {{ result.byType[t.key].length }} 条{{ t.short }}<Icon name="expandMore" :size="20" />
                </button>
              </div>
            </section>
          </template>

          <!-- 单看一种网盘 -->
          <template v-else>
            <h2 class="t-title-m fc-sec">{{ typeName(filter) }} · {{ curList.length }} 条</h2>
            <div class="fc-card list">
              <PanRow
                v-for="l in curList.slice(0, shown)"
                :key="l.url"
                :link="l"
                :type="filter"
                :copied="copiedUrl === l.url"
                @copy="copy(l, filter)"
                @open="open(l)"
                @download="askDownload(l)"
              />
              <button v-if="curList.length > shown" v-ripple class="morebtn t-label-l" type="button" @click="shown += PAGE">
                再多显示一些（还有 {{ curList.length - shown }} 条）<Icon name="expandMore" :size="20" />
              </button>
            </div>
          </template>
        </template>
      </template>
    </div>

    <!-- 磁力链接：让盒子下载 -->
    <BottomSheet :model-value="!!dlLink" title="让盒子下载" @update:model-value="dlLink = null">
      <div class="form">
        <div class="t-body-m muted clamp-2">{{ dlLink?.note || '磁力链接' }}</div>
        <label class="t-label-l">下好以后叫什么名字</label>
        <input v-model="dlName" class="textin t-body-l" placeholder="片名" />
        <label class="t-label-l">放在哪里</label>
        <div class="chips">
          <Chip :selected="dlCat === 'movie'" @click="dlCat = 'movie'">电影电视剧</Chip>
          <Chip :selected="dlCat === 'series'" @click="dlCat = 'series'">番剧</Chip>
        </div>
        <div class="t-body-s muted">磁力链接下载快慢要看分享的人多不多，有的可能很慢或者下不动。</div>
        <div class="foot">
          <Button variant="text" @click="dlLink = null">取消</Button>
          <Button :disabled="dlBusy || !dlName.trim()" @click="doDownload">{{ dlBusy ? '正在交给盒子…' : '开始下载' }}</Button>
        </div>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.bar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 4px; padding: calc(8px + var(--safe-top)) 8px 8px 4px; background: var(--md-sys-color-surface); }
.field { flex: 1; min-width: 0; display: flex; align-items: center; height: 56px; padding: 0 4px 0 20px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); }
.field:focus-within { box-shadow: var(--fc-float), 0 0 0 2px var(--md-sys-color-primary); }
input[type='search'] { flex: 1; min-width: 0; height: 100%; border: 0; outline: 0; background: transparent; color: var(--md-sys-color-on-surface); }
input::placeholder { color: var(--md-sys-color-on-surface-variant); }
input::-webkit-search-cancel-button { display: none; }

.wrap { padding: 4px 16px 32px; max-width: 900px; margin: 0 auto; }
.hhead { display: flex; justify-content: space-between; align-items: center; margin: 8px 0 12px; }
.clear { color: var(--md-sys-color-primary); padding: 6px 10px; border-radius: var(--md-shape-md); }
.chips { display: flex; flex-wrap: wrap; gap: 8px; }
.chips.scroll { flex-wrap: nowrap; overflow-x: auto; scrollbar-width: none; margin: 0 -16px; padding: 4px 16px 4px; scroll-margin-top: 80px; }
.chips.scroll::-webkit-scrollbar { display: none; }
.tip { margin-top: 16px; padding: 16px; line-height: 1.6; color: var(--md-sys-color-on-surface-variant); }
.state { display: flex; flex-direction: column; align-items: center; gap: 10px; margin-top: 12px; padding: 40px 20px; text-align: center; color: var(--md-sys-color-on-surface-variant); }
.summary { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin: 8px 4px 6px; }
.moreing { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }

.list { overflow: hidden; }
.list > * + * { border-top: 1px solid var(--md-sys-color-surface-container-high); }
.morebtn { display: flex; align-items: center; justify-content: center; gap: 4px; width: 100%; height: 48px; color: var(--md-sys-color-primary); }

/* 盒子里已经有的 */
.lrow { display: flex; align-items: center; gap: 14px; padding: 10px 14px; color: inherit; }
.grow { flex: 1; min-width: 0; }
.lthumb { flex: none; width: 40px; aspect-ratio: 2 / 3; border-radius: 8px; overflow: hidden; display: flex; align-items: center; justify-content: center; background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }
.lthumb img { width: 100%; height: 100%; object-fit: cover; }
.lplay { flex: none; display: flex; align-items: center; justify-content: center; width: 40px; height: 40px; border-radius: 12px; background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }

.form { display: flex; flex-direction: column; gap: 10px; }
.textin { width: 100%; box-sizing: border-box; height: 48px; padding: 0 14px; border-radius: 12px; border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); outline: none; }
.textin:focus { border-color: var(--md-sys-color-primary); box-shadow: 0 0 0 1px var(--md-sys-color-primary); }
.foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
</style>
