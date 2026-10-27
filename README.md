# Family Cinema · 家庭影院

把一台闲置的 ARMv7 安卓机顶盒变成家庭媒体服务器，再用手机、电脑浏览器或安卓 App 观看自己的媒体库。

主服务采用 Go，网页采用 Vue 3 + Vite，安卓 App 采用 Kotlin。网页、视频缓存、番剧聚合、手机辅助解析和追番提醒由同一套系统协作完成。

> 这是一个自托管项目，不是在线影视服务。仓库不包含影片、电视剧、账号、会员凭据或私人配置。

## 功能

- **本地媒体库**：电影、电视剧与番剧分类浏览，网页播放、封面、进度记忆和接着看。
- **资源搜索与 BT 下载**：聚合搜索、测速、下载任务、压缩包处理与入库；也可手动添加磁力或种子。
- **番剧聚合**：Bangumi 推荐与时间表、社区规则选源、多集下载、追番更新与播出提醒。
- **手机离线缓存**：单集或多选缓存、排队、暂停、继续、取消，以及断网后播放已缓存内容。
- **手机辅助解析与代取**：盒子无法直接解析或读取视频时，可由同一局域网内的安卓 App 协助。
- **管理工具**：上传、改名、删除、文件管理、磁盘与下载缓存管理。
- **救援工具箱**：独立的 8089 服务，用于查看状态、日志、磁盘和进行常见修复。
- **网页热更新与应用内更新**：服务器上的网页可同步到 App；App 更新仍须用户在系统安装界面确认。

## 组件

| 目录 | 用途 |
| --- | --- |
| `media-server/` | 主服务，默认端口 8088；含 Vue 源码和可直接嵌入的网页构建产物 |
| `android/` | 安卓 App，最低 Android 8（API 26） |
| `rescue-server/` | 独立救援工具箱，默认端口 8089 |
| `mikan-proxy/` | 可选的自托管 mikan 请求转发器 |
| `pansou/` | PanSou 固定上游版本的 ARMv7 补丁与构建脚本，默认端口 8888 |
| `scripts/` | 盒子上的服务控制脚本 |
| `tools/` | Windows 运维面板与 ADB 屏幕查看器 |

## 环境要求

- Go 1.25
- Node.js 22.12 或更新版本，以及 npm
- 构建 Android App：JDK 17、Android SDK 34、Gradle 8.7
- 部署到参考机顶盒：ARMv7 Linux/Android、ADB、BusyBox、可写媒体目录
- 运维面板：Python 3.10+；屏幕查看器另需 Pillow 和 ADB

参考设备运行 Android 4.4 / ARMv7。普通 Linux 服务器也可以运行主服务，但目录、存储和救援脚本需要按自己的系统调整。救援服务使用的 `/system/bin`、ADB 与挂载操作是面向安卓盒子的。

## 快速开始

### 1. 配置并运行主服务

```bash
cd media-server
cp config.example.json config.json
# 编辑 config.json，把媒体目录换成自己的目录
# Windows 可将 cp 换成 copy

go test ./...
go run .
```

浏览器打开 `http://127.0.0.1:8088`。`config.json` 被 Git 忽略，不要提交真实路径或令牌。配置文件字段见 [部署说明](docs/deployment.md)。

### 2. 修改并构建网页

仓库已经带有主服务所需的网页三文件，只有修改网页时才需要重新构建：

```bash
cd media-server/web-src
npm ci
npm run typecheck
npm run build
```

构建输出固定为 `media-server/web/index.html`、`app.js`、`style.css`，供 Go embed 与 App 热更新使用。

开发服务器默认把 API 转发到 `http://127.0.0.1:18088`。要连接自己的盒子，可显式设置：

```bash
API_TARGET=http://box.local:8088 npm run dev
```

### 3. 构建安卓 App

先把 JDK、Android SDK 与 Gradle 配置好，确保 `JAVA_HOME`、`ANDROID_HOME` 正确且 `gradle` 在 PATH 中：

```bash
cd android
gradle --no-daemon assembleDebug
```

Windows 也可以运行 `build_apk.bat`。输出是 `app/build/outputs/apk/debug/app-debug.apk`。这是用于开发验证的 debug 包，不是正式发布签名包。

首次使用 App 时让它自动发现盒子，或者在设置中填写 `http://<你的盒子地址>:8088`。手机辅助解析、手机代取与追番通知需要额外权限，应按实际需要使用。

## 构建 ARMv7 服务

```bash
cd media-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -buildvcs=false -ldflags "-s -w" -o media-server-armv7 .
```

部署、救援工具箱、可选代理与 PanSou 的说明见 [docs/deployment.md](docs/deployment.md)。

## 安全边界

**主服务和救援服务面向可信家庭局域网，没有面向公网的身份认证。**

- 不要把 8088、8089、8888 或 ADB 端口直接映射到公网。
- 文件管理、删除、清缓存、服务重启与挂载修复会影响真实数据，只应让可信设备访问。
- 手机代取只在解析助手开启时工作，并使用随机限时令牌；不需要时请关闭解析助手。
- `mikan-proxy` 必须配置 `PROXY_TOKEN` 才会启动。HTTP 本身不加密，跨不可信网络使用时应增加 HTTPS 或加密隧道，并限制来源地址。
- 备份配置与媒体数据后再做部署或升级；不要将真实令牌、私钥或配置提交到仓库。

安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。

## 内容与来源

本项目提供媒体管理与播放工具，不提供内容托管。搜索站点、Bangumi、社区规则及网盘服务由各自运营方负责，可用性会随站点变化。请只下载、保存和播放你有权使用的内容，并遵守所在地区法律、站点条款与版权要求。

番剧规则来自 [KazumiRules](https://github.com/Predidit/KazumiRules)，使用 MIT 许可；项目仅借鉴 Kazumi 的规则思路，未复制其应用源码。PanSou 补丁基于 [fish2018/pansou](https://github.com/fish2018/pansou)，同样保留 MIT 许可。

## 开源许可

本项目源码采用 [Apache License 2.0](LICENSE)，版权持有人为 **C₂₂H₂₅NO₆**（GitHub 昵称）。

第三方组件仍使用各自的许可证。署名、上游地址、许可差异及 npm 许可盘点见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)，许可证原文保存在 `media-server/rules/LICENSE` 与 `third_party/licenses/`。

## 参与开发

欢迎提交问题与改进。开发、测试和隐私要求见 [CONTRIBUTING.md](CONTRIBUTING.md)。
