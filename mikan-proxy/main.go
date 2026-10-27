// mikan-proxy 极简正向代理：为机顶盒访问 mikanani.me 提供出口
// 部署在自托管服务器上，监听 0.0.0.0:10808，校验 X-Proxy-Token 请求头
// 所有流量转发给本机 mihomo（127.0.0.1:7890），由 mihomo 出口访问境外站点
package main

import (
	"bufio"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var up = &url.URL{Scheme: "http", Host: "127.0.0.1:7890"}

func token() string { return os.Getenv("PROXY_TOKEN") }

func checkToken(r *http.Request) bool {
	want := token()
	if want == "" {
		return false // 必须配置令牌，避免在公网开放无鉴权代理
	}
	// 支持两种携带方式：X-Proxy-Token 头，或标准 Proxy-Authorization: Basic base64(token:)
	got := r.Header.Get("X-Proxy-Token")
	if got == "" {
		if auth := r.Header.Get("Proxy-Authorization"); strings.HasPrefix(auth, "Basic ") {
			if raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic ")); err == nil {
				got = strings.TrimSuffix(string(raw), ":")
			}
		}
	}
	if len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// 普通 HTTP 代理请求（GET/POST 等明文流量）
func handleHTTP(w http.ResponseWriter, r *http.Request) {
	transport := &http.Transport{Proxy: http.ProxyURL(up)}
	outReq := r.Clone(r.Context())
	outReq.Header.Del("X-Proxy-Token")
	outReq.URL.Scheme = "http"
	outReq.URL.Host = r.Host
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "bad gateway: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32*1024)
	_, _ = io.CopyBuffer(w, resp.Body, buf)
}

// HTTPS CONNECT 隧道：先向 mihomo 发起 CONNECT，再双向透传
func handleTunnel(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	mihomo, err := net.Dial("tcp", up.Host)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	// 向 mihomo 发送 CONNECT 请求
	if _, err := fmt.Fprintf(mihomo, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		mihomo.Close()
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	br := bufio.NewReader(mihomo)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		mihomo.Close()
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK {
		mihomo.Close()
		http.Error(w, "upstream refused", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		mihomo.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		mihomo.Close()
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	// 注意：mihomo 侧必须用 br（缓冲里可能已有 TLS 数据）
	go func() {
		_, _ = io.Copy(mihomo, client)
		if tcp, ok := mihomo.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(client, br)
	client.Close()
	mihomo.Close()
}

// 抓取端点：盒子用普通 HTTP GET 获取 mikan 页面/图片，绕开 CONNECT 隧道被中间设备拦截的问题
// 用法: GET /fetch?url=https%3A%2F%2Fmikanani.me%2FHome%2FSearch%3Fsearchstr%3D...
func handleFetch(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	// 白名单限制，避免被当作开放代理滥用
	host := strings.ToLower(u.Hostname())
	if host != "mikanani.me" && !strings.HasSuffix(host, ".mikanani.me") {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	// 重新百分号编码 query，避免非 ASCII 字符直接进入请求行导致上游 400
	u.RawQuery = u.Query().Encode()
	target = u.String()
	req, _ := http.NewRequest(r.Method, target, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	// 透传 Accept / Referer，避免目标站拒绝
	if a := r.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(up)}
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Content-Disposition", "Cache-Control", "Expires"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32*1024)
	_, _ = io.CopyBuffer(w, resp.Body, buf)
}

func main() {
	if token() == "" {
		log.Fatal("请先通过 PROXY_TOKEN 环境变量设置强随机访问令牌")
	}
	addr := os.Getenv("PROXY_ADDR")
	if addr == "" {
		addr = "0.0.0.0:10808"
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !checkToken(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/fetch" {
			handleFetch(w, r)
			return
		}
		if r.Method == http.MethodConnect {
			handleTunnel(w, r)
			return
		}
		handleHTTP(w, r)
	})
	log.Printf("mikan-proxy listening on %s -> upstream %s (token set: %v)", addr, up.Host, os.Getenv("PROXY_TOKEN") != "")
	log.Fatal(http.ListenAndServe(addr, handler))
}
