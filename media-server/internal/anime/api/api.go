// Package api 把番剧聚合的各个能力暴露成 /api/anime/* HTTP 接口（薄层：只做参数解析和 JSON 输出）。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"media-server/internal/anime/airtime"
	"media-server/internal/anime/bangumi"
	"media-server/internal/anime/engine"
	"media-server/internal/anime/follow"
	"media-server/internal/anime/hls"
	"media-server/internal/anime/resolve"
	"media-server/internal/anime/rules"
	"media-server/internal/anime/sniff"
)

// Deps 构造 API 所需的外部依赖
type Deps struct {
	HTTP       *http.Client // 抓页面/调 API 用（带总超时，使用盒子的自定义 DNS）
	StreamHTTP *http.Client // 播放代理用（不能有总超时）
	LookupIP   func(ctx context.Context, host string) ([]net.IP, error)
	Rules      *rules.Set
	CacheDir   string // 元数据与图片缓存根目录
	ImgCacheMB int
	BangumiUA  string
}

// API 番剧聚合接口
type API struct {
	rules    *rules.Set
	bgm      *bangumi.Client
	img      *bangumi.ImageProxy
	engine   *engine.Engine
	proxy    *hls.Proxy
	hub      *sniff.Hub
	resolver *resolve.Resolver
	air      *airtime.DB
	follow   *follow.Service

	searchCache   *ttlCache[[]engine.Item]
	chaptersCache *ttlCache[[]engine.Road]
	sizeCache     *ttlCache[sizeResult]
	rateCache     *ttlCache[float64] // 每部番的码率（字节/秒），估同一部番其它集的大小
}

func New(d Deps) *API {
	cache := bangumi.NewCache(filepath.Join(d.CacheDir, "meta"))
	ua := d.BangumiUA
	if ua == "" {
		ua = bangumi.DefaultUA
	}
	// 抓视频站用的 client 带 cookie 罐：站点发的通行证（如 moonci 的 403 刷新页）记住后续请求自动带上，
	// 和 Kazumi 按插件保存 cookie 是同一个意思。cookie 按域名隔离，不会串站。
	jar, _ := cookiejar.New(nil)
	eng := &engine.Engine{HTTP: &http.Client{Timeout: d.HTTP.Timeout, Transport: d.HTTP.Transport, Jar: jar}}
	proxy := &hls.Proxy{HTTP: d.StreamHTTP, Signer: hls.NewSigner(), LookupIP: d.LookupIP}
	hub := sniff.NewHub()
	maxMB := d.ImgCacheMB
	if maxMB <= 0 {
		maxMB = 300
	}
	img := &bangumi.ImageProxy{HTTP: d.HTTP, UA: ua, Dir: filepath.Join(d.CacheDir, "img"), MaxBytes: int64(maxMB) << 20}
	go img.Trim() // 启动时先清一次
	a := &API{
		rules:         d.Rules,
		bgm:           bangumi.NewClient(d.HTTP, ua, cache),
		img:           img,
		engine:        eng,
		proxy:         proxy,
		hub:           hub,
		resolver:      resolve.New(eng, proxy, hub),
		air:           &airtime.DB{HTTP: d.HTTP, File: filepath.Join(d.CacheDir, "airtime.json")},
		searchCache:   newTTLCache[[]engine.Item](10 * time.Minute),
		chaptersCache: newTTLCache[[]engine.Road](10 * time.Minute),
		sizeCache:     newTTLCache[sizeResult](6 * time.Hour),
		rateCache:     newTTLCache[float64](6 * time.Hour),
	}
	a.follow = follow.New(follow.Deps{
		File: filepath.Join(d.CacheDir, "follows.json"),
		Rules: func() []string {
			var names []string
			for _, r := range a.rules.All() {
				if r.Unsupported() == "" {
					names = append(names, r.Name)
				}
			}
			return names
		},
		Search: func(ctx context.Context, rule, q string) ([]engine.Item, error) {
			r := a.rules.Get(rule)
			if r == nil {
				return nil, errors.New("没有这个规则")
			}
			return a.engine.Search(ctx, r, q)
		},
		// 查更新要最新的选集，不走缓存；查到的顺手放进缓存，播放页打开就是新的
		Chapters: func(ctx context.Context, rule, src string) ([]engine.Road, error) {
			r := a.rules.Get(rule)
			if r == nil {
				return nil, errors.New("没有这个规则")
			}
			roads, err := a.engine.Chapters(ctx, r, src)
			if err == nil {
				a.chaptersCache.set(r.Name+"|"+src, roads)
			}
			return roads, err
		},
		Air: a.air.Get,
	})
	return a
}

// Start 启动后台任务：每天更新番剧播出时间、按时去片源站查追番更新
func (a *API) Start(ctx context.Context) {
	go a.air.Run(ctx)
	go a.follow.Run(ctx)
}

// ---- 给"下载到盒子"（main 包的 anime_download.go）用的能力 ----

// Rule 按名字取片源规则
func (a *API) Rule(name string) *rules.Rule { return a.rules.Get(name) }

// ResolveTarget 解析某一集的视频真实地址和取流请求头（与播放共用解析与缓存）；
// 站点要手机帮忙而手机不在线时返回 resolve.ErrNeedWorker
func (a *API) ResolveTarget(ctx context.Context, rule *rules.Rule, ep, ref string) (hls.Target, string, error) {
	return a.resolver.ResolveTarget(ctx, rule, ep, ref)
}

// Fetch 按 t 的请求头（Referer/UA/Cookie）取 rawURL（视频、分段、密钥都走这里，内网地址照样挡掉）
func (a *API) Fetch(ctx context.Context, t hls.Target, rawURL, rng string) (*http.Response, error) {
	t.URL = rawURL
	return a.proxy.Open(ctx, http.MethodGet, t, rng)
}

// Register 把路由挂到 mux 上
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/anime/hot", a.handleHot)
	mux.HandleFunc("GET /api/anime/calendar", a.handleCalendar)
	mux.HandleFunc("GET /api/anime/search", a.handleSearch)
	mux.HandleFunc("GET /api/anime/subject/{id}", a.handleSubject)
	mux.HandleFunc("GET /api/anime/subject/{id}/episodes", a.handleEpisodes)
	mux.Handle("GET /api/anime/img", a.img)

	mux.HandleFunc("GET /api/anime/rules", a.handleRules)
	mux.HandleFunc("GET /api/anime/source/search", a.handleSourceSearch)
	mux.HandleFunc("GET /api/anime/source/chapters", a.handleSourceChapters)
	mux.HandleFunc("GET /api/anime/resolve", a.handleResolve)
	mux.Handle(hls.PathPrefix, a.proxy)

	mux.HandleFunc("GET /api/anime/worker/poll", a.handleWorkerPoll)
	mux.HandleFunc("POST /api/anime/worker/result", a.handleWorkerResult)
	mux.HandleFunc("GET /api/anime/worker/status", a.handleWorkerStatus)

	mux.HandleFunc("GET /api/anime/follows", a.handleFollows)
	mux.HandleFunc("POST /api/anime/follows", a.handleFollowAdd)
	mux.HandleFunc("DELETE /api/anime/follows/{id}", a.handleFollowRemove)
	mux.HandleFunc("POST /api/anime/follows/{id}/source", a.handleFollowSource)
	mux.HandleFunc("GET /api/anime/follows/events", a.handleFollowEvents)
	mux.HandleFunc("GET /api/anime/airtime", a.handleAirtime)
	mux.HandleFunc("GET /api/anime/size", a.handleSize)
}

// ---- 公共小工具 ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// intParam 读整数查询参数，缺省或非法用 def，并限制在 [lo, hi]
func intParam(r *http.Request, name string, def, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		n = def
	}
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
}

// ttlCache 很小的带过期时间的内存缓存（站点搜索/选集结果用）
type ttlCache[T any] struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[string]ttlEntry[T]
}

type ttlEntry[T any] struct {
	v   T
	exp time.Time
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] {
	return &ttlCache[T]{ttl: ttl, m: map[string]ttlEntry[T]{}}
}

func (c *ttlCache[T]) get(k string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		var zero T
		return zero, false
	}
	return e.v, true
}

func (c *ttlCache[T]) set(k string, v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 500 { // 防止无限增长：满了就清掉过期的，仍然很多就整个重置
		now := time.Now()
		for key, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, key)
			}
		}
		if len(c.m) > 500 {
			c.m = map[string]ttlEntry[T]{}
		}
	}
	c.m[k] = ttlEntry[T]{v: v, exp: time.Now().Add(c.ttl)}
}
