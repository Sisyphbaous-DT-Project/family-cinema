// 追番状态（全站共用一份）：详情页/播放页的追番按钮、时间表顶上的"我追的番"都读这里。
// 清单存在盒子上，手机、电脑看到的是同一份；手机 App 据此决定要不要定时去盒子取更新消息。
import { computed, reactive } from 'vue'
import { followApi, type FollowItem } from '../api/follow'
import { bcall, hasBridge } from './bridge'
import { isOffline } from './cache'
import { toast } from './snackbar'

const state = reactive({ items: [] as FollowItem[], loaded: false })
let loading: Promise<void> | null = null

/** 从盒子重新拉一遍追番清单 */
export function refreshFollows(): Promise<void> {
  if (isOffline()) return Promise.resolve()
  loading ??= followApi
    .list()
    .then((r) => {
      state.items = r.items
      state.loaded = true
      syncApp()
    })
    .catch(() => {
      /* 盒子连不上就保持原样 */
    })
    .finally(() => {
      loading = null
    })
  return loading
}

export function useFollows() {
  if (!state.loaded) void refreshFollows()
  return { items: computed(() => state.items), loaded: computed(() => state.loaded) }
}

export const followOf = (id: number) => state.items.find((f) => f.id === id)
export const isFollowed = (id: number) => state.items.some((f) => f.id === id)

/** App 1.8 起才会提醒；旧 App 没有这个方法 */
export const appCanNotify = () => hasBridge() && !!bcall((b) => typeof b.setFollowWatch === 'function')

// 告诉 App 有没有在追的番：有就定时去盒子取更新消息，没有就不折腾
function syncApp() {
  bcall((b) => b.setFollowWatch?.(state.items.length > 0))
}

interface FollowTarget {
  id: number
  name_cn: string
  name: string
  images?: { common?: string; large?: string }
}

/** 追番 / 取消追番；source 是正在看的片源（播放页里点的就带上，以后按这个站查更新） */
export async function toggleFollow(s: FollowTarget, source?: { rule: string; src: string }): Promise<void> {
  try {
    if (isFollowed(s.id)) {
      await followApi.remove(s.id)
      state.items = state.items.filter((f) => f.id !== s.id)
      toast('已取消追番')
    } else {
      const f = await followApi.add({
        id: s.id,
        title: s.name_cn || s.name,
        name: s.name,
        cover: s.images?.common || s.images?.large || '',
        rule: source?.rule,
        src: source?.src,
      })
      state.items = [f, ...state.items.filter((x) => x.id !== f.id)]
      if (appCanNotify()) toast('追番成功！片源站出了新的一集，手机会通知你')
      else if (hasBridge()) toast('追番成功！把 App 更新到 1.8 就能收到更新通知')
      else toast('追番成功！更新了会在「时间表」最上面标出来')
    }
    syncApp()
  } catch (e) {
    toast((e instanceof Error ? e.message : '') || '没连上盒子，等会儿再试')
  }
}

/** 在某个片源看了这部番：追着的番以后就按这个站查更新 */
export function reportSource(id: number, rule: string, src: string) {
  const f = followOf(id)
  if (!f || !rule || !src || (f.rule === rule && f.src === src && f.pickedBy === 'user')) return
  followApi
    .setSource(id, rule, src)
    .then(() => {
      f.rule = rule
      f.src = src
      f.pickedBy = 'user'
    })
    .catch(() => {
      /* 下次播放再报 */
    })
}

/** 这周的新集已经等到了（最近一次更新在最近一次播出之后；不知道播出时间就看 2 天内） */
export function freshlyUpdated(f: FollowItem): boolean {
  if (!f.updatedAt) return false
  const up = new Date(f.updatedAt).getTime()
  if (f.air?.every && f.air.next) {
    const lastAir = new Date(f.air.next).getTime() - f.air.every * 86400e3
    return up >= lastAir
  }
  return Date.now() - up < 2 * 86400e3
}
