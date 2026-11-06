# Windows 安装界面

安装器的 Vue 3 / TypeScript 界面，使用与家庭影院网页相同的 Material Color Utilities 绿色种子、低饱和度配色和 Material Icons。字体使用系统字体，不从网络下载图标、字体或其他资源。浅色、深色及跟随系统可在外观菜单中选择。

## 构建

需要 Node.js 22.12 或以上版本，依赖固定版本并使用 `registry.npmmirror.com`。

```powershell
cd windows/ui
npm ci
npm run typecheck
npm run build
```

构建结果只有 `dist/index.html`，JS、CSS 和图标全部内联。该文件需要随源码提交，由 Windows 宿主作为资源打包，用户机器无需 Node.js。宿主可以直接使用 `NavigateToString` 加载，不需要本地 HTTP 服务或外部 CDN。

开发预览使用 `npm run dev`。仅开发环境包含预览数据，`?state=installed`、`running`、`warning`、`error`、`offline` 和 `unavailable` 可查看不同状态；正式构建没有预览逻辑，缺少 WebView2 消息桥时会显示明确提示。

## 宿主接口

`src/bridge.ts` 使用 `window.chrome.webview.postMessage({ id, method, args })` 发起请求；宿主返回 `{ id, result }` 或 `{ id, error }`。进度事件为 `{ type: "progress", message }`。

支持 `init`、`browse`、`install`、`start`、`stop`、`cancel`、`openWeb`、`openLogs`、`copyText`、`firewall`、`close`。`init`、安装、启动和停止返回当前状态；`browse` 返回路径字符串。日志复制通过原生剪贴板完成，不依赖内嵌页面的浏览器权限。状态中的 `options` 沿用 C# 的 PascalCase 字段，其余状态字段使用 camelCase。类型定义及方法参数以 `src/bridge.ts` 为准。

安装和启停期间禁用可变配置，取消操作仍可用。下载进度由宿主日志直接呈现，使用不定进度条，不生成虚假的完成百分比。安装器不自动确认 Windows 权限或手机安装授权。

## 浏览器验证

`scripts/visual-smoke.mjs` 使用 Playwright 启动本地临时服务，以生产 HTML 和受控宿主消息验证首次安装、转码确认、目录浏览、取消、成功、失败重试、启停、手机入口、深浅色、小窗口滚动及断开宿主状态。需要环境中已有 Playwright 与 Chromium；可通过 `PLAYWRIGHT_MODULE` 指定 Playwright 的入口文件，`UI_SCREENSHOTS` 指定截图输出目录。脚本结束后会关闭浏览器及临时服务。
