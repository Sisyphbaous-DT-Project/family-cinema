<script setup lang="ts">
// BT 搜索与下载：聚合搜索 → 展开某条看各来源的磁力 → 测速 → 下载。
// 对应旧版搜索结果页 + 详情弹层的来源/测速/下载部分。
// 注意：mikan 来源当前不可用（盒子出不了墙，见 AGENTS §15），页面上有提示。
import { ref } from 'vue'
import { btDetail, btDownload, btSearch, btSpeedtest, type SearchHit, type TorrentInfo } from '../../api/manage'
import { toast } from '../../composables/snackbar'
import TopAppBar from '../../components/m3/TopAppBar.vue'
import Button from '../../components/m3/Button.vue'
import Icon from '../../components/m3/Icon.vue'
import IconButton from '../../components/m3/IconButton.vue'
import BottomSheet from '../../components/m3/BottomSheet.vue'
import Spinner from '../../components/m3/Spinner.vue'

const q = ref('')
const hits = ref<SearchHit[]>([])
const loading = ref(false)
const searched = ref(false)
const error = ref('')
const open = ref<number>(-1) // 展开中的条目
const speedMap = ref<Record<string, string>>({}) // magnet → 测速结果文本
const testing = ref<Record<string, boolean>>({})
const detailLoading = ref<Record<string, boolean>>({}) // "site:url" → 正在补取磁力

async function search() {
  const kw = q.value.trim()
  if (!kw) return
  loading.value = true
  error.value = ''
  open.value = -1
  try {
    hits.value = (await btSearch(kw)) ?? []
    searched.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : '检索失败'
  } finally {
    loading.value = false
  }
}

// 来源没有直接给出磁力时，异步补取详情页
async function fillDetail(hit: SearchHit, site: string, url: string) {
  const key = site + ':' + url
  if (detailLoading.value[key]) return
  detailLoading.value[key] = true
  try {
    const d = await btDetail(site, url)
    if (d?.torrents?.length) {
      const src = hit.results?.find((r) => r.site === site && r.url === url)
      if (src) src.torrents = d.torrents
    }
  } catch {
    /* 补取失败就保持"未解析到磁力" */
  } finally {
    detailLoading.value[key] = false
  }
}

async function test(t: TorrentInfo) {
  testing.value[t.magnet] = true
  speedMap.value[t.magnet] = ''
  try {
    const r = await btSpeedtest(t.magnet)
    speedMap.value[t.magnet] = r.error
      ? r.error
      : `${(r.mbps || 0).toFixed(2)} MB/s · ${r.peers || 0} 个节点${r.size ? ' · ' + r.size : ''}`
  } catch {
    speedMap.value[t.magnet] = '测速请求失败'
  } finally {
    testing.value[t.magnet] = false
  }
}

// ---------- 下载 ----------
const dlHit = ref<SearchHit | null>(null)
const dlTor = ref<TorrentInfo | null>(null)
const dlName = ref('')
const dlBusy = ref(false)

function askDownload(hit: SearchHit, t: TorrentInfo, site: string) {
  dlHit.value = hit
  dlTor.value = t
  dlName.value = hit.title
  // mikan 来源进番剧，其余沿用命中条目的本地分类（没有就电影电视剧）
  dlSite = site
}
let dlSite = ''

async function doDownload() {
  const name = dlName.value.trim()
  if (!name || !dlTor.value) return
  dlBusy.value = true
  try {
    const cat = dlSite === 'mikan' ? 'series' : dlHit.value?.local?.category || 'movie'
    const task = await btDownload(dlTor.value.magnet, name, cat)
    if (!task?.id) throw new Error('提交失败')
    dlTor.value = null
    toast('已加入下载队列')
  } catch (e) {
    toast(String(e instanceof Error ? e.message : '下载请求失败'))
  } finally {
    dlBusy.value = false
  }
}
</script>

<template>
  <div class="page">
    <TopAppBar title="BT 搜索下载" back />

    <div class="wrap">
      <form class="bar" @submit.prevent="search">
        <input v-model="q" class="field t-body-l" type="search" enterkeyhint="search" placeholder="搜索想下载的影片" autocomplete="off" />
        <IconButton icon="search" label="搜索" @click="search" />
      </form>

      <p class="t-body-s muted note">mikan（番剧）来源当前不可用，番剧请先走「手动添加下载」贴磁力链接。</p>

      <div v-if="loading" class="state"><Spinner :size="36" /><div class="muted t-body-m">正在检索本地与全网资源</div></div>
      <div v-else-if="error" class="state">
        <Icon name="error" :size="40" />
        <div class="muted">{{ error }}</div>
        <Button variant="tonal" @click="search">再试一次</Button>
      </div>
      <div v-else-if="searched && !hits.length" class="state">
        <Icon name="search" :size="48" />
        <div class="muted">没有找到相关资源</div>
      </div>

      <template v-else>
      <div v-for="(h, i) in hits" :key="i" class="hit">
        <button v-ripple class="hhead" type="button" @click="open = open === i ? -1 : i">
          <div class="grow">
            <div class="t-body-l">{{ h.title }}</div>
            <div class="t-body-s muted">
              <template v-if="h.local">本地已有 · </template>
              {{ (h.results || []).length }} 个来源 · {{ (h.results || []).reduce((s, r) => s + (r.torrents || []).length, 0) }} 个资源
            </div>
          </div>
          <Icon :name="open === i ? 'expandLess' : 'expandMore'" />
        </button>

        <div v-if="open === i" class="hbody">
          <div v-for="r in h.results" :key="r.site + (r.url || '')" class="src">
            <div class="shead t-label-l">{{ r.siteName || r.site }}</div>
            <template v-if="(r.torrents || []).length">
              <div v-for="t in r.torrents" :key="t.magnet" class="tor">
                <div class="grow">
                  <div class="t-body-m clamp-2">{{ t.name || '磁力资源' }}</div>
                  <div class="t-body-s muted">
                    {{ t.size || '' }}
                    <template v-if="speedMap[t.magnet]"> · {{ speedMap[t.magnet] }}</template>
                  </div>
                </div>
                <Button variant="text" small :disabled="testing[t.magnet]" @click="test(t)">{{ testing[t.magnet] ? '测速中' : '测速' }}</Button>
                <Button variant="tonal" small @click="askDownload(h, t, r.site)">下载</Button>
              </div>
            </template>
            <div v-else class="t-body-s muted none">
              {{ detailLoading[r.site + ':' + (r.url || '')] ? '正在解析磁力…' : '该来源未解析到磁力' }}
              <Button v-if="r.url && /^https?:/.test(r.url) && !detailLoading[r.site + ':' + r.url]" variant="text" small @click="fillDetail(h, r.site, r.url)">尝试解析</Button>
            </div>
          </div>
          <div v-if="!(h.results || []).length" class="t-body-s muted none">没有在线来源</div>
        </div>
      </div>
      </template>
    </div>

    <!-- 下载确认 -->
    <BottomSheet :model-value="!!dlTor" title="确认下载" @update:model-value="dlTor = null">
      <div class="form">
        <div class="t-body-s muted clamp-2">{{ dlTor?.name || '磁力资源' }}</div>
        <input v-model="dlName" class="textin" placeholder="保存名称（会创建同名文件夹）" />
        <div class="foot">
          <Button variant="text" @click="dlTor = null">取消</Button>
          <Button :disabled="dlBusy || !dlName.trim()" @click="doDownload">{{ dlBusy ? '提交中…' : '下载' }}</Button>
        </div>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.wrap { padding: 8px 16px 32px; max-width: 900px; margin: 0 auto; }
.bar { display: flex; align-items: center; gap: 4px; margin-bottom: 8px; }
.field { flex: 1; min-width: 0; height: 48px; padding: 0 16px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: transparent; outline: none; }
.field:focus { border-color: var(--md-sys-color-primary); box-shadow: 0 0 0 1px var(--md-sys-color-primary); }
.note { margin: 0 4px 12px; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 64px 16px; text-align: center; color: var(--md-sys-color-on-surface-variant); }
.hit { border-radius: var(--fc-radius); background: var(--fc-card); box-shadow: var(--fc-float); margin-bottom: 12px; overflow: hidden; }
.hhead { display: flex; align-items: center; gap: 12px; width: 100%; padding: 14px 16px; text-align: left; color: inherit; }
.grow { flex: 1; min-width: 0; }
.hbody { border-top: 1px solid var(--md-sys-color-surface-container-highest); padding: 8px 12px 12px; }
.src { margin-top: 8px; }
.shead { color: var(--md-sys-color-primary); margin: 4px 4px 6px; }
.tor { display: flex; align-items: center; gap: 8px; padding: 8px 8px; border-radius: var(--md-shape-md); background: var(--md-sys-color-surface-container-high); margin-bottom: 6px; }
.none { padding: 8px; }
.form { display: flex; flex-direction: column; gap: 12px; }
.textin { width: 100%; box-sizing: border-box; padding: 10px 12px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 15px; }
.foot { display: flex; justify-content: flex-end; gap: 8px; }
</style>
