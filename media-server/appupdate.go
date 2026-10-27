package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

// App 应用内升级：盒子托管最新 APK，App 启动时比对版本号，
// 有新版就提示用户点击更新（下载→跳系统安装器，最后一步安装必须用户亲手点）。
//
// 部署方式：把 app.apk 和 version.json（{"versionCode":N,"versionName":"x.y"}）
// 放到 /data/server/bin/app/ 目录即可，无需重启服务（每次请求实时读盘）。

func appDir() string { return filepath.Join(exeDir(), "app") }

func readAppVersion() (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(appDir(), "version.json"))
	if err != nil {
		return nil, errors.New("盒子上还没有托管 App 安装包")
	}
	var v struct {
		VersionCode int    `json:"versionCode"`
		VersionName string `json:"versionName"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.VersionCode <= 0 {
		return nil, errors.New("version.json 内容不对")
	}
	fi, err := os.Stat(filepath.Join(appDir(), "app.apk"))
	if err != nil || fi.Size() == 0 {
		return nil, errors.New("盒子上还没有托管 App 安装包")
	}
	return map[string]any{
		"versionCode": v.VersionCode,
		"versionName": v.VersionName,
		"sizeBytes":   fi.Size(),
	}, nil
}

func mountAppUpdate(mux *http.ServeMux, api func(func(http.ResponseWriter, *http.Request) error) http.HandlerFunc) {
	// 查最新版本：App 启动时调，非 200 = 没托管 = 不提示更新
	mux.HandleFunc("/api/app/latest", api(func(w http.ResponseWriter, r *http.Request) error {
		v, err := readAppVersion()
		if err != nil {
			return err
		}
		return json.NewEncoder(w).Encode(v)
	}))
	// 下载 APK（内网直连，无需代理）
	mux.HandleFunc("/api/app/apk", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(appDir(), "app.apk")
		if _, err := os.Stat(p); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		http.ServeFile(w, r, p)
	})
}
