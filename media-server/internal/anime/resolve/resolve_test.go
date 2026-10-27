package resolve

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"media-server/internal/anime/engine"
	"media-server/internal/anime/hls"
	"media-server/internal/anime/rules"
	"media-server/internal/anime/sniff"
)

func TestKind(t *testing.T) {
	if Kind("https://a/b.mp4?x=1") != "mp4" || Kind("https://a/b.m3u8") != "hls" || Kind("https://a/b") != "hls" {
		t.Error("Kind 判断不对")
	}
}

func TestExtractPlayerVar(t *testing.T) {
	enc1 := url.PathEscape("https://cdn.test/e1/index.m3u8")
	enc2 := base64.StdEncoding.EncodeToString([]byte(url.PathEscape("https://cdn.test/e2/index.m3u8")))
	page := fmt.Sprintf(`<script>var x=1;</script>
<script type="text/javascript">var player_aaaa={"flag":"play","encrypt":0,"link":"/p/1.html","vod_data":{"vod_name":"a}b"},"url":"https:\/\/cdn.test\/e0\/index.m3u8","from":"x"}</script>
<script>var player_bbbb = {"encrypt":1,"url":"%s"};</script>
<script>var player_cccc = {"encrypt":2,"url":"%s"};</script>
<script>var player_dddd = {"encrypt":0,"url":"/rel/v.mp4"};</script>
<script>var player_eeee = {"encrypt":0,"url":"Doki-abcdef"};</script>`, enc1, enc2)
	cands, _ := Extract(page, "https://site.test/play/1.html")
	want := []string{
		"https://cdn.test/e0/index.m3u8", // 字符串里的 } 不能把对象截断
		"https://cdn.test/e1/index.m3u8",
		"https://cdn.test/e2/index.m3u8",
		"https://site.test/rel/v.mp4", // 相对地址按页面补全
	}
	if strings.Join(cands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("候选地址不对:\n got %v\nwant %v", cands, want)
	}
}

func TestExtractPlainAndIframe(t *testing.T) {
	page := `<script>var u = "https:\/\/v.cdn.test\/a\/index.m3u8?t=1";</script>
<iframe src="/jx/?url=abc" width=1></iframe><iframe src="about:blank"></iframe>`
	cands, ifr := Extract(page, "https://site.test/play")
	if len(cands) != 1 || cands[0] != "https://v.cdn.test/a/index.m3u8?t=1" {
		t.Errorf("明文地址提取不对: %v", cands)
	}
	if len(ifr) != 1 || ifr[0] != "https://site.test/jx/?url=abc" {
		t.Errorf("iframe 提取不对: %v", ifr)
	}
}

// ---- 解析器：用本地模拟站点走完整链路 ----

type fakeSite struct {
	srv *httptest.Server
}

func newSite(t *testing.T) *fakeSite {
	s := &fakeSite{}
	mux := http.NewServeMux()
	// 视频：要求 Referer 指向 /play 页，否则 403
	mux.HandleFunc("/v/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Referer"), "/play") {
			http.Error(w, "no ref", 403)
			return
		}
		io.WriteString(w, "#EXTM3U\n#EXTINF:4,\nseg.ts\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/locked/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", 401)
	})
	// 播放页 1：明文 player_aaaa
	mux.HandleFunc("/play/direct", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<script>var player_aaaa={"encrypt":0,"url":"%s/v/index.m3u8"}</script>`, s.srv.URL)
	})
	// 播放页 2：只有 iframe，真正的地址在 iframe 页里
	mux.HandleFunc("/play/iframe", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<iframe src="/play/inner"></iframe>`)
	})
	mux.HandleFunc("/play/inner", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<script>var src="%s/v/index.m3u8"</script>`, s.srv.URL)
	})
	// 播放页 3：地址要授权（401）→ 静态失败，得靠嗅探
	mux.HandleFunc("/play/locked", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<script>var player_aaaa={"encrypt":0,"url":"%s/locked/index.m3u8"}</script>`, s.srv.URL)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func newResolver(s *fakeSite) (*Resolver, *sniff.Hub) {
	hub := sniff.NewHub()
	proxy := &hls.Proxy{HTTP: s.srv.Client(), Signer: hls.NewSigner(), AllowPrivate: true}
	return New(&engine.Engine{HTTP: s.srv.Client()}, proxy, hub), hub
}

func TestResolveStatic(t *testing.T) {
	s := newSite(t)
	r, _ := newResolver(s)
	rule := &rules.Rule{Name: "t", BaseURL: s.srv.URL + "/", AdBlocker: true}
	for _, ep := range []string{"/play/direct", "/play/iframe"} {
		res := r.Resolve(context.Background(), rule, s.srv.URL+ep, s.srv.URL+"/detail")
		if res.Status != "ok" || res.Via != "static" || res.Kind != "hls" || !strings.HasPrefix(res.PlayURL, hls.PathPrefix) {
			t.Fatalf("%s 静态解析失败: %+v", ep, res)
		}
	}
	// 解析结果里的播放地址确实能通过代理拿到清单（Referer 由解析器带上了播放页）
	res := r.Resolve(context.Background(), rule, s.srv.URL+"/play/direct", "")
	rec := httptest.NewRecorder()
	r.Proxy.ServeHTTP(rec, httptest.NewRequest("GET", res.PlayURL, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), hls.PathPrefix) {
		t.Fatalf("经代理取清单失败: %d %s", rec.Code, rec.Body.String())
	}
}

func TestResolveFallsBackToSniff(t *testing.T) {
	s := newSite(t)
	r, hub := newResolver(s)
	rule := &rules.Rule{Name: "t", BaseURL: s.srv.URL + "/"}
	ep := s.srv.URL + "/play/locked"

	// 没有助手 → need-worker
	if res := r.Resolve(context.Background(), rule, ep, ""); res.Status != "need-worker" {
		t.Fatalf("没有助手应返回 need-worker: %+v", res)
	}

	// 有助手 → 拿到嗅探结果
	hub.WaitTimeout = 3 * time.Second
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			j := hub.Poll(context.Background(), "phone", 50*time.Millisecond)
			if j == nil {
				continue
			}
			hub.Report(sniff.Result{JobID: j.ID, OK: true, URL: s.srv.URL + "/v/index.m3u8", Kind: "hls",
				Headers: map[string]string{"Referer": s.srv.URL + "/play/locked", "User-Agent": "PhoneUA"}})
		}
	}()
	time.Sleep(100 * time.Millisecond) // 等助手登记在线
	res := r.Resolve(context.Background(), rule, ep, "")
	if res.Status != "ok" || res.Via != "sniff" {
		t.Fatalf("应通过嗅探解析成功: %+v", res)
	}
	rec := httptest.NewRecorder()
	r.Proxy.ServeHTTP(rec, httptest.NewRequest("GET", res.PlayURL, nil))
	if rec.Code != 200 {
		t.Fatalf("嗅探结果经代理应可播: %d %s", rec.Code, rec.Body.String())
	}
	// 第二次命中缓存（不再需要助手）
	close(stop)
	if again := r.Resolve(context.Background(), rule, ep, ""); again.Status != "ok" {
		t.Fatalf("应命中解析缓存: %+v", again)
	}
}

// 手机误把播放器网页当成视频回报时，盒子探测后应判失败，而不是返回一个放不了的"成功"
func TestResolveRejectsSniffedWebPage(t *testing.T) {
	s := newSite(t)
	r, hub := newResolver(s)
	rule := &rules.Rule{Name: "t", BaseURL: s.srv.URL + "/"}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if j := hub.Poll(context.Background(), "phone", 50*time.Millisecond); j != nil {
				hub.Report(sniff.Result{JobID: j.ID, OK: true, URL: s.srv.URL + "/play/locked", Kind: "hls"})
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	res := r.Resolve(context.Background(), rule, s.srv.URL+"/play/locked", "")
	if res.Status != "fail" {
		t.Fatalf("回报的是网页，应判失败: %+v", res)
	}
}

// 主地址盒子读不到（如先到的是广告/连不上的 CDN）时，应改用手机附带的备选地址
func TestResolveUsesSniffAlternative(t *testing.T) {
	s := newSite(t)
	r, hub := newResolver(s)
	rule := &rules.Rule{Name: "t", BaseURL: s.srv.URL + "/"}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if j := hub.Poll(context.Background(), "phone", 50*time.Millisecond); j != nil {
				hub.Report(sniff.Result{JobID: j.ID, OK: true, URL: s.srv.URL + "/play/locked", Kind: "mp4",
					Alts: []sniff.Candidate{{URL: s.srv.URL + "/v/index.m3u8", Kind: "hls"}}})
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	res := r.Resolve(context.Background(), rule, s.srv.URL+"/play/locked", "")
	if res.Status != "ok" || res.Kind != "hls" {
		t.Fatalf("主地址不可用时应改用备选的 m3u8: %+v", res)
	}
}

func TestResolveSniffFailureReported(t *testing.T) {
	s := newSite(t)
	r, hub := newResolver(s)
	rule := &rules.Rule{Name: "t", BaseURL: s.srv.URL + "/"}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if j := hub.Poll(context.Background(), "phone", 50*time.Millisecond); j != nil {
				hub.Report(sniff.Result{JobID: j.ID, OK: false, Error: "页面需要验证码"})
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	res := r.Resolve(context.Background(), rule, s.srv.URL+"/play/locked", "")
	if res.Status != "fail" || res.Reason != "页面需要验证码" {
		t.Fatalf("助手失败原因应原样带回: %+v", res)
	}
}

func TestIsDirectMedia(t *testing.T) {
	for u, want := range map[string]bool{
		"https://akua.test/01/playlist.m3u8":          true,
		"https://cdn.test/v.MP4?sign=1":               true,
		"https://site.test/play/1-1.html":             false,
		"https://site.test/player.html?url=a.m3u8":    false,
		"https://next.xifanacg.com/anime/3069/play/7": false,
	} {
		if IsDirectMedia(u) != want {
			t.Errorf("IsDirectMedia(%q) 应为 %v", u, want)
		}
	}
}

func TestResolveFallsBackToPhoneRelay(t *testing.T) {
	s := newSite(t)
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "box can not fetch", http.StatusNotFound)
	}))
	defer direct.Close()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Relay-Token") != "secret" || r.URL.Query().Get("u") == "" {
			http.Error(w, "bad relay request", http.StatusForbidden)
			return
		}
		io.WriteString(w, "#EXTM3U\n#EXTINF:4,\nseg.ts\n#EXT-X-ENDLIST\n")
	}))
	defer relay.Close()
	relayURL, _ := url.Parse(relay.URL)
	relayPort, _ := strconv.Atoi(relayURL.Port())

	r, hub := newResolver(s)
	rule := &rules.Rule{Name: "t", BaseURL: s.srv.URL + "/"}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if j := hub.Poll(context.Background(), "phone", 50*time.Millisecond); j != nil {
				hub.Report(sniff.Result{JobID: j.ID, OK: true, URL: direct.URL + "/v/index.m3u8", Kind: "hls",
					RelayAddr: "127.0.0.1", RelayPort: relayPort, RelayToken: "secret"})
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	res := r.Resolve(context.Background(), rule, s.srv.URL+"/play/locked", "")
	if res.Status != "ok" || res.Via != "sniff-relay" {
		t.Fatalf("直连失败后应走手机代取: %+v", res)
	}
	target, err := r.Proxy.Signer.Verify(strings.TrimPrefix(res.PlayURL, hls.PathPrefix))
	if err != nil || target.Relay == "" || target.RelayToken != "secret" {
		t.Fatalf("播放代理没有继承代取配置: %+v %v", target, err)
	}
}

func TestProbeRangeFallback(t *testing.T) {
	s := newSite(t)
	r, _ := newResolver(s)
	var ranges []string
	mp4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ranges = append(ranges, req.Header.Get("Range"))
		if req.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "....ftypisom")
	}))
	defer mp4.Close()
	if err := r.probeTarget(context.Background(), hls.Target{URL: mp4.URL + "/v.mp4"}, false); err != nil {
		t.Fatalf("Range 被拒后完整 GET 应成功: %v", err)
	}
	if len(ranges) != 2 || ranges[0] == "" || ranges[1] != "" {
		t.Fatalf("MP4 探测请求顺序不对: %v", ranges)
	}

	var hlsRanges []string
	m3u8 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hlsRanges = append(hlsRanges, req.Header.Get("Range"))
		io.WriteString(w, "#EXTM3U\n#EXT-X-ENDLIST\n")
	}))
	defer m3u8.Close()
	if err := r.probeTarget(context.Background(), hls.Target{URL: m3u8.URL + "/v.m3u8"}, true); err != nil {
		t.Fatalf("m3u8 探测失败: %v", err)
	}
	if len(hlsRanges) != 1 || hlsRanges[0] != "" {
		t.Fatalf("m3u8 不应带 Range: %v", hlsRanges)
	}
}

func TestProbeAddsOriginOn404(t *testing.T) {
	s := newSite(t)
	r, _ := newResolver(s)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Origin") == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if req.Header.Get("Origin") != "https://site.test" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "....ftypisom")
	}))
	defer srv.Close()
	target := hls.Target{URL: srv.URL + "/v.mp4", Referer: "https://site.test/play/1"}
	if err := r.probeTarget(context.Background(), target, false); err != nil {
		t.Fatalf("缺少 Origin 被拒后应按播放页补 Origin: %v", err)
	}
	if calls != 3 {
		t.Fatalf("应依次尝试 Range、无 Range、补 Origin，实际请求 %d 次", calls)
	}
}

func TestProbePreservesMissingReferer(t *testing.T) {
	s := newSite(t)
	r, _ := newResolver(s)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Referer") != "" || req.Header.Get("Origin") != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "....ftypisom")
	}))
	defer srv.Close()
	target := hls.Target{URL: srv.URL + "/v.mp4", FallbackReferer: "https://site.test/play"}
	if err := r.probeTarget(context.Background(), target, false); err != nil {
		t.Fatalf("原请求没有 Referer/Origin 时应原样放行: %v", err)
	}
	if calls != 1 {
		t.Fatalf("原请求可用时不应再补头，实际请求 %d 次", calls)
	}
}

func TestProbeFallbackReferer(t *testing.T) {
	s := newSite(t)
	r, _ := newResolver(s)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Referer") == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "....ftypisom")
	}))
	defer srv.Close()
	target := hls.Target{URL: srv.URL + "/v.mp4", FallbackReferer: "https://site.test/play"}
	if err := r.probeTarget(context.Background(), target, false); err != nil {
		t.Fatalf("原请求被拒后才应补播放页 Referer: %v", err)
	}
	if calls != 3 {
		t.Fatalf("应依次尝试 Range、无 Range、补 Referer，实际请求 %d 次", calls)
	}
}
