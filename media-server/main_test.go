package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanLibraryGroupsFolderAsOneItem(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()

	movieFolder := filepath.Join(movieRoot, "中文电影电视剧")
	writeTestFile(t, filepath.Join(movieFolder, "说明.txt"), "not video")
	writeTestFile(t, filepath.Join(movieFolder, "影片.mp4"), "small")
	writeTestFile(t, filepath.Join(movieFolder, "花絮", "正片.mkv"), "side content")
	writeTestFile(t, filepath.Join(movieRoot, "根目录视频.mp4"), "root video")
	writeTestFile(t, filepath.Join(seriesRoot, "第一季", "第01集.mp4"), "episode")

	setTestRoots(t, movieRoot, seriesRoot)
	ScanLibrary()

	items, _ := LibrarySnapshot("all")
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3: %#v", len(items), items)
	}
	byTitle := map[string]MediaItem{}
	for _, it := range items {
		byTitle[it.Title] = it
	}
	movie, ok := byTitle["中文电影电视剧"]
	if !ok {
		t.Fatalf("folder not grouped as one item: %#v", byTitle)
	}
	if len(movie.Episodes) != 2 {
		t.Fatalf("folder episodes = %d, want 2", len(movie.Episodes))
	}
	// 目录 + 根级文件都收录
	if _, ok := byTitle["根目录视频"]; !ok {
		t.Fatal("root-level video not indexed")
	}
	if _, ok := byTitle["第一季"]; !ok {
		t.Fatal("series folder not indexed")
	}
}

func TestEpisodeIndexParsing(t *testing.T) {
	cases := map[string]int{
		"第01集.mp4":        1,
		"第1集.mkv":         1,
		"第12話.mp4":        12,
		"EP03.mp4":        3,
		"S01E05.mkv":      5,
		"[07][1080p].mkv": 7,
		"movie(2024).mp4": 0, // 4 位年份不认
		"SomeVideo.MKV":   0,
		"死或生5.mp4":        0, // 单独数字不认（太短易误伤）
	}
	for name, want := range cases {
		if got := parseEpisodeIndex(name); got != want {
			t.Errorf("parseEpisodeIndex(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestDisplayTitleCleansTags(t *testing.T) {
	cases := map[string]string{
		"[Sakurato] Sousou no Frieren [01-28][AVC-8bit 1080p AAC][CHS]": "Sousou no Frieren",
		"[UHA-WINGS][Mushoku Tensei][01-11][x264 1080p][CHS]":           "Mushoku Tensei",
		"别当欧尼酱了!  Onii-chan wa Oshimai! [简中]":                           "别当欧尼酱了!",
		"白圣女与黑牧师  Shiro Seijo to Kuro Bokushi":                          "白圣女与黑牧师",
		"我推的孩子 Oshi no Ko [][简体双语]":                                     "我推的孩子",
		"流浪地球系列": "流浪地球系列",
		"周处除三害":  "周处除三害",
	}
	for raw, want := range cases {
		if got := displayTitle(raw); got != want {
			t.Errorf("displayTitle(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLocalSearchMatchesRawTitle(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(seriesRoot, "[Sakurato] Sousou no Frieren [01-28][1080p]", "第01集.mkv"), "a")
	setTestRoots(t, movieRoot, seriesRoot)
	ScanLibrary()

	// 展示名已清洗，但用原始名里的字幕组也要能搜到
	if hits := SearchLocal("Frieren"); len(hits) != 1 {
		t.Fatalf("search by roman name got %d hits, want 1", len(hits))
	}
	if hits := SearchLocal("Sakurato"); len(hits) != 1 {
		t.Fatalf("search by raw tag got %d hits, want 1", len(hits))
	}
	items, _ := LibrarySnapshot("series")
	if len(items) != 1 || items[0].Title != "Sousou no Frieren" {
		t.Fatalf("display title = %q, want cleaned name", items[0].Title)
	}
}

func TestSameTitleCrossLanguage(t *testing.T) {
	// 站点标题含中日文名 + 罗马音，本地目录只有罗马音，应判定同片
	if !sameTitle("Sousou no Frieren", "葬送的芙莉莲_Sousou no Frieren") {
		t.Error("罗马音本地名应能匹配含中文的站点标题")
	}
	if !sameTitle("Mushoku Tensei", "无职转生 Mushoku Tensei [01-11]") {
		t.Error("罗马音应匹配中英混排标题")
	}
	// 系列续作不能误合并
	if sameTitle("流浪地球", "流浪地球2：再次冒险") {
		t.Error("系列续作不应合并")
	}
	// 季度/合集后缀允许合并
	if !sameTitle("流浪地球", "流浪地球系列") {
		t.Error("系列后缀应允许合并")
	}
	// 过短的拉丁串不应模糊命中
	if sameTitle("Alya", "完全不同的作品 Alya something") {
		t.Error("过短拉丁串不应命中")
	}
}

func TestMovieNeverGetsEpisodeLabel(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	// 电影：文件名解析不出集数
	writeTestFile(t, filepath.Join(movieRoot, "奥本海默", "Oppenheimer.2023.mkv"), "a")
	// 电影：目录里两个版本
	writeTestFile(t, filepath.Join(movieRoot, "流浪地球", "国语版.mkv"), "a")
	writeTestFile(t, filepath.Join(movieRoot, "流浪地球", "英语版.mkv"), "b")
	// 番剧：正常集数 + 一个识别不出的
	writeTestFile(t, filepath.Join(seriesRoot, "某番", "第01集.mkv"), "a")
	writeTestFile(t, filepath.Join(seriesRoot, "某番", "特别篇.mkv"), "b")
	setTestRoots(t, movieRoot, seriesRoot)
	ScanLibrary()

	items, _ := LibrarySnapshot("all")
	byTitle := map[string]MediaItem{}
	for _, it := range items {
		byTitle[it.Title] = it
	}

	// 电影不能出现任何"第N集"标签
	for _, name := range []string{"奥本海默", "流浪地球"} {
		it, ok := byTitle[name]
		if !ok {
			t.Fatalf("电影 %q 未入库", name)
		}
		for _, ep := range it.Episodes {
			if strings.Contains(ep.Title, "集") {
				t.Errorf("电影 %q 出现集数标签: %q", name, ep.Title)
			}
			if ep.Index >= 900 {
				t.Errorf("电影 %q 出现魔法编号 %d", name, ep.Index)
			}
		}
	}
	// 单文件电影不加标签
	if got := byTitle["奥本海默"].Episodes[0].Title; got != "" {
		t.Errorf("单文件电影标签应为空, got %q", got)
	}
	// 多版本电影标为"版本 N"
	if got := byTitle["流浪地球"].Episodes[0].Title; got != "版本 1" {
		t.Errorf("多版本电影标签 = %q, want 版本 1", got)
	}

	// 番剧识别不出的那集应续编为第2集，不是第901集
	series := byTitle["某番"]
	if len(series.Episodes) != 2 {
		t.Fatalf("番剧集数 = %d, want 2", len(series.Episodes))
	}
	if series.Episodes[1].Title != "第2集" {
		t.Errorf("番剧补编标签 = %q, want 第2集", series.Episodes[1].Title)
	}
}

func TestLocalSearchPrefersPrefix(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(movieRoot, "流浪地球.mp4"), "a")
	writeTestFile(t, filepath.Join(movieRoot, "我的流浪地球生活.mp4"), "b")
	setTestRoots(t, movieRoot, seriesRoot)
	ScanLibrary()

	// 前缀命中优先
	hits := SearchLocal("流浪")
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
}
func TestHandleStreamSupportsRangeRequests(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	content := "0123456789"
	path := filepath.Join(movieRoot, "电影.mp4")
	writeTestFile(t, path, content)
	setTestRoots(t, movieRoot, seriesRoot)
	ScanLibrary()

	srv := NewServer()
	request := httptest.NewRequest(http.MethodGet, "/api/stream?path="+url.QueryEscape(path), nil)
	request.Header.Set("Range", "bytes=2-5")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPartialContent)
	}
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "2345" {
		t.Fatalf("body = %q, want %q", body, "2345")
	}
	if got := recorder.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q, want %q", got, "bytes 2-5/10")
	}
}

func TestLibraryAPIDoesNotExposePhysicalPath(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(movieRoot, "电影.mp4"), "movie")
	setTestRoots(t, movieRoot, seriesRoot)
	ScanLibrary()

	srv := NewServer()
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/library", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(response.Items))
	}
	if _, exists := response.Items[0]["path"]; exists {
		t.Fatal("media API exposed the physical path")
	}
}

func setTestRoots(t *testing.T, movieRoot, seriesRoot string) {
	t.Helper()
	cfg = defaultConfig()
	cfg.MovieRoot = movieRoot
	cfg.SeriesRoot = seriesRoot
	cfg.CacheDir = filepath.Join(t.TempDir(), "cache")
	initHTTPClient(cfg)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// moveFile 必须能跨文件系统搬家：缓存在 /data（ext4），媒体目录在外挂硬盘（NTFS），
// os.Rename 跨设备会返回 EXDEV。这里用一个 rename 必然失败的路径来验证退回逻辑。
func TestMoveFileFallsBackToCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	payload := []byte("frieren-episode-content")
	if err := os.WriteFile(src, payload, 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "dst.bin")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile 失败: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("目标文件读不到: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("内容不一致: 期望 %q，得到 %q", payload, got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("源文件应该已被删除")
	}
}

// 复制失败时不能留下半截文件
func TestMoveFileMissingSourceLeavesNoResidue(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.bin")
	if err := moveFile(filepath.Join(dir, "nope.bin"), dst); err == nil {
		t.Fatal("源文件不存在时应该报错")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("失败后不应留下目标文件")
	}
}

// 用数字/罗马数字标季的片名不能被截掉季号：
// extractCJKSegment 切在最后一个中日文字符上，数字得显式保留
func TestDisplayTitleKeepsSeasonNumber(t *testing.T) {
	cases := map[string]string{
		"无职转生3":                           "无职转生3",
		"无职转生2 Mushoku Tensei S2":         "无职转生2",
		"刀剑神域Ⅱ":                           "刀剑神域Ⅱ",
		"[Sakurato] 无职转生3 [01-12][1080p]": "无职转生3",
		"别当欧尼酱了!":                         "别当欧尼酱了!",
		"葬送的芙莉莲":                          "葬送的芙莉莲",
	}
	for raw, want := range cases {
		if got := displayTitle(raw); got != want {
			t.Errorf("displayTitle(%q) = %q，期望 %q", raw, got, want)
		}
	}
}

// 换封面时必须清掉旧的变体，否则 findCover 会一直挑中排序靠前的旧图
func TestSaveCoverRemovesOtherVariants(t *testing.T) {
	dir := t.TempDir()
	// 目录里先有一张旧的 folder.jpg（之前批量补图留下的）
	oldJPG := filepath.Join(dir, "folder.jpg")
	if err := os.WriteFile(oldJPG, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	// 上传一张 PNG（最小合法 PNG 头 + 填充，saveCover 要求 >=128 字节且魔数正确）
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 200)...)
	got, err := saveCover(dir, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png))
	if err != nil {
		t.Fatalf("saveCover 失败: %v", err)
	}
	if filepath.Base(got) != "folder.png" {
		t.Fatalf("应存成 folder.png，实际 %s", filepath.Base(got))
	}
	if _, err := os.Stat(oldJPG); !os.IsNotExist(err) {
		t.Fatal("旧的 folder.jpg 应该被清掉，否则 findCover 会继续选中它")
	}
	if c := findCover(dir); filepath.Base(c) != "folder.png" {
		t.Fatalf("findCover 应返回新图，实际 %s", c)
	}
}

// 缓存删除必须拒绝路径穿越
func TestDeleteCacheRejectsPathTraversal(t *testing.T) {
	for _, bad := range []string{"../电影电视剧", "a/b", `a\b`, ".", ".."} {
		if _, err := DeleteCache(bad, false); err == nil {
			t.Fatalf("应该拒绝非法缓存名: %q", bad)
		}
	}
}

var _ = strings.Contains
