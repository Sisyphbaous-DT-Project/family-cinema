import { ref } from 'vue'

/** 当前页面转场动画名（见 styles/base.css） */
export const transitionName = ref('fade-through')

/** 每次从一级页面进入搜索页就 +1，搜索页据此清空上次的关键词和结果 */
export const searchEpoch = ref(0)
