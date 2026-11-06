package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 盘搜只向配置的服务转发搜索，客户端不能指定目标地址或其他接口。
func mountPansou(mux *http.ServeMux) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	mux.HandleFunc("/api/features", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]bool{"pansou": currentConfig().Pansou.Enabled})
	})
	mux.HandleFunc("/api/pansou/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		config := currentConfig().Pansou
		if !config.Enabled {
			http.Error(w, "当前未启用网盘搜索，可在一键程序中安装盘搜", http.StatusServiceUnavailable)
			return
		}
		base, err := url.Parse(config.URL)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			http.Error(w, "盘搜服务地址配置不正确", http.StatusServiceUnavailable)
			return
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/api/search"
		base.RawPath = ""
		base.RawQuery = r.URL.RawQuery
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
		if err != nil {
			http.Error(w, "搜索请求过大或读取失败", http.StatusRequestEntityTooLarge)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, base.String(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, "无法创建搜索请求", http.StatusBadGateway)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "网盘搜索暂时无法连接，请在一键程序中查看盘搜状态", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
		if err != nil || len(data) > 16<<20 {
			http.Error(w, "网盘搜索响应读取失败", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(data)
	})
}
