# 配置与部署

以下示例使用 `box.local` 和 `proxy.example.com` 作为占位地址，请换成自己的设备地址，不要照抄到生产配置。

Windows 用户优先使用同一 Release 中的一键 EXE 与已签名 APK，见 [Windows 一键部署](windows-installation.md)。下面的参考盒子脚本不适用于普通 Windows 主机。

## 主服务配置

将 `media-server/config.example.json` 复制为 `media-server/config.json`。程序优先读取可执行文件同目录的 `config.json`，其次读取当前工作目录的同名文件。

| 字段 | 含义 |
| --- | --- |
| `port` | HTTP 端口，默认 8088 |
| `movieRoot` | 电影电视剧目录 |
| `seriesRoot` | 番剧目录 |
| `downloadDir` | 下载目录 |
| `cacheDir` | BT 缓存目录，建议与媒体目录在同一文件系统 |
| `caFile` | 可选 CA 证书 bundle，老安卓系统证书过旧时使用 |
| `proxy.base` / `proxy.token` | 可选 mikan 自托管代理地址与令牌 |
| `sources` | 各搜索源开关 |
| `searchTTL` | 搜索缓存秒数 |
| `speedtest` | BT 测速采样秒数 |
| `btPort` | BT 监听端口 |
| `btUploadKBs` / `btMaxConns` | BT 上传限速与连接数 |
| `btUPnP` | 是否尝试路由器 UPnP 端口映射 |
| `anime` | 番剧聚合、规则目录和缓存配置 |
| `media` | 声音转换与字幕提取（需要外部 ffmpeg/ffprobe，见下文） |
| `pansou.enabled` / `pansou.url` | 是否启用盘搜及其服务地址，默认 `http://127.0.0.1:8888` |

`rulesDir` 和 `cacheDir` 留空时，番剧模块使用可执行文件旁的 `rules/` 和 `anime-cache/`；规则已内嵌在二进制中。

`anime.ruleSync` 默认开启：服务启动 2 分钟后、之后每天北京时间凌晨 4 点多，从 KazumiRules 的镜像（默认 gh-proxy.org 与 GitCode，可用 `anime.ruleSources` 换成自己的地址，以 `/` 结尾）检查规则。候选规则会先在本地验收 XPath、JSONPath、请求模板和引擎支持情况，合格才替换；不合格的站继续使用当前版本，并在设置页说明原因。每站换新前会把上一份通过验收的规则存进规则目录的 `previous/`，当前规则文件损坏时启动会尝试用它恢复。上游新增的站通过验收后才加入，所有镜像都不再列出的站自动下架；整套规则写进规则目录后热替换，不用重启。规则目录里有规则时只读目录、不再用内嵌的那份；想退回内嵌规则，关掉 `ruleSync` 并删掉规则目录后重启。设置页「播放源」里能看到上次检查结果，也能手动立即检查（接口 `GET/POST /api/anime/rules/sync`）。

服务控制脚本只负责进程，不通过 `MEDIA_MOVIE_ROOT` 等环境变量配置媒体库；实际目录与端口以 `config.json` 或程序默认值为准。

网页通过主服务的 `/api/pansou/search` 同源访问盘搜，`/api/features` 提供功能开关。Windows 安装器为盘搜分配本机端口，并禁用盘搜时隐藏搜索入口；原盒子配置没有 `pansou` 段时仍默认使用本机 8888。Windows 使用系统磁盘空间接口，转码工具命名为 `ffmpeg.exe` 和 `ffprobe.exe`，媒体处理恢复仅清理核实属于本服务的进程。

## 可选：声音转换与字幕（ffmpeg / ffprobe）

浏览器和安卓 WebView 放不了 AC3、E-AC3、DTS、TrueHD 这类声音（有画面没声音、不报错），也不显示 MKV 里的字幕。装上 `ffmpeg` 和 `ffprobe` 后，服务会在扫库时逐个检查片子：

- 有浏览器放不了的声音时，在后台把它转成 AAC 并换成 MP4：画面原样复制，所有音轨保留且第一条设为默认，本来就是 AAC/MP3 的音轨直接复制。同一时间只转一部，番剧下载、BT 整理、上传进行中会先等；有人正在看的片子等 10 分钟没人看了再替换。
- 内嵌的 PGS 图片字幕抽成旁挂 `.sup`，文字字幕转成 `.vtt`（ASS 另存原件）；视频旁同名开头的 `.srt/.ass/.ssa` 外挂字幕自动另转 `.vtt`，GBK 编码也能认。网页放不了的字幕（如老 DVD 的图片字幕）在转声音时原样另存为 `<片名>.subs.mks`，原片删除前一条不丢。
- 只有新 MP4 的音轨、时长都对得上、字幕都已生成、同名 MP4 不存在时才删除原片；任何一步失败都保留原片并在任务页说明原因。转换后观看进度沿用原片的进度键。
- 片库管理页的「检查全部片子」会强制重新检查（之前失败或停止的也会重试）。

**本仓库不包含 ffmpeg / ffprobe**（常见静态构建按 GPLv3 分发），请自行下载与设备架构对应的版本，例如 johnvansickle.com 的 Linux 静态构建（ARMv7 盒子用 armhf 版）。默认放在主程序同目录、命名为 `ffmpeg` 和 `ffprobe` 并赋予可执行权限；也可以在 `media` 里写完整路径：

| 字段 | 含义 |
| --- | --- |
| `media.enabled` | 默认开启；任一工具缺失或不可执行时自动关闭，其它功能不受影响 |
| `media.ffmpegPath` / `media.ffprobePath` | 留空 = 可执行文件同目录的 `ffmpeg` / `ffprobe` |
| `media.stateDir` | 留空 = 可执行文件同目录的 `media-proc/`，保存检查结果、进度对照表和中断恢复信息 |

处理过程中的临时文件以 `.fcwork` 结尾，同名 MP4 替换时原片暂时改名为 `.fcold`；服务重启后只清理和恢复自己登记过的文件，不会结束别的 ffmpeg 进程。相关接口：`GET /api/media/status`、`POST /api/media/check-all`，任务也出现在 `/api/tasks` 里（`kind` 为 `media`）。

## 部署到参考安卓盒子

需要已 root 的 ARMv7 安卓盒子、ADB、BusyBox 与足够的硬盘空间。先备份正在使用的程序和配置。

```bash
cd media-server
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -buildvcs=false -ldflags "-s -w" -o media-server-armv7 .

export BOX_ADB=box.local:30016
# Git Bash 需要下一行，避免把安卓路径转换成本地路径
export MSYS_NO_PATHCONV=1
adb connect "$BOX_ADB"
adb push media-server-armv7 /data/server/bin/media-server.new
adb shell chmod 755 /data/server/bin/media-server.new
adb push web/index.html /data/server/bin/web/index.html
adb push web/app.js /data/server/bin/web/app.js
adb push web/style.css /data/server/bin/web/style.css
adb push ../scripts/mediactl.sh /data/server/bin/mediactl
adb shell chmod 755 /data/server/bin/mediactl
```

把编辑好的 `config.json` 推到 `/data/server/bin/config.json`，确认没有下载或播放任务后再替换和重启：

```bash
adb shell mv /data/server/bin/media-server.new /data/server/bin/media-server
adb shell /data/server/bin/mediactl restart
```

仅更新网页三文件时不用重启。盒子重插硬盘后设备名和挂载点可能变化，需要重新确认 `config.json` 中的路径。

## 救援工具箱

```bash
cd rescue-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -buildvcs=false -ldflags "-s -w" -o rescue-server-armv7 .

adb push rescue-server-armv7 /data/server/bin/rescue-server.new
adb push rescuectl.sh /data/server/bin/rescuectl
adb shell chmod 755 /data/server/bin/rescue-server.new /data/server/bin/rescuectl
adb shell mv /data/server/bin/rescue-server.new /data/server/bin/rescue-server
adb shell /data/server/bin/rescuectl start
```

打开 `http://<盒子地址>:8089`。救援服务独立于主服务，但没有开机自启。

固定 IP 别名是可选功能。需要时在启动环境中设置 `BOX_ALIAS_IP` 为一个**未被局域网其他设备占用**的 IPv4 地址；`BOX_SERVICE_IP` 可设置一个扫描时优先探测的地址。不要照抄别人的地址，避免 IP 冲突。未配置别名时，工具箱会跳过对应检查并禁用恢复按钮。

## 可选 mikan 代理

只在确实需要 mikan 且盒子无法直接访问时部署。`mikan-proxy` 将请求交给部署机上的 mihomo，默认上游为 `127.0.0.1:7890`。

```bash
cd mikan-proxy
go build -o mikan-proxy .
# 在部署机设置自己的强随机令牌，再启动
PROXY_TOKEN='替换为强随机令牌' ./mikan-proxy
```

默认监听 `0.0.0.0:10808`，可用 `PROXY_ADDR` 修改。未设置令牌时程序拒绝启动，所有请求也不会被放行。

在 `media-server/config.json` 中填写自己的代理：

```json
"proxy": {
  "base": "http://proxy.example.com:10808",
  "token": "替换为自己的强随机令牌"
}
```

HTTP 不提供传输加密。跨不可信网络时，请使用 HTTPS 反向代理或加密隧道并配置防火墙；不要把无鉴权组件或直接暴露管理能力的端口开放到公网。不要把真实令牌写进示例文件或提交到 Git。

## 可选 PanSou

`pansou/build.sh` 拉取固定版本的 MIT 上游源码，应用 ARMv7/Android 补丁并交叉编译。需要 Bash、Git、Go、curl；GOPROXY 和 HTTPS_PROXY 均可通过环境变量覆盖。

```bash
cd pansou
bash build.sh /tmp/family-cinema-pansou-build
```

产物为 `pansou-armv7`，脚本还会下载 `cacert.pem`。部署为 `/data/server/bin/pansou`，将 `pansouctl.sh` 推到 `/data/server/bin/pansouctl`。上游运行配置由部署者在 `/data/server/pansou/pansou.env` 中创建，仓库不包含真实配置。配置方法以固定上游提交的文档为准，完成后用 `pansouctl start` 启动。

PanSou 默认端口为 8888，同样不应直接暴露到公网。升级 `UPSTREAM_COMMIT` 后应重新检查补丁并在自己的设备验证。

## Windows 运维工具

`tools/start_box_panel.bat` 启动服务面板，`tools/start_screen_viewer.bat` 启动 ADB 屏幕查看器。将 `adb` 加入 PATH，并在界面中填入自己的盒子地址；面板也支持 `BOX_ADB`、`BOX_HOST` 环境变量，查看器支持 `ADB_TARGET`。

屏幕查看器依赖 Pillow：

```bash
python -m pip install Pillow
```

这些工具能执行删除、重启和挂载修复等操作，请先确认目标地址无误。
