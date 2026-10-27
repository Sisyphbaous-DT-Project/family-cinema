package bangumi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSeasonWindow(t *testing.T) {
	from, to := SeasonWindow(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	if from != "2026-07-01" || to != "2027-01-01" {
		t.Errorf("10 月应取 7月~次年1月: %s %s", from, to)
	}
	from, to = SeasonWindow(time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC))
	if from != "2025-10-01" || to != "2026-04-01" {
		t.Errorf("2 月应取去年10月~4月: %s %s", from, to)
	}
}

func TestCacheStaleOnError(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	calls := 0
	fetch := func(ok bool) func(context.Context) ([]byte, error) {
		return func(context.Context) ([]byte, error) {
			calls++
			if !ok {
				return nil, errors.New("boom")
			}
			return []byte(`{"v":1}`), nil
		}
	}
	// 写入一个已经过期的条目
	if _, err := c.Get(context.Background(), "k", -time.Second, fetch(true)); err != nil {
		t.Fatal(err)
	}
	// 过期后上游失败 → 用旧数据兜底
	got, err := c.Get(context.Background(), "k", time.Hour, fetch(false))
	if err != nil || string(got) != `{"v":1}` {
		t.Fatalf("应使用过期数据兜底: %q %v", got, err)
	}
	// 重新建一个 Cache（模拟重启）：磁盘上的数据仍能读到
	c2 := NewCache(dir)
	got, err = c2.Get(context.Background(), "k", time.Hour, fetch(false))
	if err != nil || string(got) != `{"v":1}` {
		t.Fatalf("重启后应从磁盘读到: %q %v", got, err)
	}
	// 从来没成功过的 key，上游失败要报错
	if _, err := c2.Get(context.Background(), "none", time.Hour, fetch(false)); err == nil {
		t.Fatal("没有旧数据时应返回错误")
	}
}

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.Client(), "", NewCache(""))
	c.Base = srv.URL
	return c
}

func TestClientEndpoints(t *testing.T) {
	var hits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("User-Agent") != DefaultUA {
			t.Errorf("缺少 User-Agent: %q", r.Header.Get("User-Agent"))
		}
		switch {
		case r.URL.Path == "/calendar":
			io.WriteString(w, `[{"weekday":{"id":1},"items":[{"id":7,"name":"n","name_cn":"中","air_date":"2026-10-06","rating":{"score":8.1,"rank":20},"images":{"common":"http://lain.bgm.tv/a.jpg"}}]}]`)
		case r.URL.Path == "/v0/subjects/7":
			io.WriteString(w, `{"id":7,"name":"n","name_cn":"中","summary":"s","date":"2026-10-06","platform":"TV","total_episodes":12,"rating":{"rank":3,"total":99,"score":8.5},
"tags":[{"name":"a","count":1},{"name":"b","count":9}],
"infobox":[{"key":"话数","value":"12"},{"key":"别名","value":[{"v":"x"},{"v":"y"}]},{"key":"空","value":""}]}`)
		case r.URL.Path == "/v0/episodes":
			io.WriteString(w, `{"total":2,"limit":100,"offset":0,"data":[{"id":1,"sort":1,"name":"a","airdate":"2026-10-06"},{"id":2,"sort":12.5,"name":"b"}]}`)
		case r.URL.Path == "/v0/search/subjects" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), `"sort":"heat"`) && !strings.Contains(string(b), "air_date") {
				t.Errorf("热门请求应带时间窗: %s", b)
			}
			io.WriteString(w, `{"total":1,"offset":0,"data":[{"id":9,"name":"s","date":"2026-07-01","eps":13,"rating":{"score":7},"images":{"large":"L"}}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	cal, err := c.Calendar(ctx)
	if err != nil || len(cal) != 1 || cal[0].Weekday != 1 || cal[0].Items[0].Date != "2026-10-06" || cal[0].Items[0].Score != 8.1 {
		t.Fatalf("日历解析不对: %+v %v", cal, err)
	}
	if cal[0].Items[0].Title() != "中" {
		t.Error("Title 应优先中文名")
	}

	s, err := c.Subject(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if s.Eps != 12 || s.Rating.Total != 99 || s.Tags[0].Name != "b" {
		t.Errorf("详情基础字段不对: %+v", s)
	}
	if len(s.Infobox) != 2 || s.Infobox[1].Value != "x、y" {
		t.Errorf("infobox 数组值/空值处理不对: %+v", s.Infobox)
	}

	eps, err := c.Episodes(ctx, 7)
	if err != nil || len(eps) != 2 || eps[1].Sort != 12.5 {
		t.Fatalf("剧集解析不对: %+v %v", eps, err)
	}

	hot, err := c.Hot(ctx, 10, 0)
	if err != nil || hot.Total != 1 || hot.Items[0].Eps != 13 || hot.Items[0].Images.Large != "L" {
		t.Fatalf("热门解析不对: %+v %v", hot, err)
	}

	before := atomic.LoadInt32(&hits)
	if _, err := c.Calendar(ctx); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != before {
		t.Error("第二次取日历应命中缓存，不再请求上游")
	}
}

func TestClientRetriesTransientErrors(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/calendar": // 第一次 502，第二次正常
			if atomic.AddInt32(&calls, 1) == 1 {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			io.WriteString(w, `[]`)
		default: // 404 不该重试
			atomic.AddInt32(&calls, 100)
			http.NotFound(w, r)
		}
	})
	if _, err := c.Calendar(context.Background()); err != nil {
		t.Fatalf("502 后应自动重试成功: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("应请求 2 次，实际 %d", n)
	}
	atomic.StoreInt32(&calls, 0)
	if _, err := c.Subject(context.Background(), 1); err == nil {
		t.Fatal("404 应返回错误")
	}
	if n := atomic.LoadInt32(&calls); n != 100 {
		t.Errorf("404 不应重试，实际请求计数 %d", n)
	}
}

func TestImageProxy(t *testing.T) {
	var upstream int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstream, 1)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	p := &ImageProxy{HTTP: srv.Client(), UA: DefaultUA, Dir: dir, MaxBytes: 1 << 20}

	// 非 bgm.tv 域名一律拒绝
	for _, bad := range []string{"http://evil.com/a.png", "http://notbgm.tv/a.png", "ftp://lain.bgm.tv/a", "http://127.0.0.1/a"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("GET", "/api/anime/img?u="+bad, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s 应被拒绝，实际 %d", bad, rec.Code)
		}
	}
	if !allowedHost("lain.bgm.tv") || !allowedHost("bgm.tv") || allowedHost("xbgm.tv") {
		t.Error("域名白名单判断不对")
	}

	// 命中缓存：直接预置一个缓存文件，验证不再请求上游
	raw := "http://lain.bgm.tv/pic/x.jpg"
	p.writeCached(raw, "image/jpeg", []byte("CACHED"))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/api/anime/img?u="+raw, nil))
	if rec.Body.String() != "CACHED" || rec.Header().Get("Content-Type") != "image/jpeg" || atomic.LoadInt32(&upstream) != 0 {
		t.Errorf("应直接返回缓存: %q %q", rec.Body.String(), rec.Header().Get("Content-Type"))
	}

	// 超限清理：写入几个大文件，Trim 后总量降到上限内，且最旧的先被删
	p.MaxBytes = 150 // 总量约 197 字节，超限；清理目标是 120 字节，删掉最旧的 90 字节即可
	old := filepath.Join(dir, "old.img")
	newer := filepath.Join(dir, "new.img")
	os.WriteFile(old, make([]byte, 90), 0o644)
	os.WriteFile(newer, make([]byte, 90), 0o644)
	os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	p.Trim()
	if _, err := os.Stat(old); err == nil {
		t.Error("最久未访问的文件应被清理")
	}
	if _, err := os.Stat(newer); err != nil {
		t.Error("较新的文件不应被清理")
	}
}
