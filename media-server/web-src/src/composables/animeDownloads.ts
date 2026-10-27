import { computed } from 'vue'
import type { Episode } from '../api/types'
import type { usePlayback } from './usePlayback'
import { useServerTasks } from './serverTasks'

export interface AnimeEpisodeState {
  kind: '' | 'queued' | 'resolving' | 'running' | 'merging' | 'done' | 'error'
  text: string
  pct: number
}

const NONE: AnimeEpisodeState = { kind: '', text: '', pct: 0 }

// 与后端 episodeNumber 保持一致：集名认不出数字时按当前线路的位置编号。
function episodeFile(e: Episode, index: number): string {
  const m = e.name.trim().match(/^(?:第\s*)?(?:ep?\s*)?0*(\d{1,4})\s*(?:集|话|話)?$/i)
  let n = m ? Number(m[1]) : index + 1
  if (!Number.isInteger(n) || n <= 0) n = index + 1
  return `第${String(n).padStart(2, '0')}集.mp4`
}

export function useAnimeDownloads(pb: ReturnType<typeof usePlayback>) {
  const { tasks, refreshTasks } = useServerTasks()
  const related = computed(() => tasks.value.filter((t) =>
    t.animeRule === pb.sel.rule && t.animeRef === (pb.currentItem.value?.src ?? ''),
  ))
  const activeTask = computed(() => related.value.find((t) => t.status === 'running' || t.status === 'processing') ?? null)
  const doneCount = computed(() => {
    const t = activeTask.value
    return t?.animeFiles?.filter((f) => t.files?.includes(f)).length ?? 0
  })

  function episodeState(index: number): AnimeEpisodeState {
    const e = pb.currentRoad.value?.episodes[index]
    if (!e) return NONE
    const file = episodeFile(e, index)
    // 新任务在前：同一集重新下载时看最新一次，不让旧失败记录盖住新状态。
    for (const t of related.value) {
      if (!t.animeFiles?.includes(file)) continue
      if (t.files?.includes(file)) return { kind: 'done', text: '已下载', pct: 100 }
      if (t.animeFailedFiles?.includes(file)) return { kind: 'error', text: '失败', pct: 0 }
      if (t.status !== 'running' && t.status !== 'processing') continue
      // 盒子同时下 2 集：在 animeActive 里的才是正在处理的，其余选中的都在排队
      const a = t.animeActive?.find((x) => x.file === file)
      if (!a) return { kind: 'queued', text: '排队中', pct: 0 }
      if (a.state === 'merging') {
        const pct = Math.max(0, Math.min(100, a.mergeProgress || 0))
        return { kind: 'merging', text: a.mergeFinal ? '收尾中' : '拼接中', pct }
      }
      if (a.state === 'mergewait') return { kind: 'merging', text: '等拼接', pct: 0 }
      if (a.state === 'resolving') return { kind: 'resolving', text: '找地址', pct: 0 }
      if (a.state === 'phone') return { kind: 'resolving', text: '等手机', pct: 0 }
      const pct = Math.max(0, Math.min(100, a.progress || 0))
      return { kind: 'running', text: '下载中', pct }
    }
    return NONE
  }

  // 百分比每秒变不用重新量格子；只有状态字变化时才重新适配大字体。
  const stateSignature = computed(() =>
    (pb.currentRoad.value?.episodes ?? []).map((_, i) => episodeState(i).kind).join(','),
  )

  return { activeTask, doneCount, episodeState, stateSignature, refreshTasks }
}
