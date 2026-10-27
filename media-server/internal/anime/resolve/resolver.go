package resolve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"media-server/internal/anime/engine"
	"media-server/internal/anime/hls"
	"media-server/internal/anime/rules"
	"media-server/internal/anime/sniff"
)

// Result 解析结果（直接序列化给前端）
type Result struct {
	Status  string `json:"status"` // ok | need-worker | fail
	Kind    string `json:"kind,omitempty"`
	PlayURL string `json:"playUrl,omitempty"` // 走盒子播放代理的相对地址
	Via     string `json:"via,omitempty"`     // static | sniff
	Reason  string `json:"reason,omitempty"`
}

const (
	cacheTTL      = 20 * time.Minute // m3u8 里的令牌会过期，不能缓存太久
	maxStaticPage = 5                // 静态解析最多抓几个页面（播放页 + iframe）
	maxIframeDeep = 2
	maxProbe      = 6 // 每个页面最多探测几个候选地址
)

type cached struct {
	target hls.Target
	kind   string
	via    string
	exp    time.Time
}

// Resolver 解析某一集的视频地址
type Resolver struct {
	Engine *engine.Engine
	Proxy  *hls.Proxy
	Hub    *sniff.Hub

	mu    sync.Mutex
	cache map[string]cached
}

func New(e *engine.Engine, p *hls.Proxy, h *sniff.Hub) *Resolver {
	return &Resolver{Engine: e, Proxy: p, Hub: h, cache: map[string]cached{}}
}

func (r *Resolver) okResult(c cached) Result {
	return Result{Status: "ok", Kind: c.kind, Via: c.via, PlayURL: r.Proxy.URL(c.target)}
}

// ErrNeedWorker 这个站要手机 App（解析助手）帮忙，但现在没有在线的手机
var ErrNeedWorker = errors.New("这个站需要手机 App 帮忙解析，请先打开手机 App")

// Resolve 解析播放页 epURL，给播放器用。referer 通常是详情页地址。
func (r *Resolver) Resolve(ctx context.Context, rule *rules.Rule, epURL, referer string) Result {
	c, err := r.resolve(ctx, rule, epURL, referer)
	switch {
	case errors.Is(err, ErrNeedWorker):
		return Result{Status: "need-worker", Reason: err.Error()}
	case err != nil:
		return Result{Status: "fail", Reason: err.Error()}
	}
	return r.okResult(c)
}

// ResolveTarget 解析出视频真实地址和取流要带的请求头（下载到盒子用），kind 为 hls 或 mp4。
// 没有在线的手机助手时返回 ErrNeedWorker。
func (r *Resolver) ResolveTarget(ctx context.Context, rule *rules.Rule, epURL, referer string) (hls.Target, string, error) {
	c, err := r.resolve(ctx, rule, epURL, referer)
	return c.target, c.kind, err
}

func (r *Resolver) resolve(ctx context.Context, rule *rules.Rule, epURL, referer string) (cached, error) {
	key := rule.Name + "|" + epURL
	r.mu.Lock()
	if c, ok := r.cache[key]; ok && time.Now().Before(c.exp) {
		r.mu.Unlock()
		return c, nil
	}
	r.mu.Unlock()

	var c cached
	if f := r.static(ctx, rule, epURL, referer); f != nil {
		c = cached{
			target: hls.Target{URL: f.URL, Referer: f.Referer, UA: rule.UserAgent, AdBlock: rule.AdBlocker},
			kind:   Kind(f.URL), via: "static",
		}
	} else {
		res, err := r.Hub.Sniff(ctx, sniff.Request{
			Key: key, PageURL: epURL, Referer: referer, UA: rule.UserAgent, TimeoutMs: 22000,
		})
		switch {
		case errors.Is(err, sniff.ErrNoWorker):
			return cached{}, ErrNeedWorker
		case err != nil:
			return cached{}, err
		case !res.OK || res.URL == "":
			reason := res.Error
			if reason == "" {
				reason = "手机没有嗅探到视频地址"
			}
			return cached{}, errors.New(reason)
		}
		picked, err := r.pickSniffed(ctx, rule, epURL, res)
		if err != nil {
			return cached{}, errors.New("手机嗅探到了视频，但盒子取不到：" + err.Error())
		}
		c = picked
	}
	c.exp = time.Now().Add(cacheTTL)
	r.mu.Lock()
	r.cache[key] = c
	r.mu.Unlock()
	return c, nil
}

func sniffHeader(headers map[string]string, name string) (string, bool) {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// maxSniffTry 手机回报的候选地址最多试几个
const maxSniffTry = 4

// pickSniffed 先试盒子直连；全部失败才用手机代取，代取也要实际探测成功才返回。
func (r *Resolver) pickSniffed(ctx context.Context, rule *rules.Rule, epURL string, res sniff.Result) (cached, error) {
	cands := append([]sniff.Candidate{{URL: res.URL, Kind: res.Kind, Headers: res.Headers}}, res.Alts...)
	if len(cands) > maxSniffTry {
		cands = cands[:maxSniffTry]
	}
	targetOf := func(cd sniff.Candidate) (hls.Target, string) {
		// 先完全照手机原请求；没有 Referer 时不能擅自补上，播放页地址只留作 404 后的备选
		ref, _ := sniffHeader(cd.Headers, "Referer")
		ua, _ := sniffHeader(cd.Headers, "User-Agent")
		if ua == "" {
			ua = rule.UserAgent
		}
		cookie, _ := sniffHeader(cd.Headers, "Cookie")
		kind := cd.Kind
		if kind != "mp4" && kind != "hls" {
			kind = Kind(cd.URL)
		}
		return hls.Target{URL: cd.URL, Referer: ref, FallbackReferer: epURL, UA: ua, Cookie: cookie, AdBlock: rule.AdBlocker, Headers: cd.Headers}, kind
	}
	var firstErr error
	for _, cd := range cands {
		t, kind := targetOf(cd)
		err := r.probeTarget(ctx, t, kind == "hls")
		if err == nil {
			return cached{target: t, kind: kind, via: "sniff"}, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if res.RelayAddr != "" && res.RelayPort > 0 && res.RelayToken != "" {
		relay := "http://" + net.JoinHostPort(res.RelayAddr, strconv.Itoa(res.RelayPort)) + "/relay"
		for _, cd := range cands {
			t, kind := targetOf(cd)
			t.Relay, t.RelayToken = relay, res.RelayToken
			if err := r.probeTarget(ctx, t, kind == "hls"); err == nil {
				return cached{target: t, kind: kind, via: "sniff-relay"}, nil
			}
		}
	}
	return cached{}, firstErr
}

type found struct{ URL, Referer string }

// static 静态解析：读播放页（必要时跟进 iframe）找候选地址，探测可用才算成功
func (r *Resolver) static(ctx context.Context, rule *rules.Rule, epURL, referer string) *found {
	// 接口模式的规则可能直接给出视频地址（如 giriGiriLove 的 .m3u8），能播就不用再当网页翻
	if IsDirectMedia(epURL) && r.probe(ctx, rule, epURL, referer) {
		return &found{URL: epURL, Referer: referer}
	}
	type page struct {
		url, ref string
		depth    int
	}
	queue := []page{{epURL, referer, 0}}
	visited := map[string]bool{}
	for len(queue) > 0 && len(visited) < maxStaticPage {
		p := queue[0]
		queue = queue[1:]
		if visited[p.url] {
			continue
		}
		visited[p.url] = true
		pg, err := r.Engine.Fetch(ctx, rule, http.MethodGet, p.url, nil, p.ref)
		if err != nil {
			continue
		}
		cands, iframes := Extract(string(pg.Body), pg.FinalURL)
		for i, c := range cands {
			if i >= maxProbe {
				break
			}
			if r.probe(ctx, rule, c, pg.FinalURL) {
				return &found{URL: c, Referer: pg.FinalURL}
			}
		}
		if p.depth < maxIframeDeep {
			for _, f := range iframes {
				queue = append(queue, page{f, pg.FinalURL, p.depth + 1})
			}
		}
	}
	return nil
}

// probe 请求候选地址的前 1KB，确认真的是可播放的媒体（401/403 或返回网页都算失败）
func (r *Resolver) probe(ctx context.Context, rule *rules.Rule, u, referer string) bool {
	return r.probeTarget(ctx, hls.Target{URL: u, Referer: referer, UA: rule.UserAgent}, Kind(u) == "hls") == nil
}

func headerRetryStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound
}

// probeTarget 按目标自带的头请求前 1KB；isHLS 时要求内容是 m3u8，否则要求像 mp4/flv 视频流。
// 失败时返回一句能直接给用户看的原因（只含主机名，不含地址里的签名参数）。
func (r *Resolver) probeTarget(ctx context.Context, t hls.Target, isHLS bool) error {
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	host := t.URL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?"); i >= 0 {
		host = host[:i]
	}
	channel := "直连"
	if t.Relay != "" {
		channel = "代取"
	}
	rng := ""
	if !isHLS {
		rng = "bytes=0-1023"
	}
	resp, err := r.Proxy.Open(pctx, http.MethodGet, t, rng)
	if err == nil && !isHLS && resp.StatusCode == http.StatusNotFound {
		// 有的站拒绝 Range 探测；完整 GET 仍然只读前 1KB，不会把整集拉下来
		resp.Body.Close()
		resp, err = r.Proxy.Open(pctx, http.MethodGet, t, "")
	}
	if err == nil && headerRetryStatus(resp.StatusCode) && t.Referer == "" && t.FallbackReferer != "" {
		// 先尊重原请求的"无 Referer"；确实被拒后，才退回播放页地址
		resp.Body.Close()
		t.Referer = t.FallbackReferer
		log.Printf("%s 视频探测补 Referer: %s", channel, t.Referer)
		resp, err = r.Proxy.Open(pctx, http.MethodGet, t, "")
	}
	if err == nil && headerRetryStatus(resp.StatusCode) {
		if _, hasOrigin := sniffHeader(t.Headers, "Origin"); !hasOrigin {
			if u, parseErr := url.Parse(t.Referer); parseErr == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
				// WebView 有时不暴露 Origin；只在 404 时按播放页来源补一次，不改变正常请求
				headers := map[string]string{}
				for k, v := range t.Headers {
					headers[k] = v
				}
				origin := u.Scheme + "://" + u.Host
				headers["Origin"] = origin
				t.Headers = headers
				resp.Body.Close()
				log.Printf("%s 视频探测补 Origin: %s", channel, origin)
				resp, err = r.Proxy.Open(pctx, http.MethodGet, t, "")
			}
		}
	}
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // 去掉错误里带的完整地址
		}
		log.Printf("%s 视频探测失败 %s: %v", channel, host, err)
		return fmt.Errorf("盒子连不上 %s，可能被网络屏蔽", host)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		log.Printf("%s 视频探测失败 %s: HTTP %d", channel, host, resp.StatusCode)
		return fmt.Errorf("%s 拒绝了盒子的请求（HTTP %d），可能绑定了手机的网络", host, resp.StatusCode)
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	ok := false
	if isHLS {
		ok = hls.IsM3U8(head)
	} else {
		ok = strings.Contains(ct, "video") || strings.Contains(ct, "octet-stream") ||
			(len(head) >= 8 && string(head[4:8]) == "ftyp")
	}
	if !ok {
		log.Printf("%s 视频探测失败 %s: 内容不是视频 (类型 %q, 前 16 字节 %q)", channel, host, ct, head[:min(len(head), 16)])
		return fmt.Errorf("%s 返回的不是视频", host)
	}
	return nil
}
