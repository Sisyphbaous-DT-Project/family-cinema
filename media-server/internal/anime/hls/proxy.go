package hls

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PathPrefix 播放代理的路由前缀，后面跟令牌
const PathPrefix = "/api/anime/p/"

const defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var errPrivate = errors.New("不允许访问内网/本机地址")

// Proxy 播放代理。
// HTTP 必须是"没有总超时"的 client（分片/大文件会超过任何固定总时长），超时交给请求 context。
type Proxy struct {
	HTTP   *http.Client
	Signer *Signer
	// LookupIP 用于把域名解析成 IP 后检查是否内网（盒子上需要用自定义 DNS）；为空则只检查 IP 字面量
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
	// AllowPrivate 为 true 时跳过内网地址检查（仅测试用）
	AllowPrivate bool

	okHosts sync.Map // host -> 检查通过的有效期
}

// URL 为目标生成走本代理的相对地址
func (p *Proxy) URL(t Target) string { return PathPrefix + p.Signer.Sign(t) }

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// guard 拒绝指向本机/内网的目标，免得代理被拿去探测家里的路由器等设备
func (p *Proxy) guard(ctx context.Context, host string) error {
	if p.AllowPrivate {
		return nil
	}
	if v, ok := p.okHosts.Load(host); ok && time.Now().Before(v.(time.Time)) {
		return nil
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else if strings.EqualFold(host, "localhost") {
		return errPrivate
	} else if p.LookupIP != nil {
		ips, _ = p.LookupIP(ctx, host) // 解析失败就让真正的请求去失败
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return errPrivate
		}
	}
	p.okHosts.Store(host, time.Now().Add(5*time.Minute))
	return nil
}

// ErrBadTarget 目标地址不是合法的 http(s) 地址
var ErrBadTarget = errors.New("bad target")

// Open 带上令牌里的 UA/Referer/Cookie（以及可选的 Range）向上游发请求。
// 有 Relay 时，原地址仍先做公网检查，再把原始地址编码给手机代取；不能借代取绕过内网拦截。
func (p *Proxy) Open(ctx context.Context, method string, t Target, rng string) (*http.Response, error) {
	u, err := url.Parse(t.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, ErrBadTarget
	}
	if err := p.guard(ctx, u.Hostname()); err != nil {
		return nil, err
	}
	requestURL := t.URL
	if t.Relay != "" {
		relay, err := url.Parse(t.Relay)
		if err != nil || relay.Scheme != "http" || relay.Host == "" {
			return nil, ErrBadTarget
		}
		q := relay.Query()
		q.Set("u", base64.RawURLEncoding.EncodeToString([]byte(t.URL)))
		relay.RawQuery = q.Encode()
		requestURL = relay.String()
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return nil, ErrBadTarget
	}
	ua := t.UA
	if ua == "" {
		ua = defaultUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	if t.Referer != "" {
		req.Header.Set("Referer", t.Referer)
	}
	if t.Cookie != "" {
		req.Header.Set("Cookie", t.Cookie)
	}
	for k, v := range t.Headers {
		if safeRequestHeaders[http.CanonicalHeaderKey(k)] {
			req.Header.Set(k, v)
		}
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	if t.Relay != "" && t.RelayToken != "" {
		req.Header.Set("X-Relay-Token", t.RelayToken)
	}
	return p.HTTP.Do(req)
}

var safeRequestHeaders = map[string]bool{
	"Accept": true, "Accept-Language": true, "Origin": true, "X-Requested-With": true,
	"Sec-Fetch-Dest": true, "Sec-Fetch-Mode": true, "Sec-Fetch-Site": true, "Sec-Fetch-User": true,
	"Dnt": true, "Cache-Control": true, "Pragma": true,
}

var passHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"}

// ServeHTTP GET {PathPrefix}{token}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	t, err := p.Signer.Verify(strings.TrimPrefix(r.URL.Path, PathPrefix))
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	resp, err := p.Open(r.Context(), r.Method, t, r.Header.Get("Range"))
	switch {
	case errors.Is(err, errPrivate):
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	case errors.Is(err, ErrBadTarget):
		http.Error(w, "bad target", http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	br := bufio.NewReaderSize(resp.Body, 4096)
	head, _ := br.Peek(16)
	if resp.StatusCode == http.StatusOK && IsM3U8(head) {
		body, err := io.ReadAll(io.LimitReader(br, 4<<20))
		if err != nil {
			http.Error(w, "upstream read failed", http.StatusBadGateway)
			return
		}
		if t.AdBlock {
			body = FilterAds(body)
		}
		base := resp.Request.URL
		if t.Relay != "" {
			// 代取响应的请求地址是手机，不是原站；分片必须按原视频地址展开
			base, _ = url.Parse(t.URL)
			if final := resp.Header.Get("X-Upstream-Final-URL"); final != "" {
				if u, err := url.Parse(final); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
					base = u
				}
			}
		}
		out := Rewrite(body, base, func(abs string) string {
			return p.URL(Target{URL: abs, Referer: t.Referer, UA: t.UA, Cookie: t.Cookie, AdBlock: t.AdBlock, Relay: t.Relay, RelayToken: t.RelayToken, Headers: t.Headers, FallbackReferer: t.FallbackReferer})
		})
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(out)
		return
	}

	for _, h := range passHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, br)
}
