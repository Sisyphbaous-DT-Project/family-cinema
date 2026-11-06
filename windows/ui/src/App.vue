<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { call, connected, onProgress, type Options, type SetupState } from './bridge'
import { theme, type ThemeMode } from './theme'
import Icon from './Icon.vue'
import DirectoryField from './components/DirectoryField.vue'
import ComponentRow from './components/ComponentRow.vue'
import ToggleRow from './components/ToggleRow.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'

const options = reactive<Options>({
  Root: '', Media: '', Pansou: true, FFmpeg: false, Lan: true, AutoStart: false, Port: 8088, PansouPort: 18888,
})
const state = ref<SetupState>()
const loading = ref(true)
const checkingRoot = ref(false)
const working = ref<'install' | 'start' | 'stop' | ''>('')
const cancelling = ref(false)
const operation = ref<'idle' | 'success' | 'failed' | 'cancelled'>('idle')
const operationMessage = ref('')
const initError = ref('')
const progressMessage = ref('')
const logs = ref<{ time: string; message: string }[]>([])
const logsOpen = ref(false)
const logBody = ref<HTMLElement>()
const themeMenu = ref(false)
const dialog = ref<'ffmpeg' | 'firewall' | ''>('')
const snackbar = ref('')
let snackbarTimer: ReturnType<typeof setTimeout> | undefined
let rootRequest = 0
const busy = computed(() => !!working.value)
const disabled = computed(() => busy.value || loading.value || checkingRoot.value || !!initError.value)
const sameRoot = computed(() => !!state.value && normalize(options.Root) === normalize(state.value.options.Root))
const installed = computed(() => sameRoot.value && !!state.value?.installed)
const running = computed(() => installed.value && !!state.value?.running)
const activeLan = computed(() => installed.value && !!state.value?.options.Lan)
const ffmpegAvailable = computed(() => !!state.value?.ffmpegAvailable)
const warnings = computed(() => state.value?.warnings ?? [])
const rootError = computed(() => loading.value || !options.Root ? '' : pathError(options.Root))
const mediaError = computed(() => loading.value || !options.Media ? '' : pathError(options.Media))
const valid = computed(() => !!options.Root.trim() && !!options.Media.trim() && !rootError.value && !mediaError.value
  && Number.isInteger(options.Port) && options.Port >= 1024 && options.Port <= 65505)
const status = computed(() => initError.value ? '无法读取状态' : loading.value ? '正在检查'
  : busy.value ? ({ install: '正在安装', start: '正在启动', stop: '正在停止' }[working.value as 'install' | 'start' | 'stop'])
    : running.value ? '运行中' : installed.value ? '已停止' : '尚未安装')
const footerTitle = computed(() => cancelling.value ? '正在取消' : busy.value ? status.value
  : operation.value === 'success' ? (warnings.value.length ? '已完成，部分组件需要重试' : operationMessage.value)
    : operation.value === 'failed' ? '操作未完成' : operation.value === 'cancelled' ? '操作已取消'
      : installed.value ? '可以更新或修复现有安装' : '准备好后即可安装')
const footerDescription = computed(() => busy.value ? progressMessage.value || '请稍候'
  : operation.value === 'failed' ? operationMessage.value : operation.value === 'cancelled' ? '可以调整选项后重新开始'
    : '主程序与手机 App 已包含，扩展组件按需安装')
const themeModes: { value: ThemeMode; title: string }[] = [
  { value: 'auto', title: '跟随系统' }, { value: 'light', title: '浅色' }, { value: 'dark', title: '深色' },
]

function normalize(path: string) { return path.trim().replace(/[\\/]+$/, '').toLowerCase() }
function pathError(path: string): string {
  if (!/^(?:[a-z]:[\\/].+|\\\\[^\\]+\\[^\\]+\\.+)/i.test(path.trim())) return '请选择一个专用文件夹，不要选择磁盘根目录'
  return ''
}
function notify(message: string) {
  snackbar.value = message
  clearTimeout(snackbarTimer)
  snackbarTimer = setTimeout(() => { snackbar.value = '' }, 6000)
}
function addLog(message: string) {
  logs.value.push({ time: new Date().toLocaleTimeString('zh-CN', { hour12: false }), message })
  if (logs.value.length > 600) logs.value.splice(0, logs.value.length - 600)
  nextTick(() => { if (logBody.value) logBody.value.scrollTop = logBody.value.scrollHeight })
}
const unsubscribe = onProgress(message => { progressMessage.value = message; addLog(message) })
function acceptState(result: SetupState, replaceOptions = true) {
  state.value = result
  if (replaceOptions) Object.assign(options, result.options)
}
async function initialize() {
  loading.value = true
  initError.value = ''
  try { acceptState(await call('init', {})) }
  catch (error) { initError.value = errorText(error); addLog(initError.value) }
  finally { loading.value = false }
}
async function checkRoot() {
  if (disabled.value || !options.Root.trim() || sameRoot.value || pathError(options.Root)) return
  const request = ++rootRequest
  checkingRoot.value = true
  try {
    const result = await call('init', { root: options.Root.trim() })
    if (request === rootRequest) acceptState(result, result.installed)
  } catch (error) { notify(errorText(error)); addLog(errorText(error)) }
  finally { if (request === rootRequest) checkingRoot.value = false }
}
async function browse(kind: 'root' | 'media') {
  if (disabled.value) return
  try {
    const key = kind === 'root' ? 'Root' : 'Media'
    options[key] = await call('browse', { kind, path: options[key] })
    if (kind === 'root') await checkRoot()
  } catch (error) { notify(errorText(error)); addLog(errorText(error)) }
}
function selectFFmpeg(value: boolean) {
  if (value) dialog.value = 'ffmpeg'
  else options.FFmpeg = false
}
function errorText(error: unknown): string { return error instanceof Error ? error.message : String(error) }
async function perform(action: 'install' | 'start' | 'stop') {
  if (disabled.value) return
  if (action === 'install' && !valid.value) { notify('请检查安装位置、媒体位置和网页端口'); return }
  working.value = action
  operation.value = 'idle'
  progressMessage.value = ''
  cancelling.value = false
  addLog(action === 'install' ? '开始安装或更新' : action === 'start' ? '启动家庭影院' : '停止家庭影院')
  try {
    const result = action === 'install' ? await call('install', { ...options, Root: options.Root.trim(), Media: options.Media.trim() })
      : await call(action, { root: options.Root.trim() })
    acceptState(result)
    operation.value = 'success'
    operationMessage.value = action === 'install' ? '家庭影院已准备就绪' : action === 'start' ? '家庭影院已启动' : '家庭影院已停止'
    addLog(operationMessage.value)
    if (warnings.value.length) logsOpen.value = true
  } catch (error) {
    operation.value = cancelling.value ? 'cancelled' : 'failed'
    operationMessage.value = errorText(error)
    addLog(operationMessage.value)
    if (!cancelling.value) { logsOpen.value = true; notify(operationMessage.value) }
  } finally {
    working.value = ''
    cancelling.value = false
  }
}
async function cancel() {
  if (!busy.value || cancelling.value) return
  cancelling.value = true
  try { await call('cancel', {}) }
  catch (error) { cancelling.value = false; notify(errorText(error)); addLog(errorText(error)) }
}
async function command(method: 'openWeb' | 'openLogs' | 'firewall', path: '' | '/api/app/apk' = '') {
  if (disabled.value) return
  try {
    if (method === 'openWeb') await call('openWeb', { root: options.Root.trim(), path })
    else await call(method, { root: options.Root.trim() })
    if (method === 'firewall') notify('已请求允许手机访问，请按 Windows 提示完成确认')
  } catch (error) { notify(errorText(error)); addLog(errorText(error)); logsOpen.value = true }
}
async function copyLogs() {
  try {
    await call('copyText', { text: logs.value.map(line => `${line.time}  ${line.message}`).join('\n').slice(-50000) })
    notify('日志已复制')
  } catch { notify('未能复制日志，可以打开日志文件夹查看') }
}
function confirmDialog() {
  const current = dialog.value
  dialog.value = ''
  if (current === 'ffmpeg') options.FFmpeg = true
  else if (current === 'firewall') void command('firewall')
}
onMounted(initialize)
onBeforeUnmount(() => { unsubscribe(); clearTimeout(snackbarTimer) })
</script>

<template>
  <div class="app-shell" @keydown.esc="themeMenu = false">
    <header class="app-header">
      <div class="brand-symbol"><Icon name="movie" :size="27" /></div>
      <div class="brand-copy">
        <h1>家庭影院</h1>
        <p>Windows 一键部署<span v-if="state?.version" class="version">v{{ state.version.replace(/^v/, '') }}</span></p>
      </div>
      <div class="header-actions">
        <span class="platform-label">Windows 10 / 11 · 64 位</span>
        <div class="theme-control">
          <button class="icon-button" title="外观" aria-label="外观" :aria-expanded="themeMenu" @click="themeMenu = !themeMenu">
            <Icon name="palette" />
          </button>
          <template v-if="themeMenu">
            <button class="menu-scrim" aria-label="关闭外观菜单" tabindex="-1" @click="themeMenu = false"></button>
            <div class="theme-menu" role="menu" aria-label="外观">
              <button v-for="mode in themeModes" :key="mode.value" role="menuitemradio"
                :aria-checked="theme.mode === mode.value" @click="theme.mode = mode.value; themeMenu = false">
                <Icon v-if="theme.mode === mode.value" name="check" :size="18" /><span v-else class="menu-spacer"></span>{{ mode.title }}
              </button>
            </div>
          </template>
        </div>
      </div>
    </header>

    <main class="main-scroll">
      <div v-if="initError" class="connection-error" role="alert">
        <Icon name="error" :size="28" />
        <h2>{{ connected ? '暂时无法读取安装信息' : '请在一键程序中打开' }}</h2>
        <p>{{ initError }}</p>
        <button class="button tonal" @click="initialize"><Icon name="refresh" :size="19" />重试</button>
      </div>
      <div v-else class="workspace">
        <div class="configuration">
          <section class="settings-section">
            <div class="section-heading"><h2>安装位置</h2><span v-if="checkingRoot" class="muted">正在读取安装记录</span></div>
            <div class="directory-fields">
              <DirectoryField v-model="options.Root" label="程序文件夹" :disabled="disabled" :error="rootError"
                @browse="browse('root')" @blur="checkRoot" />
              <DirectoryField v-model="options.Media" label="媒体文件夹" :disabled="disabled" :error="mediaError"
                @browse="browse('media')" />
            </div>
            <p class="section-note">影片和下载保存在媒体文件夹，更新程序时会保留。</p>
          </section>

          <section class="settings-section">
            <div class="section-heading"><h2>安装组件</h2><span class="muted">可随时补装</span></div>
            <div class="component-list">
              <ComponentRow title="家庭影院与手机 App" description="媒体库、网页播放与下载管理" icon="movie" required />
              <ComponentRow v-model="options.Pansou" title="网盘搜索" description="PanSou · 聚合搜索网盘资源" icon="search" :disabled="disabled" />
              <ComponentRow title="声音与字幕处理" :description="ffmpegAvailable
                ? 'FFmpeg · 处理成功后会转换并替换原视频' : '此发行版本暂未提供转码工具'"
                icon="volume" :model-value="options.FFmpeg" :disabled="disabled || !ffmpegAvailable"
                :warning="options.FFmpeg" @update:model-value="selectFFmpeg" />
            </div>
          </section>

          <section class="settings-section preferences">
            <div class="section-heading"><h2>启动与连接</h2></div>
            <ToggleRow v-model="options.Lan" title="允许局域网访问" description="供同一网络的手机和电脑连接" icon="wifi" :disabled="disabled" />
            <ToggleRow v-model="options.AutoStart" title="登录 Windows 后自动启动" icon="schedule" :disabled="disabled" />
            <details class="advanced">
              <summary>高级设置<Icon name="expandMore" :size="20" /></summary>
              <div class="advanced-body">
                <label class="outlined-field port-field">
                  <span class="field-label">网页端口</span>
                  <input v-model.number="options.Port" type="number" min="1024" max="65505" step="1"
                    :disabled="disabled" aria-label="网页端口" />
                </label>
                <p class="muted">默认 8088；被其他程序占用时会自动选择可用端口。</p>
              </div>
            </details>
          </section>
        </div>

        <aside class="service-panel">
          <div class="section-heading">
            <h2>运行状态</h2>
            <button class="icon-button compact" title="刷新运行状态" aria-label="刷新运行状态" :disabled="disabled" @click="initialize">
              <Icon name="refresh" :size="19" />
            </button>
          </div>
          <div class="service-state" :class="{ running, loading: loading || busy }" aria-live="polite">
            <span class="state-dot"></span>{{ status }}
          </div>
          <template v-if="running">
            <p class="address-label">本机访问地址</p>
            <button class="local-address" title="打开家庭影院" @click="command('openWeb')">{{ state?.address || `http://127.0.0.1:${options.Port}` }}</button>
          </template>
          <p v-else class="service-description">{{ installed ? '影片与设置已保留，启动后即可访问。' : '安装完成后，可以在这里启动和管理。' }}</p>
          <div class="service-actions">
            <button class="button filled full" :disabled="disabled || !running" @click="command('openWeb')">
              <Icon name="openInNew" :size="19" />打开影院
            </button>
            <div class="service-controls">
              <button class="button tonal" :disabled="disabled || !installed || running" @click="perform('start')"><Icon name="play" :size="20" />启动</button>
              <button class="button outlined" :disabled="disabled || !running" @click="perform('stop')"><Icon name="pause" :size="18" />停止</button>
            </div>
          </div>
          <div class="phone-section">
            <Icon name="phone" :size="24" />
            <div><h3>在手机上观看</h3><p>{{ running && activeLan ? '连接同一局域网，在 App 中自动查找此电脑。' : running ? '手机访问尚未启用，可调整左侧选项后更新安装。' : '启动影院后，即可获取手机安装包。' }}</p></div>
          </div>
          <button class="button outlined full" :disabled="disabled || !running" @click="command('openWeb', '/api/app/apk')">
            <Icon name="download" :size="19" />下载手机 App
          </button>
          <button class="button text full phone-access" :disabled="disabled || !installed || !activeLan" @click="dialog = 'firewall'">
            <Icon name="wifi" :size="19" />允许手机访问
          </button>
          <p v-if="activeLan" class="permissions-note">手机连不上时，可在这里配置 Windows 访问权限。</p>
          <div v-if="warnings.length" class="component-warnings" role="status">
            <Icon name="info" :size="20" /><div><h3>部分组件需要重试</h3><p v-for="warning in warnings" :key="warning">{{ warning }}</p></div>
          </div>
        </aside>

        <section class="log-section" :class="{ expanded: logsOpen }">
          <div class="log-heading">
            <button class="log-toggle" :aria-expanded="logsOpen" @click="logsOpen = !logsOpen">
              <Icon name="list" :size="21" /><span>操作日志</span>
              <span v-if="logs.length" class="log-count">{{ logs.length }}</span>
              <Icon name="expandMore" :size="20" />
            </button>
            <div class="log-tools">
              <button v-if="logsOpen" class="icon-button compact" title="复制操作日志" aria-label="复制操作日志" :disabled="!logs.length" @click="copyLogs"><Icon name="copy" :size="18" /></button>
              <button class="button text small" :disabled="disabled || !options.Root" @click="command('openLogs')"><Icon name="folder" :size="18" />日志文件夹</button>
            </div>
          </div>
          <div v-if="logsOpen" ref="logBody" class="log-body" tabindex="0" aria-label="操作日志">
            <p v-if="!logs.length" class="log-empty">操作开始后，详细记录会显示在这里。</p>
            <div v-for="(line, index) in logs" :key="index" class="log-line"><span>{{ line.time }}</span><span>{{ line.message }}</span></div>
          </div>
        </section>
      </div>
    </main>

    <footer class="action-bar" :class="{ 'has-error': operation === 'failed' }">
      <div v-if="busy" class="linear-progress" role="progressbar" :aria-label="footerTitle" :aria-valuetext="progressMessage"><span></span></div>
      <div class="action-state" aria-live="polite">
        <Icon v-if="operation === 'success'" name="check" :size="23" />
        <Icon v-else-if="operation === 'failed'" name="error" :size="23" />
        <span v-else-if="busy" class="spinner"></span>
        <div><strong>{{ footerTitle }}</strong><p :title="footerDescription">{{ footerDescription }}</p></div>
      </div>
      <button v-if="busy" class="button outlined cancel-button" :disabled="cancelling" @click="cancel"><Icon name="close" :size="19" />{{ cancelling ? '正在取消' : '取消操作' }}</button>
      <button v-else class="button filled install-button" :disabled="disabled || !valid" @click="perform('install')">
        <Icon :name="installed ? 'refresh' : 'download'" :size="20" />{{ installed ? '安装 / 更新' : '开始安装' }}
      </button>
    </footer>

    <ConfirmDialog :open="!!dialog" :title="dialog === 'ffmpeg' ? '启用声音与字幕处理？' : '允许同一网络的手机访问？'"
      :confirm="dialog === 'ffmpeg' ? '启用处理' : '继续'" :icon="dialog === 'ffmpeg' ? 'volume' : 'wifi'" @cancel="dialog = ''" @confirm="confirmDialog">
      <p v-if="dialog === 'ffmpeg'">安装 FFmpeg 后，家庭影院会自动处理不兼容的声音与字幕。处理并校验成功后会替换原视频，请先保留需要的原片备份。</p>
      <p v-else>接下来 Windows 会请求管理员确认，仅为专用网络中的同一子网放行家庭影院。请在系统提示中完成确认。</p>
    </ConfirmDialog>
    <Transition name="snackbar"><div v-if="snackbar" class="snackbar" role="status"><span>{{ snackbar }}</span><button class="icon-button compact" title="关闭提示" aria-label="关闭提示" @click="snackbar = ''"><Icon name="close" :size="18" /></button></div></Transition>
  </div>
</template>
