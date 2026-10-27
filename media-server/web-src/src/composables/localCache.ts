// 本地片库条目的缓存小工具：详情页的"当前集操作条"和选集区的多选缓存共用
import { coverURL, epLabel, type LocalItem } from '../api/local'
import { cacheState, downloadEpisode, resumeByPath, type CacheState } from './cache'

/** 每种缓存状态写在集名后面的字（给中老年人看：直接写字，不靠小图标） */
export const STATE_TEXT: Record<CacheState['kind'], string> = {
  none: '',
  queued: '排队中',
  running: '下载中',
  paused: '已暂停',
  done: '已缓存',
}

/**
 * 把一集加入缓存：没缓存的新下，暂停的接着下（断点续传）；已缓存/排队中/下载中的不动。
 * 返回是否真的发起了。只在"App 内且在线且不是缓存条目"时调用方该出现入口。
 */
export function cacheLocalEp(it: LocalItem, i: number, fromCache: boolean): boolean {
  const e = it.episodes[i]
  if (!e) return false
  const k = cacheState(e.path).kind
  if (k === 'paused') {
    resumeByPath(e.path)
    return true
  }
  if (k !== 'none') return false
  downloadEpisode({
    itemTitle: it.title,
    epPath: e.path,
    epSize: e.size,
    epTitle: epLabel(e, i, it.episodes.length, fromCache),
    coverAbsUrl: it.cover ? location.origin + coverURL(it.cover) : '',
    category: it.category || 'movie',
  })
  return true
}
