# Windows 一键部署器

使用 Windows 内置 .NET Framework 4.x，目标为 Windows 10/11 x64。安装器下载独立 Go 1.25.5，以国内 Go 模块镜像编译固定版本主服务和 PanSou。前端使用仓库内已构建的文件，不安装 Node.js。APK 从同一发布版本下载，不安装 Android 开发环境。

## 构建

发布维护者准备不含私密信息的 `release-settings.json`，运行：

```powershell
.\windows\build.ps1 -Settings C:\Release\release-settings.json -Output C:\Release\FamilyCinema-Setup.exe
```

设置字段：`Version`、`SourceCommit`、`SourceUrl`、`SourceSha256`、`ApkUrl`、`ApkSha256`、`ApkVersionCode`、`ApkVersionName`、`GoVersion`（不含 `go` 前缀）、`GoSha256`、`PansouSha256`。声音工具可选，配置 `FFmpegUrl` 和 `FFmpegSha256` 才启用安装选项。支持静态与 shared Windows ZIP；shared 构建的 DLL、许可和说明会一起保留，安装前实际执行工具版本检查。所有校验值为 SHA256，内嵌到 EXE，不从下载镜像读取可信校验值。

源码和 APK 可使用 gh-proxy.org、gh-proxy.com、ghfast.top、ghproxy.net 与官方地址；Go 使用阿里云、中科大和官方源。每个文件下载前会在后台并行取样测速，按实际吞吐自动选择可用源，失败后切换备用源，并缓存最近的测速及成功记录。所有内容仍须通过内嵌校验值。编译前也对 goproxy.cn、goproxy.io 的实际模块 ZIP 取样测速，按结果排序并使用可在网络失败后切换的 `|` 分隔，保留 Go 校验数据库验证。镜像可用性取决于用户网络，失败会显示原因，可重复执行安装。

国内 Go 和工具镜像使用直连，避免继承电脑上的显式代理后绕远；其他下载遵循当前网络的可用代理设置。编译器的 `NO_PROXY` 只设置在本次子进程中，保留原值，不修改系统代理或其他软件。代理的 TUN 模式仍由用户自己的网络工具控制。

## 使用

双击 EXE，选择两个分开的专用文件夹。盘搜默认启用，FFmpeg 可选。勾选声音处理后，原有媒体处理流程会转换声音并在校验成功后替换原视频。安装器不删除媒体。

启动、停止、打开网页、下载手机 App 与日志入口位于同一窗口。允许局域网访问时主服务监听局域网；需要时点击“允许手机访问”，Windows 会请求管理员权限，仅为专用网络、同一子网添加 TCP 网页端口与 UDP 45222 发现规则。公共网络和盘搜端口不开放。PanSou 在编译时仅将监听地址改为 127.0.0.1，通过主服务同源 API 使用，不直接向局域网开放。

登录后自动启动使用当前用户的 Run 注册表项。关闭窗口不会停止已经启动的服务。电脑休眠或关机后服务不可用；此选项不等于无需登录的系统服务。

目录中 `config.json` 为稳定配置，`data/` 保存规则、缓存与媒体处理记录，`versions/` 保留当前与上一个程序版本，`logs/` 保存日志，`tools/` 和 `cache/` 保存便携编译器与可复用下载。更新先在工作目录完成下载校验和编译，再切换程序；失败尝试恢复上一个版本。版本更新不修改媒体目录。可重复运行同一 EXE 修复失败安装。

高级配置（例如自有 mikan 代理）可在停止服务后编辑安装目录的 `config.json`，再次启动时自动复制到活动程序目录。安装器不提供共享代理、令牌或公网部署。

## 自动验证

```powershell
FamilyCinema-Setup.exe --self-test
FamilyCinema-Setup.exe --install --root C:\CinemaApp --media D:\CinemaMedia --pansou true --ffmpeg false --lan false --autostart false --no-start
FamilyCinema-Setup.exe --start --root C:\CinemaApp
FamilyCinema-Setup.exe --stop --root C:\CinemaApp
```

命令行日志可通过环境变量 `FAMILY_CINEMA_SETUP_LOG` 指定输出文件。`--install` 会完整下载、校验并编译；`--no-start` 只安装不启动。无人值守验证不要使用真实媒体目录。
