import { ref } from 'vue'

/** 当前页面转场动画名（老内核兜底用，见 styles/motion.css；支持视图过渡时由 composables/motion.ts 接管） */
export const transitionName = ref('page-fade')

/** 每次从一级页面进入搜索页就 +1，搜索页据此清空上次的关键词和结果 */
export const searchEpoch = ref(0)
