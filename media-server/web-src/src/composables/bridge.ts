// 与安卓 App 原生桥（window.AndroidBridge）的对接；普通浏览器里没有桥，全部自动降级。
interface NativeBridge {
  setOrientation?: (o: string) => void
  setKeepScreenOn?: (on: boolean) => void
  /** App 1.5 起：把手机状态栏、底部手势条刷成页面底色（dark=深色模式，用白色图标） */
  setSystemBars?: (color: string, dark: boolean) => void
  startSniffWorker?: () => string
  stopSniffWorker?: () => string
  isSniffWorkerRunning?: () => string
  openExternal?: (url: string) => void
  // Bangumi 网络（自动代理）：配置读写 + 主动探测当前选路
  getBgmProxy?: () => string
  setBgmProxy?: (auto: boolean, addr: string) => void
  bgmProbe?: () => string
  // 应用内更新：取版本（含 code）、下载并跳安装器
  getVersion?: () => string
  downloadAndInstallApk?: (url: string) => string
  // 在线状态与服务器地址（App 启动时探测一次，之后不变）
  isOnline?: () => string
  getServerAddr?: () => string
  setServerAddr?: (addr: string) => void
  discoverServer?: () => string
  // 离线缓存（App 专属）：清单 / 占用 / 下载 / 删除 / 本地播放与封面地址
  listCached?: () => string
  getCacheSize?: () => string
  getLocalLibrary?: () => string
  playLocalUrl?: (epId: string) => string
  coverUrl?: (epId: string) => string
  downloadVideo?: (
    url: string,
    title: string,
    itemId: string,
    epId: string,
    size: string,
    coverUrl: string,
    category: string,
    epTitle: string,
  ) => string
  deleteCached?: (epId: string) => string
  // 暂停/取消下载（App 1.3 起才有；旧 App 没有这两个方法，界面要先判断再显示按钮）
  pauseDownload?: (epId: string) => string
  cancelDownload?: (epId: string) => string
  // 后台缓存体检：电池白名单 / 通知权限
  getBackgroundStatus?: () => string
  requestBatteryWhitelist?: () => string
  openAppSettings?: () => string
  // 追番提醒（App 1.8 起）：有追番时定时去盒子取更新消息并发通知
  setFollowWatch?: (on: boolean) => void
  /** {notify, battery, watching, lastCheck(毫秒), lastError} */
  getFollowNotifyStatus?: () => string
  testFollowNotify?: () => string
  /** 小米等手机的"自启动"设置页（找不到就打开应用详情页） */
  openAutostartSettings?: () => string
}

function bridge(): NativeBridge | null {
  return (window as unknown as { AndroidBridge?: NativeBridge }).AndroidBridge ?? null
}

export const hasBridge = (): boolean => !!bridge()

/** 当前 App 是否支持暂停/取消下载（App 1.3 起）。界面热更新比 App 本体先到，所以按钮必须先判断 */
export const canControlDownload = (): boolean => !!bridge() && typeof bridge()!.pauseDownload === 'function'

/** 安全调原生方法：桥不存在或调用出错都返回 undefined */
export function bcall<T = unknown>(fn: (b: NativeBridge) => T): T | undefined {
  const b = bridge()
  if (!b) return undefined
  try {
    return fn(b)
  } catch {
    return undefined
  }
}

/** 调返回 JSON 字符串的原生方法：解析失败或没有桥都返回 fallback */
export function bjson<T>(fn: (b: NativeBridge) => string | undefined, fallback: T): T {
  const raw = bcall(fn)
  if (!raw) return fallback
  try {
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

/** 横屏：App 走原生（系统旋转锁定也生效），浏览器退化为网页接口 */
export function lockLandscape(): void {
  if (hasBridge()) {
    bcall((b) => b.setOrientation?.('landscape'))
    return
  }
  const o = (screen as unknown as { orientation?: { lock?: (s: string) => Promise<void> } }).orientation
  o?.lock?.('landscape').catch(() => {})
}

export function unlockOrientation(): void {
  if (hasBridge()) {
    bcall((b) => b.setOrientation?.('portrait'))
    return
  }
  const o = (screen as unknown as { orientation?: { unlock?: () => void } }).orientation
  o?.unlock?.()
}

// 播放期间屏幕常亮：App 走原生 FLAG_KEEP_SCREEN_ON（内网 http 下唯一可靠的手段），
// 浏览器顺带试 Wake Lock API（非安全上下文会静默失败，无妨）。
// 策略：播放器打开就常亮（暂停也算），关掉播放器立即恢复息屏。
let wakeLock: { release: () => Promise<void> } | null = null
let wantAwake = false

export function acquireWakeLock(): void {
  wantAwake = true
  bcall((b) => b.setKeepScreenOn?.(true))
  const nav = navigator as unknown as { wakeLock?: { request: (t: string) => Promise<{ release: () => Promise<void> }> } }
  if (nav.wakeLock && !wakeLock) {
    nav.wakeLock.request('screen').then((l) => (wakeLock = l)).catch(() => {})
  }
}

export function releaseWakeLock(): void {
  wantAwake = false
  bcall((b) => b.setKeepScreenOn?.(false))
  if (wakeLock) {
    wakeLock.release().catch(() => {})
    wakeLock = null
  }
}

// 页面切后台时系统会自动收回 Wake Lock，回前台时播放器还开着就重新申请
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && wantAwake) acquireWakeLock()
})
