#!/usr/bin/env bash
# 在 Windows 开发机（Git Bash）上编译盒子用的盘搜（PanSou）：
# 拉官方源码的固定版本 → 打 patches/ 里的补丁 → 交叉编译成 ARMv7 → 输出 ./pansou-armv7
#
# 为什么要打补丁：
#   1. sonic（JSON 加速库）不支持 32 位，换成标准库 encoding/json
#   2. 安卓盒子没有 /etc/resolv.conf，改用公共 DNS
#   3. 3 个结构体里的 64 位原子计数器在 32 位 ARM 上没对齐，一搜就崩，挪到结构体开头
#
# 用法：bash build.sh <一个不存在的临时目录>
# 升级上游：改下面的 UPSTREAM_COMMIT，跑一遍；补丁打不上就手工改完重新导出补丁，
#          编译好后一定要在盒子上真搜一轮确认不崩（官方的对齐检查工具查不出这类问题）。
set -euo pipefail

UPSTREAM_REPO=https://github.com/fish2018/pansou.git
UPSTREAM_COMMIT=221665fb2c9279f22af4684ea2d8c0c331eac1cc # 2026-10-12「新增插件488run」
GO_TOOLCHAIN=go1.25.5 # 与官方 Dockerfile 一致；sonic 不认 Go 1.26+，换掉 sonic 后也保持同版本

HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${1:?用法: bash build.sh <一个不存在的临时目录>}"
if [ -e "$WORK" ]; then
  echo "临时目录已存在：$WORK（请换一个不存在的路径）" >&2
  exit 1
fi

# Go 使用 PATH；需要自定义镜像时设置 GOPROXY，需要代理时设置 HTTPS_PROXY。
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"

# 只拉需要的那一个版本；core.autocrlf=false 保持 LF，补丁才能打上
mkdir -p "$WORK"
git -C "$WORK" -c core.autocrlf=false init -q pansou
cd "$WORK/pansou"
git config core.autocrlf false
git fetch -q --depth 1 "$UPSTREAM_REPO" "$UPSTREAM_COMMIT"
git checkout -q FETCH_HEAD
git apply "$HERE/patches/armv7-android.patch"

GOTOOLCHAIN=$GO_TOOLCHAIN CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -buildvcs=false -ldflags "-s -w" -o "$HERE/pansou-armv7" .

# 安卓 4.4 的根证书太老，运行时用 SSL_CERT_FILE 指向这份新证书
if [ ! -s "$HERE/cacert.pem" ]; then
  curl -sSf -o "$HERE/cacert.pem" https://curl.se/ca/cacert.pem
fi

echo "编译完成：$HERE/pansou-armv7"
echo "临时源码留在 $WORK，确认没问题后可以删掉"
