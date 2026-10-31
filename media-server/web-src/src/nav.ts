import { createRouter, createWebHashHistory } from 'vue-router'
import { searchEpoch, transitionName } from './navState'
import { installViewTransitions, transitionKind } from './composables/motion'
import LibraryView from './views/LibraryView.vue'
import LocalDetailView from './views/LocalDetailView.vue'
import HomeView from './views/HomeView.vue'
import TimelineView from './views/TimelineView.vue'
import SettingsView from './views/SettingsView.vue'
import SearchView from './views/SearchView.vue'
import DetailView from './views/DetailView.vue'
import PlayView from './views/PlayView.vue'
import CacheView from './views/CacheView.vue'
import TasksView from './views/manage/TasksView.vue'
import LibManageView from './views/manage/LibManageView.vue'
import UploadView from './views/manage/UploadView.vue'
import ManualView from './views/manage/ManualView.vue'
import FilesView from './views/manage/FilesView.vue'
import BtSearchView from './views/manage/BtSearchView.vue'
import PanSearchView from './views/PanSearchView.vue'

declare module 'vue-router' {
  interface RouteMeta {
    /** 层级深度：决定转场方向（变深=向前滑入，变浅=向后滑出） */
    depth: number
    /** 是否属于底部导航的一级页面 */
    tab?: boolean
    /** 一级页面在底部导航里的顺序：决定切换时页面往哪边移 */
    order?: number
    /** 本地片库页面显示哪一类：movie=电影电视剧，series=番剧 */
    cat?: 'movie' | 'series'
  }
}

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    // 前两栏：盒子里保存的电影电视剧、番剧（互不混杂）；后面才是在线聚合的推荐/时间表
    { path: '/', name: 'movies', component: LibraryView, meta: { depth: 0, tab: true, order: 0, cat: 'movie' } },
    { path: '/series', name: 'series', component: LibraryView, meta: { depth: 0, tab: true, order: 1, cat: 'series' } },
    { path: '/discover', name: 'discover', component: HomeView, meta: { depth: 0, tab: true, order: 2 } },
    { path: '/timeline', name: 'timeline', component: TimelineView, meta: { depth: 0, tab: true, order: 3 } },
    { path: '/settings', name: 'settings', component: SettingsView, meta: { depth: 0, tab: true, order: 4 } },
    { path: '/search', name: 'search', component: SearchView, meta: { depth: 1 } },
    { path: '/cache', name: 'cache', component: CacheView, meta: { depth: 1 } },
    // 网盘搜索（盘搜），从「电影电视剧」页的搜索框进来
    { path: '/pansou', name: 'pansou', component: PanSearchView, meta: { depth: 1 } },
    // 片库管理（从设置页进入的子页面）
    { path: '/manage', name: 'manage', component: LibManageView, meta: { depth: 1 } },
    { path: '/manage/tasks', name: 'manageTasks', component: TasksView, meta: { depth: 1 } },
    { path: '/manage/upload', name: 'manageUpload', component: UploadView, meta: { depth: 1 } },
    { path: '/manage/manual', name: 'manageManual', component: ManualView, meta: { depth: 1 } },
    { path: '/manage/files', name: 'manageFiles', component: FilesView, meta: { depth: 1 } },
    { path: '/manage/bt', name: 'manageBt', component: BtSearchView, meta: { depth: 1 } },
    { path: '/local/:id', name: 'local', component: LocalDetailView, meta: { depth: 2 } },
    { path: '/subject/:id(\\d+)', name: 'subject', component: DetailView, meta: { depth: 2 } },
    { path: '/play/:id(\\d+)', name: 'play', component: PlayView, meta: { depth: 3 } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior: () => false, // 每个页面自己是滚动容器，切换时位置由 KeepAlive/新实例自然保留
})

// 老内核兜底时 <Transition> 用的动画名
const FALLBACK = { forward: 'page-push', back: 'page-pop', 'tab-next': 'page-tab-next', 'tab-prev': 'page-tab-prev', fade: 'page-fade' } as const

router.beforeEach((to, from) => {
  if (from.name === undefined) {
    transitionName.value = '' // 首次进入不播放转场
    return
  }
  transitionName.value = FALLBACK[transitionKind(to, from)]
  if (to.name === 'search' && from.meta.depth === 0) searchEpoch.value++
})

// 支持视图过渡的内核：换页做成一镜到底（海报飞进详情页、搜索框展开成搜索栏等）
installViewTransitions(router)
