import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'

const apiTarget = process.env.API_TARGET || 'http://127.0.0.1:18088'
const pansouTarget = (() => {
  const u = new URL(apiTarget)
  u.port = '8888'
  return u.origin
})()

// 构建产物固定为 app.js + style.css（index.html 来自 public/，原样拷贝）。
// 安卓 App 的热更新只认 index.html / app.js / style.css 这三个文本文件，
// 所以：不分块、不带哈希、不引入图片/字体等二进制资源。
// 开发时把 OUT_DIR 指到别处可避免覆盖 media-server/web。
export default defineConfig({
  plugins: [vue()],
  define: {
    // 库模式不会替换这些，Vue 运行时会读，浏览器里没有 process
    'process.env.NODE_ENV': '"production"',
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  build: {
    outDir: process.env.OUT_DIR || '../web',
    emptyOutDir: false,
    cssCodeSplit: false,
    sourcemap: false,
    target: 'es2019',
    lib: {
      entry: fileURLToPath(new URL('./src/main.ts', import.meta.url)),
      name: 'FamilyCinema',
      formats: ['iife'],
      fileName: () => 'app.js',
      cssFileName: 'style',
    },
  },
  server: {
    // 默认代理到本机 18088 的开发后端；想连真盒子测试：API_TARGET=http://box.local:8088 npx vite
    // 网盘搜索（盘搜）在同一台机器的 8888 端口，前端开发时走 /pansou-api
    proxy: {
      '/api': apiTarget,
      '/pansou-api': { target: pansouTarget, changeOrigin: true, rewrite: (p) => p.replace(/^\/pansou-api/, '') },
    },
  },
})
