<script setup lang="ts">
// 手动添加下载：磁力链接或 .torrent 文件 + 分类 + 可选封面（上传图或图链）+ 可并入已有条目（追番新集不新建文件夹）。
// 对应旧版 bindManual/openManual。
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { addManual, type FolderItem } from '../../api/manage'
import { toast } from '../../composables/snackbar'
import { fmtSize } from '../../api/local'
import TopAppBar from '../../components/m3/TopAppBar.vue'
import Button from '../../components/m3/Button.vue'
import Icon from '../../components/m3/Icon.vue'
import SegmentedButton from '../../components/m3/SegmentedButton.vue'
import FolderPicker from '../../components/manage/FolderPicker.vue'

const router = useRouter()
const mode = ref('magnet') // magnet | torrent
const cat = ref('movie')
const dest = ref('new') // new | existing
const magnet = ref('')
const title = ref('')
const torrentData = ref('') // base64
const torrentName = ref('')
const coverData = ref('')
const coverUrl = ref('')
const target = ref<FolderItem | null>(null)
const picker = ref<InstanceType<typeof FolderPicker>>()
const err = ref('')
const busy = ref(false)

function switchDest(v: string) {
  dest.value = v
  if (v === 'existing') picker.value?.open()
}

function pickTorrent(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f) return
  if (!/\.torrent$/i.test(f.name)) {
    err.value = '请选择 .torrent 文件'
    return
  }
  if (f.size > 8 * 1024 * 1024) {
    err.value = '种子文件过大'
    return
  }
  const fr = new FileReader()
  fr.onload = () => {
    torrentData.value = String(fr.result).split(',')[1] || ''
    torrentName.value = f.name + ' · ' + fmtSize(f.size)
    err.value = ''
    // 没填名称时用种子文件名兜底
    if (!title.value.trim()) title.value = f.name.replace(/\.torrent$/i, '')
  }
  fr.onerror = () => (err.value = '读取种子文件失败')
  fr.readAsDataURL(f)
}

function pickCover(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f) return
  if (f.size > 10 * 1024 * 1024) {
    err.value = '封面图片过大（上限 10MB）'
    return
  }
  const fr = new FileReader()
  fr.onload = () => {
    coverData.value = String(fr.result)
    coverUrl.value = ''
    err.value = ''
  }
  fr.readAsDataURL(f)
}

async function submit() {
  const m = magnet.value.trim()
  if (mode.value === 'magnet' && !m) return (err.value = '请粘贴磁力链接')
  if (mode.value === 'magnet' && !/^magnet:\?/i.test(m)) return (err.value = '磁力链接应以 magnet:? 开头')
  if (mode.value === 'torrent' && !torrentData.value) return (err.value = '请选择 .torrent 文件')
  if (dest.value === 'existing' && !target.value) return (err.value = '请选择要并入的条目')
  if (dest.value === 'new' && !title.value.trim()) return (err.value = '请填写保存名称')

  busy.value = true
  err.value = ''
  try {
    const payload: Parameters<typeof addManual>[0] =
      dest.value === 'existing'
        ? { targetId: target.value!.id, title: target.value!.title }
        : { title: title.value.trim(), category: cat.value as 'movie' | 'series' }
    if (mode.value === 'torrent') payload.torrent = torrentData.value
    else payload.magnet = m
    if (coverData.value) payload.cover = coverData.value
    else if (coverUrl.value.trim()) payload.cover = coverUrl.value.trim()

    const task = await addManual(payload)
    if (!task || !task.id) throw new Error('提交失败')
    toast('已加入下载队列')
    router.push('/manage/tasks')
  } catch (e) {
    err.value = String(e instanceof Error ? e.message : e).slice(0, 200)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="page">
    <TopAppBar title="手动添加下载" back />

    <div class="wrap">
      <div class="group">
        <div class="t-label-l muted">来源</div>
        <SegmentedButton v-model="mode" :options="[{ value: 'magnet', label: '磁力链接' }, { value: 'torrent', label: '种子文件' }]" />
        <textarea v-if="mode === 'magnet'" v-model="magnet" class="textin area" rows="3" placeholder="magnet:?xt=urn:btih:…" autocapitalize="off" spellcheck="false" />
        <label v-else class="drop" :class="{ has: !!torrentData }">
          <input type="file" accept=".torrent" hidden @change="pickTorrent" />
          <Icon name="upload" :size="24" />
          <span class="t-body-m">{{ torrentName || '点击选择 .torrent 文件' }}</span>
        </label>
      </div>

      <div class="group">
        <div class="t-label-l muted">保存到</div>
        <SegmentedButton :model-value="dest" :options="[{ value: 'new', label: '新建条目' }, { value: 'existing', label: '并入已有（追番）' }]" @update:model-value="switchDest" />
        <template v-if="dest === 'new'">
          <SegmentedButton v-model="cat" :options="[{ value: 'movie', label: '电影电视剧' }, { value: 'series', label: '番剧' }]" />
          <input v-model="title" class="textin" placeholder="保存名称（会创建同名文件夹）" />
        </template>
        <FolderPicker v-else ref="picker" v-model="target" />
      </div>

      <div class="group">
        <div class="t-label-l muted">封面（可选）</div>
        <div class="coverrow">
          <label class="coverbtn">
            <input type="file" accept="image/*" hidden @change="pickCover" />
            <Icon name="image" :size="20" />
            <span class="t-label-l">{{ coverData ? '已选择图片' : '上传图片' }}</span>
          </label>
          <img v-if="coverData" :src="coverData" class="coverprev" alt="封面预览" />
        </div>
        <input v-model="coverUrl" class="textin" placeholder="或粘贴封面图片链接（开始下载后就会显示在库里）" autocapitalize="off" />
      </div>

      <p v-if="err" class="errtext t-body-s">{{ err }}</p>
      <Button :disabled="busy" @click="submit">{{ busy ? '提交中…' : '开始下载' }}</Button>
      <p class="t-body-s muted note">提交后盒子会自动下载、解压、识别集数并入库；进度在「下载任务」里看。</p>
    </div>
  </div>
</template>

<style scoped>
.wrap { padding: 8px 16px 32px; max-width: 720px; margin: 0 auto; display: flex; flex-direction: column; gap: 16px; }
.group { display: flex; flex-direction: column; gap: 10px; }
.textin { padding: 10px 12px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 15px; }
.area { resize: vertical; font-family: monospace; font-size: 13px; }
.drop { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 24px; border: 2px dashed var(--md-sys-color-outline); border-radius: var(--md-shape-lg); cursor: pointer; color: var(--md-sys-color-on-surface-variant); text-align: center; }
.drop.has { border-color: var(--md-sys-color-primary); color: var(--md-sys-color-primary); }
.coverrow { display: flex; align-items: center; gap: 12px; }
.coverbtn { display: inline-flex; align-items: center; gap: 8px; padding: 10px 16px; border-radius: var(--md-shape-md); background: var(--md-sys-color-secondary-container); cursor: pointer; }
.coverprev { width: 48px; height: 64px; object-fit: cover; border-radius: var(--md-shape-sm); }
.errtext { color: var(--md-sys-color-error, #ba1a1a); margin: 0; }
.note { margin: 0 4px; }
</style>
