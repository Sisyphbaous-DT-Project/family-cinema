// 下载多选时每集有多大：MP4 直接问文件大小；HLS 第一集逐段量准，同部后面的集按时长和码率估算。
// 量好一集显示一集；还没量到的先按已经量出来的平均值估个数（标"约"），全选就是整部的大小。
import { computed, reactive, ref, watch, type Ref } from 'vue'
import { followApi } from '../api/follow'
import { fmtSize } from '../api/local'
import { fetchDisk } from '../api/manage'
import type { usePlayback } from './usePlayback'

interface SizeState {
  status: 'loading' | 'ok' | 'fail'
  bytes: number
  exact: boolean
  reason?: string
}

// 按"片源|播放页地址"记着（盒子那边也记 6 小时），退出多选再进来不用重新量
const sizes = reactive(new Map<string, SizeState>())

export interface EpSize {
  text: string // "245 MB" / "约 350 MB" / "计算中…" / ""
  guess: boolean // 还没量到，按别的集估的
}

export function useEpisodeSizes(pb: ReturnType<typeof usePlayback>, active: Ref<boolean>, picked: () => number[]) {
  const disk = ref<{ free: number; freeH: string } | null>(null)
  let ctrl: AbortController | null = null

  const keyOf = (i: number) => {
    const e = pb.currentRoad.value?.episodes[i]
    return e ? pb.sel.rule + '|' + e.url : ''
  }

  async function run() {
    stop()
    const road = pb.currentRoad.value
    const rule = pb.sel.rule
    const ref = pb.currentItem.value?.src ?? ''
    if (!road || !rule) return
    const c = new AbortController()
    ctrl = c
    fetchDisk()
      .then((d) => (disk.value = d))
      .catch(() => (disk.value = null))
    // 正在看的那集先量（播放时地址刚找过，盒子里有记录，一秒就好），再从头往后
    const all = road.episodes.map((_, i) => i)
    const order = road.episodes[pb.sel.ep] ? [pb.sel.ep, ...all.filter((i) => i !== pb.sel.ep)] : all
    let next = 0
    const worker = async () => {
      while (!c.signal.aborted && next < order.length) {
        const e = road.episodes[order[next++]]
        const k = rule + '|' + e.url
        const st = sizes.get(k)
        if (st && st.status !== 'fail') continue
        sizes.set(k, { status: 'loading', bytes: 0, exact: false })
        try {
          const r = await followApi.size(rule, e.url, ref, c.signal)
          sizes.set(k, r.status === 'ok' ? { status: 'ok', bytes: r.bytes, exact: r.exact } : { status: 'fail', bytes: 0, exact: false, reason: r.reason })
        } catch {
          if (c.signal.aborted) {
            sizes.delete(k) // 盒子那边会照样量完记下，下次进来直接拿
            return
          }
          sizes.set(k, { status: 'fail', bytes: 0, exact: false })
        }
      }
    }
    await Promise.all([worker(), worker()])
  }

  function stop() {
    ctrl?.abort()
    ctrl = null
  }

  watch(active, (on) => (on ? void run() : stop()))

  // 已经量出来的平均每集多大（给还没量到的集估个数）
  const avg = computed(() => {
    let n = 0
    let sum = 0
    for (let i = 0; i < (pb.currentRoad.value?.episodes.length ?? 0); i++) {
      const st = sizes.get(keyOf(i))
      if (st?.status === 'ok') {
        n++
        sum += st.bytes
      }
    }
    return n ? sum / n : 0
  })

  function sizeOf(i: number): EpSize {
    const st = sizes.get(keyOf(i))
    if (st?.status === 'ok') return { text: (st.exact ? '' : '约 ') + fmtSize(st.bytes), guess: false }
    if (avg.value) return { text: '约 ' + fmtSize(avg.value), guess: true }
    if (st?.status === 'fail') return { text: '大小未知', guess: true }
    return { text: '计算中…', guess: true }
  }

  // 选中的几集一共多大
  const total = computed(() => {
    let bytes = 0
    let exact = true
    let pending = 0
    let unknown = 0
    for (const i of picked()) {
      const st = sizes.get(keyOf(i))
      if (st?.status === 'ok') {
        bytes += st.bytes
        exact &&= st.exact
        continue
      }
      exact = false
      if (st?.status !== 'fail') pending++
      if (avg.value) bytes += avg.value
      else unknown++
    }
    return { bytes, exact, pending, unknown }
  })

  const totalText = computed(() => {
    const t = total.value
    if (!picked().length) return ''
    if (t.unknown === picked().length) return t.pending ? '大小计算中…' : ''
    return (t.exact ? '共 ' : '约 ') + fmtSize(t.bytes)
  })

  // 盒子留 2GB 余量（分段和拼好的 mp4 会临时各占一份）
  const tooBig = computed(() => !!disk.value && total.value.bytes > 0 && total.value.bytes + (2 << 30) > disk.value.free)

  // 这个站要手机帮忙才找得到地址，而手机没开着：大小量不出来
  const needPhone = computed(() => {
    for (let i = 0; i < (pb.currentRoad.value?.episodes.length ?? 0); i++) {
      if (sizes.get(keyOf(i))?.reason?.includes('手机')) return true
    }
    return false
  })

  // 每集状态的签名：只在"出了结果"时变化，给大字体适配决定要不要重新量格子
  const signature = computed(() => (pb.currentRoad.value?.episodes ?? []).map((_, i) => sizeOf(i).text).join(','))

  return { sizeOf, total, totalText, disk, tooBig, needPhone, signature, stop }
}
