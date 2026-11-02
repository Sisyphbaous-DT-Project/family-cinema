import { markRaw, reactive } from 'vue'
import { stopTask } from '../api/manage'
import { toast } from './snackbar'

export type UploadState = 'queued' | 'uploading' | 'done' | 'failed' | 'stopped'

export interface UploadItem {
  id: string
  file: File
  name: string
  size: number
  cat: 'movie' | 'series'
  dest: 'new' | 'existing'
  title: string
  targetId: string
  state: UploadState
  loaded: number
  speed: number
  error: string
  path?: string
  nextTaskId?: string
}

export interface UploadOptions {
  cat: 'movie' | 'series'
  dest: 'new' | 'existing'
  title: string
  targetId: string
}

interface UploadResponse {
  ok?: boolean
  path?: string
  nextTaskId?: string
  error?: string
}

export const uploadItems = reactive<UploadItem[]>([])
let sequence = 0
let current: UploadItem | null = null
let xhr: XMLHttpRequest | null = null
let stopRequested = false
let notifyDone = 0
let notifyFailed = 0

function newId(): string {
  sequence++
  return `upload-${Date.now().toString(36)}-${sequence.toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

const hasPending = () => uploadItems.some((item) => item.state === 'queued' || item.state === 'uploading')

function beforeUnload(event: BeforeUnloadEvent) {
  if (!hasPending()) return
  event.preventDefault()
  event.returnValue = ''
}

function syncBeforeUnload() {
  window.removeEventListener('beforeunload', beforeUnload)
  if (hasPending()) window.addEventListener('beforeunload', beforeUnload)
}

function responseOf(req: XMLHttpRequest): UploadResponse {
  try {
    return JSON.parse(req.responseText || '{}') as UploadResponse
  } catch {
    return {}
  }
}

function settle(item: UploadItem, state: UploadState, response?: UploadResponse, message = '') {
  if (current !== item) return
  const stopped = state === 'failed' && stopRequested
  item.state = stopped ? 'stopped' : state
  item.speed = 0
  item.error = stopped ? '' : message
  if (state === 'done') {
    item.loaded = item.size
    item.path = response?.path
    item.nextTaskId = response?.nextTaskId
    notifyDone++
  } else if (state === 'failed' && !stopped) {
    notifyFailed++
  }
  stopRequested = false
  current = null
  xhr = null
  pump()
}

function notifyBatch() {
  if (!notifyDone && !notifyFailed) return
  if (!notifyFailed) toast('上传完成')
  else if (notifyDone) toast(`上传结束：成功 ${notifyDone} 个，失败 ${notifyFailed} 个`)
  else toast(`${notifyFailed} 个文件上传失败，可以点「重新上传」再试`)
  notifyDone = 0
  notifyFailed = 0
}

function pump() {
  syncBeforeUnload()
  if (current) return
  const item = uploadItems.find((entry) => entry.state === 'queued')
  if (!item) {
    notifyBatch()
    return
  }

  current = item
  item.state = 'uploading'
  item.loaded = 0
  item.speed = 0
  item.error = ''
  // 先问盒子同名文件是不是正在别处上传：传到一半才被拒，浏览器只会报"网络中断"
  void checkUpload(item).then((busy) => {
    if (current !== item || item.state !== 'uploading') return
    if (busy) settle(item, 'failed', undefined, busy)
    else send(item)
  })
}

/** 返回冲突提示；问不到（老版本盒子、网络抖动）就照常上传，盒子收文件时还会再查一遍。 */
async function checkUpload(item: UploadItem): Promise<string> {
  try {
    const res = await fetch('/api/upload/check', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cat: item.cat, dest: item.dest, title: item.title, targetId: item.targetId, name: item.name }),
    })
    if (res.status === 409) return (await res.text()).trim() || '这个文件正在上传，请等它传完'
  } catch {
    // 问不到不拦着
  }
  return ''
}

function send(item: UploadItem) {
  const fd = new FormData()
  // 后端流式读取依赖字段顺序：保存参数、登记信息，最后才是文件。
  fd.append('cat', item.cat)
  fd.append('dest', item.dest)
  fd.append('title', item.title)
  fd.append('targetId', item.targetId)
  fd.append('size', String(item.size))
  fd.append('uploadId', item.id)
  fd.append('file', item.file, item.name)

  const req = new XMLHttpRequest()
  xhr = req
  let lastLoaded = 0
  let lastAt = Date.now()
  req.upload.onprogress = (event) => {
    if (current !== item || !event.lengthComputable) return
    item.loaded = Math.min(event.loaded, item.size)
    const now = Date.now()
    if (now - lastAt >= 800) {
      item.speed = Math.max(0, ((event.loaded - lastLoaded) * 1000) / (now - lastAt))
      lastLoaded = event.loaded
      lastAt = now
    }
  }
  req.onload = () => {
    const response = responseOf(req)
    if (req.status >= 200 && req.status < 300 && response.ok !== false) {
      settle(item, 'done', response)
      return
    }
    const message = response.error || String(req.responseText || `上传失败（${req.status}）`).slice(0, 160)
    settle(item, 'failed', response, message)
  }
  req.onerror = () => settle(item, 'failed', undefined, '网络中断，半截文件已自动清理')
  req.onabort = () => settle(item, 'stopped')
  try {
    req.open('POST', '/api/upload')
    req.send(fd)
  } catch (error) {
    settle(item, 'failed', undefined, error instanceof Error ? error.message : '上传没能开始')
  }
}

export function enqueueUploads(files: File[], options: UploadOptions): { added: number; duplicates: string[] } {
  let added = 0
  const duplicates: string[] = []
  for (const file of files) {
    if (uploadItems.some((item) => item.state !== 'done' && item.name === file.name && item.size === file.size)) {
      duplicates.push(file.name)
      continue
    }
    uploadItems.push({
      id: newId(),
      file: markRaw(file),
      name: file.name,
      size: file.size,
      cat: options.cat,
      dest: options.dest,
      title: options.title,
      targetId: options.targetId,
      state: 'queued',
      loaded: 0,
      speed: 0,
      error: '',
    })
    added++
  }
  if (added) pump()
  return { added, duplicates }
}

export async function stopUpload(id: string): Promise<boolean> {
  const item = uploadItems.find((entry) => entry.id === id)
  if (!item) return false
  if (item.state === 'queued') {
    item.state = 'stopped'
    syncBeforeUnload()
    return true
  }
  if (item.state !== 'uploading' || current !== item) return false
  stopRequested = true
  // 先让盒子停（它会叫醒卡住的接收、删半截文件，记录写成"已停止"）；
  // 盒子没回话也不等太久，最后浏览器自己挂断——用户主动挂断，浏览器不会自动重发
  try {
    await Promise.race([stopTask(id), new Promise((resolve) => setTimeout(resolve, 2000))])
  } catch {
    // 连不上盒子也照样挂断
  }
  const req = xhr
  if (current === item && item.state === 'uploading') settle(item, 'stopped')
  // settle 之后这次请求的回调都会被忽略，下一个文件要先问过盒子才开新请求，不会误挂
  req?.abort()
  return true
}

/** 首页、任务页点"停止任务"时用：本机正在传的走这里，界面才会显示"已停止"而不是"失败"。 */
export function isLocalUpload(id: string): boolean {
  return uploadItems.some((item) => item.id === id && (item.state === 'uploading' || item.state === 'queued'))
}

export function retryUpload(id: string): boolean {
  const item = uploadItems.find((entry) => entry.id === id)
  if (!item || (item.state !== 'failed' && item.state !== 'stopped')) return false
  if (item.state === 'failed' && notifyFailed > 0) notifyFailed--
  item.id = newId()
  item.state = 'queued'
  item.loaded = 0
  item.speed = 0
  item.error = ''
  item.path = undefined
  item.nextTaskId = undefined
  pump()
  return true
}
