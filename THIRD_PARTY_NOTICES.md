# 第三方许可与署名

本项目自身源码采用 Apache-2.0。以下组件保留原有许可，不表示第三方为该项目背书，也不把第三方代码改称 Apache-2.0。

## 直接随仓库再分发的第三方材料

| 组件 | 上游 | 许可与署名 | 本仓库位置 |
| --- | --- | --- | --- |
| KazumiRules | https://github.com/Predidit/KazumiRules | MIT，Copyright 2024 Predidit | `media-server/rules/`，许可证同目录 |
| PanSou ARMv7 补丁对应的上游 | https://github.com/fish2018/pansou | MIT，Copyright 2025 fish2018 | `pansou/patches/`，许可证 `third_party/licenses/PanSou-MIT.txt` |
| Vue 3 | https://github.com/vuejs/core | MIT，Copyright 2018-present Yuxi (Evan) You | 打包进 `media-server/web/app.js` |
| Vue Router | https://github.com/vuejs/router | MIT，Copyright 2019-present Eduardo San Martin Morote | 打包进 `media-server/web/app.js` |
| hls.js | https://github.com/video-dev/hls.js | Apache-2.0，Copyright 2017 Dailymotion；含上游注明的 2013-2015 Brightcove 权利声明 | 打包进 `media-server/web/app.js` |
| Material Color Utilities | https://github.com/material-foundation/material-color-utilities | Apache-2.0，Material Eng / Material Foundation | 打包进 `media-server/web/app.js` |

KazumiRules 的许可证历史显示其初始许可提交即为 MIT。项目旧记录曾误写为 GPL-3.0，公开版已更正；当前规则文件及其 MIT 许可证一并保留。

PanSou 构建脚本固定上游提交 `221665fb2c9279f22af4684ea2d8c0c331eac1cc`，补丁只处理 32 位 JSON 库兼容、Android DNS 和 ARM 原子计数器对齐。构建时由脚本重新下载上游源码，本仓库不复制整个 PanSou 项目。

## 构建与运行依赖

- Go 依赖及精确版本见 `media-server/go.mod` 与 `go.sum`。实际编译依赖涉及 86 个模块，已逐一检查本地许可证文件：Apache-2.0、MIT/ISC、BSD 与 MPL；其中 `go-spew` 的 ISC 许可证已人工核实，未发现 GPL/AGPL 阻断项。
- 网页依赖及精确版本见 `media-server/web-src/package.json` 与 `package-lock.json`。
- Android 使用 AndroidX 与 NanoHTTPD；NanoHTTPD 上游为 BSD-3-Clause。
- 当前 npm lockfile 的许可字段盘点为：MIT、Apache-2.0、BSD-2-Clause、BSD-3-Clause、ISC、MPL-2.0。未发现 GPL/AGPL 许可字段；MPL-2.0 的文件级义务仍由对应组件的使用者保留。

许可证全文及依赖自身的 NOTICE（如上游提供）应随相应二进制或源码再分发。本仓库保存了直接打包/分发组件的主要许可证，位于 `third_party/licenses/`；Go module、npm 与 Gradle 包在获取时仍带有各自上游许可证。

## 规则中的网站公共键

个别上游规则带有网站自己分发的 `sb_publishable_...` 公共客户端键。这些字符串来自上游公开规则，不是本项目部署者的私人令牌。它们按原规则保留以维持解析功能；若上游撤销或限制使用，应通过更新规则处理，不能把它替换成任何私人密钥。
