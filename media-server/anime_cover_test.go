package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	animeapi "media-server/internal/anime/api"
	"media-server/internal/anime/rules"
)

func animeCoverTestImage(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 24; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 9), G: uint8(y * 7), B: uint8(x*y + 3), A: 255})
		}
	}
	var out bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&out, img, nil)
	case "gif":
		err = gif.Encode(&out, img, nil)
	default:
		err = png.Encode(&out, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func animeCoverTestSelection() *animeCoverSelection {
	return &animeCoverSelection{Source: "bangumi", URL: "https://lain.bgm.tv/pic/cover/l/test.jpg"}
}

func waitAnimeCoverCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("等待封面后台操作超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitAnimeCoverJobs(t *testing.T) {
	t.Helper()
	waitAnimeCoverCondition(t, func() bool {
		animeCoverJobs.mu.Lock()
		defer animeCoverJobs.mu.Unlock()
		return len(animeCoverJobs.dirs) == 0
	})
}

func setupAnimeCoverTest(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *rules.Rule) {
	t.Helper()
	oldCfg, oldHTTP := cfg, httpClient
	animeDL.mu.Lock()
	oldAPI, oldTasks, oldWake := animeDL.api, animeDL.tasks, animeDL.wake
	animeDL.tasks, animeDL.wake = nil, make(chan struct{}, 1)
	animeDL.mu.Unlock()
	library.mu.Lock()
	oldItems, oldScan, oldError := library.items, library.lastScan, library.scanError
	library.mu.Unlock()
	root := t.TempDir()
	cfg = Config{MovieRoot: filepath.Join(root, "movie"), SeriesRoot: filepath.Join(root, "series"), CacheDir: filepath.Join(root, "cache")}
	for _, dir := range []string{cfg.MovieRoot, cfg.SeriesRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(handler)
	cfg.Proxy = ProxyConfig{Base: srv.URL, Token: "local-test-token"}
	httpClient = srv.Client()
	rule, err := rules.ParseRule([]byte(fmt.Sprintf(`{"name":"cover-test","version":"1","baseURL":%q,"searchURL":"x","searchList":"//a","chapterRoads":"//b"}`, srv.URL+"/")))
	if err != nil {
		t.Fatal(err)
	}
	animeDL.api = animeapi.New(animeapi.Deps{
		HTTP: srv.Client(), StreamHTTP: srv.Client(), Rules: rules.NewSet([]*rules.Rule{rule}),
		CacheDir: filepath.Join(root, "anime-cache"), AllowPrivate: true,
	})
	t.Cleanup(func() {
		waitAnimeCoverJobs(t)
		srv.Close()
		animeDL.mu.Lock()
		for _, task := range animeDL.tasks {
			task.cancel()
		}
		animeDL.api, animeDL.tasks, animeDL.wake = oldAPI, oldTasks, oldWake
		animeDL.mu.Unlock()
		cfg, httpClient = oldCfg, oldHTTP
		library.mu.Lock()
		library.items, library.lastScan, library.scanError = oldItems, oldScan, oldError
		library.mu.Unlock()
	})
	return srv, rule
}

func animeCoverPost(t *testing.T, path string, body any, handler http.HandlerFunc) map[string]any {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	if strings.Contains(path, "/retry") {
		parts := strings.Split(path, "/")
		req.SetPathValue("id", parts[len(parts)-2])
	}
	w := httptest.NewRecorder()
	handler(w, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func animeCoverRequest(rule, folder string, cover *animeCoverSelection) map[string]any {
	return map[string]any{
		"rule": rule, "title": folder, "folder": folder, "cover": cover,
		"episodes": []map[string]any{{"name": "第01集", "url": "https://example.com/1.mp4", "index": 0}},
	}
}

func TestAnimeCoverValidation(t *testing.T) {
	for _, raw := range []string{
		"https://lain.bgm.tv/pic/cover/l/test.jpg",
		"https://lain.bgm.tv:443/pic/cover/l/test.jpg?x=1",
	} {
		if _, err := validateAnimeCover(&animeCoverSelection{Source: "bangumi", URL: raw}); err != nil {
			t.Errorf("合法地址被拒绝: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "http://lain.bgm.tv/test.jpg", "https://lain.bgm.tv:8443/test.jpg",
		"https://lain.bgm.tv:/test.jpg", "https://lain.bgm.tv.evil.example/test.jpg",
		"https://evil-lain.bgm.tv/test.jpg", "https://lain.bgm.tv./test.jpg",
		"https://user@lain.bgm.tv/test.jpg", "https://example.com/lain.bgm.tv/test.jpg",
		"https://lain.bgm.tv/test.jpg#fragment", "//lain.bgm.tv/test.jpg",
	} {
		if _, err := validateAnimeCover(&animeCoverSelection{Source: "bangumi", URL: raw}); err == nil {
			t.Errorf("非法地址被接受: %s", raw)
		}
	}
	if _, err := validateAnimeCover(nil); err == nil {
		t.Fatal("缺省封面不能当成有效选择")
	}
	if _, err := validateAnimeCover(&animeCoverSelection{Source: "other", URL: animeCoverTestSelection().URL}); err == nil {
		t.Fatal("只允许 bangumi 来源")
	}
}

func TestAnimeCoverUsesEncodedProxyAndActualFormat(t *testing.T) {
	images := map[string][]byte{
		"jpeg": animeCoverTestImage(t, "jpeg"),
		"png":  animeCoverTestImage(t, "png"),
		"gif":  animeCoverTestImage(t, "gif"),
	}
	setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/fetch" || r.URL.Query().Get("url") != "" {
			t.Errorf("新封面请求没有使用 GET /fetch?u=: %s", r.RequestURI)
		}
		if strings.Contains(r.RequestURI, "lain.bgm.tv") {
			t.Error("请求地址泄露了明文目标域名")
		}
		if r.Header.Get("X-Proxy-Token") != "local-test-token" {
			t.Error("没有复用代理鉴权")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("u"))
		if err != nil {
			t.Error(err)
		}
		format := strings.TrimPrefix(string(decoded), "https://lain.bgm.tv/test.")
		data := images[format]
		if len(data) == 0 {
			t.Errorf("目标地址解码不对: %s", decoded)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(data)
	})
	for _, format := range []string{"jpeg", "png", "gif"} {
		t.Run(format, func(t *testing.T) {
			dir := filepath.Join(cfg.SeriesRoot, format)
			cover := &animeCoverSelection{Source: "bangumi", URL: "https://lain.bgm.tv/test." + format}
			p, published, err := saveAnimeCover(context.Background(), dir, cover)
			if err != nil || !published {
				t.Fatalf("保存失败: published=%v err=%v", published, err)
			}
			wantFormat, wantExt := format, ".jpg"
			if format != "jpeg" {
				wantFormat, wantExt = "png", ".png"
			}
			if filepath.Base(p) != "folder"+wantExt || findCover(dir) != p {
				t.Fatalf("真实图片格式未使用可识别文件名: %s", p)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, gotFormat, err := image.Decode(bytes.NewReader(data)); err != nil || gotFormat != wantFormat {
				t.Fatalf("内容与文件名格式不符: format=%s err=%v", gotFormat, err)
			}
		})
	}
}

func TestAnimeCoverSkipsEveryExistingCoverName(t *testing.T) {
	var requests atomic.Int32
	setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, base := range coverNames {
		for _, name := range []string{base, strings.ToUpper(base)} {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				p := filepath.Join(dir, name)
				writeTestFile(t, p, "existing user cover")
				if startAnimeCover(dir, animeCoverTestSelection()) {
					t.Fatal("已有封面不应启动补图")
				}
				path, published, err := saveAnimeCover(context.Background(), dir, animeCoverTestSelection())
				found, foundErr := os.Stat(path)
				original, originalErr := os.Stat(p)
				if err != nil || published || foundErr != nil || originalErr != nil || !os.SameFile(found, original) {
					t.Fatalf("没有保留已有封面: %s %v %v", path, published, err)
				}
			})
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("已有封面仍联网 %d 次", requests.Load())
	}
}

func TestAnimeCoverManualWinsDuringFetch(t *testing.T) {
	auto := animeCoverTestImage(t, "jpeg")
	manual := animeCoverTestImage(t, "png")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.Write(auto)
		case <-r.Context().Done():
		}
	})
	dir := filepath.Join(cfg.SeriesRoot, "manual-wins")
	if !startAnimeCover(dir, animeCoverTestSelection()) {
		t.Fatal("没有启动补图")
	}
	<-entered
	if !coverPublishMu.TryLock() {
		t.Fatal("自动取图联网时持有发布锁")
	}
	coverPublishMu.Unlock()
	if startAnimeCover(filepath.Join(dir, "."), animeCoverTestSelection()) {
		t.Fatal("同目录进行中的补图不应重复启动")
	}
	manualPath, err := saveCover(dir, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(manual))
	if err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	waitAnimeCoverJobs(t)
	got, err := os.ReadFile(manualPath)
	if err != nil || !bytes.Equal(got, manual) || findCover(dir) != manualPath {
		t.Fatalf("手动封面被自动补图覆盖: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("放弃自动发布后有临时文件残留: %v %v", entries, err)
	}
}

func TestAnimeCoverAndSaveCoverSharePublishLock(t *testing.T) {
	data := animeCoverTestImage(t, "jpeg")
	for _, mode := range []string{"automatic", "manual"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			srv, _ := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				w.Write(data)
			})
			dir := filepath.Join(cfg.SeriesRoot, mode)
			done := make(chan error, 1)
			coverPublishMu.Lock()
			locked := true
			defer func() {
				if locked {
					coverPublishMu.Unlock()
				}
			}()
			go func() {
				var err error
				if mode == "automatic" {
					_, _, err = saveAnimeCover(context.Background(), dir, animeCoverTestSelection())
				} else {
					_, err = saveCover(dir, srv.URL+"/manual.jpg")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("发布锁不应该阻止联网")
			}
			select {
			case err := <-done:
				t.Fatalf("封面绕过了共享发布锁: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if findCover(dir) != "" {
				t.Fatal("共享发布锁持有期间不应该写正式封面")
			}
			coverPublishMu.Unlock()
			locked = false
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchCoverFetchSharesPublishLock(t *testing.T) {
	data := append(animeCoverTestImage(t, "png"), make([]byte, 512)...)
	entered := make(chan struct{}, 1)
	srv, _ := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		w.Write(data)
	})
	spy := &animeCoverSearchSpy{results: []SourceResult{{Title: "Batch Cover", Image: srv.URL + "/batch.png"}}}
	oldSources := sources
	sources = []searchSource{spy}
	defer func() { sources = oldSources }()
	dir := filepath.Join(cfg.SeriesRoot, "batch-lock")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	coverPublishMu.Lock()
	locked := true
	defer func() {
		if locked {
			coverPublishMu.Unlock()
		}
	}()
	done := make(chan CoverResult, 1)
	go func() {
		done <- fetchCoverFor(MediaItem{Title: "Batch Cover", RawTitle: "Batch Cover", Path: dir, Category: "series", IsDir: true})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("批量补封面不应在联网前等待发布锁")
	}
	select {
	case res := <-done:
		t.Fatalf("批量补封面绕过了共享发布锁: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}
	if findCover(dir) != "" {
		t.Fatal("发布锁持有期间批量补封面不应落盘")
	}
	coverPublishMu.Unlock()
	locked = false
	res := <-done
	if res.Error != "" || res.Path == "" || findCover(dir) != res.Path {
		t.Fatalf("解锁后批量补封面未正常完成: %+v", res)
	}
}

func TestAnimeCoverRejectsBadResponsesWithoutArtifacts(t *testing.T) {
	pngData := animeCoverTestImage(t, "png")
	cases := []struct {
		name   string
		status int
		body   []byte
	}{
		{"http-error", http.StatusBadGateway, pngData},
		{"partial", http.StatusPartialContent, pngData},
		{"html", http.StatusOK, []byte("<html>not an image</html>")},
		{"truncated", http.StatusOK, []byte("\x89PNG\r\n\x1a\nincomplete")},
		{"oversize", http.StatusOK, append(bytes.Clone(pngData), make([]byte, animeCoverMaxBytes+1-len(pngData))...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write(tc.body)
			})
			for _, existed := range []bool{false, true} {
				dir := filepath.Join(t.TempDir(), "cover")
				if existed {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if _, published, err := saveAnimeCover(context.Background(), dir, animeCoverTestSelection()); err == nil || published {
					t.Fatalf("无效响应仍发布: published=%v err=%v", published, err)
				}
				entries, err := os.ReadDir(dir)
				if existed && (err != nil || len(entries) != 0) {
					t.Fatalf("失败有文件残留: %v %v", entries, err)
				}
				if !existed && !os.IsNotExist(err) {
					t.Fatalf("无效响应创建了空目录: %v", err)
				}
			}
		})
	}
}

func TestAnimeCoverAcceptsExactlyLimitAndRejectsProxyRedirect(t *testing.T) {
	pngData := animeCoverTestImage(t, "png")
	full := append(bytes.Clone(pngData), make([]byte, animeCoverMaxBytes-len(pngData))...)
	var redirected atomic.Int32
	srv, _ := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fetch" {
			w.Write(full)
			return
		}
		if r.URL.Path == "/redirect/fetch" {
			http.Redirect(w, r, "/other", http.StatusFound)
			return
		}
		redirected.Add(1)
		w.Write(pngData)
	})
	if _, _, err := saveAnimeCover(context.Background(), filepath.Join(cfg.SeriesRoot, "exact-limit"), animeCoverTestSelection()); err != nil {
		t.Fatalf("恰好 12MiB 不应该误判超限: %v", err)
	}
	cfg.Proxy.Base = srv.URL + "/redirect"
	if _, _, err := saveAnimeCover(context.Background(), filepath.Join(cfg.SeriesRoot, "redirect"), animeCoverTestSelection()); err == nil {
		t.Fatal("代理端点跳转应该作为失败处理")
	}
	if redirected.Load() != 0 {
		t.Fatal("新链路不应跟随代理端点的跳转")
	}
}

func TestAnimeCoverFetchHonorsContextDeadline(t *testing.T) {
	setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	dir := filepath.Join(cfg.SeriesRoot, "timeout")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, _, err := saveAnimeCover(ctx, dir, animeCoverTestSelection()); err == nil {
		t.Fatal("网络请求超时后应结束取图")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("超时后不应创建目录: %v", err)
	}
}

func TestAnimeCoverDeduplicatesAndRetriesAfterFailure(t *testing.T) {
	data := animeCoverTestImage(t, "png")
	var requests atomic.Int32
	setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write(data)
	})
	dir := filepath.Join(cfg.SeriesRoot, "retry-cover")
	if !startAnimeCover(dir, animeCoverTestSelection()) {
		t.Fatal("首次补图未启动")
	}
	waitAnimeCoverJobs(t)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("失败不应创建目录: %v", err)
	}
	if !startAnimeCover(dir, animeCoverTestSelection()) {
		t.Fatal("失败后的再次提交应允许重试")
	}
	waitAnimeCoverJobs(t)
	if requests.Load() != 2 || findCover(dir) == "" {
		t.Fatalf("再次补图失败: requests=%d cover=%s", requests.Load(), findCover(dir))
	}
	if items, _ := LibrarySnapshot("series"); len(items) != 0 {
		t.Fatalf("只有封面的新目录不应成为片库条目: %+v", items)
	}
}

func TestAnimeCoverDownloadReturnsBeforeFetchAndSurvivesRequestCancel(t *testing.T) {
	data := animeCoverTestImage(t, "png")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	_, rule := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.Write(data)
		case <-r.Context().Done():
		}
	})
	body, err := json.Marshal(animeCoverRequest(rule.Name, "queued-cover", animeCoverTestSelection()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/anime/download", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	handleAnimeDownload(w, req)
	cancel()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out["queued"] != float64(1) {
		t.Fatalf("提交没有立即排队: %v %v", out, err)
	}
	<-entered
	animeDL.mu.Lock()
	task := animeDL.tasks[0]
	localCover := task.info.Cover
	savedSelection := task.cover
	animeDL.mu.Unlock()
	if localCover != "" || savedSelection == nil || savedSelection.URL != animeCoverTestSelection().URL {
		t.Fatalf("输入 URL 不应被写进本地 Cover: cover=%q selection=%+v", localCover, savedSelection)
	}
	releaseOnce.Do(func() { close(release) })
	waitAnimeCoverJobs(t)
	animeDL.mu.Lock()
	localCover = task.info.Cover
	animeDL.mu.Unlock()
	if localCover == "" || strings.HasPrefix(localCover, "http") || findCover(task.info.TargetDir) != localCover {
		t.Fatalf("提交请求取消后独立补图没完成: %s", localCover)
	}
}

func TestAnimeCoverZeroQueuedExistingAndBusy(t *testing.T) {
	for _, mode := range []string{"existing-new", "existing-merge", "busy"} {
		t.Run(mode, func(t *testing.T) {
			data := animeCoverTestImage(t, "png")
			_, rule := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
			dir := filepath.Join(cfg.SeriesRoot, mode)
			if mode == "busy" {
				task := newAnimeDLTask(rule, "", mode, "series", dir, []animeDLEpisode{{File: "第01集.mp4"}})
				animeDL.tasks = []*animeDLTask{task}
			} else {
				writeTestFile(t, filepath.Join(dir, "第01集.mp4"), "existing video")
				ScanLibrary()
			}
			body := animeCoverRequest(rule.Name, mode, animeCoverTestSelection())
			if mode == "existing-merge" {
				body["targetId"] = "series:" + mode
			}
			out := animeCoverPost(t, "/api/anime/download", body, handleAnimeDownload)
			if out["ok"] != true || out["queued"] != float64(0) || out["skipped"] != float64(1) {
				t.Fatalf("已有/正在下的集不应重复排队: %v", out)
			}
			waitAnimeCoverJobs(t)
			if findCover(dir) == "" {
				t.Fatal("queued=0 仍应补缺图")
			}
			if mode == "busy" {
				if animeTaskList()[0].Cover != findCover(dir) {
					t.Fatal("同目录既有任务的本地封面未更新")
				}
			} else if it := FindItem("series:" + mode); it == nil || it.Cover == "" {
				t.Fatal("成功发布后没有刷新已有片库封面")
			}
		})
	}
}

func TestAnimeCoverInvalidInputDoesNotCreateEmptyFolder(t *testing.T) {
	var requests atomic.Int32
	_, rule := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	body := animeCoverRequest(rule.Name, "invalid-episode", animeCoverTestSelection())
	body["episodes"] = []map[string]any{{"name": "第01集", "url": "invalid", "index": 0}}
	if out := animeCoverPost(t, "/api/anime/download", body, handleAnimeDownload); out["queued"] != float64(0) {
		t.Fatalf("无效视频地址不应排队: %v", out)
	}
	if _, err := os.Stat(filepath.Join(cfg.SeriesRoot, "invalid-episode")); !os.IsNotExist(err) {
		t.Fatalf("全是无效集地址仍创建了空目录: %v", err)
	}
	for i, cover := range []*animeCoverSelection{nil, {Source: "bangumi", URL: "https://example.com/not-allowed.jpg"}} {
		body = animeCoverRequest(rule.Name, fmt.Sprintf("invalid-cover-%d", i), cover)
		out := animeCoverPost(t, "/api/anime/download", body, handleAnimeDownload)
		if out["ok"] != true || out["queued"] != float64(1) {
			t.Fatalf("缺省或非法封面不应影响视频排队: %v", out)
		}
	}
	waitAnimeCoverJobs(t)
	if requests.Load() != 0 {
		t.Fatalf("无效输入仍取图 %d 次", requests.Load())
	}
}

func TestAnimeCoverSelectionSurvivesBothRetryModes(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprint(running), func(t *testing.T) {
			data := animeCoverTestImage(t, "png")
			_, rule := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
			dir := filepath.Join(cfg.SeriesRoot, "retry-selection")
			task := newAnimeDLTask(rule, "original-ref", "retry-selection", "series", dir, []animeDLEpisode{
				{Label: "第01集", File: "第01集.mp4", URL: "https://example.com/1.mp4"},
			})
			task.cover = animeCoverTestSelection()
			task.queued = false
			task.info.AnimeFailedFiles = []string{"第01集.mp4"}
			if !running {
				task.info.Status, task.closing = "error", true
			}
			animeDL.tasks = []*animeDLTask{task}
			out := animeCoverPost(t, "/api/anime/download/"+task.info.ID+"/retry", map[string]any{}, handleAnimeRetry)
			if out["ok"] != true || out["queued"] != float64(1) {
				t.Fatalf("重试失败: %v", out)
			}
			animeDL.mu.Lock()
			retry := animeDL.tasks[0]
			saved := retry.cover
			animeDL.mu.Unlock()
			if saved == nil || *saved != *task.cover || (running && retry != task) || (!running && retry == task) {
				t.Fatalf("两种重试都必须沿用原封面选择: running=%v original=%p retry=%p selection=%+v", running, task, retry, saved)
			}
			waitAnimeCoverJobs(t)
			for _, info := range animeTaskList() {
				if info.Cover != findCover(dir) {
					t.Fatalf("同目录任务的落盘封面没更新: %+v", info)
				}
			}
		})
	}
}

type animeCoverSearchSpy struct {
	calls   atomic.Int32
	results []SourceResult
}

func (s *animeCoverSearchSpy) Name() string { return "mikan" }
func (s *animeCoverSearchSpy) Search(context.Context, string) ([]SourceResult, error) {
	s.calls.Add(1)
	return s.results, nil
}
func (s *animeCoverSearchSpy) FetchDetail(context.Context, string) ([]TorrentLink, string, error) {
	return nil, "", nil
}

func TestAnimeCoverFailureAndMissingSelectionNeverGuessByTitle(t *testing.T) {
	for _, withSelection := range []bool{true, false} {
		t.Run(fmt.Sprint(withSelection), func(t *testing.T) {
			video := append([]byte("\x00\x00\x00\x18ftypisom"), bytes.Repeat([]byte("v"), 2048)...)
			srv, rule := setupAnimeCoverTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/fetch" {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "video/mp4")
				http.ServeContent(w, r, "episode.mp4", time.Time{}, bytes.NewReader(video))
			})
			spy, oldSources := &animeCoverSearchSpy{}, sources
			sources = []searchSource{spy}
			defer func() { sources = oldSources }()
			var cover *animeCoverSelection
			if withSelection {
				cover = animeCoverTestSelection()
			}
			body := animeCoverRequest(rule.Name, "no-title-guess", cover)
			body["episodes"] = []map[string]any{{"name": "第01集", "url": srv.URL + "/episode.mp4", "index": 0}}
			out := animeCoverPost(t, "/api/anime/download", body, handleAnimeDownload)
			if out["ok"] != true {
				t.Fatalf("排队失败: %v", out)
			}
			task := nextAnimeDLTask()
			runAnimeDLTask(task)
			waitAnimeCoverJobs(t)
			if task.info.Status != "done" || len(task.info.Files) != 1 {
				t.Fatalf("图片失败不应影响视频完成: %+v", task.info)
			}
			if spy.calls.Load() != 0 || findCover(task.info.TargetDir) != "" {
				t.Fatalf("番剧完成后仍按片名猜图: searches=%d cover=%s", spy.calls.Load(), findCover(task.info.TargetDir))
			}
		})
	}
}

func TestSaveCoverStillActivelyReplacesExistingCover(t *testing.T) {
	dir := t.TempDir()
	data := animeCoverTestImage(t, "png")
	writeTestFile(t, filepath.Join(dir, "folder.jpg"), "old jpeg")
	writeTestFile(t, filepath.Join(dir, "poster.png"), "old poster")
	p, err := saveCover(dir, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data))
	if err != nil || filepath.Base(p) != "folder.png" {
		t.Fatalf("主动换封面失败: %s %v", p, err)
	}
	got, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("新封面未写入: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || findCover(dir) != p {
		t.Fatalf("主动替换没有清理其它旧封面: %v %v", entries, err)
	}
}
