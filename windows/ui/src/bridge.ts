export interface Options {
  Root: string
  Media: string
  Pansou: boolean
  FFmpeg: boolean
  Lan: boolean
  AutoStart: boolean
  Port: number
  PansouPort: number
}

export interface SetupState {
  version: string
  options: Options
  installed: boolean
  running: boolean
  ffmpegAvailable: boolean
  warnings: string[]
  address: string
}

type Methods = {
  init: { args: { root?: string }; result: SetupState }
  browse: { args: { kind: 'root' | 'media'; path: string }; result: string }
  install: { args: Options; result: SetupState }
  start: { args: { root: string }; result: SetupState }
  stop: { args: { root: string }; result: SetupState }
  cancel: { args: Record<string, never>; result: unknown }
  openWeb: { args: { root: string; path: '' | '/api/app/apk' }; result: unknown }
  openLogs: { args: { root: string }; result: unknown }
  copyText: { args: { text: string }; result: unknown }
  firewall: { args: { root: string }; result: unknown }
  close: { args: Record<string, never>; result: unknown }
}
type Message = { id?: number; result?: unknown; error?: unknown; type?: string; message?: string }
type Webview = {
  postMessage: (message: unknown) => void
  addEventListener: (event: 'message', callback: (event: MessageEvent<Message>) => void) => void
}
declare global { interface Window { chrome?: { webview?: Webview } } }
const pending = new Map<number, { resolve: (result: unknown) => void; reject: (error: Error) => void; timer?: ReturnType<typeof setTimeout> }>()
const listeners = new Set<(message: string) => void>()
const webview = window.chrome?.webview
let nextId = 0

webview?.addEventListener('message', ({ data }) => {
  if (!data || typeof data !== 'object') return
  if (data.type === 'progress' && typeof data.message === 'string') {
    listeners.forEach(callback => callback(data.message!))
    return
  }
  if (typeof data.id !== 'number') return
  const request = pending.get(data.id)
  if (!request) return
  pending.delete(data.id)
  clearTimeout(request.timer)
  if (data.error) {
    const error = typeof data.error === 'string' ? data.error : '操作未能完成，请查看日志后重试。'
    request.reject(new Error(error))
  } else request.resolve(data.result)
})

export function onProgress(callback: (message: string) => void): () => void {
  listeners.add(callback)
  return () => listeners.delete(callback)
}

export const connected = !!webview || import.meta.env.DEV

export function call<K extends keyof Methods>(method: K, args: Methods[K]['args']): Promise<Methods[K]['result']> {
  if (!webview) {
    if (import.meta.env.DEV) return mockCall(method, args) as Promise<Methods[K]['result']>
    return Promise.reject(new Error('未连接到安装程序。请关闭此页面，重新打开 Windows 一键程序。'))
  }
  const id = ++nextId
  return new Promise((resolve, reject) => {
    const request: { resolve: (result: unknown) => void; reject: (error: Error) => void; timer?: ReturnType<typeof setTimeout> } = {
      resolve: result => resolve(result as Methods[K]['result']), reject,
    }
    if (method === 'init') {
      request.timer = setTimeout(() => {
        pending.delete(id)
        reject(new Error('读取安装状态超时，请稍后重试或重新打开程序。'))
      }, 15000)
    }
    pending.set(id, request)
    try { webview.postMessage({ id, method, args }) }
    catch {
      pending.delete(id)
      clearTimeout(request.timer)
      reject(new Error('未能发送操作，请重新打开安装程序。'))
    }
  })
}

// 预览数据仅在开发构建中存在，正式发行始终等待宿主返回真实状态。
let mockState: SetupState | undefined
let cancelled = false
async function mockCall(method: keyof Methods, args: unknown): Promise<unknown> {
  const scenario = new URLSearchParams(location.search).get('state')
  mockState ??= {
    version: '1.11.0',
    options: { Root: 'C:\\Users\\User\\AppData\\Local\\FamilyCinema', Media: 'D:\\FamilyCinema',
      Pansou: true, FFmpeg: false, Lan: true, AutoStart: false, Port: 8088, PansouPort: 18888 },
    installed: ['installed', 'running', 'warning'].includes(scenario ?? ''),
    running: ['running', 'warning'].includes(scenario ?? ''),
    ffmpegAvailable: scenario !== 'unavailable',
    warnings: scenario === 'warning' ? ['盘搜下载未完成，主程序可正常使用。请点击“安装 / 修复”重试。'] : [],
    address: '',
  }
  if (method === 'init') {
    if (scenario === 'offline') throw new Error('安装记录暂时无法读取，请重新打开程序后重试。')
    if ((args as { root?: string }).root) mockState.options.Root = (args as { root: string }).root
    return snapshot()
  }
  if (method === 'browse') return (args as { path: string }).path
  if (method === 'cancel') { cancelled = true; return null }
  if (method === 'install') {
    cancelled = false
    const steps = ['正在检查安装位置', '镜像测速：正在选择可用下载源', '下载主程序源码', '正在校验下载文件',
      '正在编译家庭影院', '正在准备网盘搜索', '正在下载手机 App', '正在检查服务']
    for (const step of steps) {
      if (cancelled) throw new Error('操作已取消，本次临时文件已清理。')
      listeners.forEach(listener => listener(step))
      await new Promise(resolve => setTimeout(resolve, 350))
    }
    if (cancelled) throw new Error('操作已取消，本次临时文件已清理。')
    if (scenario === 'error') throw new Error('源码下载未完成，请检查网络后重试。原有安装未被替换。')
    mockState.options = { ...(args as Options) }
    mockState.installed = true
    mockState.running = true
    return snapshot()
  }
  if (method === 'start') mockState.running = true
  if (method === 'stop') mockState.running = false
  return ['start', 'stop'].includes(method) ? snapshot() : null
}

function snapshot(): SetupState {
  const state = mockState!
  return { ...state, options: { ...state.options },
    address: state.running ? `http://127.0.0.1:${state.options.Port}` : '' }
}
