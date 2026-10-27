/// <reference types="vite/client" />
// hls.js 精简版没有单独的类型声明，直接借用完整版的
declare module 'hls.js/light' {
  import Hls from 'hls.js'
  export default Hls
}

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<object, object, unknown>
  export default component
}
