// 番剧几点更新：盒子按 bangumi-data 算好北京时间；查不到的退回 Bangumi 资料里写的"放送星期"（只有星期几）
import { computed, ref, type Ref } from 'vue'
import { airText, cnText, followApi, type AirView } from '../api/follow'
import type { Subject } from '../api/types'

// 查过的都记着（null = 盒子那边没有这部番的时间），切页面回来不用再查
const cache = new Map<number, AirView | null>()

/** 一次查一批（时间表一天几十部），已经查过的不再问 */
export async function loadAirtimes(ids: number[], signal?: AbortSignal): Promise<Map<number, AirView | null>> {
  const need = [...new Set(ids)].filter((id) => !cache.has(id))
  for (let i = 0; i < need.length; i += 150) {
    const part = need.slice(i, i + 150)
    try {
      const r = await followApi.airtime(part, signal)
      for (const id of part) cache.set(id, r[String(id)] ?? null)
    } catch {
      /* 盒子连不上就不显示时间，下次再查 */
    }
  }
  return cache
}

export const airtimeOf = (id: number) => cache.get(id) ?? null

/** 详情页/播放页用：这部番的更新时间文案 */
export function useAirtime(id: number, subject: Ref<Subject | null>) {
  const air = ref<AirView | null>(cache.get(id) ?? null)
  if (!cache.has(id)) void loadAirtimes([id]).then(() => (air.value = cache.get(id) ?? null))
  // Bangumi 资料里的"放送星期"（如"星期三"），只有星期几
  const weekday = computed(() => subject.value?.infobox.find((x) => x.key === '放送星期')?.value.replace('星期', '周') ?? '')
  const line = computed(() => {
    const a = air.value
    if (!a) return weekday.value ? `每${weekday.value}更新` : ''
    return a.ended && a.every > 0 ? `已完结（原来${airText(a)}）` : airText(a)
  })
  const cn = computed(() => (air.value ? cnText(air.value) : ''))
  const ended = computed(() => !!air.value?.ended)
  return { air, line, cn, ended }
}
