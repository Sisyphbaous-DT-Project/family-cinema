package main

import (
	"embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// 旧前端（BT 下载/本地片库/上传/文件管理）整体放在 web-legacy/，挂在 /legacy/ 下继续可用。
// 新前端只做番剧聚合，旧功能的入口先隐藏，后端接口一个没动。
//
//go:embed web-legacy/*
var legacyFS embed.FS

// readLegacyFile 与 readWebFile 同一套思路：优先读可执行文件旁的 web-legacy/，没有就用编译进二进制的版本
func readLegacyFile(name string) ([]byte, error) {
	if !strings.Contains(name, "..") {
		if data, err := os.ReadFile(filepath.Join(exeDir(), "web-legacy", name)); err == nil {
			return data, nil
		}
	}
	return legacyFS.ReadFile("web-legacy/" + name)
}

// serveLegacy 处理 /legacy 开头的请求；返回 true 表示已经处理
func serveLegacy(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/legacy" {
		http.Redirect(w, r, "/legacy/", http.StatusMovedPermanently)
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/legacy/") {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, "/legacy/")
	if name == "" {
		name = "index.html"
	}
	ct := ""
	switch name {
	case "index.html":
		ct = "text/html; charset=utf-8"
	case "app.js":
		ct = "application/javascript"
	case "style.css":
		ct = "text/css"
	default:
		http.NotFound(w, r)
		return true
	}
	data, err := readLegacyFile(name)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(data)
	return true
}
