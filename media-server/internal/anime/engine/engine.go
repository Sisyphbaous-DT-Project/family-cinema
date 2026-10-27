package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"media-server/internal/anime/rules"
)

// DefaultUA 规则没有指定 userAgent 时使用的桌面浏览器 UA
const DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

const maxPageBytes = 4 << 20

// Engine 抓取页面并按规则解析。HTTP 由外部注入（盒子上需要自定义 DNS 的那个 client）。
type Engine struct {
	HTTP *http.Client
}

// Page 一次页面抓取的结果
type Page struct {
	Body        []byte
	ContentType string
	FinalURL    string // 跟随重定向后的最终地址，用来补全相对链接
}

// Fetch 用规则指定的 UA/Referer 抓页面。body 非 nil 时按表单 POST。
func (e *Engine) Fetch(ctx context.Context, r *rules.Rule, method, target string, body io.Reader, referer string) (*Page, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	ua := r.UserAgent
	if ua == "" {
		ua = DefaultUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if referer == "" {
		referer = r.Referer
	}
	if referer == "" {
		referer = r.BaseURL
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := e.HTTP.Do(req)
	if err != nil && ctx.Err() == nil && body == nil {
		// 视频站偶尔直接断连（EOF），GET 请求幂等，重试一次
		time.Sleep(300 * time.Millisecond)
		resp, err = e.HTTP.Do(req)
	}
	if err == nil && resp.StatusCode == http.StatusForbidden && body == nil && len(resp.Cookies()) > 0 && e.HTTP.Jar != nil {
		// 有的站（如 moonci）第一次先回 403 发一张通行证 cookie，再让浏览器刷新本页；带上 cookie 重试一次
		resp.Body.Close()
		resp, err = e.HTTP.Do(req)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	return &Page{Body: data, ContentType: resp.Header.Get("Content-Type"), FinalURL: resp.Request.URL.String()}, nil
}

// buildSearch 把 @keyword 代入搜索地址。关键词落在路径里用 %20，落在查询串里用 +。
func buildSearch(searchURL, kw string) (base, query string) {
	i := strings.Index(searchURL, "?")
	if i < 0 {
		return strings.ReplaceAll(searchURL, "@keyword", url.PathEscape(kw)), ""
	}
	base = strings.ReplaceAll(searchURL[:i], "@keyword", url.PathEscape(kw))
	query = strings.ReplaceAll(searchURL[i+1:], "@keyword", url.QueryEscape(kw))
	return base, query
}

// Search 用规则在视频站里搜关键词
func (e *Engine) Search(ctx context.Context, r *rules.Rule, kw string) ([]Item, error) {
	if reason := r.Unsupported(); reason != "" {
		return nil, errors.New("规则暂不支持: " + reason)
	}
	if r.SearchByAPI() {
		return e.searchAPI(ctx, r, kw)
	}
	base, query := buildSearch(r.SearchURL, kw)
	var page *Page
	var err error
	if r.UsePost {
		page, err = e.Fetch(ctx, r, http.MethodPost, base, strings.NewReader(query), "")
	} else {
		target := base
		if query != "" {
			target += "?" + query
		}
		page, err = e.Fetch(ctx, r, http.MethodGet, target, nil, "")
	}
	if err != nil {
		return nil, err
	}
	return ParseSearch(r, page.Body, page.ContentType, page.FinalURL)
}

// Chapters 取某部番在该视频站的线路与选集。src 是搜索结果给的来源：
// 网页搜索给的是详情页地址；接口搜索可能只给一个编号（接口选集直接用，网页选集按 baseURL 补成地址）。
func (e *Engine) Chapters(ctx context.Context, r *rules.Rule, src string) ([]Road, error) {
	if reason := r.Unsupported(); reason != "" {
		return nil, errors.New("规则暂不支持: " + reason)
	}
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, errors.New("缺少作品来源")
	}
	if r.ChaptersByAPI() {
		return e.chaptersAPI(ctx, r, src)
	}
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		src = normalizeURL(r.BaseURL, src)
		if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
			return nil, errors.New("作品详情页地址不对")
		}
	}
	page, err := e.Fetch(ctx, r, http.MethodGet, src, nil, "")
	if err != nil {
		return nil, err
	}
	return ParseChapters(r, page.Body, page.ContentType, page.FinalURL)
}
