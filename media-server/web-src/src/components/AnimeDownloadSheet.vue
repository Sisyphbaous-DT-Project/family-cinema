<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { api } from '../api/client'
import type { FolderItem } from '../api/manage'
import { toast } from '../composables/snackbar'
import BottomSheet from './m3/BottomSheet.vue'
import Button from './m3/Button.vue'
import SegmentedButton from './m3/SegmentedButton.vue'
import FolderPicker from './manage/FolderPicker.vue'

const props = defineProps<{
  modelValue: boolean
  rule: string
  sourceRef: string
  title: string
  episodes: { name: string; url: string; index: number }[]
  /** "约 1.1 GB"（多选时量出来的），空串 = 还不知道 */
  sizeText?: string
  /** 盒子硬盘还剩多少（"120 GB"） */
  diskFree?: string
  tooBig?: boolean
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'done'): void
}>()

const open = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit('update:modelValue', v),
})
const dest = ref('new')
const cat = ref('series')
const folder = ref('')
const target = ref<FolderItem | null>(null)
const picker = ref<InstanceType<typeof FolderPicker>>()
const busy = ref(false)
const err = ref('')

watch(() => props.modelValue, (v) => {
  if (!v) return
  dest.value = 'new'
  cat.value = 'series'
  folder.value = props.title
  target.value = null
  busy.value = false
  err.value = ''
})

function switchDest(v: string) {
  dest.value = v
  err.value = ''
  if (v === 'existing') nextTick(() => picker.value?.open())
}

const canSubmit = computed(() => {
  if (busy.value || !props.episodes.length) return false
  return dest.value === 'existing' ? !!target.value : !!folder.value.trim()
})

async function submit() {
  if (!canSubmit.value) return
  busy.value = true
  err.value = ''
  try {
    const r = await api.download({
      rule: props.rule,
      ref: props.sourceRef,
      title: props.title,
      category: cat.value as 'movie' | 'series',
      folder: dest.value === 'new' ? folder.value.trim() : '',
      targetId: dest.value === 'existing' ? target.value!.id : '',
      episodes: props.episodes,
    })
    if (!r.ok) throw new Error('提交失败，请再试一次')
    open.value = false
    if (r.queued > 0) {
      toast(`已交给盒子下载 ${r.queued} 集` + (r.skipped ? `，已有 ${r.skipped} 集自动跳过` : '') + '。下好会出现在片库里', 5000)
    } else {
      toast('选中的集都已经有了，没有重复下载')
    }
    emit('done')
  } catch (e) {
    err.value = e instanceof Error ? e.message : '提交失败，请再试一次'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <BottomSheet v-model="open" title="存到盒子的哪个文件夹">
    <div class="form">
      <p class="t-body-m muted intro">
        这次选了 {{ episodes.length }} 集<template v-if="sizeText">，{{ sizeText }}</template>，盒子会按顺序下载，一次同时下 2 集。已有的集会自动跳过，不会重复占空间。
      </p>
      <p v-if="diskFree" class="t-body-m disk" :class="{ bad: tooBig }">
        {{ tooBig ? '盒子硬盘快不够了：' : '' }}盒子硬盘还剩 {{ diskFree }}{{ tooBig ? '，下到一半可能会停，建议先少选几集' : '' }}
      </p>

      <SegmentedButton
        :model-value="dest"
        :options="[{ value: 'new', label: '新建文件夹' }, { value: 'existing', label: '放进已有文件夹' }]"
        @update:model-value="switchDest"
      />

      <template v-if="dest === 'new'">
        <SegmentedButton
          v-model="cat"
          :options="[{ value: 'series', label: '番剧' }, { value: 'movie', label: '电影电视剧' }]"
        />
        <label class="field">
          <span class="t-label-l muted">文件夹名字</span>
          <input v-model="folder" class="textin" placeholder="会用这个名字新建文件夹" />
        </label>
      </template>
      <FolderPicker v-else ref="picker" v-model="target" />

      <p v-if="err" class="errtext t-body-m">{{ err }}</p>
      <div class="actions">
        <Button variant="text" :disabled="busy" @click="open = false">取消</Button>
        <Button :disabled="!canSubmit" @click="submit">{{ busy ? '正在交给盒子…' : '开始下载' }}</Button>
      </div>
    </div>
  </BottomSheet>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 16px; }
.form :deep(.seg) { display: flex; width: 100%; }
.form :deep(.seg-item) { flex: 1; justify-content: center; min-width: 0; height: auto; min-height: 48px; padding: 8px 10px; text-align: center; }
.intro { margin: 0; }
.disk { margin: -4px 0 0; color: var(--md-sys-color-on-surface-variant); }
.disk.bad { color: var(--md-sys-color-error, #ba1a1a); font-weight: 600; }
.field { display: flex; flex-direction: column; gap: 8px; }
.textin { box-sizing: border-box; width: 100%; min-height: 48px; padding: 10px 12px; border: 1px solid var(--md-sys-color-outline); border-radius: var(--md-shape-md); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 16px; }
.textin:focus { outline: none; border-color: var(--md-sys-color-primary); box-shadow: 0 0 0 1px var(--md-sys-color-primary); }
.errtext { margin: 0; color: var(--md-sys-color-error, #ba1a1a); }
.actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
