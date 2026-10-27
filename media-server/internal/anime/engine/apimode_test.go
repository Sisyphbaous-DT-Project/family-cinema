package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"media-server/internal/anime/rules"
)

func mustDoc(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func mustRule(t *testing.T, s string) *rules.Rule {
	t.Helper()
	r, err := rules.ParseRule([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestJSONPath(t *testing.T) {
	doc := mustDoc(t, `{"data":{"list":[{"id":1,"n":"a"},{"id":2.0,"n":" b "}],"k-1":{"x":"y"}}}`)
	vs, err := jsonRead(doc, "$.data.list[*].n")
	if err != nil || len(vs) != 2 || jsonString(vs[1]) != "b" {
		t.Fatalf("[*] 取值不对: %v %v", vs, err)
	}
	if v, _ := jsonFirst(doc, "$.data.list[1].id"); jsonString(v) != "2.0" {
		t.Errorf("数字应保留原文 2.0（Kazumi 也是），得到 %q", jsonString(v))
	}
	for lit, want := range map[string]string{"1.000": "1.0", "1.50": "1.5", "30222": "30222", "-2.0": "-2.0"} {
		if got := jsonString(json.Number(lit)); got != want {
			t.Errorf("数字 %s 应写成 %s（同 Dart），得到 %s", lit, want, got)
		}
	}
	if v, _ := jsonFirst(doc, "$['data'][\"k-1\"].x"); jsonString(v) != "y" {
		t.Errorf("引号字段取值不对: %v", v)
	}
	if v, _ := jsonFirst(doc, "$.data.nope"); v != nil {
		t.Errorf("不存在的字段应为 nil: %v", v)
	}
	for _, bad := range []string{"data", "$..x", "$.a[?(@.b)]", "$.a[1"} {
		if _, err := jsonRead(doc, bad); err == nil {
			t.Errorf("不支持的写法应报错: %s", bad)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	vars := map[string]any{"source": "a b/c", "n": 3}
	s, err := renderTemplate("https://x.test/@source/@n?m=a@b.com", vars, true)
	if err != nil || s != "https://x.test/a%20b%2Fc/3?m=a@b.com" {
		t.Errorf("模板替换不对: %q %v", s, err)
	}
	if _, err := renderTemplate("@nope", vars, false); err == nil {
		t.Error("缺变量应报错")
	}
	// 整个值就是 @变量 时保留原类型（JSON 请求体里数字还是数字）
	v, _ := renderValue(map[string]any{"id": "@n", "s": "id=@source"}, vars)
	b, _ := json.Marshal(v)
	if string(b) != `{"id":3,"s":"id=a b/c"}` {
		t.Errorf("请求体渲染不对: %s", b)
	}
}

func TestParseSearchAPI(t *testing.T) {
	// 苹果 CMS 的搜索联想接口（dalvdm/moonci/LMM 的新规则）：只给编号，按模板拼详情页
	r := mustRule(t, `{"name":"d","searchMode":"api","chapterRoads":"//x","searchApiConfig":{
		"request":{"url":"https://www.sbdl.cc/index.php/ajax/suggest","query":{"mid":"1","wd":"@keyword"}},
		"listPath":"$.list[*]","namePath":"$.name","sourcePath":"$.id",
		"sourceTemplate":"https://www.sbdl.cc/index.php/vod/detail/id/@source.html"}}`)
	doc := mustDoc(t, `{"code":1,"list":[{"id":30222,"name":"孤独摇滚！"},{"id":30222,"name":"重复"},{"id":5,"name":""}]}`)
	items, err := parseSearchAPI(r.SearchAPI, doc)
	if err != nil || len(items) != 1 {
		t.Fatalf("应去重、跳过没名字的: %+v %v", items, err)
	}
	if items[0].Name != "孤独摇滚！" || items[0].Src != "https://www.sbdl.cc/index.php/vod/detail/id/30222.html" {
		t.Errorf("结果不对: %+v", items[0])
	}
	// 没有模板时来源原样保留（编号），交给选集那一步
	r.SearchAPI.SourceTemplate = ""
	items, _ = parseSearchAPI(r.SearchAPI, doc)
	if items[0].Src != "30222" {
		t.Errorf("无模板时应保留编号: %q", items[0].Src)
	}
}

func TestParseChaptersNested(t *testing.T) {
	// sorani：整个 data 当一条线路，播放页按模板拼（episodeOrder 是 1.0，原样代入）
	r := mustRule(t, `{"name":"s","baseURL":"https://www.sorani.net/","searchMode":"api","chapterMode":"api",
		"searchApiConfig":{"request":{"url":"https://api.test/s"}},
		"chapterApiConfig":{"request":{"url":"https://api.test/v/@source"},"format":"nested",
		"roadsPath":"$.data","roadNamePath":"","episodesPath":"$.episodes[*]","episodeNamePath":"$.episodeLabel",
		"episodeUrlPath":"$.episodeOrder","episodePage":{"url":"https://www.sorani.net/anime/mal/@source/episode/@episodeUrl","query":{}}}}`)
	doc := mustDoc(t, `{"data":{"episodes":[{"episodeLabel":"1080P","episodeOrder":1.0},{"episodeLabel":"","episodeOrder":2}]}}`)
	roads, err := parseChaptersAPI(r.BaseURL, r.ChapterAPI, doc, "1641")
	if err != nil || len(roads) != 1 || len(roads[0].Episodes) != 2 {
		t.Fatalf("线路/集数不对: %+v %v", roads, err)
	}
	if roads[0].Name != "线路 1" || roads[0].Episodes[0].Name != "1080P" || roads[0].Episodes[1].Name != "第2集" {
		t.Errorf("名字不对: %+v", roads[0])
	}
	if u := roads[0].Episodes[0].URL; u != "https://www.sorani.net/anime/mal/1641/episode/1.0" {
		t.Errorf("播放页地址不对: %q", u)
	}

	// xfdmnext：多条线路，线路有名字，模板用 @source + 剧集编号；模板查询参数可用序号变量
	r2 := mustRule(t, `{"name":"x","baseURL":"https://next.xifanacg.com/","searchMode":"api","chapterMode":"api",
		"searchApiConfig":{"request":{"url":"https://api.test/s"}},
		"chapterApiConfig":{"request":{"url":"https://api.test/c"},"roadsPath":"$.sources[*]","roadNamePath":"$.name",
		"episodesPath":"$.episodes[*]","episodeNamePath":"$.title","episodeUrlPath":"$.id",
		"episodePage":{"url":"https://next.xifanacg.com/anime/@source/play/@episodeUrl","query":{"n":"@episodeNumber"}}}}`)
	doc2 := mustDoc(t, `{"sources":[{"name":"稀饭主线","episodes":[{"id":44575,"title":"后篇"}]},{"name":"空线路","episodes":[]}]}`)
	roads, err = parseChaptersAPI(r2.BaseURL, r2.ChapterAPI, doc2, "3069")
	if err != nil || len(roads) != 1 {
		t.Fatalf("空线路应跳过: %+v %v", roads, err)
	}
	if roads[0].Name != "稀饭主线" || roads[0].Episodes[0].URL != "https://next.xifanacg.com/anime/3069/play/44575?n=1" {
		t.Errorf("结果不对: %+v", roads[0])
	}
}

func TestParseChaptersDelimited(t *testing.T) {
	// giriGiriLove：苹果 CMS 的 vod_play_from / vod_play_url 拼接格式，地址直接就是 m3u8
	r := mustRule(t, `{"name":"g","baseURL":"https://m3u8.girigirilove.com/","searchMode":"api","chapterMode":"api",
		"searchApiConfig":{"request":{"url":"https://api.test/s"}},
		"chapterApiConfig":{"request":{"url":"https://api.test/c","query":{"vod_id":"@source"}},"format":"delimited",
		"roadNamesPath":"$.info.vod_play_from","roadEpisodesPath":"$.info.vod_play_url",
		"roadSeparator":"$$$","episodeSeparator":"#","fieldSeparator":"$"}}`)
	doc := mustDoc(t, `{"info":{"vod_play_from":"chs$$$","vod_play_url":"前篇$https://akua.test/01/playlist.m3u8#后篇$https://akua.test/02/playlist.m3u8#坏条目$$$$/rel/1.m3u8"}}`)
	roads, err := parseChaptersAPI(r.BaseURL, r.ChapterAPI, doc, "26538")
	if err != nil || len(roads) != 2 {
		t.Fatalf("线路数不对: %+v %v", roads, err)
	}
	if roads[0].Name != "chs" || len(roads[0].Episodes) != 2 || roads[0].Episodes[1].URL != "https://akua.test/02/playlist.m3u8" {
		t.Errorf("第一条线路不对: %+v", roads[0])
	}
	// 第二条线路没名字用默认名；没名字的集用"第N集"；相对地址按 baseURL 补全
	if roads[1].Name != "线路 2" || roads[1].Episodes[0].Name != "第1集" || roads[1].Episodes[0].URL != "https://m3u8.girigirilove.com/rel/1.m3u8" {
		t.Errorf("第二条线路不对: %+v", roads[1])
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct{ base, raw, want string }{
		{"https://www.mgnacg.com/media/", "1446", "https://www.mgnacg.com/media/1446"},
		{"https://www.2kdm.com/voddetail/", "1762", "https://www.2kdm.com/voddetail/1762"},
		{"https://x.test/", "http://x.test/a/b/?", "https://x.test/a/b"},
		{"https://x.test/", "http://other.test/a/", "http://other.test/a"},
		{"https://x.test/", "/", "https://x.test/"},
		{"", "abc", "abc"},
		{"https://x.test/", "  ", ""},
	}
	for _, c := range cases {
		if got := normalizeURL(c.base, c.raw); got != c.want {
			t.Errorf("normalizeURL(%q,%q)=%q 期望 %q", c.base, c.raw, got, c.want)
		}
	}
}

// 用本地模拟站点把"接口搜索 → 网页选集（来源只有编号）"和"接口搜索 + 接口选集（POST JSON）"完整走一遍
func TestEngineAPIEndToEnd(t *testing.T) {
	var gotBody, gotKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php/ajax/suggest", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("wd") != "孤独摇滚" || r.URL.Query().Get("mid") != "1" {
			http.Error(w, "bad query", 400)
			return
		}
		io.WriteString(w, `{"list":[{"id":1446,"name":"孤独摇滚！"}]}`)
	})
	mux.HandleFunc("/media/1446", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html><ul class="p"><li><a href="/play/1446-1-1">第01集</a></li></ul></html>`)
	})
	mux.HandleFunc("/rpc/search", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotKey = string(b), r.Header.Get("apikey")
		io.WriteString(w, `[{"id":3069,"title":"孤独摇滚！"}]`)
	})
	mux.HandleFunc("/rpc/detail", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["p_id"] != "3069" {
			http.Error(w, "bad id", 400)
			return
		}
		io.WriteString(w, `{"sources":[{"name":"主线","episodes":[{"id":7,"title":"第1集"}]}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := &Engine{HTTP: srv.Client()}
	ctx := context.Background()

	// mgnacg 式：接口搜索只给编号，选集走网页，按 baseURL 补成详情页
	r1 := mustRule(t, `{"name":"m","baseURL":"`+srv.URL+`/media/","searchMode":"api","chapterMode":"xpath",
		"searchApiConfig":{"request":{"method":"GET","url":"`+srv.URL+`/index.php/ajax/suggest","query":{"mid":"1","wd":"@keyword"}},
		"listPath":"$.list[*]","namePath":"$.name","sourcePath":"$.id"},
		"chapterRoads":"//ul[@class='p']","chapterResult":".//li/a"}`)
	items, err := e.Search(ctx, r1, "孤独摇滚")
	if err != nil || len(items) != 1 || items[0].Src != "1446" {
		t.Fatalf("接口搜索不对: %+v %v", items, err)
	}
	roads, err := e.Chapters(ctx, r1, items[0].Src)
	if err != nil || len(roads) != 1 || roads[0].Episodes[0].URL != srv.URL+"/play/1446-1-1" {
		t.Fatalf("编号补成详情页后取选集不对: %+v %v", roads, err)
	}

	// xfdmnext 式：POST JSON + 自定义请求头
	r2 := mustRule(t, `{"name":"x","baseURL":"`+srv.URL+`/","searchMode":"api","chapterMode":"api",
		"searchApiConfig":{"request":{"method":"POST","url":"`+srv.URL+`/rpc/search","headers":{"apikey":"k1"},
		"bodyType":"json","body":{"search_term":"@keyword","page_number":1}},"listPath":"$[*]","namePath":"$.title","sourcePath":"$.id"},
		"chapterApiConfig":{"request":{"method":"POST","url":"`+srv.URL+`/rpc/detail","bodyType":"json","body":{"p_id":"@source"}},
		"roadsPath":"$.sources[*]","roadNamePath":"$.name","episodesPath":"$.episodes[*]","episodeNamePath":"$.title",
		"episodeUrlPath":"$.id","episodePage":{"url":"`+srv.URL+`/anime/@source/play/@episodeUrl"}}}`)
	items, err = e.Search(ctx, r2, "孤独摇滚")
	if err != nil || len(items) != 1 || items[0].Src != "3069" {
		t.Fatalf("POST 搜索不对: %+v %v", items, err)
	}
	if gotKey != "k1" || !bytes.Contains([]byte(gotBody), []byte(`"search_term":"孤独摇滚"`)) || !bytes.Contains([]byte(gotBody), []byte(`"page_number":1`)) {
		t.Errorf("请求头/请求体不对: key=%q body=%s", gotKey, gotBody)
	}
	roads, err = e.Chapters(ctx, r2, "3069")
	if err != nil || len(roads) != 1 || roads[0].Episodes[0].URL != srv.URL+"/anime/3069/play/7" {
		t.Fatalf("接口选集不对: %+v %v", roads, err)
	}
}

// moonci 式：第一次 403 + 发 cookie，带上 cookie 再请求才放行
func TestFetchRetries403WithCookie(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if _, err := r.Cookie("pass"); err != nil {
			http.SetCookie(w, &http.Cookie{Name: "pass", Value: "1", Path: "/"})
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `<script>window.location.href="/a.html";</script>`)
			return
		}
		io.WriteString(w, "<html>ok</html>")
	}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	e := &Engine{HTTP: &http.Client{Jar: jar}}
	pg, err := e.Fetch(context.Background(), &rules.Rule{Name: "m"}, http.MethodGet, srv.URL+"/a.html", nil, "")
	if err != nil || string(pg.Body) != "<html>ok</html>" || hits != 2 {
		t.Fatalf("应带上 cookie 重试一次成功: hits=%d err=%v", hits, err)
	}
	// 没有 cookie 罐时不重试，照常报错
	e2 := &Engine{HTTP: &http.Client{}}
	if _, err := e2.Fetch(context.Background(), &rules.Rule{Name: "m"}, http.MethodGet, srv.URL+"/a.html", nil, ""); err == nil {
		t.Error("没有 cookie 罐时 403 应报错")
	}
}
