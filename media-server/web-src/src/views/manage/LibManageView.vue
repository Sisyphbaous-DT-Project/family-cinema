<script setup lang="ts">
// 片库管理：统计、改名、换封面、删除（含强制删除二次确认）、补封面、服务端下载缓存清理、清任务记录。
// 对应旧版管理页的"条目"部分（loadMng/renderMngTable/openEdit/openDelete/loadCache）。
import { computed, onMounted, ref } from 'vue'
import {
  coversStatus,
  deleteItem,
  deleteServerCache,
  fetchManageStats,
  fetchServerCache,
  renameItem,
  setItemCover,
  startCoversFetch,
  type CacheEntry,
  type ManageItem,
} from '../../api/manage'
import { coverURL } from '../../api/local'
import { toast } from '../../composables/snackbar'
import TopAppBar from '../../components/m3/TopAppBar.vue'
import Button from '../../components/m3/Button.vue'
import Icon from '../../components/m3/Icon.vue'
import BottomSheet from '../../components/m3/BottomSheet.vue'
import SegmentedButton from '../../components/m3/SegmentedButton.vue'

const stats = ref<{ total: number; movie: number; series: number; totalSizeH: string; missingCover: number; taskCount: number } | null>(null)
const items = ref<ManageItem[]>([])
const cacheEntries = ref<CacheEntry[]>([])
const cacheMeta = ref('')
const filter = ref('')
const cat = ref('all')
const error = ref('')
const coverJob = ref('')

async function load() {
  error.value = ''
  try {
    const d = await fetchManageStats()
    items.value = d.items ?? []
    const byId: Record<string, number> = {}
    ;(d.categories ?? []).forEach((c) => (byId[c.id] = c.count))
    stats.value = {
      total: byId.all ?? 0,
      movie: byId.movie ?? 0,
      series: byId.series ?? 0,
      totalSizeH: d.totalSizeH || '0',
      missingCover: d.missingCover || 0,
      taskCount: d.taskCount || 0,
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
  }
  loadCache()
}

async function loadCache() {
  try {
    const d = await fetchServerCache()
    cacheEntries.value = d.entries ?? []
    cacheMeta.value = cacheEntries.value.length
      ? `${cacheEntries.value.length} 项 · 共占用 ${d.totalHuman || '0'} · ${d.dir || ''}`
      : '缓存目录：' + (d.dir || '')
  } catch {
    cacheMeta.value = '无法读取缓存信息'
  }
}
onMounted(load)

const catName = (c: string) => (c === 'series' ? '番剧' : '电影电视剧')
const shown = computed(() => {
  const kw = filter.value.trim().toLowerCase()
  return items.value.filter((it) => {
    if (cat.value !== 'all' && it.category !== cat.value) return false
    if (!kw) return true
    return (it.title || '').toLowerCase().includes(kw) || (it.rawTitle || '').toLowerCase().includes(kw)
  })
})

// ---------- 编辑（改名 + 换封面） ----------
const edItem = ref<ManageItem | null>(null)
const edName = ref('')
const edCoverUrl = ref('')
const edCoverData = ref('')
const edErr = ref('')
const edBusy = ref(false)

function openEdit(it: ManageItem) {
  edItem.value = it
  edName.value = it.title
  edCoverUrl.value = ''
  edCoverData.value = ''
  edErr.value = ''
}
function pickCover(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f) return
  if (f.size > 10 * 1024 * 1024) {
    edErr.value = '图片过大（上限 10MB）'
    return
  }
  const fr = new FileReader()
  fr.onload = () => {
    edCoverData.value = String(fr.result)
    edCoverUrl.value = ''
  }
  fr.readAsDataURL(f)
}
async function saveEdit() {
  const it = edItem.value
  if (!it) return
  const name = edName.value.trim()
  const cover = edCoverData.value || edCoverUrl.value.trim()
  if (!name) {
    edErr.value = '名称不能为空'
    return
  }
  edBusy.value = true
  edErr.value = ''
  try {
    // 必须按顺序：先换封面再改名。改名会把文件夹搬走，并行时封面写到旧路径，
    // 后端会在旧位置重建一个只含封面的空文件夹
    if (cover) await setItemCover(it.id, cover)
    if (name !== it.title) await renameItem(it.id, name)
    edItem.value = null
    toast('已保存')
    setTimeout(load, 800)
  } catch (e) {
    edErr.value = String(e instanceof Error ? e.message : e).slice(0, 160)
  } finally {
    edBusy.value = false
  }
}

// ---------- 删除（需要强制删除时才显示 force 开关） ----------
const delItem = ref<ManageItem | null>(null)
const delForce = ref(false)
const delForceShow = ref(false)
const delErr = ref('')
const delBusy = ref(false)

async function confirmDelete() {
  const it = delItem.value
  if (!it) return
  delBusy.value = true
  delErr.value = ''
  try {
    const res = await deleteItem(it.id, delForce.value)
    delItem.value = null
    toast('已删除，释放 ' + (res.freedHuman || ''))
    setTimeout(load, 800)
  } catch (e) {
    const msg = String(e instanceof Error ? e.message : e)
    delErr.value = msg.slice(0, 200)
    // 后端提示需要强制删除时，把选项显出来
    if (msg.includes('强制删除')) delForceShow.value = true
  } finally {
    delBusy.value = false
  }
}

// ---------- 补封面 ----------
async function fetchCovers() {
  try {
    await startCoversFetch()
    toast('开始抓取封面，完成后会自动刷新')
    pollCovers()
  } catch (e) {
    toast(String(e instanceof Error ? e.message : '启动失败').slice(0, 80))
  }
}
function pollCovers() {
  const t = setInterval(async () => {
    try {
      const st = await coversStatus()
      if (st.running) {
        coverJob.value = `正在抓取封面 ${st.done} / ${st.total}，已找到 ${st.found} 张`
        return
      }
      clearInterval(t)
      coverJob.value = ''
      toast(st.total ? `封面抓取完成：${st.found} / ${st.total} 张` : '所有条目都已有封面')
      load()
    } catch {
      clearInterval(t)
      coverJob.value = ''
    }
  }, 2000)
}

// ---------- 服务端下载缓存 ----------
const delCacheEntry = ref<CacheEntry | null>(null)
async function doDelCache() {
  const e = delCacheEntry.value
  if (!e) return
  try {
    const d = await deleteServerCache(e.name)
    toast('已释放 ' + (d.freedHuman || '0'))
    delCacheEntry.value = null
    loadCache()
  } catch (err) {
    toast(String(err instanceof Error ? err.message : '删除失败'))
  }
}
const clearingAll = ref(false)
async function clearAllCache() {
  clearingAll.value = true
  try {
    const d = await deleteServerCache(undefined, true)
    toast(`已清理 ${d.removed || ''}，释放 ${d.freedHuman || '0'}`)
    clearingAll.value = false
    loadCache()
  } catch (err) {
    clearingAll.value = false
    toast(String(err instanceof Error ? err.message : '清理失败'))
  }
}
</script>

<template>
  <div class="page">
    <TopAppBar title="片库管理" back />

    <div class="wrap">
      <div v-if="error" class="state">
        <Icon name="error" :size="40" />
        <div class="muted">{{ error }}</div>
        <Button variant="tonal" @click="load">再试一次</Button>
      </div>

      <template v-else>
        <!-- 统计 -->
        <div v-if="stats" class="stats">
          <div class="stat"><div class="slabel">资源总数</div><div class="sval">{{ stats.total }}<small>部</small></div></div>
          <div class="stat"><div class="slabel">电影电视剧</div><div class="sval">{{ stats.movie }}<small>部</small></div></div>
          <div class="stat"><div class="slabel">番剧</div><div class="sval">{{ stats.series }}<small>部</small></div></div>
          <div class="stat"><div class="slabel">磁盘占用</div><div class="sval">{{ stats.totalSizeH }}</div></div>
          <div class="stat" :class="{ warn: stats.missingCover > 0 }"><div class="slabel">缺少封面</div><div class="sval">{{ stats.missingCover }}<small>部</small></div></div>
        </div>
        <div v-if="coverJob" class="t-body-s muted coverjob">{{ coverJob }}</div>

        <div class="toolbar">
          <SegmentedButton v-model="cat" :options="[{ value: 'all', label: '全部' }, { value: 'movie', label: '电影电视剧' }, { value: 'series', label: '番剧' }]" />
          <input v-model="filter" class="textin grow" type="search" placeholder="筛选名称" />
          <Button variant="tonal" small @click="fetchCovers">补封面</Button>
        </div>

        <!-- 条目列表 -->
        <div class="list">
          <div v-for="it in shown" :key="it.id" class="mrow">
            <div class="mcover" :class="{ none: !it.cover }">
              <img v-if="it.cover" :src="coverURL(it.cover)" alt="" loading="lazy" />
              <Icon v-else name="tv" :size="20" />
            </div>
            <div class="grow">
              <div class="t-body-m clamp-1">{{ it.title }}</div>
              <div class="t-body-s muted clamp-1">
                {{ catName(it.category) }} · {{ it.episodes }} 个文件 · {{ it.sizeHuman }}
                <template v-if="it.rawTitle && it.rawTitle !== it.title"> · {{ it.rawTitle }}</template>
              </div>
            </div>
            <button v-ripple class="icbtn" type="button" aria-label="编辑" @click="openEdit(it)"><Icon name="edit" :size="20" /></button>
            <button v-ripple class="icbtn danger" type="button" aria-label="删除" @click="delItem = it; delForce = false; delForceShow = false; delErr = ''"><Icon name="delete" :size="20" /></button>
          </div>
          <div v-if="!shown.length" class="state"><div class="muted">没有匹配的条目</div></div>
        </div>

        <!-- 服务端下载缓存 -->
        <h2 class="t-title-s sec">下载缓存（盒子上未完成的 BT 数据）</h2>
        <div class="t-body-s muted cachemeta">{{ cacheMeta }}</div>
        <div class="list">
          <div v-for="e in cacheEntries" :key="e.name" class="mrow">
            <Icon name="folder" :size="20" class="muted" />
            <div class="grow">
              <div class="t-body-m clamp-1">{{ e.title || e.name }}</div>
              <div class="t-body-s muted">
                {{ e.sizeHuman }}
                <template v-if="e.inUse"> · 正在下载{{ e.taskName ? '：' + e.taskName : '' }}</template>
                <template v-else-if="e.partial"> · 未下载完</template>
              </div>
            </div>
            <button v-ripple class="icbtn danger" type="button" :disabled="e.inUse" :title="e.inUse ? '正在下载，请先停止任务' : '删除这份缓存'" aria-label="删除缓存" @click="delCacheEntry = e"><Icon name="delete" :size="20" /></button>
          </div>
          <div class="acts" v-if="cacheEntries.length">
            <Button variant="text" small danger :disabled="clearingAll" @click="clearAllCache">{{ clearingAll ? '清理中…' : '清空全部缓存' }}</Button>
          </div>
        </div>
      </template>
    </div>

    <!-- 编辑弹层 -->
    <BottomSheet :model-value="!!edItem" title="编辑条目" @update:model-value="edItem = null">
      <div class="form">
        <div class="t-body-s muted">{{ edItem ? catName(edItem.category) + ' · ' + edItem.episodes + ' 个文件 · ' + edItem.sizeHuman : '' }}</div>
        <input v-model="edName" class="textin" placeholder="名称" />
        <div class="coverrow">
          <label class="coverbtn">
            <input type="file" accept="image/*" hidden @change="pickCover" />
            <Icon name="image" :size="20" />
            <span class="t-label-l">{{ edCoverData ? '已选择图片' : '上传封面' }}</span>
          </label>
          <img
            v-if="edCoverData || edItem?.cover"
            :src="edCoverData || (edItem?.cover ? coverURL(edItem.cover) : '')"
            class="coverprev"
            alt="封面预览"
          />
        </div>
        <input v-model="edCoverUrl" class="textin" placeholder="或粘贴封面图片链接" autocapitalize="off" />
        <p v-if="edErr" class="errtext t-body-s">{{ edErr }}</p>
        <div class="foot">
          <Button variant="text" @click="edItem = null">取消</Button>
          <Button :disabled="edBusy" @click="saveEdit">{{ edBusy ? '保存中…' : '保存' }}</Button>
        </div>
      </div>
    </BottomSheet>

    <!-- 删除确认 -->
    <BottomSheet :model-value="!!delItem" title="删除条目" @update:model-value="delItem = null">
      <div class="form">
        <p class="t-body-m">
          将从硬盘删除 <b>{{ delItem?.title }}</b>（{{ delItem?.episodes }} 个文件，{{ delItem?.sizeHuman }}）。此操作不可撤销。
        </p>
        <label v-if="delForceShow" class="forcerow t-body-m">
          <input v-model="delForce" type="checkbox" />
          强制删除（条目里有非视频文件）
        </label>
        <p v-if="delErr" class="errtext t-body-s">{{ delErr }}</p>
        <div class="foot">
          <Button variant="text" @click="delItem = null">取消</Button>
          <Button danger :disabled="delBusy" @click="confirmDelete">{{ delBusy ? '删除中…' : '删除' }}</Button>
        </div>
      </div>
    </BottomSheet>

    <!-- 缓存删除确认 -->
    <BottomSheet :model-value="!!delCacheEntry" title="删除缓存" @update:model-value="delCacheEntry = null">
      <div class="form">
        <p class="t-body-m">删除「{{ delCacheEntry?.title || delCacheEntry?.name }}」的缓存（{{ delCacheEntry?.sizeHuman }}）？已下载的进度会一并丢失，需要重新下载。</p>
        <div class="foot">
          <Button variant="text" @click="delCacheEntry = null">取消</Button>
          <Button danger @click="doDelCache">删除</Button>
        </div>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.wrap { padding: 8px 16px 32px; max-width: 900px; margin: 0 auto; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 48px 16px; color: var(--md-sys-color-on-surface-variant); }
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(96px, 1fr)); gap: 8px; margin-bottom: 16px; }
.stat { padding: 14px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); }
.stat.warn { background: var(--md-sys-color-error-container, #ffdad6); }
.slabel { font-size: 12px; color: var(--md-sys-color-on-surface-variant); }
.sval { font-size: 20px; font-weight: 500; margin-top: 4px; }
.sval small { font-size: 12px; font-weight: 400; margin-left: 2px; }
.coverjob { margin: 0 4px 12px; }
.toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.textin { padding: 10px 12px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 15px; min-width: 140px; }
.grow { flex: 1; min-width: 0; }
.list { display: flex; flex-direction: column; gap: 4px; }
.mrow { display: flex; align-items: center; gap: 12px; padding: 12px 14px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); }
.mcover { width: 44px; height: 60px; border-radius: 8px; display: flex; align-items: center; justify-content: center; overflow: hidden; background: rgb(76 175 80 / 0.15); color: var(--md-sys-color-primary); flex-shrink: 0; }
.mcover img { display: block; width: 100%; height: 100%; object-fit: cover; }
.mcover.none { background: var(--md-sys-color-surface-container-high); color: var(--md-sys-color-on-surface-variant); }
.icbtn { width: 40px; height: 40px; border-radius: 50%; display: flex; align-items: center; justify-content: center; color: var(--md-sys-color-on-surface-variant); flex-shrink: 0; }
.icbtn.danger { color: var(--md-sys-color-error, #ba1a1a); }
.icbtn:disabled { opacity: 0.38; }
.sec { margin: 20px 4px 8px; color: var(--md-sys-color-primary); }
.cachemeta { margin: 0 4px 8px; }
.acts { display: flex; justify-content: flex-end; padding: 8px 4px; }
.form { display: flex; flex-direction: column; gap: 12px; }
.form .textin { width: 100%; box-sizing: border-box; }
.coverrow { display: flex; align-items: center; gap: 12px; }
.coverbtn { display: inline-flex; align-items: center; gap: 8px; padding: 10px 16px; border-radius: var(--md-shape-md); background: var(--md-sys-color-secondary-container); cursor: pointer; }
.coverprev { width: 48px; height: 64px; object-fit: cover; border-radius: var(--md-shape-sm); }
.errtext { color: var(--md-sys-color-error, #ba1a1a); margin: 0; }
.foot { display: flex; justify-content: flex-end; gap: 8px; }
.forcerow { display: flex; align-items: center; gap: 8px; }
@media (max-width: 480px) {
  .mrow { gap: 10px; padding: 10px; }
  .mcover { width: 40px; height: 56px; }
}
</style>
