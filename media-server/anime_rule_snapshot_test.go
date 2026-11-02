package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	animeapi "media-server/internal/anime/api"
	"media-server/internal/anime/rules"
)

// 规则每天自动更新/下架：下载任务排进队时拿到的规则，排队期间或两集之间被下架，照样用那份解析，
// 不能按名字重新取到空规则（以前会空指针，把整个媒体服务带崩）
func TestResolveForDownloadUsesTaskRule(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/play/1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<script>var player_aaaa={"encrypt":0,"url":"%s/v/index.m3u8"}</script>`, srv.URL)
	})
	mux.HandleFunc("/v/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXTINF:4,\nseg.ts\n#EXT-X-ENDLIST\n")
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	rule, err := rules.ParseRule([]byte(fmt.Sprintf(`{"name":"t","version":"1","baseURL":%q,"searchURL":"x","searchList":"//a","chapterRoads":"//b"}`, srv.URL+"/")))
	if err != nil {
		t.Fatal(err)
	}
	set := rules.NewSet([]*rules.Rule{rule})
	old := animeDL.api
	animeDL.api = animeapi.New(animeapi.Deps{HTTP: srv.Client(), StreamHTTP: srv.Client(), Rules: set, CacheDir: t.TempDir(), AllowPrivate: true})
	defer func() { animeDL.api = old }()

	ep := animeDLEpisode{Label: "第01集", File: "第01集.mp4", URL: srv.URL + "/play/1"}
	task := newAnimeDLTask(rule, srv.URL+"/detail", "测试番", "series", t.TempDir(), []animeDLEpisode{ep})
	defer task.cancel()

	set.Replace(nil) // 任务还在排队（或第一集刚下完），这个站被下架了
	target, kind, err := resolveForDownload(task, task.ruleObj, ep, func() {})
	if err != nil || kind != "hls" || target.URL != srv.URL+"/v/index.m3u8" {
		t.Fatalf("应该用任务开头的规则照常解析: %v %s %+v", err, kind, target)
	}
}

// 中途停下（规则不见了、硬盘满了）：没开始的集记成失败、能点重试，任务不能显示"下载完成"
func TestAnimeTaskStoppedEarlyIsNotDone(t *testing.T) {
	old := cfg
	cfg.CacheDir = t.TempDir()
	defer func() { cfg = old }()
	task := newAnimeDLTask(&rules.Rule{Name: "t"}, "https://example.com/1.html", "测试番", "series", t.TempDir(), []animeDLEpisode{
		{Label: "第01集", File: "第01集.mp4", URL: "https://example.com/1-1.html"},
		{Label: "第02集", File: "第02集.mp4", URL: "https://example.com/1-2.html"},
	})
	defer task.cancel()
	task.ruleObj = nil // 走"规则不见了"停下的那条路
	runAnimeDLTask(task)

	x := task.info
	if x.Status != "error" || len(x.AnimeFailedFiles) != 2 || len(x.AnimeFailures) != 2 || !strings.Contains(x.Error, "还有 2 集没下") {
		t.Fatalf("没下的集应记成失败、任务显示失败: status=%s failed=%v err=%q", x.Status, x.AnimeFailedFiles, x.Error)
	}
	if !strings.Contains(x.AnimeFailures[0].Message, "片源规则不见了") {
		t.Errorf("失败原因应写清楚: %+v", x.AnimeFailures)
	}
}
