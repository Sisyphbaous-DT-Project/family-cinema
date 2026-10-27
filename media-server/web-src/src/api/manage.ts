// 片库管理相关接口（旧后端原有接口的封装，详见 AGENTS.md §4.2）。
// 全部直接打盒子，离线时用不了——管理页本身就是在线功能。

const enc = encodeURIComponent

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch('/api' + path, init)
  const text = await res.text()
  if (!res.ok) throw new Error(text || `HTTP ${res.status}`)
  return (text ? JSON.parse(text) : {}) as T
}

const post = <T>(path: string, body: unknown): Promise<T> =>
  req<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })

// ---------- 下载任务 ----------
export interface Task {
  id: string
  name: string
  category: 'movie' | 'series'
  status: string // running | done | error | stopped | processing
  progress: number
  speedKBs: number
  peers: number
  bytesDone: number
  bytesTotal: number
  error?: string
  targetDir?: string
  stage?: string
  files?: string[]
  cover?: string
  animeRule?: string
  animeRef?: string
  animeFiles?: string[]
  animeFailedFiles?: string[]
  /** 失败的集和原因（片库页重试按钮旁边的小问号） */
  animeFailures?: { file: string; message: string }[]
  /** 正在处理的几集（盒子同时下 2 集） */
  animeActive?: { file: string; state: 'resolving' | 'phone' | 'running' | 'mergewait' | 'merging'; progress: number }[]
}

export const fetchTasks = () => req<Task[]>('/tasks')
export const stopTask = (id: string) => post<{ ok: boolean }>('/task/stop', { id })
export const clearTaskRecords = () => post<{ cleared: number }>('/manage/clear-tasks', {})

// ---------- 管理（统计/改名/封面/删除） ----------
export interface ManageItem {
  id: string
  title: string
  rawTitle: string
  category: 'movie' | 'series'
  episodes: number
  sizeHuman: string
  hasCover: boolean
  cover?: string
}

export interface ManageStats {
  items: ManageItem[]
  categories: { id: string; count: number }[]
  totalSizeH: string
  missingCover: number
  taskCount: number
}

export const fetchManageStats = () => req<ManageStats>('/manage/stats')
export const renameItem = (id: string, name: string) => post('/manage/rename', { id, name })
export const setItemCover = (id: string, cover: string) => post('/manage/cover', { id, cover })
export const deleteItem = (id: string, force: boolean) => post<{ freedHuman?: string }>('/manage/delete', { id, force })

// ---------- 封面补全 ----------
export const startCoversFetch = () => req('/covers/fetch', { method: 'POST' })
export const coversStatus = () => req<{ running: boolean; done: number; total: number; found: number }>('/covers/status')

// ---------- 服务端下载缓存（.torrent-cache） ----------
export interface CacheEntry {
  name: string
  title: string
  sizeHuman: string
  modified: number
  inUse: boolean
  partial: boolean
  taskName?: string
}
export const fetchServerCache = () => req<{ entries: CacheEntry[]; totalHuman: string; dir: string }>('/manage/cache')
export const deleteServerCache = (name?: string, all = false) =>
  post<{ freedHuman?: string; removed?: string }>('/manage/cache/delete', all ? { all: true } : { name })

// ---------- 手动添加下载 / 可并入条目 ----------
export interface FolderItem {
  id: string
  title: string
  category: 'movie' | 'series'
  episodes: number
  sizeHuman: string
  cover?: string
}
export const fetchFolders = (q = '') => req<FolderItem[]>('/folders?q=' + enc(q))

export interface ManualPayload {
  magnet?: string
  torrent?: string // base64
  title: string
  category?: 'movie' | 'series'
  targetId?: string
  cover?: string
}
export const addManual = (p: ManualPayload) => post<{ id?: string }>('/manual', p)

// ---------- 磁盘 ----------
export const fetchDisk = () => req<{ free: number; freeH: string; totalH: string }>('/disk')

// ---------- 文件管理器 ----------
export interface FsEntry {
  name: string
  path: string
  isDir: boolean
  video: boolean
  sizeHuman: string
  modified: number
  protected: boolean
  inUse: boolean
}
export const fsList = (path: string) => req<{ path: string; root: string; entries: FsEntry[] }>('/fs/list?path=' + enc(path))
export const fsMkdir = (path: string, name: string) => post<{ ok: boolean }>('/fs/mkdir', { path, name })
export const fsRename = (path: string, name: string) => post<{ ok: boolean }>('/fs/rename', { path, name })
export const fsDelete = (path: string, force: boolean) =>
  post<{ ok: boolean; freed?: number; needConfirm?: boolean; dirFiles?: number; dirSizeHuman?: string }>('/fs/delete', {
    path,
    force,
  })

// ---------- BT 搜索与下载 ----------
export interface SearchHit {
  title: string
  image?: string
  local?: { id: string; category: string; cover?: string; episodes?: unknown[] }
  results?: SearchSource[]
  from?: string[]
}
export interface SearchSource {
  site: string
  siteName?: string
  url?: string
  torrents?: TorrentInfo[]
}
export interface TorrentInfo {
  name?: string
  magnet: string
  size?: string
}
export const btSearch = (q: string) => req<SearchHit[]>('/search?q=' + enc(q))
export const btDetail = (site: string, url: string) => req<{ torrents?: TorrentInfo[] }>('/detail?site=' + enc(site) + '&url=' + enc(url))
export const btSpeedtest = (magnet: string) =>
  post<{ mbps?: number; peers?: number; size?: string; error?: string }>('/speedtest', { magnet })
export const btDownload = (magnet: string, title: string, category: string) =>
  post<{ id?: string }>('/download', { magnet, title, category })
