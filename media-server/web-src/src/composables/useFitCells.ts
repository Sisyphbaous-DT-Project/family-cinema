import { ref, watch, type Ref, type WatchSource } from 'vue'

// 大字体适配：按格子里内容的真实宽度决定网格每格的最小宽度。
// 安卓把系统字体调大后，WebView 只放大文字，用 px 写的宽度不会跟着变（em 也是按放大前的字号算的），
// 写死宽度的选集方块就装不下"第12集"，只剩"第1…"。所以这里在渲染后直接量实际有多宽。
//
// 用法：网格元素绑 grid；每个格子里的文字包一层 class="lab"（不能被拉伸，宽度要等于文字本身）；
// 格子里其它占位置的元素（比如勾选圈）照实量进去，绝对定位的角标、进度线不算。
// 把返回值设成网格的 --cell-min，CSS 写 minmax(min(var(--cell-min), 上限), 1fr)：
// 上限写 calc(50% - 半个间距) = 最多压到一行两个、再长的名字省略号（番剧在线页，站点集名可能很长）；
// 写 100% = 一行一个也照放（本地详情页，集名后面带"（已缓存）"这类状态字，要完整显示）。
export function useFitCells(
  grid: Ref<HTMLElement | null>,
  opts: {
    base: number // 最小宽度（正常字号时的格子宽度）
    deps?: WatchSource[] // 这些变了就重新量（集数列表、模式切换、状态变化等）
  },
): Ref<number> {
  const min = ref(opts.base)

  function measure() {
    const box = grid.value
    const first = box?.firstElementChild
    if (!box || !first) return
    const cs = getComputedStyle(first)
    const pad = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight)
    const gap = parseFloat(cs.columnGap) || 0
    let need = 0
    for (const cell of Array.from(box.children)) {
      let w = 0
      let n = 0
      for (const c of Array.from(cell.children) as HTMLElement[]) {
        if (getComputedStyle(c).position === 'absolute') continue
        // 被省略号截断时 scrollWidth 仍是文字的完整宽度
        w += c.classList.contains('lab') ? c.scrollWidth : c.getBoundingClientRect().width
        n++
      }
      need = Math.max(need, w + gap * Math.max(0, n - 1))
    }
    // +1：scrollWidth 是取整后的值，留 1px 防止小数部分又触发省略号
    min.value = Math.max(opts.base, Math.ceil(need + pad + 1))
  }

  // 网格元素本身也在监听列表里：v-if 切回来重新挂载时会重新量
  watch([grid, ...(opts.deps ?? [])], measure, { flush: 'post' })
  return min
}
