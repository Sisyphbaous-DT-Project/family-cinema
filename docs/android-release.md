# Android 正式发布

Windows 一键程序从同一个 GitHub Release 下载已编译的 APK，用户无需安装 Java、Gradle 或 Android SDK。APK 放在主服务旁的 `app/app.apk`，`app/version.json` 提供版本信息，手机沿用现有的应用内更新接口。

## 固定签名

正式包使用独立的发布签名，不使用 Android 调试证书。发布密钥和密码配置必须保存在源码仓库之外，并且不加入 EXE、APK、源码归档或 Release。所有后续正式版本必须继续使用同一份密钥；丢失密钥后无法为现有安装提供正常覆盖升级。

1.11 正式版签名证书的 SHA256 为 `26b5d07e73609421ded86714e4875d8a62bffd68857bb1c77f06d6b298684841`。这是用于核对发布者的公开证书摘要，不是私钥或 APK 文件哈希。

`scripts/build-release.ps1` 读取外部 JSON 配置，字段为 `keystore`、`storePassword`、`keyAlias` 和 `keyPassword`。脚本将凭据仅传入本次构建的进程环境，构建结束后恢复环境。首次使用 `-CreateSigningKey` 时会生成 RSA 4096 位密钥，并限制签名目录为当前用户和 SYSTEM 可访问。完成首次生成后，应将该目录备份到自己的安全存储，后续构建不得重复生成新密钥。

也可在 CI 中通过 `FAMILY_CINEMA_KEYSTORE`、`FAMILY_CINEMA_STORE_PASSWORD`、`FAMILY_CINEMA_KEY_ALIAS`、`FAMILY_CINEMA_KEY_PASSWORD` 配置签名。未提供完整签名的 release 构建会直接失败，不会输出一个被误认为正式包的未签名产物。

## 构建与校验

发布环境需要 JDK 17、Android SDK 34、Build Tools 34.0.0 和 Gradle 8.7。`JAVA_HOME`、`ANDROID_HOME` 及 Gradle 均可通过脚本参数指定，示例路径请替换为实际目录。

```powershell
.\scripts\build-release.ps1 `
  -OutputDirectory ..\family-cinema-release\v1.11.0 `
  -JavaHome C:\Tools\jdk-17 `
  -AndroidHome C:\Tools\android-sdk `
  -Gradle C:\Tools\gradle-8.7\bin\gradle.bat `
  -SigningConfig C:\Private\family-cinema\release-signing.json `
  -CreateSigningKey
```

后续构建去掉 `-CreateSigningKey`。依赖已经缓存时可以增加 `-Offline`。默认不会修改系统 PATH、用户 Gradle 配置或全局代理。

部分 Windows 环境会在 Gradle 启动时报 `Unable to establish loopback connection`，实际出错的是 JDK 的 AF_UNIX 本地管道。发布脚本在本次 JVM 中为 Unix socket 指定一个不存在的临时子目录，让 JDK 自动回退到本机 TCP；不会创建该子目录，也不会修改系统网络配置。普通临时文件仍使用独立目录，构建结束后清理。

脚本读取 APK 构建元数据，执行 `apksigner verify`，拒绝调试证书，然后输出：

- `family-cinema-android-<版本>.apk`
- `app-version.json`，包含包名、版本号、文件大小、APK SHA256 与签名证书 SHA256

上传时将 APK、Windows 一键 EXE 与校验文件放入同一个 Release。每次发布增加 `android/app/build.gradle` 中的 `versionCode`，有新版名称时同步调整 `versionName`。安装器内嵌的 APK 地址、哈希及版本信息必须对应同一发布版本，不能使用未经校验的 `latest` 包。

## 从旧调试版迁移

包名仍为 `com.family.cinema`。早期调试包和正式包的签名不同，Android 不允许直接覆盖。已经安装早期调试版时，先保留需要的离线影片与使用记录，再卸载调试版、安装正式包；卸载会删除 App 私有目录中的离线缓存、设置和本地记录。服务器媒体库不受影响。当前版本没有通用的 App 私有数据导出工具，不能承诺卸载后恢复这些数据。

首次正式安装后，后续正式版只要沿用同一签名并增加版本号，就可通过 App 的系统安装确认界面完成覆盖升级。安装或升级的最后一步仍需手机用户亲自确认，Windows 一键程序不会绕过 Android 的安装授权。

参考：[Android 官方应用签名说明](https://developer.android.com/studio/publish/app-signing)。
