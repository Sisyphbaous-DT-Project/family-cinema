<script setup lang="ts">
// 上传队列和 XHR 都在模块级 composable 里：离开页面继续传，回来后接着看进度。
import { computed, onMounted, ref } from 'vue'
import { fetchDisk } from '../../api/manage'
import { fmtSize } from '../../api/local'
import { toast } from '../../composables/snackbar'
import { enqueueUploads, retryUpload, stopUpload, uploadItems, type UploadItem } from '../../composables/uploads'
import TopAppBar from '../../components/m3/TopAppBar.vue'
import Button from '../../components/m3/Button.vue'
import Icon from '../../components/m3/Icon.vue'
import SegmentedButton from '../../components/m3/SegmentedButton.vue'
import FolderPicker from '../../components/manage/FolderPicker.vue'
import type { FolderItem } from '../../api/manage'

const VIDEO_EXT = /\.(mkv|mp4|avi|mov|m4v|ts|m2ts|webm|wmv|rm|rmvb|flv|mpg|mpeg|iso)$/i

const files = ref<File[]>([])
const err = ref('')
const starting = ref(false)
const busy = computed(() => starting.value)
const diskText = ref('')
const dest = ref<'new' | 'existing'>('new')
const cat = ref<'movie' | 'series'>('movie')
const title = ref('')
const target = ref<FolderItem | null>(null)
const picker = ref<InstanceType<typeof FolderPicker>>()
const rows = uploadItems
const stopping = ref('')

onMounted(async () => {
  try {
    const d = await fetchDisk()
    diskText.value = d && d.freeH ? `硬盘剩余 ${d.freeH} / 共 ${d.totalH}` : ''
  } catch {
    /* 读不到磁盘信息就不显示 */
  }
})

function addFiles(list: FileList | null) {
  if (!list) return
  let added = 0
  let duplicates = 0
  for (const f of Array.from(list)) {
    if (!VIDEO_EXT.test(f.name)) continue
    const duplicate = files.value.some((item) => item.name === f.name && item.size === f.size)
      || rows.some((item) => item.state !== 'done' && item.name === f.name && item.size === f.size)
    if (duplicate) {
      duplicates++
      continue
    }
    files.value.push(f)
    added++
  }
  if (duplicates) toast(`${duplicates} 个文件已在上传列表里，没有重复添加`)
  err.value = added || duplicates ? '' : '只支持视频文件'
}

function removeFile(i: number) {
  if (busy.value) return
  files.value.splice(i, 1)
}

function switchDest(v: string) {
  dest.value = v === 'existing' ? 'existing' : 'new'
  if (v === 'existing') picker.value?.open()
}

async function start() {
  if (busy.value) return
  if (!files.value.length) {
    err.value = '请先选择视频文件'
    return
  }
  if (dest.value === 'new' && !title.value.trim()) {
    err.value = '请填写保存名称'
    return
  }
  if (dest.value === 'existing' && !target.value) {
    err.value = '请选择要并入的条目'
    return
  }
  const need = files.value.reduce((s, f) => s + f.size, 0)
  starting.value = true
  err.value = ''
  try {
    const d = await fetchDisk()
    if (d && d.free && need > d.free) throw new Error(`硬盘空间不足：需要 ${fmtSize(need)}，仅剩 ${d.freeH}`)
    const result = enqueueUploads(files.value, {
      // 并入已有条目时分类跟着那个条目走，进度才会显示在对的页面上
      cat: dest.value === 'existing' && target.value ? target.value.category : cat.value,
      dest: dest.value,
      title: title.value.trim(),
      targetId: dest.value === 'existing' && target.value ? target.value.id : '',
    })
    if (result.duplicates.length) toast(`${result.duplicates.length} 个文件已在上传列表里，没有重复添加`)
    if (result.added) files.value = []
  } catch (e) {
    err.value = String(e instanceof Error ? e.message : '上传失败')
  } finally {
    starting.value = false
  }
}

const rowPct = (row: UploadItem) => row.state === 'done' ? 100 : row.size ? Math.min(99, Math.round(row.loaded * 100 / row.size)) : 0

function rowSub(row: UploadItem): string {
  if (row.state === 'queued') return '等待中'
  if (row.state === 'done') return '已完成 · ' + fmtSize(row.size) + (row.nextTaskId ? ' · 已交给盒子检查声音和字幕' : '')
  if (row.state === 'failed') return '失败：' + row.error
  if (row.state === 'stopped') return '已停止，可以重新上传'
  if (!row.speed) return row.loaded ? `${fmtSize(row.loaded)} / ${fmtSize(row.size)} · 正在计算速度…` : '正在开始上传…'
  const eta = Math.round((row.size - row.loaded) / row.speed)
  const etaText = eta >= 60 ? Math.floor(eta / 60) + ' 分 ' + (eta % 60) + ' 秒' : eta + ' 秒'
  return `${fmtSize(row.loaded)} / ${fmtSize(row.size)} · ${fmtSize(row.speed)}/s · 约剩 ${etaText}`
}

async function stopRow(row: UploadItem) {
  if (stopping.value) return
  const queued = row.state === 'queued'
  stopping.value = row.id
  try {
    const ok = await stopUpload(row.id)
    toast(ok ? (queued ? '已取消排队' : '上传已停止') : '没能停止，可能已经结束')
  } catch (e) {
    toast(e instanceof Error ? e.message : '停止失败')
  } finally {
    stopping.value = ''
  }
}
</script>

<template>
  <div class="page">
    <TopAppBar title="上传视频" back />

    <div class="wrap">
      <div v-if="diskText" class="t-body-s muted disk">{{ diskText }}</div>

      <!-- 选文件 -->
      <label class="drop" :class="{ busy }">
        <input type="file" multiple accept="video/*" hidden :disabled="busy" @change="addFiles(($event.target as HTMLInputElement).files); ($event.target as HTMLInputElement).value = ''" />
        <Icon name="upload" :size="32" />
        <div class="t-body-m">{{ files.length ? `已选 ${files.length} 个文件 · 共 ${fmtSize(files.reduce((s, f) => s + f.size, 0))}` : '点击选择视频文件（可多选）' }}</div>
      </label>

      <div v-if="files.length" class="flist">
        <div v-for="(f, i) in files" :key="i" class="frow">
          <span class="t-body-m clamp-1 grow">{{ f.name }}</span>
          <span class="t-body-s muted">{{ fmtSize(f.size) }}</span>
          <button v-if="!busy" v-ripple class="rm" type="button" aria-label="移除" @click="removeFile(i)"><Icon name="close" :size="16" /></button>
        </div>
      </div>

      <!-- 保存位置 -->
      <div class="group">
        <div class="t-label-l muted glabel">保存位置</div>
        <SegmentedButton :model-value="dest" :options="[{ value: 'new', label: '新建条目' }, { value: 'existing', label: '并入已有' }]" @update:model-value="switchDest" />
        <template v-if="dest === 'new'">
          <SegmentedButton v-model="cat" :options="[{ value: 'movie', label: '电影电视剧' }, { value: 'series', label: '番剧' }]" />
          <input v-model="title" class="textin" placeholder="保存名称（会创建同名文件夹）" />
        </template>
        <FolderPicker v-else ref="picker" v-model="target" />
      </div>

      <p v-if="err" class="errtext t-body-s">{{ err }}</p>

      <!-- 全局上传队列：离开本页也继续传，回来还能看到 -->
      <div v-if="rows.length" class="progs">
        <div v-for="r in rows" :key="r.id" class="prow" :class="{ failed: r.state === 'failed' }">
          <div class="ptop">
            <span class="t-body-m clamp-1 grow">{{ r.name }}</span>
            <span class="t-label-m">{{ rowPct(r) }}%</span>
          </div>
          <div class="bar"><i :style="{ width: rowPct(r) + '%' }" /></div>
          <div class="prow-foot">
            <div class="t-body-s muted grow">{{ rowSub(r) }}</div>
            <Button
              v-if="r.state === 'queued' || r.state === 'uploading'"
              variant="text"
              small
              danger
              :disabled="stopping === r.id"
              @click="stopRow(r)"
            >{{ stopping === r.id ? '停止中…' : '停止' }}</Button>
            <Button
              v-else-if="r.state === 'failed' || r.state === 'stopped'"
              variant="text"
              small
              @click="retryUpload(r.id)"
            >重新上传</Button>
          </div>
        </div>
      </div>

      <Button :disabled="busy || !files.length" @click="start">{{ busy ? '上传中…' : '开始上传' }}</Button>
    </div>
  </div>
</template>

<style scoped>
.wrap { padding: 8px 16px 32px; max-width: 720px; margin: 0 auto; display: flex; flex-direction: column; gap: 16px; }
.disk { margin: 0 4px; }
.drop { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 32px; border: 2px dashed var(--md-sys-color-outline); border-radius: var(--md-shape-lg); cursor: pointer; color: var(--md-sys-color-on-surface-variant); text-align: center; }
.drop.busy { opacity: 0.5; pointer-events: none; }
.flist { display: flex; flex-direction: column; gap: 4px; }
.frow { display: flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); }
.grow { flex: 1; min-width: 0; }
.rm { width: 28px; height: 28px; border-radius: 50%; display: flex; align-items: center; justify-content: center; color: var(--md-sys-color-on-surface-variant); }
.group { display: flex; flex-direction: column; gap: 12px; }
.glabel { margin: 0 4px; }
.textin { padding: 10px 12px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 15px; }
.errtext { color: var(--md-sys-color-error, #ba1a1a); margin: 0; }
.progs { display: flex; flex-direction: column; gap: 10px; }
.prow { display: flex; flex-direction: column; gap: 4px; padding: 12px 14px; border-radius: 16px; background: var(--fc-card); box-shadow: var(--fc-float); }
.prow.failed { background: var(--md-sys-color-error-container, #ffdad6); }
.ptop { display: flex; gap: 12px; }
.prow-foot { display: flex; align-items: center; gap: 8px; }
.bar { height: 4px; border-radius: 2px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.bar i { display: block; height: 100%; background: var(--md-sys-color-primary); transition: width 300ms linear; }
</style>
