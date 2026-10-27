<script setup lang="ts">
// 上传本地视频：多选排队传（一个传完再传下一个，盒子 USB 写入和 WiFi 都是瓶颈，并发反而慢）。
// 流式上传 + 进度 + 磁盘空间预检；中断的半截文件由后端自动清理。
// 对应旧版 bindUpload/startUpload/runQueue。
import { onMounted, ref } from 'vue'
import { fetchDisk } from '../../api/manage'
import { fmtSize } from '../../api/local'
import { toast } from '../../composables/snackbar'
import TopAppBar from '../../components/m3/TopAppBar.vue'
import Button from '../../components/m3/Button.vue'
import Icon from '../../components/m3/Icon.vue'
import SegmentedButton from '../../components/m3/SegmentedButton.vue'
import FolderPicker from '../../components/manage/FolderPicker.vue'
import type { FolderItem } from '../../api/manage'

const VIDEO_EXT = /\.(mkv|mp4|avi|mov|m4v|ts|m2ts|webm|wmv|rm|rmvb|flv|mpg|mpeg|iso)$/i

const files = ref<File[]>([])
const err = ref('')
const busy = ref(false)
const diskText = ref('')
const dest = ref('new') // new | existing
const cat = ref('movie')
const title = ref('')
const target = ref<FolderItem | null>(null)
const picker = ref<InstanceType<typeof FolderPicker>>()

interface RowState {
  name: string
  pct: number
  sub: string
  failed: boolean
}
const rows = ref<RowState[]>([])

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
  for (const f of Array.from(list)) {
    if (!VIDEO_EXT.test(f.name)) continue
    files.value.push(f)
    added++
  }
  err.value = added ? '' : '只支持视频文件'
}

function removeFile(i: number) {
  if (busy.value) return
  files.value.splice(i, 1)
}

function switchDest(v: string) {
  dest.value = v
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
  // 磁盘空间预检：总大小超剩余空间就直接拦下
  const need = files.value.reduce((s, f) => s + f.size, 0)
  busy.value = true
  err.value = ''
  rows.value = files.value.map((f) => ({ name: f.name, pct: 0, sub: '等待中', failed: false }))
  try {
    const d = await fetchDisk()
    if (d && d.free && need > d.free) throw new Error(`硬盘空间不足：需要 ${fmtSize(need)}，仅剩 ${d.freeH}`)
    await runQueue(0)
    // 传成功的移出列表，失败的留着，修好问题后直接再点"开始上传"重试
    const failed = rows.value.filter((r) => r.failed).length
    files.value = files.value.filter((_, i) => rows.value[i]?.failed)
    if (failed) err.value = `${failed} 个文件上传失败，详见下方红色条目，可再点「开始上传」重试`
    else toast('上传完成')
  } catch (e) {
    err.value = String(e instanceof Error ? e.message : '上传失败')
  } finally {
    busy.value = false
  }
}

// 顺序传队列里的第 idx 个
function runQueue(idx: number): Promise<void> {
  return new Promise((resolve) => {
    if (idx >= files.value.length) return resolve()
    const f = files.value[idx]
    const row = rows.value[idx]
    const fd = new FormData()
    // 文本字段必须在文件前 append，后端流式读取靠这个顺序
    fd.append('cat', cat.value)
    fd.append('dest', dest.value)
    fd.append('title', title.value.trim())
    fd.append('targetId', dest.value === 'existing' && target.value ? target.value.id : '')
    fd.append('file', f, f.name)

    const xhr = new XMLHttpRequest()
    let lastLoaded = 0
    let lastT = Date.now()
    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return
      row.pct = Math.round((e.loaded * 100) / e.total)
      const now = Date.now()
      if (now - lastT > 800) {
        const speed = ((e.loaded - lastLoaded) * 1000) / (now - lastT)
        lastLoaded = e.loaded
        lastT = now
        const eta = speed > 0 ? Math.round((e.total - e.loaded) / speed) : 0
        row.sub = `${fmtSize(e.loaded)} / ${fmtSize(e.total)} · ${fmtSize(speed)}/s · 约剩 ${eta >= 60 ? Math.floor(eta / 60) + ' 分 ' + (eta % 60) + ' 秒' : eta + ' 秒'}`
      }
    }
    xhr.onload = () => {
      if (xhr.status === 200) {
        row.pct = 100
        row.sub = '已完成 · ' + fmtSize(f.size)
        runQueue(idx + 1).then(resolve)
      } else {
        row.sub = '失败：' + String(xhr.responseText).slice(0, 80)
        row.failed = true
        runQueue(idx + 1).then(resolve) // 这个失败了，不拦着后面的文件
      }
    }
    xhr.onerror = () => {
      row.sub = '网络中断，半截文件已自动清理'
      row.failed = true
      runQueue(idx + 1).then(resolve)
    }
    xhr.open('POST', '/api/upload')
    xhr.send(fd)
  })
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

      <!-- 进度 -->
      <div v-if="rows.length" class="progs">
        <div v-for="r in rows" :key="r.name" class="prow" :class="{ failed: r.failed }">
          <div class="ptop">
            <span class="t-body-m clamp-1 grow">{{ r.name }}</span>
            <span class="t-label-m">{{ r.pct }}%</span>
          </div>
          <div class="bar"><i :style="{ width: r.pct + '%' }" /></div>
          <div class="t-body-s muted">{{ r.sub }}</div>
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
.bar { height: 4px; border-radius: 2px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.bar i { display: block; height: 100%; background: var(--md-sys-color-primary); transition: width 300ms linear; }
</style>
