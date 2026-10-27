# 安全说明

## 支持范围

本项目是个人自托管的家庭影院系统。使用最新源码或发布版本，并在部署前备份真实媒体与配置。

## 部署边界

主服务（8088）、救援工具箱（8089）、PanSou（8888）与 ADB 不应直接暴露公网。媒体管理和救援接口包含文件删除、缓存清理、进程控制与挂载修复等能力，只适合可信局域网。

`mikan-proxy` 必须配置强随机 `PROXY_TOKEN`。跨不可信网络时，需要额外的加密传输与访问控制。不要把真实配置、日志或令牌上传到 Issue。

## 报告漏洞

请通过项目 GitHub 仓库的 **Security → Report a vulnerability** 私密报告，入口为：

<https://github.com/Sisyphbaous-DT-Project/family-cinema/security/advisories/new>

报告请包含受影响组件、版本、复现条件与可能影响；不要附上私人令牌、完整媒体数据或他人的个人信息。在问题修复前不要公开可被直接利用的细节。
