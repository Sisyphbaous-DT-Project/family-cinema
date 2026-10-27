# 参与开发

## 开发原则

- 保持现有目录结构与 Go、Vue、Kotlin 的代码风格，不为小改动引入新框架。
- 源代码与 Shell 脚本使用 UTF-8、LF 换行；Windows bat 使用 CRLF。
- 注释与面向用户的说明使用清楚、专业的简体中文。
- 新功能或修复应覆盖真实的失败路径，但不要为现实中不会出现的输入堆砌防御逻辑。
- 如果修改了文档所描述的行为，请同步更新相关文档与第三方声明。

## 提交前检查

```bash
cd media-server
go test -count=1 ./...

cd web-src
npm ci
npm run typecheck
npm run build

cd ../../rescue-server
go test ./...

cd ../mikan-proxy
go test ./...

cd ../android
gradle --no-daemon assembleDebug
```

修改 ARMv7 或部署相关代码时，再执行对应交叉构建；修改 PanSou 补丁时，验证固定上游提交能够应用补丁并编译。

## 隐私与数据

不要提交：

- 私钥、访问令牌、真实 `.env`、`config.json`、签名文件或本机路径；
- 真实媒体库、观看记录、下载记录、日志、会话记录或他人个人信息；
- 编译产物、`node_modules`、Gradle 缓存、APK 或从设备提取的固件；
- 未确认许可的第三方源码、规则或图片。

示例统一使用 `box.local`、`proxy.example.com` 和空白令牌。发现秘密曾进入提交时，不要只新增一次“删除秘密”的提交，应先撤销公开访问、更换凭据，再清理历史。

## 许可

提交即表示你有权提供相应改动，并同意本项目以 Apache-2.0 发布。第三方材料必须保留原始许可证与署名，不能把 MIT、BSD、MPL 等其他许可的材料直接标为本项目所有。
