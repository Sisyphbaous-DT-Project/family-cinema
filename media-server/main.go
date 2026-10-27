package main

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var webFS embed.FS

var cfg Config

func currentConfig() Config { return cfg }

// readAllLimit 限量读取响应体
func readAllLimit(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

// Server HTTP 服务
type Server struct {
	http.Handler
}

func NewServer() *Server {
	mux := http.NewServeMux()
	s := &Server{Handler: mux}

	// 前端静态资源
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 前端文件不缓存：App/浏览器每次都重新校验，保证"只改服务器就生效"的热更新
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		if serveLegacy(w, r) {
			return
		}
		if r.URL.Path == "/" {
			data, err := readWebFile("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "index.html" || name == "app.js" || name == "style.css" || strings.HasPrefix(name, "static/") {
			data, err := readWebFile(name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			ct := "application/octet-stream"
			switch {
			case strings.HasSuffix(name, ".js"):
				ct = "application/javascript"
			case strings.HasSuffix(name, ".css"):
				ct = "text/css"
			case strings.HasSuffix(name, ".svg"):
				ct = "image/svg+xml"
			}
			w.Header().Set("Content-Type", ct)
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	})

	// ---- API ----
	api := func(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if err := fn(w, r); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
	}
	writeJSON := func(w http.ResponseWriter, v any) error {
		return json.NewEncoder(w).Encode(v)
	}

	// 番剧聚合接口（/api/anime/*）
	mountAnime(mux)

	// App 应用内升级（/api/app/*）
	mountAppUpdate(mux, api)

	// 前端热更新清单：App 启动拉这个比对版本，决定要不要下载新前端
	mux.HandleFunc("/api/web-manifest", api(func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, buildWebManifest())
	}))

	// 媒体库列表（含正在下载的条目，前端可展示进度条）
	mux.HandleFunc("/api/library", api(func(w http.ResponseWriter, r *http.Request) error {
		cat := r.URL.Query().Get("cat")
		items, cats := LibrarySnapshot(cat)
		return writeJSON(w, map[string]any{
			"categories":  cats,
			"items":       items,
			"downloading": downloadingCards(cat),
			"lastScan":    library.lastScan.Unix(),
		})
	}))

	// 重新扫描
	mux.HandleFunc("/api/scan", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		go ScanLibrary()
		return writeJSON(w, map[string]any{"ok": true})
	}))

	// 视频流（支持 Range）
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			http.Error(w, "missing path", http.StatusBadRequest)
			return
		}
		if !safeMediaPath(p) {
			http.Error(w, "forbidden path", http.StatusForbidden)
			return
		}
		http.ServeFile(w, r, p)
	})

	// 本地封面
	mux.HandleFunc("/api/cover", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" || !safeMediaPath(p) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// 换封面时文件名不变（都是 folder.jpg），浏览器会拿缓存里的旧图不再请求。
		// no-cache 让它每次都带 If-Modified-Since 来问：文件没变 ServeFile 回 304 省流量，
		// 变了就立即拿到新图，用户不需要手动强制刷新。
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		http.ServeFile(w, r, p)
	})

	// 聚合搜索
	mux.HandleFunc("/api/search", api(func(w http.ResponseWriter, r *http.Request) error {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			return writeJSON(w, []SearchHit{})
		}
		hits := AggregateSearch(q)
		return writeJSON(w, hits)
	}))

	// 站详情（磁力列表）
	mux.HandleFunc("/api/detail", api(func(w http.ResponseWriter, r *http.Request) error {
		site := r.URL.Query().Get("site")
		pageURL := r.URL.Query().Get("url")
		tls, cover := GetDetail(site, pageURL)
		return writeJSON(w, map[string]any{"site": site, "torrents": tls, "cover": cover})
	}))

	// mikan 图片代理（经配置的自托管代理）
	mux.HandleFunc("/api/mikan-img", func(w http.ResponseWriter, r *http.Request) {
		imgURL := r.URL.Query().Get("url")
		u, err := url.Parse(imgURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !strings.HasSuffix(strings.ToLower(u.Hostname()), "mikanani.me") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		body, err := fetchViaProxy(r.Context(), imgURL)
		if err != nil {
			http.Error(w, "fetch failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write([]byte(body))
	})

	// BT 测速
	mux.HandleFunc("/api/speedtest", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			Magnet string `json:"magnet"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Magnet == "" {
			return errors.New("magnet required")
		}
		// 测速耗时长，异步到后台执行，立即返回任务标记
		secs := currentConfig().Speedtest
		if v := r.URL.Query().Get("secs"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 30 {
				secs = n
			}
		}
		type resultChan struct{ r SpeedResult }
		ch := make(chan SpeedResult, 1)
		go func() { ch <- SpeedTest(req.Magnet, secs) }()
		select {
		case res := <-ch:
			return writeJSON(w, res)
		case <-time.After(75 * time.Second):
			// 超时也返回（后台继续？这里直接放弃）
			return writeJSON(w, SpeedResult{Error: "测速超时"})
		}
	}))

	// 磁力元数据（名字/大小，不下载）
	mux.HandleFunc("/api/meta", api(func(w http.ResponseWriter, r *http.Request) error {
		mag := r.URL.Query().Get("magnet")
		if mag == "" && r.Method == http.MethodPost {
			var req struct {
				Magnet string `json:"magnet"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				mag = req.Magnet
			}
		}
		if mag == "" {
			return errors.New("magnet required")
		}
		return writeJSON(w, FetchMeta(mag))
	}))

	// 下载任务
	mux.HandleFunc("/api/download", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			Magnet   string `json:"magnet"`
			Title    string `json:"title"`
			Category string `json:"category"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Magnet == "" || req.Title == "" {
			return errors.New("magnet and title required")
		}
		if req.Category != "movie" && req.Category != "series" {
			req.Category = "movie"
		}
		task, err := StartDownload(req.Magnet, req.Title, req.Category)
		if err != nil {
			return err
		}
		return writeJSON(w, task)
	}))
	// 管理：删除媒体条目（整个目录或单个文件）
	mux.HandleFunc("/api/manage/delete", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			ID    string `json:"id"`    // 媒体条目 ID（删整部）
			Path  string `json:"path"`  // 单个文件路径（删单集）
			Force bool   `json:"force"` // 目录非空时是否强制删
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return errors.New("请求解析失败")
		}
		res, err := DeleteMedia(req.ID, req.Path, req.Force)
		if err != nil {
			return err
		}
		go ScanLibrary()
		return writeJSON(w, res)
	}))

	// 管理：重命名条目（改文件夹名，等于改展示名）
	mux.HandleFunc("/api/manage/rename", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return errors.New("请求解析失败")
		}
		if err := RenameMedia(req.ID, req.Name); err != nil {
			return err
		}
		go ScanLibrary()
		return writeJSON(w, map[string]any{"ok": true})
	}))

	// 管理：设置单个条目的封面
	mux.HandleFunc("/api/manage/cover", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			ID    string `json:"id"`
			Cover string `json:"cover"` // http(s) URL 或 data:image/...;base64,
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
			return errors.New("请求解析失败")
		}
		it := FindItem(req.ID)
		if it == nil {
			return errors.New("条目不存在")
		}
		if !it.IsDir {
			return errors.New("根目录下的单文件无法附加封面，请先归入文件夹")
		}
		p, err := saveCover(it.Path, req.Cover)
		if err != nil {
			return err
		}
		go ScanLibrary()
		return writeJSON(w, map[string]any{"ok": true, "cover": p})
	}))

	// 管理：磁盘占用概览
	mux.HandleFunc("/api/manage/stats", api(func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, ManageStats())
	}))

	// 管理：BT 缓存列表（停掉的任务会把半成品留在缓存里白占硬盘）
	mux.HandleFunc("/api/manage/cache", api(func(w http.ResponseWriter, r *http.Request) error {
		entries, total := ListCache()
		return writeJSON(w, map[string]any{
			"entries":    entries,
			"total":      total,
			"totalHuman": humanSize(total),
			"dir":        currentConfig().CacheDir,
		})
	}))

	// 管理：删除缓存（单项或全部，正在下载的会跳过）
	mux.HandleFunc("/api/manage/cache/delete", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			Name string `json:"name"` // 缓存项名称（infohash 目录名或文件名）
			All  bool   `json:"all"`  // 清空全部
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return errors.New("请求解析失败")
		}
		res, err := DeleteCache(req.Name, req.All)
		if err != nil {
			return err
		}
		return writeJSON(w, res)
	}))

	// 管理：清理任务记录（只清列表，不动文件）
	mux.HandleFunc("/api/manage/clear-tasks", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		n := ClearFinishedTasks()
		return writeJSON(w, map[string]any{"ok": true, "cleared": n})
	}))

	// 批量抓取封面（给媒体库里没有封面的条目补图）
	mux.HandleFunc("/api/covers/fetch", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		force := r.URL.Query().Get("force") == "1"
		if !FetchCoversForLibrary(force) {
			return errors.New("封面抓取任务正在进行中")
		}
		return writeJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("/api/covers/status", api(func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, CoverJobStatus())
	}))

	// 手动添加任务：磁力链接 / 上传 .torrent，可附封面（URL 或 base64 图片）
	mux.HandleFunc("/api/manual", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			Magnet   string `json:"magnet"`
			Torrent  string `json:"torrent"` // base64 编码的 .torrent 文件内容
			Title    string `json:"title"`
			Category string `json:"category"`
			Cover    string `json:"cover"`    // http(s) URL 或 data:image/...;base64,
			TargetID string `json:"targetId"` // 非空则并入这个已有条目（追番加新集）
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 24<<20)).Decode(&req); err != nil {
			return errors.New("请求解析失败: " + err.Error())
		}

		// 并入已有条目：目录、分类都沿用那个条目的
		targetDir := ""
		if id := strings.TrimSpace(req.TargetID); id != "" {
			it := FindItem(id)
			if it == nil {
				return errors.New("目标条目不存在，请重新选择")
			}
			if !it.IsDir {
				return errors.New("目标是根目录下的单个文件，无法并入；请选择文件夹条目")
			}
			targetDir = it.Path
			req.Category = it.Category
			if strings.TrimSpace(req.Title) == "" {
				req.Title = it.Title
			}
		}

		req.Title = strings.TrimSpace(req.Title)
		if req.Title == "" {
			return errors.New("请填写保存名称")
		}
		if req.Category != "movie" && req.Category != "series" {
			return errors.New("请选择分类：movie 或 series")
		}

		var task *TorrentTask
		var err error
		if strings.TrimSpace(req.Torrent) != "" {
			raw, derr := base64.StdEncoding.DecodeString(stripDataURL(req.Torrent))
			if derr != nil {
				return errors.New("种子文件解码失败")
			}
			task, err = StartDownloadFromTorrent(raw, req.Title, req.Category, req.Cover, targetDir)
		} else if strings.TrimSpace(req.Magnet) != "" {
			task, err = StartManualMagnet(strings.TrimSpace(req.Magnet), req.Title, req.Category, req.Cover, targetDir)
		} else {
			return errors.New("请提供磁力链接或上传种子文件")
		}
		if err != nil {
			return err
		}
		return writeJSON(w, task)
	}))

	// 已有条目清单（供手动添加时搜索选择要并入哪一部）
	mux.HandleFunc("/api/folders", api(func(w http.ResponseWriter, r *http.Request) error {
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		cat := r.URL.Query().Get("cat")
		items, _ := LibrarySnapshot(cat)
		type folder struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Category string `json:"category"`
			Episodes int    `json:"episodes"`
			SizeH    string `json:"sizeHuman"`
			Cover    string `json:"cover,omitempty"`
		}
		out := []folder{}
		for _, it := range items {
			if !it.IsDir {
				continue // 只有文件夹条目能并入新文件
			}
			if q != "" &&
				!strings.Contains(strings.ToLower(it.Title), q) &&
				!strings.Contains(strings.ToLower(it.RawTitle), q) {
				continue
			}
			out = append(out, folder{
				ID: it.ID, Title: it.Title, Category: it.Category,
				Episodes: len(it.Episodes), SizeH: humanSize(it.Size), Cover: it.Cover,
			})
			if len(out) >= 40 {
				break
			}
		}
		return writeJSON(w, out)
	}))

	mux.HandleFunc("/api/tasks", api(func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, ListTasks())
	}))
	mux.HandleFunc("/api/task/stop", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return err
		}
		return writeJSON(w, map[string]any{"ok": StopTask(req.ID) == nil})
	}))

	// ---- 上传与文件管理 ----
	// 上传视频（流式，不走内存）
	mux.HandleFunc("/api/upload", api(func(w http.ResponseWriter, r *http.Request) error {
		res, err := HandleUpload(w, r)
		if err != nil {
			return err
		}
		return writeJSON(w, res)
	}))
	// 磁盘剩余空间（上传前检查放不放得下）
	mux.HandleFunc("/api/disk", api(func(w http.ResponseWriter, r *http.Request) error {
		d, err := DiskSpace()
		if err != nil {
			return err
		}
		return writeJSON(w, d)
	}))
	// 文件管理：列目录
	mux.HandleFunc("/api/fs/list", api(func(w http.ResponseWriter, r *http.Request) error {
		d, err := FsList(r.URL.Query().Get("path"))
		if err != nil {
			return err
		}
		return writeJSON(w, d)
	}))
	// 文件管理：新建文件夹
	mux.HandleFunc("/api/fs/mkdir", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			return errors.New("请求解析失败")
		}
		if err := FsMkdir(req.Path, req.Name); err != nil {
			return err
		}
		return writeJSON(w, map[string]any{"ok": true})
	}))
	// 文件管理：删除（非空目录会先返回 needConfirm，前端确认后带 force 再调一次）
	mux.HandleFunc("/api/fs/delete", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			Path  string `json:"path"`
			Force bool   `json:"force"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			return errors.New("请求解析失败")
		}
		res, err := FsDelete(req.Path, req.Force)
		if err != nil {
			return err
		}
		go ScanLibrary() // 媒体目录里的删除要让媒体库立刻反映出来
		return writeJSON(w, res)
	}))
	// 文件管理：改名
	mux.HandleFunc("/api/fs/rename", api(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return errors.New("method not allowed")
		}
		var req struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			return errors.New("请求解析失败")
		}
		if err := FsRename(req.Path, req.Name); err != nil {
			return err
		}
		return writeJSON(w, map[string]any{"ok": true})
	}))

	return s
}

// DownloadCard 媒体库里"正在下载"的占位卡片
type DownloadCard struct {
	TaskID   string  `json:"taskId"`
	Title    string  `json:"title"`
	Category string  `json:"category"`
	Progress float64 `json:"progress"`
	SpeedKBs float64 `json:"speedKBs"`
	Status   string  `json:"status"`
	Stage    string  `json:"stage,omitempty"`
	Cover    string  `json:"cover,omitempty"`
	Total    int64   `json:"total"`
	Done     int64   `json:"done"`
}

// downloadingCards 未完成的任务转成媒体库卡片。
// 已完成的任务不返回：那时文件已入库，正常条目会显示出来。
func downloadingCards(cat string) []DownloadCard {
	var out []DownloadCard
	for _, t := range ListTasks() {
		if t.Status != "running" && t.Status != "processing" {
			continue
		}
		if cat != "" && cat != "all" && t.Category != cat {
			continue
		}
		out = append(out, DownloadCard{
			TaskID:   t.ID,
			Title:    t.Name,
			Category: t.Category,
			Progress: t.Progress,
			SpeedKBs: t.SpeedKBs,
			Status:   t.Status,
			Stage:    t.Stage,
			Cover:    t.Cover,
			Total:    t.BytesTotal,
			Done:     t.BytesDone,
		})
	}
	return out
}

// ---- 前端资源热更新支持 ----

// webFiles 参与热更新版本计算的前端文件
var webFiles = []string{"index.html", "app.js", "style.css"}

// webRoot 可执行文件同目录的 web/ 目录（与 config.json 同一套定位逻辑）
func webRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return "web"
	}
	return filepath.Join(filepath.Dir(exe), "web")
}

// readWebFile 读前端资源：优先读磁盘 web/ 目录（改前端只推文件、不用重编二进制），
// 磁盘上没有时回退到编译进二进制的版本，保证旧部署方式下服务照常可用。
func readWebFile(name string) ([]byte, error) {
	// 防路径穿越：含 .. 的请求不碰磁盘（embed 会把 .. 当普通路径段，天然安全）
	if !strings.Contains(name, "..") {
		if data, err := os.ReadFile(filepath.Join(webRoot(), name)); err == nil {
			return data, nil
		}
	}
	return webFS.ReadFile("web/" + name)
}

// WebManifest 前端热更新清单
type WebManifest struct {
	Version string            `json:"version"`
	Note    string            `json:"note"`
	Files   map[string]string `json:"files"` // 文件名 -> sha256
}

// buildWebManifest 生成前端清单。运维可在 web/ 里放手写的 web-manifest.json
// 自定义版本号与更新说明；文件哈希始终以当前实际内容为准，不信手写的。
func buildWebManifest() WebManifest {
	files := hashWebFiles()
	if data, err := os.ReadFile(filepath.Join(webRoot(), "web-manifest.json")); err == nil {
		var m WebManifest
		if json.Unmarshal(data, &m) == nil && m.Version != "" {
			m.Files = files
			return m
		}
	}
	// 版本号取三个文件哈希的组合摘要前 8 位：任何一个前端文件变了版本号就变，
	// App 据此判断要不要热更新（只看 index.html 会漏掉只改 app.js/style.css 的情况）
	h := sha256.New()
	for _, name := range webFiles {
		h.Write([]byte(files[name]))
	}
	v := hex.EncodeToString(h.Sum(nil))
	if len(v) > 8 {
		v = v[:8]
	}
	return WebManifest{Version: v, Note: "auto", Files: files}
}

func hashWebFiles() map[string]string {
	out := map[string]string{}
	for _, name := range webFiles {
		data, err := readWebFile(name)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		out[name] = hex.EncodeToString(sum[:])
	}
	return out
}

// stripDataURL 去掉 data URL 前缀，只保留 base64 载荷
func stripDataURL(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i >= 0 {
			return s[i+1:]
		}
	}
	return s
}

// safeMediaPath 校验路径位于媒体根目录内
func safeMediaPath(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	cfg := currentConfig()
	for _, root := range []string{cfg.MovieRoot, cfg.SeriesRoot, cfg.DownloadDir} {
		ra, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if abs == ra || strings.HasPrefix(abs, ra+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func main() {
	cfg = loadConfig()
	initHTTPClient(cfg)
	initSources(cfg)
	if err := initBT(cfg); err != nil {
		log.Printf("BT 引擎启动失败（测速/下载不可用）: %v", err)
	}
	// 启动时扫描媒体库
	go ScanLibrary()
	// 局域网自动发现应答（App 找盒子用）
	startDiscovery(cfg.Port)

	addr := cfg.ListenAddr + ":" + cfg.Port
	if cfg.ListenAddr == "" {
		addr = ":" + cfg.Port
	}
	log.Printf("media-server v2 启动于 http://0.0.0.0:%s", cfg.Port)
	log.Printf("媒体目录: 电影电视剧=%s 番剧=%s", cfg.MovieRoot, cfg.SeriesRoot)
	if err := http.ListenAndServe(addr, NewServer()); err != nil {
		log.Fatal(err)
	}
}
