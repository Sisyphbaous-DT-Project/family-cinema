// 开发用假桥：只在 `npx vite` 开发模式生效（import.meta.env.DEV，正式构建会被摇树干掉）。
// 模拟安卓 App 的 window.AndroidBridge，让电脑浏览器也能把缓存/离线 UI 走通。
// 数据放 localStorage（键 mockbridge.*），页面刷新不丢。
//
// 用法：开发服务器打开页面时加查询参数控制场景：
//   ?mockbridge=1        在线（默认模拟 App 1.4）
//   ?mockbridge=offline  离线模式（isOnline() 返回 "0"）
//   &app13=1             模拟 App 1.3：有暂停/取消，但清单里没有"排队中/手动暂停"标记
//   &oldapp=1            模拟 App 1.2：连暂停/取消都没有
// 不加参数 = 无桥（纯浏览器行为）。
interface MockEp {
  itemId: string
  epId: string
  title: string
  epTitle: string
  path: string
  size: number
  done: boolean
  speed: number
  category: string
  progress: number
  queued?: boolean
  paused?: boolean
}

const LS_LIST = 'mockbridge.list'
const LS_ADDR = 'mockbridge.addr'

function readList(): MockEp[] {
  try {
    return JSON.parse(localStorage.getItem(LS_LIST) || '[]')
  } catch {
    return []
  }
}
function writeList(l: MockEp[]) {
  localStorage.setItem(LS_LIST, JSON.stringify(l))
}

const qs = new URLSearchParams(location.search)
const offline = qs.get('mockbridge') === 'offline'
const oldApp = !!qs.get('oldapp')
const v14 = !oldApp && !qs.get('app13') // 1.4：加入队列就进清单，手动暂停有标记

// 假下载：照真的 DownloadService 的行为模拟——
//  - 单线程排队：一次只下一集；1.4 起排队的集立刻以 queued=true 出现在清单里，旧版要轮到才写进去
//  - 开始后头一秒速度为 0，之后才有速度；每 300ms 前进 2%，下完标记 done
//  - 暂停/取消：排队中的直接移出队列；正在下的立刻停手，暂停保留进度、取消删掉清单项，然后轮到下一集
const queue: MockEp[] = []
let current: { epId: string; timer: ReturnType<typeof setInterval> } | null = null

const isActive = (epId: string) => current?.epId === epId || queue.some((x) => x.epId === epId)

/** 停掉某一集的下载（排队的移出队列，正在下的停手）；返回是否确有其事 */
function stopDownload(epId: string): boolean {
  let hit = false
  const q = queue.findIndex((x) => x.epId === epId)
  if (q >= 0) {
    queue.splice(q, 1)
    hit = true
  }
  if (current && current.epId === epId) {
    clearInterval(current.timer)
    current = null
    hit = true
    setTimeout(pump, 0)
  }
  return hit
}

/** 同 CacheStore.markQueued：已在队列里/在跑的，只有"刚被手动暂停"的才改成排队中 */
function markQueued(ep: MockEp, already: boolean) {
  const list = readList()
  const old = list.find((x) => x.epId === ep.epId)
  if (!old) list.push({ ...ep, queued: true, paused: false })
  else {
    if (old.done || (already && !old.paused)) return
    old.queued = true
    old.paused = false
    old.speed = 0
  }
  writeList(list)
}

function pump() {
  if (current) return
  const ep = queue.shift()
  if (!ep) return
  const list = readList()
  const old = list.find((x) => x.epId === ep.epId)
  if (old) {
    old.done = false
    old.speed = 0
    old.queued = false
    old.paused = false
  } else {
    list.push({ ...ep, queued: false, paused: false })
  }
  writeList(list)
  const t0 = Date.now()
  let progress = old?.progress || 0
  const timer = setInterval(() => {
    progress = Math.min(1, progress + 0.02)
    const l = readList()
    const cur = l.find((x) => x.epId === ep.epId)
    if (cur) {
      cur.progress = progress
      cur.speed = Date.now() - t0 > 1000 && progress < 1 ? 2.5 * 1024 * 1024 : 0
      if (progress >= 1) cur.done = true
      writeList(l)
    }
    if (progress >= 1) {
      clearInterval(timer)
      current = null
      pump()
    }
  }, 300)
  current = { epId: ep.epId, timer }
}

let followWatching = false

export function installMockBridge(): void {
  const param = qs.get('mockbridge')
  if (!param) return
  // 同真 App 启动时的 resetStalled()：上次被打断的下载留下的旧速度和"排队中"要清掉，界面才会显示"已暂停"
  writeList(readList().map((x) => (x.done ? x : { ...x, speed: 0, queued: false })))

  ;(window as unknown as { AndroidBridge: unknown }).AndroidBridge = {
    isOnline: () => (offline ? '0' : '1'),
    getServerAddr: () => localStorage.getItem(LS_ADDR) || 'http://box.local:8088',
    setServerAddr: (a: string) => localStorage.setItem(LS_ADDR, a),
    discoverServer: () => 'http://box.local:8088',
    getVersion: () => JSON.stringify({ app: v14 ? '1.4-mock' : oldApp ? '1.2-mock' : '1.3-mock', code: v14 ? 5 : oldApp ? 3 : 4, web: 'dev' }),
    getBgmProxy: () => JSON.stringify({ auto: true, addr: '127.0.0.1:7890' }),
    setBgmProxy: () => {},
    bgmProbe: () => JSON.stringify({ route: 'direct' }),
    isSniffWorkerRunning: () => '0',
    startSniffWorker: () => '1',
    stopSniffWorker: () => '1',
    setOrientation: () => {},
    setKeepScreenOn: () => {},
    // App 1.5：状态栏跟着页面底色走。假桥只把最后一次的参数记在 window.__sysBars 上，测试时核对用
    setSystemBars: (color: string, dark: boolean) => {
      ;(window as unknown as { __sysBars: unknown }).__sysBars = { color, dark }
    },
    openExternal: (u: string) => window.open(u),
    getBackgroundStatus: () => JSON.stringify({ battery: true, notify: false }),
    requestBatteryWhitelist: () => '1',
    openAppSettings: () => '1',
    // 追番提醒（模拟 App 1.8）
    setFollowWatch: (on: boolean) => {
      followWatching = on
    },
    getFollowNotifyStatus: () =>
      JSON.stringify({ notify: true, battery: false, watching: followWatching, lastCheck: Date.now() - 600e3, lastError: '' }),
    testFollowNotify: () => 'ok',
    openAutostartSettings: () => '1',
    // 进度（与真桥同格式）
    getProg: (k: string) => localStorage.getItem('mockbridge.' + k) || '',
    setProg: (k: string, pos: number, dur: number) => {
      localStorage.setItem('mockbridge.' + k, JSON.stringify({ pos, dur, ts: Math.floor(Date.now() / 1000) }))
      return '1'
    },
    clearProg: (k: string) => {
      localStorage.removeItem('mockbridge.' + k)
      return '1'
    },
    // 缓存（旧版 App 的清单里没有 queued/paused 字段）
    listCached: () =>
      JSON.stringify(v14 ? readList() : readList().map(({ queued: _q, paused: _p, ...x }) => x)),
    getCacheSize: () => {
      const l = readList().filter((x) => x.done)
      return JSON.stringify({ total: l.reduce((s, x) => s + (x.size || 0), 0), count: l.length })
    },
    getLocalLibrary: () => {
      const done = readList().filter((x) => x.done)
      const byItem = new Map<string, MockEp[]>()
      for (const e of done) {
        const arr = byItem.get(e.itemId) ?? []
        arr.push(e)
        byItem.set(e.itemId, arr)
      }
      const items = [...byItem.entries()].map(([itemId, eps]) => ({
        id: 'cache:' + itemId,
        category: eps[0].category || 'movie',
        title: itemId,
        rawTitle: itemId,
        isDir: true,
        episodes: eps.map((e, i) => ({ index: i + 1, title: e.epTitle, filename: e.epTitle, path: e.epId, size: e.size })),
        size: eps.reduce((s, e) => s + (e.size || 0), 0),
        modified: 0,
        cover: eps[0].epId,
      }))
      return JSON.stringify({ items })
    },
    playLocalUrl: (epId: string) => '/api/stream?path=' + encodeURIComponent(epId), // 假桥直接回源真接口
    coverUrl: () => '',
    downloadVideo: (url: string, title: string, itemId: string, epId: string, size: string, _cover: string, category: string, epTitle: string) => {
      const ep: MockEp = { itemId, epId, title, epTitle, path: epId, size: Number(size) || 0, done: false, speed: 0, category, progress: 0 }
      if (v14) markQueued(ep, isActive(epId))
      queue.push(ep)
      pump()
      return '1'
    },
    pauseDownload: (epId: string) => {
      stopDownload(epId)
      // 速度立刻归零；1.4 起还记下"手动暂停"、移出排队中
      writeList(readList().map((x) => (x.epId === epId && !x.done ? { ...x, speed: 0, ...(v14 ? { queued: false, paused: true } : {}) } : x)))
      return '1'
    },
    cancelDownload: (epId: string) => {
      stopDownload(epId)
      writeList(readList().filter((x) => x.epId !== epId))
      return '1'
    },
    deleteCached: (epId: string) => {
      stopDownload(epId) // 和真 App 1.3 起一致：删除 = 先取消再删
      writeList(readList().filter((x) => x.epId !== epId))
      return '1'
    },
  }

  // App 1.2：没有暂停/取消，删除也只删清单
  if (oldApp) {
    const b = (window as unknown as { AndroidBridge: Record<string, unknown> }).AndroidBridge
    delete b.pauseDownload
    delete b.cancelDownload
    b.deleteCached = (epId: string) => {
      writeList(readList().filter((x) => x.epId !== epId))
      return '1'
    }
  }
}
