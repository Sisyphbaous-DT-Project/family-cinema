<script setup lang="ts">
// 文件管理器：浏览整个硬盘、新建文件夹、改名、删除（沿用旧版三道保护：
// 受保护目录不可删 / 下载中的路径不可删 / 非空目录需二次确认）。
// 对应旧版 bindFs/loadFs/renderFs/fsDelete。
import { computed, onMounted, ref } from 'vue'
import { fsDelete, fsList, fsMkdir, fsRename, type FsEntry } from '../../api/manage'
import { toast } from '../../composables/snackbar'
import TopAppBar from '../../components/m3/TopAppBar.vue'
import Button from '../../components/m3/Button.vue'
import Icon from '../../components/m3/Icon.vue'
import IconButton from '../../components/m3/IconButton.vue'
import BottomSheet from '../../components/m3/BottomSheet.vue'

const path = ref('')
const root = ref('')
const entries = ref<FsEntry[]>([])
const error = ref('')
const sel = ref<Set<string>>(new Set())
let selMap = new Map<string, FsEntry>()

async function load(p: string) {
  error.value = ''
  sel.value = new Set()
  selMap = new Map()
  try {
    const d = await fsList(p)
    path.value = d.path
    root.value = d.root
    entries.value = d.entries ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : '目录读取失败'
  }
}
onMounted(() => load(''))

// 面包屑
const crumbs = computed(() => {
  const rel = path.value === root.value ? '' : path.value.slice(root.value.length).replace(/^[\\/]/, '')
  const out: { name: string; p: string }[] = [{ name: '硬盘', p: '' }]
  let acc = ''
  for (const seg of rel.split(/[\\/]/).filter(Boolean)) {
    acc += '/' + seg
    out.push({ name: seg, p: root.value + acc })
  }
  return out
})

function toggleSel(e: FsEntry, on: boolean) {
  const s = new Set(sel.value)
  if (on) {
    s.add(e.path)
    selMap.set(e.path, e)
  } else {
    s.delete(e.path)
    selMap.delete(e.path)
  }
  sel.value = s
}
const selList = computed(() => [...sel.value].map((p) => selMap.get(p)!).filter(Boolean))

function fmtTime(ts: number) {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  const p = (v: number) => String(v).padStart(2, '0')
  return `${d.getMonth() + 1}月${d.getDate()}日 ${p(d.getHours())}:${p(d.getMinutes())}`
}

// ---------- 新建文件夹 ----------
const mkOpen = ref(false)
const mkName = ref('')
async function doMkdir() {
  const name = mkName.value.trim()
  if (!name) return
  try {
    const d = await fsMkdir(path.value, name)
    if (d.ok) {
      toast('已创建')
      mkOpen.value = false
      mkName.value = ''
      load(path.value)
    } else toast('创建失败')
  } catch {
    toast('创建失败')
  }
}

// ---------- 改名 ----------
const rnOpen = ref(false)
const rnName = ref('')
function openRename() {
  const e = selList.value[0]
  if (!e) return
  rnName.value = e.isDir ? e.name : e.name.replace(/\.[^.]+$/, '')
  rnOpen.value = true
}
async function doRename() {
  const e = selList.value[0]
  const name = rnName.value.trim()
  if (!e || !name) return
  try {
    const d = await fsRename(e.path, name)
    if (d.ok) {
      toast('已改名')
      rnOpen.value = false
      load(path.value)
    } else toast('改名失败')
  } catch (err) {
    toast(String(err instanceof Error ? err.message : '改名失败'))
  }
}

// ---------- 删除（逐个删，非空目录二次确认） ----------
const delOpen = ref(false)
const delBusy = ref(false)
async function doDelete() {
  delBusy.value = true
  let freed = 0
  let failed = 0
  for (const e of selList.value) {
    try {
      const d = await fsDelete(e.path, false)
      if (d.needConfirm) {
        // 非空目录：逐个问
        if (window.confirm(`「${e.name}」不是空文件夹，里面有 ${d.dirFiles} 个文件共 ${d.dirSizeHuman}。确认全部删除？`)) {
          const d2 = await fsDelete(e.path, true)
          if (d2.ok) freed += d2.freed || 0
          else failed++
        }
      } else if (d.ok) freed += d.freed || 0
      else failed++
    } catch {
      failed++
    }
  }
  delBusy.value = false
  delOpen.value = false
  toast(failed ? `部分删除失败（${failed} 项）` : '已删除，释放 ' + fmtSize(freed))
  load(path.value)
}
function fmtSize(n: number) {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(0) + ' MB'
  return Math.max(1, Math.round(n / 1024)) + ' KB'
}
</script>

<template>
  <div class="page">
    <TopAppBar title="文件管理" back>
      <template #actions>
        <IconButton icon="refresh" label="刷新" @click="load(path)" />
      </template>
    </TopAppBar>

    <div class="wrap">
      <!-- 面包屑 -->
      <div class="crumbs t-body-m">
        <template v-for="(c, i) in crumbs" :key="c.p || 'root'">
          <span v-if="i" class="sep">/</span>
          <button v-ripple class="crumb" type="button" @click="load(c.p)">{{ c.name }}</button>
        </template>
      </div>

      <!-- 工具条 -->
      <div class="tools">
        <Button variant="tonal" small @click="mkOpen = true; mkName = ''">新建文件夹</Button>
        <Button variant="tonal" small :disabled="sel.size !== 1" @click="openRename">改名</Button>
        <Button variant="text" small danger :disabled="!sel.size" @click="delOpen = true">删除{{ sel.size ? `（${sel.size}）` : '' }}</Button>
      </div>

      <div v-if="error" class="state">
        <Icon name="error" :size="40" />
        <div class="muted">{{ error }}</div>
        <Button variant="tonal" @click="load(path)">再试一次</Button>
      </div>
      <div v-else-if="!entries.length" class="state">
        <Icon name="folder" :size="48" />
        <div class="muted">空文件夹</div>
      </div>

      <div v-else class="list">
        <div v-for="e in entries" :key="e.path" class="frow" :class="{ on: sel.has(e.path) }">
          <label class="cb" @click.stop>
            <input type="checkbox" :checked="sel.has(e.path)" @change="toggleSel(e, ($event.target as HTMLInputElement).checked)" />
          </label>
          <Icon :name="e.isDir ? 'folder' : e.video ? 'movie' : 'list'" :size="22" class="ficon" />
          <button class="grow fname" type="button" @click="e.isDir ? load(e.path) : toggleSel(e, !sel.has(e.path))">
            <span class="t-body-m clamp-1">
              {{ e.name }}
              <span v-if="e.protected" class="badge">受保护</span>
              <span v-else-if="e.inUse" class="badge use">下载中</span>
            </span>
            <span class="t-body-s muted">{{ e.isDir ? '文件夹' : e.sizeHuman }} · {{ fmtTime(e.modified) }}</span>
          </button>
          <Button v-if="e.isDir" variant="text" small @click="load(e.path)">打开</Button>
        </div>
      </div>
    </div>

    <!-- 新建文件夹 -->
    <BottomSheet v-model="mkOpen" title="新建文件夹">
      <div class="form">
        <input v-model="mkName" class="textin" placeholder="文件夹名称" @keyup.enter="doMkdir" />
        <div class="foot">
          <Button variant="text" @click="mkOpen = false">取消</Button>
          <Button @click="doMkdir">创建</Button>
        </div>
      </div>
    </BottomSheet>

    <!-- 改名 -->
    <BottomSheet v-model="rnOpen" title="改名">
      <div class="form">
        <input v-model="rnName" class="textin" placeholder="新名称" @keyup.enter="doRename" />
        <div class="foot">
          <Button variant="text" @click="rnOpen = false">取消</Button>
          <Button @click="doRename">保存</Button>
        </div>
      </div>
    </BottomSheet>

    <!-- 删除确认 -->
    <BottomSheet v-model="delOpen" title="删除文件">
      <div class="form">
        <p class="t-body-m">确认删除选中的 {{ sel.size }} 项？删除后无法恢复。</p>
        <div class="foot">
          <Button variant="text" @click="delOpen = false">取消</Button>
          <Button danger :disabled="delBusy" @click="doDelete">{{ delBusy ? '删除中…' : '删除' }}</Button>
        </div>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.wrap { padding: 8px 16px 32px; max-width: 900px; margin: 0 auto; }
.crumbs { display: flex; flex-wrap: wrap; align-items: center; gap: 2px; margin-bottom: 8px; }
.crumb { padding: 6px 8px; border-radius: var(--md-shape-sm); color: var(--md-sys-color-primary); }
.sep { color: var(--md-sys-color-on-surface-variant); }
.tools { display: flex; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 48px 16px; color: var(--md-sys-color-on-surface-variant); }
.list { display: flex; flex-direction: column; gap: 2px; }
.frow { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: var(--md-shape-md); }
.frow.on { background: var(--md-sys-color-secondary-container); }
.cb { display: flex; padding: 8px; }
.cb input { width: 18px; height: 18px; accent-color: var(--md-sys-color-primary); }
.ficon { color: var(--md-sys-color-on-surface-variant); flex-shrink: 0; }
.fname { flex: 1; min-width: 0; display: flex; flex-direction: column; align-items: flex-start; text-align: left; gap: 2px; padding: 4px; }
.badge { font-size: 11px; padding: 1px 6px; border-radius: 4px; background: var(--md-sys-color-surface-container-high); color: var(--md-sys-color-on-surface-variant); margin-left: 6px; }
.badge.use { background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }
.grow { flex: 1; min-width: 0; }
.form { display: flex; flex-direction: column; gap: 12px; }
.textin { width: 100%; box-sizing: border-box; padding: 10px 12px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 15px; }
.foot { display: flex; justify-content: flex-end; gap: 8px; }
</style>
