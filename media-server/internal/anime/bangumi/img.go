package bangumi

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ImageProxy Bangumi 封面代理：只放行 bgm.tv 域名，磁盘缓存，总量超限时按最久未访问清理。
// 前端不直连 lain.bgm.tv，避免防盗链、混合内容和重复下载。
type ImageProxy struct {
	HTTP     *http.Client
	UA       string
	Dir      string
	MaxBytes int64 // 缓存总量上限，0 表示不限

	mu     sync.Mutex
	writes int
}

// ProxyURL 把上游图片地址换成走本服务代理的地址
func ProxyURL(raw string) string {
	if raw == "" {
		return ""
	}
	return "/api/anime/img?u=" + url.QueryEscape(raw)
}

// allowedHost 只允许 bgm.tv 及其子域（lain.bgm.tv 等）
func allowedHost(h string) bool {
	h = strings.ToLower(h)
	return h == "bgm.tv" || strings.HasSuffix(h, ".bgm.tv")
}

func (p *ImageProxy) path(raw string) string {
	sum := sha1.Sum([]byte(raw))
	return filepath.Join(p.Dir, hex.EncodeToString(sum[:])+".img")
}

// cached 缓存文件格式：第一行是 Content-Type，其后是图片字节
func (p *ImageProxy) readCached(raw string) (ct string, body []byte, ok bool) {
	data, err := os.ReadFile(p.path(raw))
	if err != nil {
		return "", nil, false
	}
	i := bytes.IndexByte(data, '\n')
	if i < 0 {
		return "", nil, false
	}
	// 读到了就刷新访问时间，清理时按它判断"最久未访问"
	now := time.Now()
	_ = os.Chtimes(p.path(raw), now, now)
	return string(data[:i]), data[i+1:], true
}

func (p *ImageProxy) writeCached(raw, ct string, body []byte) {
	if p.Dir == "" {
		return
	}
	_ = os.MkdirAll(p.Dir, 0o755)
	data := append([]byte(ct+"\n"), body...)
	tmp := p.path(raw) + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, p.path(raw))
	p.mu.Lock()
	p.writes++
	need := p.writes%50 == 0
	p.mu.Unlock()
	if need {
		go p.Trim()
	}
}

// Trim 缓存超过上限时，删除最久未访问的文件直到降到上限的 80%
func (p *ImageProxy) Trim() {
	if p.MaxBytes <= 0 || p.Dir == "" {
		return
	}
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return
	}
	type f struct {
		path string
		size int64
		mod  time.Time
	}
	var files []f
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".img") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, f{filepath.Join(p.Dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= p.MaxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	target := p.MaxBytes * 8 / 10
	for _, x := range files {
		if total <= target {
			break
		}
		if os.Remove(x.path) == nil {
			total -= x.size
		}
	}
}

// ServeHTTP GET /api/anime/img?u=<上游图片地址>
func (p *ImageProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !allowedHost(u.Hostname()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if ct, body, ok := p.readCached(raw); ok {
		writeImage(w, ct, body)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", p.UA)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream "+resp.Status, http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadGateway)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}
	p.writeCached(raw, ct, body)
	writeImage(w, ct, body)
}

func writeImage(w http.ResponseWriter, ct string, body []byte) {
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(body)
}
