import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

/**
 * 页面往上划过了没有（找 anchor 所在的 .page 滚动容器来听）。
 * 吸顶的栏用它决定要不要显示下边缘的"渐隐带"：没划时内容紧贴着栏，渐隐带会把内容顶部盖淡，所以只在划过后才显示。
 */
export function useScrolled(anchor: Ref<HTMLElement | null | undefined>, threshold = 4): Ref<boolean> {
  const scrolled = ref(false)
  let host: Element | null = null
  const onScroll = () => {
    scrolled.value = !!host && host.scrollTop > threshold
  }
  onMounted(() => {
    host = anchor.value?.closest('.page') ?? null
    host?.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
  })
  onBeforeUnmount(() => host?.removeEventListener('scroll', onScroll))
  return scrolled
}
