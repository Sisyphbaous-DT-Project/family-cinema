<script setup lang="ts">
// 本地片库详情页的选集区（为中老年人设计：字大、直接写字、不靠小图标）：
//  - 每集后面直接写缓存状态：第3集（已缓存）/（下载中）/（排队中）/（已暂停）
//  - 「缓存视频」「删除缓存」两个多选模式：选中的集前面有醒目的实心勾
//      缓存：已缓存/排队中/下载中的变灰不能选，点一下会说明原因；暂停的可以选（接着下）
//      删除：只列出缓存过的集（没下完的要 App 1.3 起能取消才列出来）
//  - 上方一条状态说明：正在下载哪一集、还有几集在排队
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { epLabel, fmtSize, type LocalItem } from '../api/local'
import { isWatched, progRatio } from '../composables/progress'
import { cacheState, cancelCache, deleteCache } from '../composables/cache'
import { cacheLocalEp, STATE_TEXT } from '../composables/localCache'
import { canControlDownload, hasBridge } from '../composables/bridge'
import { useFitCells } from '../composables/useFitCells'
import { toast } from '../composables/snackbar'
import Button from './m3/Button.vue'
import Icon from './m3/Icon.vue'
import BottomSheet from './m3/BottomSheet.vue'

const props = defineProps<{
  item: LocalItem
  cur: number // 正在播的集（-1 = 没在播）
  tick: number // 观看进度变了就 +1（进度存在 localStorage 里，不是响应式的，靠它让列表重画）
  canCache: boolean // App 内、在线、不是缓存条目时才能新加缓存
  fromCache: boolean // 从「我的缓存」点进来的条目
}>()
const emit = defineEmits<{ play: [i: number]; removed: [paths: string[]] }>()

const router = useRouter()
const inApp = hasBridge()
const ctl = canControlDownload() // App 1.3 起才能取消下载中/排队中的缓存

type Mode = '' | 'cache' | 'delete'
const mode = ref<Mode>('')
const sel = reactive(new Set<string>()) // 选中的集（按路径记）
const confirming = ref(false)

const eps = computed(() => props.item.episodes)
const kind = (path: string) => cacheState(path).kind
const label = (i: number) => epLabel(eps.value[i], i, eps.value.length, props.fromCache)
const stText = (path: string) => (inApp ? STATE_TEXT[kind(path)] : '')

/** 删除模式里能选的：缓存好的、暂停的；排队中/下载中的要 App 1.3 起能取消才行 */
function deletable(path: string): boolean {
  const k = kind(path)
  if (k === 'done' || k === 'paused') return true
  return ctl && (k === 'running' || k === 'queued')
}
function selectable(path: string): boolean {
  if (mode.value === 'cache') {
    const k = kind(path)
    return k === 'none' || k === 'paused'
  }
  return mode.value === 'delete' && deletable(path)
}

// 删除模式只列出缓存过的集
const shown = computed(() =>
  eps.value.map((e, i) => ({ e, i })).filter(({ e }) => mode.value !== 'delete' || deletable(e.path)),
)
const showCacheBtn = computed(() => inApp && props.canCache)
const showDeleteBtn = computed(() => inApp && eps.value.some((e) => deletable(e.path)))

function enter(m: Mode) {
  sel.clear()
  mode.value = m
}
function exit() {
  sel.clear()
  mode.value = ''
  confirming.value = false
}

function onChip(i: number) {
  const e = eps.value[i]
  if (!mode.value) return emit('play', i)
  if (!selectable(e.path)) {
    // 只有缓存模式会走到这里（删除模式不显示不能选的）
    const s = cacheState(e.path)
    if (s.kind === 'done') toast(`${label(i)}已经缓存好了，没网也能看`)
    else if (s.kind === 'queued') toast(`${label(i)}正在排队，等前面的缓存完就会开始下载`)
    else if (s.kind === 'running') toast(`${label(i)}正在下载中（${s.pct}%）`)
    return
  }
  if (sel.has(e.path)) sel.delete(e.path)
  else sel.add(e.path)
}

const choosable = computed(() => shown.value.filter(({ e }) => selectable(e.path)))
const allOn = computed(() => choosable.value.length > 0 && choosable.value.every(({ e }) => sel.has(e.path)))
function toggleAll() {
  const on = !allOn.value
  for (const { e } of choosable.value) {
    if (on) sel.add(e.path)
    else sel.delete(e.path)
  }
}

// 选中后状态变了、已经不能选的（比如正好下完了）不算
const picked = computed(() => shown.value.filter(({ e }) => sel.has(e.path) && selectable(e.path)))
const pickedSize = computed(() => picked.value.reduce((s, { e }) => s + (e.size || 0), 0))
const pickedUnfinished = computed(() => picked.value.filter(({ e }) => kind(e.path) !== 'done').length)

function commit() {
  if (!picked.value.length) return
  if (mode.value === 'delete') {
    confirming.value = true
    return
  }
  let n = 0
  for (const { i } of picked.value) if (cacheLocalEp(props.item, i, props.fromCache)) n++
  exit()
  if (n) toast(`已加入缓存：${n} 集，会按顺序一集一集下载`)
}

function confirmDelete() {
  const paths = picked.value.map(({ e }) => e.path)
  for (const p of paths) {
    if (kind(p) === 'done') deleteCache(p)
    else cancelCache(p) // 没下完的：停下来并清掉已下载的部分
  }
  exit()
  toast(`已删除 ${paths.length} 集的缓存`)
  emit('removed', paths)
}

// ---------- 状态说明条 ----------
const busyLines = computed(() => {
  let running: { i: number; pct: number } | null = null
  let queued = 0
  let paused = 0
  eps.value.forEach((e, i) => {
    const s = cacheState(e.path)
    if (s.kind === 'running' && !running) running = { i, pct: s.pct }
    else if (s.kind === 'queued') queued++
    else if (s.kind === 'paused') paused++
  })
  const r = running as { i: number; pct: number } | null
  const out: string[] = []
  if (r) out.push(`正在下载：${label(r.i)}（${r.pct}%）`)
  if (queued) out.push(`${r ? '还有' : '有'} ${queued} 集在排队，等前面的缓存完就会自动开始下载`)
  if (paused) out.push(`有 ${paused} 集暂停了` + (props.canCache ? '，点「缓存视频」选上就能接着下' : ''))
  return out
})

// ---------- 方块 ----------
const seen = (i: number) => !mode.value && i !== props.cur && isWatched(eps.value[i].path)
function chipClass(path: string, i: number) {
  const k = kind(path)
  return {
    on: !mode.value && i === props.cur,
    watched: !mode.value && isWatched(path),
    filling: k === 'running' || k === 'paused',
    picked: !!mode.value && sel.has(path),
    off: mode.value === 'cache' && !selectable(path),
  }
}
function fillPct(path: string): number {
  const s = cacheState(path)
  return s.kind === 'running' || s.kind === 'paused' ? s.pct : 0
}

// 方块宽度按每格实际内容（集名 + 状态字 + 勾选圈）来量。
// 状态签名只在某一集状态变了时才变，下载进度每秒刷新不会触发重新测量；选中/取消选中不改变宽度，不用量
const grid = ref<HTMLElement | null>(null)
const sig = computed(() => eps.value.map((e) => kind(e.path)).join(','))
const cellMin = useFitCells(grid, { base: 76, deps: [() => eps.value.length, mode, sig] })
</script>

<template>
  <div class="area">
    <!-- 状态说明：正在下载哪一集、还有几集在排队 -->
    <div v-if="inApp && busyLines.length" class="banner">
      <Icon name="download" :size="22" />
      <div class="grow">
        <div v-for="l in busyLines" :key="l" class="t-body-l">{{ l }}</div>
      </div>
      <Button variant="text" small @click="router.push('/cache')">查看</Button>
    </div>

    <!-- 选集卡：标题 + 两个大按钮（或多选时的说明） + 选集方块 -->
    <div class="fc-card epcard">
      <template v-if="!mode">
        <div class="labelrow">
          <span class="t-title-m">选集</span>
          <span class="t-body-m muted">共 {{ eps.length }} 集</span>
        </div>
        <div v-if="showCacheBtn || showDeleteBtn" class="actrow">
          <Button v-if="showCacheBtn" variant="tonal" @click="enter('cache')"><Icon name="download" :size="20" />缓存视频</Button>
          <Button v-if="showDeleteBtn" variant="tonal" danger @click="enter('delete')"><Icon name="delete" :size="20" />删除缓存</Button>
        </div>
      </template>
      <template v-else>
        <div class="selhead">
          <div class="t-title-m">{{ mode === 'cache' ? '选择要缓存的集' : '选择要删除缓存的集' }}</div>
          <div class="t-body-m muted">
            {{ mode === 'cache' ? '点一下选中，可以选好几集。灰色的集已经缓存过或正在缓存。' : '这里只列出了缓存过的集。点一下选中，可以选好几集。' }}
          </div>
        </div>
        <div class="selacts">
          <Button variant="outlined" small :disabled="!choosable.length" @click="toggleAll">{{ allOn ? '全不选' : '全选' }}</Button>
          <Button variant="text" small @click="exit">退出</Button>
        </div>
      </template>

      <div ref="grid" class="eps" :data-tick="tick" :style="{ '--cell-min': cellMin + 'px' }">
        <button
          v-for="{ e, i } in shown"
          :key="e.path"
          v-ripple
          class="ep"
          :class="chipClass(e.path, i)"
          :style="{ '--p': fillPct(e.path) + '%' }"
          type="button"
          @click="onChip(i)"
        >
          <span v-if="mode && selectable(e.path)" class="tick" :class="{ on: sel.has(e.path) }">
            <Icon v-if="sel.has(e.path)" name="check" :size="16" />
          </span>
          <span class="lab">{{ label(i) }}<span v-if="stText(e.path)" class="st" :class="'st-' + kind(e.path)">（{{ stText(e.path) }}）</span></span>
          <Icon v-if="seen(i)" name="check" :size="12" class="seen" />
          <i v-if="!mode && progRatio(e.path) > 0 && !isWatched(e.path)" class="wbar"><i :style="{ width: Math.round(progRatio(e.path) * 100) + '%' }" /></i>
        </button>
      </div>
    </div>

    <!-- 多选模式的底部操作条：一直贴在屏幕底部，进入多选时从下面弹上来 -->
    <Transition name="bar-rise">
      <div v-if="mode" class="selbar">
        <div class="grow t-body-l">
          已选 <b>{{ picked.length }}</b> 集<span v-if="picked.length" class="muted"> · 共 {{ fmtSize(pickedSize) }}</span>
        </div>
        <Button variant="filled" :danger="mode === 'delete'" :disabled="!picked.length" @click="commit">
          {{ mode === 'cache' ? '开始缓存' : '删除' }}
        </Button>
      </div>
    </Transition>

    <BottomSheet :model-value="confirming" title="删除缓存" @update:model-value="confirming = false">
      <p class="del-text t-body-l">
        删除选中的 {{ picked.length }} 集的缓存？手机上下载的文件会被删掉，想看的时候可以重新缓存。
        <template v-if="pickedUnfinished">其中 {{ pickedUnfinished }} 集还没下完，会停止下载。</template>
      </p>
      <div class="del-acts">
        <Button variant="text" @click="confirming = false">先不删</Button>
        <Button variant="filled" danger @click="confirmDelete">删除</Button>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.area { display: flex; flex-direction: column; gap: 12px; }
.grow { flex: 1; min-width: 0; }

.banner { display: flex; align-items: center; gap: 12px; padding: 14px 8px 14px 16px; border-radius: var(--fc-radius); background: var(--md-sys-color-tertiary-container); color: var(--md-sys-color-on-tertiary-container); box-shadow: var(--fc-float); }

/* 选集卡（悬浮卡片，里面的小方块不再加阴影） */
.epcard { display: flex; flex-direction: column; gap: 12px; padding: 16px; }
.labelrow { display: flex; align-items: baseline; justify-content: space-between; }
.actrow { display: flex; gap: 8px; }
.actrow > * { flex: 1; }
.selhead { display: flex; flex-direction: column; gap: 4px; }
.selacts { display: flex; gap: 8px; }

/* 每格最小宽度由 useFitCells 按集名实际宽度算（--cell-min）；写了状态字后可能一行只放得下一个，也照放 */
.eps { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(var(--cell-min, 76px), 100%), 1fr)); gap: 8px; }
.ep {
  --p: 0%;
  position: relative;
  display: flex; align-items: center; justify-content: center; gap: 5px;
  min-height: 44px; padding: 6px 8px; overflow: hidden; white-space: nowrap;
  border-radius: var(--md-shape-md);
  background-color: var(--md-sys-color-surface-container-high);
  color: var(--md-sys-color-on-surface);
  font-size: 16px; line-height: 22px; font-weight: 500;
  transition: background-color var(--md-dur-medium) var(--md-ease-standard), opacity var(--md-dur-medium) linear;
}
.ep .lab { overflow: hidden; text-overflow: ellipsis; }
.ep.watched:not(.on) { color: var(--md-sys-color-on-surface-variant); }
.ep.on { background-color: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }
/* 缓存进度：从左到右铺底色 */
.ep.filling { background-image: linear-gradient(90deg, var(--md-sys-color-tertiary-container) var(--p), transparent var(--p)); }
/* 缓存模式里不能选的：变灰，点了会提示原因 */
.ep.off { opacity: 0.45; }
/* 选中：底色加深 + 粗边框 */
.ep.picked { background-color: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); box-shadow: inset 0 0 0 2px var(--md-sys-color-primary); }

/* 状态字 */
.st-done { color: var(--md-sys-color-primary); }
.st-running { color: var(--md-sys-color-tertiary); }
.st-queued { color: var(--md-sys-color-on-surface-variant); }
.st-paused { color: var(--md-sys-color-outline); }

/* 勾选圈：没选是空心圈，选中是实心圆 + 白勾 */
.tick { flex: none; box-sizing: border-box; display: flex; align-items: center; justify-content: center; width: 20px; height: 20px; border-radius: 50%; border: 2px solid var(--md-sys-color-outline); }
.tick.on { background: var(--md-sys-color-primary); border-color: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }

/* 看完小勾（左上）；看到一半的细进度线 */
.seen { position: absolute; top: 3px; left: 3px; color: var(--md-sys-color-primary); }
.wbar { position: absolute; left: 6px; right: 6px; bottom: 3px; height: 2px; border-radius: 1px; background: var(--md-sys-color-surface-container-highest); }
.wbar i { display: block; height: 100%; border-radius: 1px; background: var(--md-sys-color-primary); }

/* 底部操作条：和上面的卡片一样是圆角矩形；列表长时悬浮在离屏幕底部一点的位置 */
.selbar {
  position: sticky; bottom: calc(12px + env(safe-area-inset-bottom)); z-index: 5;
  display: flex; align-items: center; gap: 12px;
  margin-top: 4px; padding: 10px 10px 10px 18px;
  border-radius: var(--fc-radius);
  background: var(--fc-card);
  box-shadow: 0 2px 6px rgb(0 0 0 / 0.08), 0 8px 28px rgb(0 0 0 / 0.14); /* 浮在选集上面，阴影比普通卡片重一点 */
}
.del-text { margin: 0 0 8px; color: var(--md-sys-color-on-surface-variant); }
.del-acts { display: flex; justify-content: flex-end; gap: 8px; }
</style>
