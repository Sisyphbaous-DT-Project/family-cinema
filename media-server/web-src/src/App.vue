<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'
import NavBar from './components/m3/NavBar.vue'
import Snackbar from './components/m3/Snackbar.vue'
import { transitionName } from './navState'
import { useViewTransition } from './composables/motion'
import { watchAppUpdate } from './composables/appUpdate'
import { refreshFollows } from './composables/follows'

const route = useRoute()
const tabs = [
  { path: '/', label: '电影电视剧', icon: 'movie' },
  { path: '/series', label: '番剧', icon: 'tv' },
  { path: '/discover', label: '推荐', icon: 'fire' },
  { path: '/timeline', label: '时间表', icon: 'calendar' },
  { path: '/settings', label: '设置', icon: 'settings' },
]

onMounted(() => {
  // 打开、切回前台、每半小时查一次新版本（仅 App 内生效；浏览器/盒子没托管安装包时静默跳过）
  watchAppUpdate()
  void refreshFollows() // 顺便告诉手机 App 有没有在追的番（要不要定时取更新消息）
})
</script>

<template>
  <div class="shell">
    <router-view v-slot="{ Component, route: r }">
      <!-- 支持视图过渡时换页动画交给它（composables/motion.ts），这里只管切组件，旧页面立刻下场 -->
      <Transition :name="transitionName" :css="!useViewTransition">
        <KeepAlive include="LibraryView,HomeView,TimelineView,SearchView">
          <component :is="Component" :key="r.path" />
        </KeepAlive>
      </Transition>
    </router-view>
    <NavBar :items="tabs" class="nav" :class="{ hidden: !route.meta.tab }" />
    <Snackbar />
  </div>
</template>

<style>
.shell { position: relative; height: 100%; overflow: hidden; }
/* 一级页面给悬浮的底部导航/侧边导航让出位置 */
.tab-page { padding-bottom: var(--nav-space); }
@media (min-width: 840px) {
  .tab-page { padding-bottom: 0; padding-left: calc(var(--rail-w) + 24px); }
}
/* 进入二级页面时导航栏滑出（多滑 24px，连阴影一起藏住） */
.nav { transition: transform var(--md-dur-medium) var(--md-ease-emphasized); }
.nav.hidden { transform: translateY(calc(100% + 24px)); pointer-events: none; }
@media (min-width: 840px) { .nav.hidden { transform: translateX(calc(-100% - 24px)); } }
</style>
