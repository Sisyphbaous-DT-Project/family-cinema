import { reactive } from 'vue'
import { bcall, hasBridge } from './bridge'
import { toast } from './snackbar'

// App 应用内更新：拿本地 versionCode 和盒子上的最新版比对，有新版就提示。浏览器里没有桥，整个功能静默跳过。
// 什么时候查：打开 App、从后台切回 App、开着的时候每半小时（一分钟内不重复查）——
// 以前只在网页刚加载时查一次，App 常年挂在后台不重新加载，放到盒子上的新版本就一直发现不了。
// 怎么提示：不弹窗，片库页右上角「我的缓存」旁边常驻「有新版本」按钮，点一下就更新；设置页也有入口。
export const appUpdate = reactive({
  checked: false, // 是否查过
  available: false, // 有新版
  versionCode: 0,
  versionName: '',
  sizeBytes: 0,
  busy: false, // 正在下载/跳转
})

const MIN_GAP = 60 * 1000
const PERIOD = 30 * 60 * 1000
let lastCheck = 0

export async function checkAppUpdate(force = false): Promise<void> {
  if (!hasBridge()) return
  if (!force && Date.now() - lastCheck < MIN_GAP) return
  lastCheck = Date.now()
  let localCode = 0
  try {
    localCode = Number(JSON.parse(bcall((b) => b.getVersion?.()) ?? '{}').code ?? 0)
  } catch {
    /* 旧版 App 没有 code 字段，按 0 算（一定会提示更新，符合预期） */
  }
  try {
    const res = await fetch('/api/app/latest')
    if (!res.ok) return // 盒子没托管安装包，不提示
    const v = (await res.json()) as { versionCode?: number; versionName?: string; sizeBytes?: number }
    appUpdate.checked = true
    const code = v.versionCode ?? 0
    appUpdate.available = code > localCode
    if (!appUpdate.available) return
    appUpdate.versionCode = code
    appUpdate.versionName = v.versionName ?? ''
    appUpdate.sizeBytes = v.sizeBytes ?? 0
  } catch {
    /* 盒子不在线/接口不存在：不提示 */
  }
}

/** 开始盯着新版本（App.vue 挂载时调一次） */
export function watchAppUpdate(): void {
  if (!hasBridge()) return
  checkAppUpdate(true)
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) checkAppUpdate()
  })
  setInterval(() => {
    if (!document.hidden) checkAppUpdate()
  }, PERIOD)
}

/** 开始更新：下载 APK 并跳系统安装器（安装那一下必须用户亲手点，安卓规定） */
export function startAppUpdate(): void {
  if (appUpdate.busy) return
  appUpdate.busy = true
  toast('正在下载新版本，稍等一下…', 8000)
  // 桥调用是同步阻塞的，放下一拍让提示先渲染出来
  setTimeout(() => {
    let r: { ok?: boolean; error?: string } = {}
    try {
      r = JSON.parse(
        bcall((b) => b.downloadAndInstallApk?.(`${location.origin}/api/app/apk`)) ??
          '{"error":"当前 App 版本太旧，不支持在线更新，请用电脑重装一次"}',
      )
    } catch {
      r = { error: '更新通道异常' }
    }
    appUpdate.busy = false
    if (r.ok) toast('已弹出安装界面，点「安装」就更新好了', 6000)
    else toast(r.error || '更新没成功，等一会儿再试试', 5000)
  }, 50)
}
