import { createApp } from 'vue'
import App from './App.vue'
import { router } from './nav'
import { initTheme } from './theme/theme'
import { ripple } from './directives/ripple'
import { reveal } from './directives/reveal'
import './styles/base.css'
import './styles/motion.css'

// 开发模式专用：?mockbridge=1 / ?mockbridge=offline 装假桥（正式构建不包含此模块）
if (import.meta.env.DEV) {
  const { installMockBridge } = await import('./dev/mockBridge')
  installMockBridge()
}

initTheme()
createApp(App).use(router).directive('ripple', ripple).directive('reveal', reveal).mount('#app')
