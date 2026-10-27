package hls

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTokenRoundTripAndTamper(t *testing.T) {
	s := NewSigner()
	tok := s.Sign(Target{URL: "https://a.test/x.m3u8", Referer: "https://a.test/", AdBlock: true})
	got, err := s.Verify(tok)
	if err != nil || got.URL != "https://a.test/x.m3u8" || got.Referer != "https://a.test/" || !got.AdBlock {
		t.Fatalf("令牌往返不对: %+v %v", got, err)
	}
	// 篡改载荷
	i := strings.IndexByte(tok, '.')
	if _, err := s.Verify("x" + tok[1:i] + tok[i:]); err == nil {
		t.Error("载荷被篡改应校验失败")
	}
	// 换一个签名器（模拟重启）
	if _, err := NewSigner().Verify(tok); err == nil {
		t.Error("不同密钥签的令牌应校验失败")
	}
	for _, bad := range []string{"", "abc", "a.b", "."} {
		if _, err := s.Verify(bad); err == nil {
			t.Errorf("%q 应无效", bad)
		}
	}
}

func TestRewrite(t *testing.T) {
	base, _ := url.Parse("https://cdn.test/path/v/index.m3u8?t=1")
	in := "#EXTM3U\r\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x1\r\n#EXT-X-MAP:URI=\"init.mp4\"\r\n#EXTINF:4.0,\r\nseg1.ts\r\n#EXTINF:4.0,\r\n/abs/seg2.ts\r\n#EXTINF:4.0,\r\nhttps://other.test/seg3.ts?x=1\r\n#EXT-X-ENDLIST\r\n"
	out := string(Rewrite([]byte(in), base, func(abs string) string { return "<" + abs + ">" }))
	for _, want := range []string{
		`URI="<https://cdn.test/path/v/key.bin>"`,
		`URI="<https://cdn.test/path/v/init.mp4>"`,
		"\n<https://cdn.test/path/v/seg1.ts>\n",
		"\n<https://cdn.test/abs/seg2.ts>\n",
		"\n<https://other.test/seg3.ts?x=1>\n",
		"#EXT-X-ENDLIST",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q，实际:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\r") {
		t.Error("应统一成 \\n 换行")
	}
}

func seg(group []string) string { return strings.Join(group, "\n") }

func TestFilterAds(t *testing.T) {
	// 正片分前后两半（各 2 段 x600s），中间插了 2 段 x3s 的广告
	in := seg([]string{
		"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:600", "#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:600.0,", "m1.ts", "#EXTINF:600.0,", "m2.ts",
		"#EXT-X-DISCONTINUITY", "#EXTINF:3.0,", "ad1.ts", "#EXTINF:3.0,", "ad2.ts",
		"#EXT-X-DISCONTINUITY", "#EXTINF:600.0,", "m4.ts", "#EXTINF:600.0,", "m5.ts",
		"#EXT-X-ENDLIST",
	})
	out := string(FilterAds([]byte(in)))
	if strings.Contains(out, "ad1.ts") || strings.Contains(out, "ad2.ts") {
		t.Errorf("广告段应被剔除:\n%s", out)
	}
	for _, keep := range []string{"m1.ts", "m2.ts", "m4.ts", "m5.ts", "#EXT-X-TARGETDURATION:600", "#EXT-X-ENDLIST"} {
		if !strings.Contains(out, keep) {
			t.Errorf("应保留 %s:\n%s", keep, out)
		}
	}

	// 两个长度相当的大段（比如番剧分上下半）不能误删
	two := seg([]string{"#EXTM3U", "#EXTINF:600.0,", "a.ts", "#EXT-X-DISCONTINUITY", "#EXTINF:590.0,", "b.ts", "#EXT-X-ENDLIST"})
	if string(FilterAds([]byte(two))) != two {
		t.Error("两个相当的大段不应被当成广告")
	}

	// 没有间断标记 → 原样
	plain := seg([]string{"#EXTM3U", "#EXTINF:5.0,", "a.ts", "#EXT-X-ENDLIST"})
	if string(FilterAds([]byte(plain))) != plain {
		t.Error("无间断标记应原样返回")
	}

	// #EXT-X-DISCONTINUITY-SEQUENCE 不是间断标记
	dseq := seg([]string{"#EXTM3U", "#EXT-X-DISCONTINUITY-SEQUENCE:3", "#EXTINF:5.0,", "a.ts", "#EXTINF:5.0,", "b.ts", "#EXT-X-ENDLIST"})
	if string(FilterAds([]byte(dseq))) != dseq {
		t.Error("DISCONTINUITY-SEQUENCE 被误当成间断")
	}

	// 主清单原样
	master := seg([]string{"#EXTM3U", "#EXT-X-STREAM-INF:BANDWIDTH=800000", "low/index.m3u8"})
	if string(FilterAds([]byte(master))) != master {
		t.Error("主清单应原样返回")
	}
}

// 端到端：上游要求 Referer；经代理取清单 → 清单里的分片地址也走代理 → 取分片带 Range
func TestProxyEndToEnd(t *testing.T) {
	var gotRefs []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRefs = append(gotRefs, r.Header.Get("Referer"))
		if r.Header.Get("Referer") != "https://site.test/play" {
			http.Error(w, "no referer", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v/index.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXTINF:4,\nseg1.ts\n#EXT-X-ENDLIST\n")
		case "/v/seg1.ts":
			if r.Header.Get("Range") == "bytes=2-3" {
				w.Header().Set("Content-Range", "bytes 2-3/6")
				w.WriteHeader(http.StatusPartialContent)
				io.WriteString(w, "CD")
				return
			}
			io.WriteString(w, "ABCDEF")
		}
	}))
	defer up.Close()

	p := &Proxy{HTTP: up.Client(), Signer: NewSigner(), AllowPrivate: true}
	target := Target{URL: up.URL + "/v/index.m3u8", Referer: "https://site.test/play"}

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", p.URL(target), nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "mpegurl") {
		t.Fatalf("清单请求失败: %d %v", rec.Code, rec.Header())
	}
	var segLine string
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(l, PathPrefix) {
			segLine = l
		}
	}
	if segLine == "" {
		t.Fatalf("清单里的分片地址应被改写成代理地址:\n%s", rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, httptest.NewRequest("GET", segLine, nil))
	if rec2.Code != 200 || rec2.Body.String() != "ABCDEF" {
		t.Fatalf("分片请求失败: %d %q", rec2.Code, rec2.Body.String())
	}

	req3 := httptest.NewRequest("GET", segLine, nil)
	req3.Header.Set("Range", "bytes=2-3")
	rec3 := httptest.NewRecorder()
	p.ServeHTTP(rec3, req3)
	if rec3.Code != 206 || rec3.Body.String() != "CD" || rec3.Header().Get("Content-Range") != "bytes 2-3/6" {
		t.Fatalf("Range 应原样透传: %d %q %v", rec3.Code, rec3.Body.String(), rec3.Header())
	}
	for _, ref := range gotRefs {
		if ref != "https://site.test/play" {
			t.Errorf("上游请求缺少 Referer: %q", ref)
		}
	}
}

func TestProxyRejects(t *testing.T) {
	p := &Proxy{HTTP: http.DefaultClient, Signer: NewSigner()}
	// 无效令牌
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", PathPrefix+"garbage", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("无效令牌应 403，实际 %d", rec.Code)
	}
	// 内网/本机目标
	for _, u := range []string{"http://127.0.0.1:8088/api/library", "http://192.168.1.1/", "http://localhost/x", "http://[::1]/"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("GET", p.URL(Target{URL: u}), nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s 应被拒绝，实际 %d", u, rec.Code)
		}
	}
	// 非 http(s)
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", p.URL(Target{URL: "file:///etc/passwd"}), nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("file:// 应 400，实际 %d", rec.Code)
	}
}

func TestProxyRelayEndToEnd(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "代取不应绕过私网检查直接请求原站", http.StatusForbidden)
	}))
	defer up.Close()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Relay-Token") != "secret" {
			http.Error(w, "missing ticket", http.StatusForbidden)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("u"))
		if err != nil {
			http.Error(w, "bad target encoding", http.StatusBadRequest)
			return
		}
		u, err := url.Parse(string(raw))
		if err != nil || u.Host != strings.TrimPrefix(up.URL, "http://") {
			http.Error(w, "wrong original target", http.StatusBadRequest)
			return
		}
		switch u.Path {
		case "/v/index.m3u8":
			if r.Header.Get("Origin") != "https://site.test" || r.Host == "evil.test" {
				http.Error(w, "request headers were not forwarded safely", http.StatusBadRequest)
				return
			}
			io.WriteString(w, "#EXTM3U\n#EXTINF:4,\nseg1.ts\n#EXT-X-ENDLIST\n")
		case "/v/seg1.ts":
			if r.Header.Get("Range") != "bytes=2-3" {
				http.Error(w, "missing range", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Range", "bytes 2-3/6")
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, "CD")
		default:
			http.NotFound(w, r)
		}
	}))
	defer relay.Close()

	p := &Proxy{HTTP: relay.Client(), Signer: NewSigner(), AllowPrivate: true}
	target := Target{URL: up.URL + "/v/index.m3u8", Relay: relay.URL + "/relay", RelayToken: "secret",
		Headers: map[string]string{"Origin": "https://site.test", "Host": "evil.test"}}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", p.URL(target), nil))
	if rec.Code != 200 {
		t.Fatalf("代取清单失败: %d %s", rec.Code, rec.Body.String())
	}
	var segLine string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, PathPrefix) {
			segLine = line
		}
	}
	if segLine == "" {
		t.Fatalf("分片没有改写成代理地址: %s", rec.Body.String())
	}
	segTarget, err := p.Signer.Verify(strings.TrimPrefix(segLine, PathPrefix))
	if err != nil || segTarget.URL != up.URL+"/v/seg1.ts" || segTarget.Relay != target.Relay || segTarget.RelayToken != "secret" || segTarget.Headers["Origin"] != "https://site.test" {
		t.Fatalf("分片没有按原站展开并继承代取: %+v %v", segTarget, err)
	}
	req := httptest.NewRequest("GET", segLine, nil)
	req.Header.Set("Range", "bytes=2-3")
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req)
	if rec2.Code != 206 || rec2.Body.String() != "CD" || rec2.Header().Get("Content-Range") != "bytes 2-3/6" {
		t.Fatalf("代取 Range 透传失败: %d %q %v", rec2.Code, rec2.Body.String(), rec2.Header())
	}

	bad := &Proxy{HTTP: relay.Client(), Signer: NewSigner()}
	badRec := httptest.NewRecorder()
	bad.ServeHTTP(badRec, httptest.NewRequest("GET", bad.URL(target), nil))
	if badRec.Code != http.StatusForbidden {
		t.Errorf("私网原地址不能借代取绕过检查，实际 %d", badRec.Code)
	}
}
