<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api } from '../api/client'
import type { RuleInfo } from '../api/types'
import { SEED_PRESETS, themeState, type ThemeMode } from '../theme/theme'
import { isRuleEnabled, setRuleEnabled, settings } from '../composables/settings'
import { bcall, bjson, hasBridge } from '../composables/bridge'
import { cacheSize } from '../composables/cache'
import { fmtSize } from '../api/local'
import { appUpdate, checkAppUpdate, startAppUpdate } from '../composables/appUpdate'
import { toast } from '../composables/snackbar'
import TopAppBar from '../components/m3/TopAppBar.vue'
import SegmentedButton from '../components/m3/SegmentedButton.vue'
import Switch from '../components/m3/Switch.vue'
import Icon from '../components/m3/Icon.vue'

defineOptions({ name: 'SettingsView' })

const modes = [
  { value: 'auto', label: '跟随系统' },
  { value: 'light', label: '浅色' },
  { value: 'dark', label: '深色' },
]

const modeModel = computed<string>({
  get: () => themeState.mode,
  set: (v) => (themeState.mode = v as ThemeMode),
})

const rules = ref<RuleInfo[]>([])
const online = ref<number | null>(null)
const worker = ref(false)
const inApp = hasBridge()
const REASON: Record<string, string> = { captcha: '需要验证码', 'api-mode': '暂不支持', incomplete: '规则不完整' }

// ---- Bangumi 网络（仅 App 内可见；浏览器靠系统代理，无需设置） ----
const bgmAuto = ref(true)
const bgmAddr = ref('127.0.0.1:7890')
const bgmProbeResult = ref('')

// ---- 应用更新（仅 App 内可见） ----
const appVer = ref('')
const webVer = ref('')
onMounted(() => {
  if (inApp) {
    try {
      const v = JSON.parse(bcall((b) => b.getVersion?.()) ?? '{}') as { app?: string; web?: string }
      appVer.value = v.app ?? ''
      webVer.value = v.web ?? ''
    } catch {
      /* 读不到就不显示 */
    }
    checkAppUpdate(true)
  }
})

// ---- 连接（仅 App 内可见）：服务器地址 + 重新发现 ----
const serverAddr = ref('')
function saveAddr() {
  const addr = serverAddr.value.trim()
  if (!addr) {
    toast('请先填上盒子地址')
    return
  }
  bcall((b) => b.setServerAddr?.(addr))
  toast('盒子地址已保存')
}
function rediscover() {
  toast('正在家里的网络里找盒子…', 8000)
  // 桥调用是同步阻塞的（扫网段要几秒），放到下一拍让上面的提示先渲染出来
  setTimeout(() => {
    const found = bcall((b) => b.discoverServer?.())
    if (found) {
      serverAddr.value = found
      toast('已找到盒子：' + found)
    } else {
      toast('没找到盒子，请确认连着家里的 WiFi，或手动填写地址')
    }
  }, 50)
}

// ---- 离线缓存（仅 App 内可见）：占用 + 后台缓存体检 ----
const cacheText = ref('')
const bgStatus = ref<{ battery: boolean; notify: boolean } | null>(null)
// 追番提醒（App 1.8 起才有这些桥方法；旧 App 为 null）
const followNotify = ref<{ notify: boolean; battery: boolean; watching: boolean; lastCheck: number; lastError: string } | null>(null)
function refreshCacheInfo() {
  if (!inApp) return
  const s = cacheSize()
  cacheText.value = `已缓存 ${s.count} 个 · 占用 ${fmtSize(s.total)}`
  bgStatus.value = bjson((b) => b.getBackgroundStatus?.(), null)
  followNotify.value = bjson((b) => b.getFollowNotifyStatus?.(), null)
}
const followCheckText = computed(() => {
  const f = followNotify.value
  if (!f?.lastCheck) return '手机隔二三十分钟问一次盒子，有新的一集就在通知栏提醒'
  const t = new Date(f.lastCheck)
  const hm = `${t.getHours()}:${String(t.getMinutes()).padStart(2, '0')}`
  return f.lastError ? `上次 ${hm} 没连上盒子（不在家里 WiFi 时就是这样，回家会补上）` : `上次 ${hm} 问过盒子，一切正常`
})
function testFollowNotify() {
  const r = bcall((b) => b.testFollowNotify?.())
  toast(r === 'ok' ? '发了一条测试通知，看看手机顶上' : '发不出来：先打开通知权限')
}
// 从系统设置页回来时重新体检
function onVisible() {
  if (!document.hidden) refreshCacheInfo()
}

onMounted(async () => {
  try {
    rules.value = await api.rules()
  } catch {
    /* 规则列表拿不到就显示空 */
  }
  if (inApp) {
    try {
      const p = JSON.parse(bcall((b) => b.getBgmProxy?.()) ?? '{}') as { auto?: boolean; addr?: string }
      bgmAuto.value = p.auto ?? true
      bgmAddr.value = p.addr || '127.0.0.1:7890'
    } catch {
      /* 读不到配置就用默认值 */
    }
  }
  if (inApp) {
    serverAddr.value = bcall((b) => b.getServerAddr?.()) || location.origin
    refreshCacheInfo()
    document.addEventListener('visibilitychange', onVisible)
  }
  refreshWorker()
})

onBeforeUnmount(() => document.removeEventListener('visibilitychange', onVisible))

function saveBgmProxy() {
  bcall((b) => b.setBgmProxy?.(bgmAuto.value, bgmAddr.value.trim()))
}

function probeBgm() {
  bgmProbeResult.value = '正在测试…'
  // 桥调用是同步阻塞的，放到下一拍让"正在测试"先渲染出来
  setTimeout(() => {
    try {
      const r = JSON.parse(bcall((b) => b.bgmProbe?.()) ?? '{}') as { route?: string; detail?: string }
      const label: Record<string, string> = {
        direct: '直连可用（梯子接管了流量，无需代理）',
        proxy: '本地代理可用',
        dead: '直连和代理都连不上，请检查梯子',
      }
      bgmProbeResult.value = label[r.route ?? ''] ?? (r.detail || '未知结果')
    } catch {
      bgmProbeResult.value = '测试失败'
    }
  }, 50)
}

async function refreshWorker() {
  worker.value = bcall((b) => b.isSniffWorkerRunning?.()) === '1'
  try {
    online.value = (await api.workerStatus()).online
  } catch {
    online.value = null
  }
}

function toggleWorker(on: boolean) {
  bcall((b) => (on ? b.startSniffWorker?.() : b.stopSniffWorker?.()))
  worker.value = on
  setTimeout(refreshWorker, 1500)
}
</script>

<template>
  <div class="page tab-page">
    <TopAppBar title="设置" />
    <div class="wrap">
      <section v-if="inApp">
        <h2 class="t-title-s sec">连接盒子</h2>
        <div class="card">
          <div class="row col">
            <div class="t-body-l">盒子地址</div>
            <input
              v-model="serverAddr"
              class="textin"
              placeholder="http://box.local:8088"
              autocapitalize="off"
              autocorrect="off"
              spellcheck="false"
              @change="saveAddr"
            />
            <div class="t-body-s muted">一般会自动找到；连不上时点「自动找盒子」，还不行再手动填</div>
          </div>
          <div class="row">
            <button v-ripple class="probebtn" type="button" @click="rediscover">自动找盒子</button>
            <button v-ripple class="probebtn" type="button" @click="saveAddr">保存</button>
          </div>
        </div>
      </section>

      <section v-if="inApp">
        <h2 class="t-title-s sec">缓存到手机</h2>
        <div class="card">
          <router-link v-ripple class="row link" to="/cache">
            <Icon name="download" />
            <div class="grow">
              <div class="t-body-l">我的缓存</div>
              <div class="t-body-s muted">{{ cacheText || '手机里还没有缓存的片子' }}</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </router-link>
          <template v-if="bgStatus">
            <div v-if="bgStatus.battery && bgStatus.notify" class="row">
              <Icon name="check" class="ok" />
              <div class="grow t-body-m">手机锁屏或切到别的 App，也会接着缓存</div>
            </div>
            <button v-if="!bgStatus.battery" v-ripple class="row link" type="button" @click="bcall((b) => b.requestBatteryWhitelist?.())">
              <Icon name="error" class="warn" />
              <div class="grow">
                <div class="t-body-l">允许后台运行</div>
                <div class="t-body-s muted">不放行的话，手机一锁屏缓存就会停，点这里放行</div>
              </div>
              <Icon name="expandMore" class="chev" />
            </button>
            <button v-if="!bgStatus.notify" v-ripple class="row link" type="button" @click="bcall((b) => b.openAppSettings?.())">
              <Icon name="error" class="warn" />
              <div class="grow">
                <div class="t-body-l">开启通知权限</div>
                <div class="t-body-s muted">打开后能在手机顶部的通知里看到缓存进度</div>
              </div>
              <Icon name="expandMore" class="chev" />
            </button>
          </template>
        </div>
      </section>

      <section v-if="inApp">
        <h2 class="t-title-s sec">追番提醒</h2>
        <div class="card">
          <div v-if="!followNotify" class="row">
            <Icon name="info" />
            <div class="grow t-body-m">把 App 更新到 1.8 以后，追的番在片源站出了新的一集，手机通知栏会提醒你</div>
          </div>
          <template v-else>
            <div class="row">
              <Icon name="bell" :class="followNotify.watching ? 'ok' : ''" />
              <div class="grow">
                <div class="t-body-l">{{ followNotify.watching ? '正在帮你盯着追的番' : '还没有追番' }}</div>
                <div class="t-body-s muted">{{ followNotify.watching ? followCheckText : '在番剧详情页点「追番」，更新了就会提醒你' }}</div>
              </div>
            </div>
            <button v-if="!followNotify.notify" v-ripple class="row link" type="button" @click="bcall((b) => b.openAppSettings?.())">
              <Icon name="error" class="warn" />
              <div class="grow">
                <div class="t-body-l">开启通知权限</div>
                <div class="t-body-s muted">不打开就收不到追番提醒，点这里打开</div>
              </div>
              <Icon name="expandMore" class="chev" />
            </button>
            <button v-if="!followNotify.battery" v-ripple class="row link" type="button" @click="bcall((b) => b.requestBatteryWhitelist?.())">
              <Icon name="error" class="warn" />
              <div class="grow">
                <div class="t-body-l">允许后台运行</div>
                <div class="t-body-s muted">不放行的话，手机锁屏久了就收不到提醒</div>
              </div>
              <Icon name="expandMore" class="chev" />
            </button>
            <button v-ripple class="row link" type="button" @click="bcall((b) => b.openAutostartSettings?.())">
              <Icon name="settings" />
              <div class="grow">
                <div class="t-body-l">允许自启动</div>
                <div class="t-body-s muted">小米、红米手机要打开这个，手机重启后才会接着提醒（打开过就不用管了）</div>
              </div>
              <Icon name="expandMore" class="chev" />
            </button>
            <button v-ripple class="row link" type="button" @click="testFollowNotify">
              <Icon name="bell" />
              <div class="grow">
                <div class="t-body-l">发一条试试</div>
                <div class="t-body-s muted">看看手机顶上能不能收到提醒</div>
              </div>
            </button>
          </template>
        </div>
      </section>

      <section>
        <h2 class="t-title-s sec">外观</h2>
        <div class="card">
          <div class="row col">
            <div class="t-body-l">颜色模式</div>
            <SegmentedButton v-model="modeModel" :options="modes" />
          </div>
          <div class="row col">
            <div class="t-body-l">主题色</div>
            <div class="swatches">
              <button
                v-for="s in SEED_PRESETS"
                :key="s.hex"
                v-ripple
                class="swatch"
                :class="{ on: themeState.seed === s.hex }"
                :style="{ background: s.hex }"
                :title="s.name"
                :aria-label="s.name"
                type="button"
                @click="themeState.seed = s.hex"
              >
                <Icon v-if="themeState.seed === s.hex" name="check" :size="20" />
              </button>
            </div>
          </div>
        </div>
      </section>

      <section>
        <h2 class="t-title-s sec">解析助手</h2>
        <div class="card">
          <div v-if="inApp" class="row">
            <div class="grow">
              <div class="t-body-l">把这台手机作为解析助手</div>
              <div class="t-body-s muted">有些片源站必须用手机浏览器才能解析；开启后电脑上也能播放和下载，必要时由手机代取视频流（App 需保持打开）</div>
            </div>
            <Switch :model-value="worker" @update:model-value="toggleWorker" />
          </div>
          <div class="row">
            <Icon name="phone" />
            <div class="grow">
              <div class="t-body-l">当前在线的解析助手：{{ online === null ? '未知' : online + ' 台' }}</div>
              <div v-if="!inApp" class="t-body-s muted">在手机 App 里打开本页并开启上面的开关，电脑就能借助手机解析</div>
            </div>
          </div>
        </div>
      </section>

      <section v-if="inApp">
        <h2 class="t-title-s sec">Bangumi 网络</h2>
        <div class="card">
          <div class="row">
            <div class="grow">
              <div class="t-body-l">自动选路</div>
              <div class="t-body-s muted">先直连（梯子 VPN 模式直接就能通），不通再走下面的本地代理，都不行就报错</div>
            </div>
            <Switch v-model="bgmAuto" @update:model-value="saveBgmProxy" />
          </div>
          <div class="row col">
            <div class="t-body-l">本地代理地址</div>
            <input
              v-model="bgmAddr"
              class="textin"
              :disabled="!bgmAuto"
              placeholder="127.0.0.1:7890"
              autocapitalize="off"
              autocorrect="off"
              spellcheck="false"
              @change="saveBgmProxy"
            />
            <div class="t-body-s muted">手机梯子「仅代理模式」开的端口，一般是 7890；VPN 模式下用不到它</div>
          </div>
          <div class="row">
            <button v-ripple class="probebtn" type="button" @click="probeBgm">测试连接</button>
            <div class="t-body-s muted grow">{{ bgmProbeResult }}</div>
          </div>
        </div>
      </section>

      <section>
        <h2 class="t-title-s sec">播放源</h2>
        <div class="card">
          <div v-for="r in rules" :key="r.name" class="row">
            <div class="grow">
              <div class="t-body-l">{{ r.name }}</div>
              <div class="t-body-s muted">
                v{{ r.version }}<template v-if="r.adBlocker"> · 自动去广告</template
                ><template v-if="r.unsupported"> · {{ REASON[r.unsupported] ?? r.unsupported }}</template>
              </div>
            </div>
            <Switch
              :model-value="!r.unsupported && isRuleEnabled(r.name)"
              :disabled="!!r.unsupported"
              @update:model-value="(v: boolean) => setRuleEnabled(r.name, v)"
            />
          </div>
          <div v-if="!rules.length" class="row muted">没有加载到播放源规则</div>
        </div>
        <p class="t-body-s muted note">规则来自开源社区 Kazumi Rules。已关闭 {{ settings.disabledRules.length }} 个。</p>
      </section>

      <section>
        <h2 class="t-title-s sec">片库管理</h2>
        <div class="card">
          <router-link v-ripple class="row link" to="/manage/tasks">
            <Icon name="download" />
            <div class="grow">
              <div class="t-body-l">下载任务</div>
              <div class="t-body-s muted">查看进度、停止任务、清理记录</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </router-link>
          <router-link v-ripple class="row link" to="/manage">
            <Icon name="list" />
            <div class="grow">
              <div class="t-body-l">库管理</div>
              <div class="t-body-s muted">统计、改名、换封面、删除、补封面、下载缓存清理</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </router-link>
          <router-link v-ripple class="row link" to="/manage/upload">
            <Icon name="upload" />
            <div class="grow">
              <div class="t-body-l">上传视频</div>
              <div class="t-body-s muted">把本地视频传到盒子硬盘</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </router-link>
          <router-link v-ripple class="row link" to="/manage/manual">
            <Icon name="edit" />
            <div class="grow">
              <div class="t-body-l">手动添加下载</div>
              <div class="t-body-s muted">贴磁力/传种子，可并入已有条目（追番）</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </router-link>
          <router-link v-ripple class="row link" to="/manage/files">
            <Icon name="folder" />
            <div class="grow">
              <div class="t-body-l">文件管理</div>
              <div class="t-body-s muted">浏览硬盘、新建文件夹、改名、删除</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </router-link>
          <router-link v-ripple class="row link" to="/manage/bt">
            <Icon name="search" />
            <div class="grow">
              <div class="t-body-l">BT 搜索下载</div>
              <div class="t-body-s muted">聚合搜索 + 测速 + 下载（mikan 暂不可用）</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </router-link>
          <a v-ripple class="row link" href="/legacy/">
            <Icon name="history" />
            <div class="grow">
              <div class="t-body-l">旧版管理页（备用）</div>
              <div class="t-body-s muted">上面的新页面有问题时，可以回旧版操作</div>
            </div>
            <Icon name="expandMore" class="chev" />
          </a>
        </div>
      </section>

      <section v-if="inApp">
        <h2 class="t-title-s sec">应用更新</h2>
        <div class="card">
          <div class="row">
            <div class="grow">
              <div class="t-body-l">当前版本 {{ appVer || '未知' }}</div>
              <div class="t-body-s muted">
                <template v-if="appUpdate.available">发现新版本 {{ appUpdate.versionName }}，点右侧按钮更新（会弹系统安装界面）</template>
                <template v-else-if="appUpdate.checked">已是最新</template>
                <template v-else>没检查到新版本</template>
              </div>
            </div>
            <button
              v-if="appUpdate.available"
              v-ripple
              class="probebtn"
              type="button"
              :disabled="appUpdate.busy"
              @click="startAppUpdate"
            >
              {{ appUpdate.busy ? '下载中…' : '更新' }}
            </button>
          </div>
        </div>
      </section>

      <section>
        <h2 class="t-title-s sec">关于</h2>
        <div class="card">
          <div class="row">
            <Icon name="info" />
            <div class="grow t-body-m muted">家庭影院 · 番剧聚合。内容来自第三方网站，仅供局域网内个人使用。</div>
          </div>
          <div v-if="inApp" class="row">
            <Icon name="phone" />
            <div class="grow t-body-m muted">App {{ appVer || '?' }}<template v-if="webVer"> · 界面 {{ webVer }}</template></div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.wrap { max-width: 720px; margin: 0 auto; padding: 0 16px 24px; }
.sec { margin: 22px 6px 10px; color: var(--md-sys-color-primary); }
/* 悬浮卡片（同 base.css 的 .fc-card） */
.card { background: var(--fc-card); border-radius: var(--fc-radius); box-shadow: var(--fc-float); overflow: hidden; }
.row { display: flex; align-items: center; gap: 16px; padding: 14px 16px; min-height: 56px; text-align: left; }
button.row { width: 100%; }
.row + .row { border-top: 1px solid var(--md-sys-color-surface-container-high); }
.row.col { flex-direction: column; align-items: flex-start; gap: 12px; }
.grow { flex: 1; min-width: 0; }
.link { color: inherit; }
.chev { transform: rotate(-90deg); color: var(--md-sys-color-on-surface-variant); }
.swatches { display: flex; gap: 12px; flex-wrap: wrap; }
.swatch { width: 40px; height: 40px; border-radius: 50%; color: #fff; display: flex; align-items: center; justify-content: center; transition: transform var(--md-dur-medium) var(--md-ease-emphasized); }
.swatch.on { transform: scale(1.12); box-shadow: 0 0 0 3px var(--fc-card), 0 0 0 5px var(--md-sys-color-primary); }
.note { margin: 8px 4px 0; }
.textin { width: 100%; box-sizing: border-box; padding: 10px 12px; border-radius: var(--md-shape-md); border: 1px solid var(--md-sys-color-outline); background: var(--md-sys-color-surface); color: var(--md-sys-color-on-surface); font-size: 15px; }
.textin:disabled { opacity: 0.4; }
.probebtn { padding: 9px 18px; border-radius: var(--md-shape-md); border: none; background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); font-size: 14px; flex-shrink: 0; }
.ok { color: var(--md-sys-color-primary); }
.warn { color: var(--md-sys-color-error, #ba1a1a); }
</style>
