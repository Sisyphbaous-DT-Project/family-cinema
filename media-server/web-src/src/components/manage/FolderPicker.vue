<script setup lang="ts">
// 并入已有条目的选择器：搜索 /api/folders，点选后通过 v-model 回报。
// 手动添加下载和上传视频共用。
import { ref, watch } from 'vue'
import { coverURL } from '../../api/local'
import { fetchFolders, type FolderItem } from '../../api/manage'
import Icon from '../m3/Icon.vue'

const selected = defineModel<FolderItem | null>({ default: null })

const q = ref('')
const list = ref<FolderItem[]>([])
const loading = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined

async function load(kw: string) {
  loading.value = true
  try {
    list.value = (await fetchFolders(kw)) ?? []
  } catch {
    list.value = []
  } finally {
    loading.value = false
  }
}

watch(q, (v) => {
  clearTimeout(timer)
  timer = setTimeout(() => load(v.trim()), 250)
})

// 父组件切到"并入已有"时才展开加载
function open() {
  if (!list.value.length) load('')
}
defineExpose({ open })

function pick(f: FolderItem) {
  selected.value = f
  list.value = []
  q.value = ''
}
function clear() {
  selected.value = null
  open()
}
</script>

<template>
  <div class="picker">
    <div v-if="selected" class="picked">
      <Icon name="folder" :size="20" />
      <div class="grow">
        <div class="t-body-m">{{ selected.title }}</div>
        <div class="t-body-s muted">
          {{ selected.category === 'series' ? '番剧' : '电影电视剧' }} · 已有 {{ selected.episodes }} 个文件 · {{ selected.sizeHuman }}
        </div>
      </div>
      <button v-ripple class="t-label-m clr" type="button" @click="clear">重选</button>
    </div>
    <template v-else>
      <input v-model="q" class="textin" type="search" placeholder="搜索要并入的条目" autocapitalize="off" />
      <div class="plist">
        <div v-if="loading" class="hint muted t-body-s">正在加载…</div>
        <div v-else-if="!list.length" class="hint muted t-body-s">没有匹配的条目</div>
        <template v-else>
          <button v-for="f in list" :key="f.id" v-ripple class="pitem" type="button" @click="pick(f)">
            <img v-if="f.cover" :src="coverURL(f.cover)" class="thumb" alt="" loading="lazy" />
            <Icon v-else name="tv" :size="20" />
            <div class="grow">
              <div class="t-body-m clamp-1">{{ f.title }}</div>
              <div class="t-body-s muted">{{ f.category === 'series' ? '番剧' : '电影电视剧' }} · {{ f.episodes }} 个文件 · {{ f.sizeHuman }}</div>
            </div>
          </button>
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
.picker { display: flex; flex-direction: column; gap: 10px; }
.textin { width: 100%; box-sizing: border-box; min-height: 48px; padding: 10px 12px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 16px; }
.plist { display: flex; flex-direction: column; gap: 6px; max-height: min(320px, 40vh); padding: 2px; overflow-y: auto; overscroll-behavior: contain; border-radius: var(--md-shape-md); }
.hint { padding: 20px 16px; text-align: center; }
.pitem { display: flex; align-items: center; flex: none; gap: 12px; width: 100%; min-height: 76px; padding: 10px 12px; border-radius: var(--md-shape-md); text-align: left; color: inherit; }
.pitem:hover { background: var(--md-sys-color-surface-container-high); }
.pitem .grow, .picked .grow { display: flex; flex-direction: column; gap: 5px; }
.pitem .t-body-m, .picked .t-body-m { line-height: 24px; }
.pitem .t-body-s, .picked .t-body-s { line-height: 20px; }
.thumb { flex: none; width: 40px; height: 56px; object-fit: cover; border-radius: 6px; }
.grow { flex: 1; min-width: 0; }
.picked { display: flex; align-items: center; gap: 12px; padding: 14px 12px; border-radius: var(--md-shape-md); background: var(--md-sys-color-secondary-container); }
.clr { flex: none; min-height: 40px; color: var(--md-sys-color-primary); padding: 6px 10px; border-radius: var(--md-shape-md); }
</style>
