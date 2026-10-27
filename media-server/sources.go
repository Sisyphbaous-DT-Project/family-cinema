package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// TorrentLink 一个磁力/种子资源（同一部片子的不同版本：1080p/4K 等）
type TorrentLink struct {
	Name   string `json:"name"`   // 版本标注
	Magnet string `json:"magnet"` // magnet:?xt=...
	Size   string `json:"size"`   // 可读大小（站方标注）
}

// SourceResult 一个搜索源的命中条目
type SourceResult struct {
	Site     string        `json:"site"`     // dytt8899 | dygangs | mp4z6 | mikan
	SiteName string        `json:"siteName"` // 站点显示名
	Title    string        `json:"title"`
	URL      string        `json:"url"`   // 详情页地址
	Image    string        `json:"image"` // 封面地址（原站或代理）
	Torrents []TorrentLink `json:"torrents"`
}

// searchSource 搜索源接口
type searchSource interface {
	Name() string
	// Search 关键词搜索，返回条目列表（不含磁力或含均可）
	Search(ctx context.Context, kw string) ([]SourceResult, error)
	// FetchDetail 抓取详情页，返回磁力列表与封面
	FetchDetail(ctx context.Context, pageURL string) ([]TorrentLink, string, error)
}

var (
	// httpClient 全局 HTTP 客户端：自定义 DNS（Android 无 resolv.conf）+ 超时
	httpClient *http.Client
	// 浏览器 UA，规避部分站点反爬
	ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// initHTTPClient 初始化全局 HTTP 客户端（读取 CA 目录/文件）
func initHTTPClient(cfg Config) {
	pool := x509.NewCertPool()
	if cfg.CAFile != "" {
		if fi, err := os.Stat(cfg.CAFile); err == nil && fi.IsDir() {
			// Android 的 CA 目录（/system/etc/security/cacerts），含 *.0 和 PEM
			entries, err := os.ReadDir(cfg.CAFile)
			if err == nil {
				for _, e := range entries {
					if e.IsDir() {
						continue
					}
					if data, err := os.ReadFile(filepath.Join(cfg.CAFile, e.Name())); err == nil {
						pool.AppendCertsFromPEM(data)
					}
				}
			}
		} else if data, err := os.ReadFile(cfg.CAFile); err == nil {
			pool.AppendCertsFromPEM(data)
		}
	}
	if len(pool.Subjects()) == 0 {
		// 无自定义 CA 时用系统证书（开发机/正常环境）
		if sysPool, err := x509.SystemCertPool(); err == nil {
			pool = sysPool
		}
	}
	httpClient = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialWithDNS,
			TLSClientConfig:     &tls.Config{RootCAs: pool},
			ForceAttemptHTTP2:   false,
			MaxIdleConnsPerHost: 4,
		},
	}
}

func newReq(method, rawURL string, body *strings.Reader) (*http.Request, error) {
	// 注意：nil *strings.Reader 转 io.Reader 接口后非 nil，必须显式传 nil
	var rdr io.Reader
	if body != nil {
		rdr = body
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req, nil
}

// fetchGET 带超时抓页，支持自定义 transports（代理等）
func fetchWith(ctx context.Context, client *http.Client, rawURL string, header map[string]string) (string, error) {
	req, err := newReq("GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := readAllLimit(resp.Body, 8<<20) // 8MB 上限
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// fetchRaw 抓页返回原始字节（编码未知时用）
func fetchRaw(ctx context.Context, client *http.Client, rawURL string, header map[string]string) ([]byte, error) {
	req, err := newReq("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readAllLimit(resp.Body, 8<<20)
}

// postFormRaw 表单提交，手动处理 302/301 跳转后返回最终响应字节
// 帝国 CMS 的 302 Location 是相对路径且可能带提示页，交给 Go 自动跟随行为不可控
func postFormRaw(ctx context.Context, client *http.Client, action string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", action, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Referer", action)
	// 不自动跟随，自己控制跳转
	noFollow := &http.Client{Timeout: client.Timeout, Transport: client.Transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noFollow.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusSeeOther {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return readAllLimit(resp.Body, 8<<20)
		}
		target := absURL(action, loc)
		return fetchRaw(ctx, client, target, map[string]string{"Referer": action})
	}
	return readAllLimit(resp.Body, 8<<20)
}

func absURL(base, ref string) string {
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	u, err := url.Parse(base)
	if err != nil {
		return ref
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return u.ResolveReference(ru).String()
}

var reMagnet = regexp.MustCompile(`magnet:\?xt=urn:btih:[a-zA-Z0-9]+[^"'<\s]*`)
var reEd2k = regexp.MustCompile(`ed2k://[^"'<\s]+`)

// 提取页面里所有磁力链接，可附带标签文本
func extractMagnets(html string) []TorrentLink {
	var out []TorrentLink
	seen := map[string]bool{}
	// 常见格式：<a href="magnet:...">1080p</a>
	re := regexp.MustCompile(`(?is)<a[^>]+href="(magnet:[^"]+)"[^>]*>([^<]*)</a>`)
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		mag := strings.TrimSpace(m[1])
		label := strings.TrimSpace(m[2])
		if seen[mag] {
			continue
		}
		seen[mag] = true
		if label == "" || len(label) > 60 {
			label = ""
		}
		out = append(out, TorrentLink{Name: label, Magnet: mag})
	}
	// 兜底：裸磁力
	for _, mag := range reMagnet.FindAllString(html, -1) {
		if !seen[mag] {
			seen[mag] = true
			out = append(out, TorrentLink{Magnet: mag})
		}
	}
	return out
}

// 图片地址处理：mikan 站图 -> 本服务代理；其余原样
func (s *Server) procImage(site, img string) string {
	if img == "" {
		return ""
	}
	if site == "mikan" {
		// 经配置的自托管代理取 mikan 图片
		if strings.Contains(img, "mikanani.me") {
			return "/api/mikan-img?url=" + url.QueryEscape(img)
		}
		return img
	}
	return img
}

// ---------- 站点配置表 ----------

var siteNames = map[string]string{
	"dytt8899": "电影天堂",
	"dygangs":  "电影港",
	"mp4z6":    "MP4资源",
	"mikan":    "蜜柑计划",
}

var sources []searchSource

func initSources(cfg Config) {
	sources = nil
	if cfg.Sources.Dytt8899 {
		sources = append(sources, &dyttSource{})
	}
	if cfg.Sources.Dygangs {
		sources = append(sources, &dygangsSource{})
	}
	if cfg.Sources.Mp4z6 {
		sources = append(sources, &mp4z6Source{})
	}
	if cfg.Sources.Mikan {
		sources = append(sources, &mikanSource{})
	}
	log.Printf("已启用搜索源: %d 个", len(sources))
}
