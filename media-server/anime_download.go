package main

// 番剧"下载到盒子"：前端在番剧播放页选几集（或整部），选好存到哪（新建文件夹 / 并入已有文件夹），
// 盒子同时处理 2 集：解析视频地址 → 下载（分段视频拼成 mp4）→ 存成"第NN集.mp4"，下好一集就重扫片库，
// 全部下完没封面的自动补封面。任务并进 ListTasks（和 BT 下载同一张任务表）：片库页"盒子正在下载"
// 卡片、片库管理的下载任务页都能看进度、能停。任务只在内存里（和 BT 任务一样，盒子重启就没了）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	animeapi "media-server/internal/anime/api"
	"media-server/internal/anime/dl"
	"media-server/internal/anime/hls"
	"media-server/internal/anime/resolve"
)

const (
	animeDLMinFree     = 2 << 30          // 剩余空间少于 2GB 就不下了（分段 + 拼好的 mp4 会临时占两份）
	animeDLWaitPhone   = 20 * time.Minute // 要手机解析的站，最多等手机这么久
	animeDLKeepHistory = 30               // 任务表里最多留多少个已结束的番剧下载任务
)

type animeDLEpisode struct {
	Label string // 第05集
	File  string // 第05集.mp4
	URL   string // 播放页地址
}

type animeDLTask struct {
	info   TorrentTask // 给任务表看的那份（受 animeDL.mu 保护）
	rule   string
	ref    string
	eps    []animeDLEpisode
	ctx    context.Context
	cancel context.CancelFunc
	queued bool
}

var animeDL struct {
	mu    sync.Mutex
	api   *animeapi.API
	tasks []*animeDLTask // 新的在前
	wake  chan struct{}
}

// initAnimeDownload 由 mountAnime 调用：挂接口、起后台下载协程
func initAnimeDownload(mux *http.ServeMux, a *animeapi.API) {
	animeDL.api = a
	animeDL.wake = make(chan struct{}, 1)
	go animeDLWorker()
	mux.HandleFunc("POST /api/anime/download", handleAnimeDownload)
}

// ---- 接口 ----

type animeDLRequest struct {
	Rule     string `json:"rule"`
	Ref      string `json:"ref"`      // 作品来源（详情页地址或编号），解析时当 Referer 用
	Title    string `json:"title"`    // 番名
	Category string `json:"category"` // 新建文件夹时放哪类：series（默认）/ movie
	Folder   string `json:"folder"`   // 新建文件夹的名字（空则用番名）
	TargetID string `json:"targetId"` // 非空：并入这个已有条目的文件夹（分类跟着它）
	Episodes []struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Index int    `json:"index"` // 在这条线路里的位置（从 0 起），集名里认不出集数时用它
	} `json:"episodes"`
}

// POST /api/anime/download
func handleAnimeDownload(w http.ResponseWriter, r *http.Request) {
	var req animeDLRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeAnimeDLError(w, "请求格式不对")
		return
	}
	rule := animeDL.api.Rule(req.Rule)
	if rule == nil || rule.Unsupported() != "" {
		writeAnimeDLError(w, "这个片源用不了")
		return
	}
	if len(req.Episodes) == 0 {
		writeAnimeDLError(w, "还没选要下载的集")
		return
	}
	if len(req.Episodes) > 500 {
		writeAnimeDLError(w, "一次选的集太多了")
		return
	}

	// 存到哪：并入已有文件夹，或在番剧/电影电视剧下新建
	cfg := currentConfig()
	var dir, category, title string
	if id := strings.TrimSpace(req.TargetID); id != "" {
		it := FindItem(id)
		if it == nil {
			writeAnimeDLError(w, "要放进去的文件夹不见了，请重新选")
			return
		}
		if !it.IsDir {
			writeAnimeDLError(w, "选的是单个文件，不是文件夹，请换一个")
			return
		}
		dir, category, title = it.Path, it.Category, it.Title
	} else {
		category = req.Category
		if category != "movie" {
			category = "series"
		}
		name := strings.TrimSpace(req.Folder)
		if name == "" {
			name = strings.TrimSpace(req.Title)
		}
		name = sanitizeDirName(name)
		if name == "." || name == ".." {
			name = "未命名"
		}
		root := cfg.SeriesRoot
		if category == "movie" {
			root = cfg.MovieRoot
		}
		dir, title = filepath.Join(root, name), name
	}

	// 每集的文件名；文件夹里已经有这一集（或别的任务正在下）就跳过
	have := existingEpisodes(dir)
	busy := downloadingFiles(dir)
	var eps []animeDLEpisode
	seen := map[string]bool{}
	skipped := 0
	for _, e := range req.Episodes {
		if !strings.HasPrefix(e.URL, "http://") && !strings.HasPrefix(e.URL, "https://") {
			continue
		}
		n := episodeNumber(e.Name, e.Index)
		label := fmt.Sprintf("第%02d集", n)
		file := label + ".mp4"
		if have[n] || busy[file] || seen[file] {
			skipped++
			continue
		}
		seen[file] = true
		eps = append(eps, animeDLEpisode{Label: label, File: file, URL: e.URL})
	}
	if len(eps) == 0 {
		animeDLJSON(w, map[string]any{"ok": true, "queued": 0, "skipped": skipped, "dir": title})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	files := make([]string, len(eps))
	for i := range eps {
		files[i] = eps[i].File
	}
	t := &animeDLTask{
		info: TorrentTask{
			ID: "anime-" + strconv.FormatInt(time.Now().UnixNano(), 36), Name: title, Category: category,
			TargetDir: dir, Status: "running", Stage: "排队中，前面的下完就开始", CreatedAt: time.Now().Unix(),
			AnimeRule: rule.Name, AnimeRef: req.Ref, AnimeFiles: files,
		},
		rule: rule.Name, ref: req.Ref, eps: eps, ctx: ctx, cancel: cancel, queued: true,
	}
	animeDL.mu.Lock()
	animeDL.tasks = append([]*animeDLTask{t}, animeDL.tasks...)
	trimAnimeDLHistory()
	animeDL.mu.Unlock()
	select {
	case animeDL.wake <- struct{}{}:
	default:
	}
	animeDLJSON(w, map[string]any{"ok": true, "id": t.info.ID, "queued": len(eps), "skipped": skipped, "dir": title})
}

func writeAnimeDLError(w http.ResponseWriter, msg string) {
	animeDLJSON(w, map[string]any{"ok": false, "error": msg})
}

// reEpisodeName 集名本身就是集数：第5集 / 第05话 / EP05 / 05 / 5
var reEpisodeName = regexp.MustCompile(`^(?:第\s*)?(?:(?i:ep?)\s*)?0*(\d{1,4})\s*(?:集|话|話)?$`)

// episodeNumber 从集名认集数，认不出（如"前篇""1080P"）就用它在线路里的位置
func episodeNumber(name string, index int) int {
	if m := reEpisodeName.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	if index < 0 {
		index = 0
	}
	return index + 1
}

// existingEpisodes 文件夹里已有视频的集数
func existingEpisodes(dir string) map[int]bool {
	out := map[int]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		if n := parseEpisodeIndex(e.Name()); n > 0 {
			out[n] = true
		}
	}
	return out
}

// downloadingFiles 还没下完的番剧任务里，要往 dir 里放的文件
func downloadingFiles(dir string) map[string]bool {
	out := map[string]bool{}
	animeDL.mu.Lock()
	defer animeDL.mu.Unlock()
	for _, t := range animeDL.tasks {
		if t.info.TargetDir != dir || t.info.Status != "running" {
			continue
		}
		for _, e := range t.eps {
			out[e.File] = true
		}
	}
	return out
}

// trimAnimeDLHistory 已结束的任务只留最近 animeDLKeepHistory 个（调用方持锁）
func trimAnimeDLHistory() {
	ended := 0
	kept := animeDL.tasks[:0]
	for _, t := range animeDL.tasks {
		if t.info.Status != "running" {
			ended++
			if ended > animeDLKeepHistory {
				continue
			}
		}
		kept = append(kept, t)
	}
	animeDL.tasks = kept
}

// ---- 和 BT 任务表合并 ----

// animeTaskList 番剧下载任务（新的在前），并进 ListTasks
func animeTaskList() []TorrentTask {
	animeDL.mu.Lock()
	defer animeDL.mu.Unlock()
	out := make([]TorrentTask, 0, len(animeDL.tasks))
	for _, t := range animeDL.tasks {
		out = append(out, t.info)
	}
	return out
}

// clearFinishedAnimeTasks 清理已结束的番剧任务，正在排队或下载的保留
func clearFinishedAnimeTasks() int {
	animeDL.mu.Lock()
	defer animeDL.mu.Unlock()
	n := 0
	kept := animeDL.tasks[:0]
	for _, t := range animeDL.tasks {
		if t.info.Status == "running" {
			kept = append(kept, t)
		} else {
			n++
		}
	}
	animeDL.tasks = kept
	return n
}

// stopAnimeTask 停止番剧下载任务；不是番剧任务返回 false（交给 BT 那边处理）
func stopAnimeTask(id string) bool {
	animeDL.mu.Lock()
	defer animeDL.mu.Unlock()
	for _, t := range animeDL.tasks {
		if t.info.ID != id {
			continue
		}
		if t.info.Status == "running" {
			t.cancel()
			if t.queued { // 还没开始的直接标停止；在下的由后台协程收尾
				t.info.Status, t.info.Stage, t.info.FinishedAt = "stopped", "", time.Now().Unix()
			}
		}
		return true
	}
	return false
}

// ---- 后台下载 ----

func animeDLWorker() {
	for {
		t := nextAnimeDLTask()
		if t == nil {
			<-animeDL.wake
			continue
		}
		runAnimeDLTask(t)
	}
}

// nextAnimeDLTask 最早排进来、还在等的任务
func nextAnimeDLTask() *animeDLTask {
	animeDL.mu.Lock()
	defer animeDL.mu.Unlock()
	for i := len(animeDL.tasks) - 1; i >= 0; i-- {
		t := animeDL.tasks[i]
		if t.queued && t.info.Status == "running" {
			t.queued = false
			return t
		}
	}
	return nil
}

func (t *animeDLTask) update(f func(i *TorrentTask)) {
	animeDL.mu.Lock()
	f(&t.info)
	animeDL.mu.Unlock()
}

// AnimeActiveEp 正在处理的一集（任务表里给前端画每集进度）
type AnimeActiveEp struct {
	File     string  `json:"file"`
	State    string  `json:"state"` // resolving 找地址 | phone 等手机 | running 下载中 | mergewait 等着拼 | merging 拼接中
	Progress float64 `json:"progress"`
}

// animeDLParallel 同时下几集（和 Kazumi 一样 2 集；每集里再同时拉 3 个分段）。
// 一集在拼接（读写硬盘）时另一集照样在下（吃网络），比一集一集来快不少；再多容易被片源站限流。
const animeDLParallel = 2

var animeDLScanMu sync.Mutex

// animeDLMergeSlot 同一时间只拼一集：拼接主要在读写移动硬盘，两集同时拼反而更慢、整台盒子都跟着卡
// （实测两集一起拼 4 分多钟）；排队的那集等着，另一个工位照样下载下一集
var animeDLMergeSlot = make(chan struct{}, 1)

// animeDLSlot 一个工位正在处理的那集
type animeDLSlot struct {
	idx   int
	ep    animeDLEpisode
	state string
	frac  float64
	bytes int64
}

// animeDLRun 一个任务跑起来以后的汇总状态（受 animeDL.mu 保护）
type animeDLRun struct {
	t         *animeDLTask
	finished  int   // 已结束的集（成功 + 失败）
	bytesBase int64 // 已下好的集的总大小
	active    []*animeDLSlot
	lastBytes int64
	lastAt    time.Time
}

// sync 把各工位的状态汇总进任务表那份（调用方持有 animeDL.mu）
func (r *animeDLRun) sync(x *TorrentTask) {
	total := len(r.t.eps)
	sort.Slice(r.active, func(i, j int) bool { return r.active[i].idx < r.active[j].idx })
	sum, bytes := float64(r.finished), r.bytesBase
	x.AnimeActive = x.AnimeActive[:0]
	var parts []string
	phone, downloading := false, false
	for _, a := range r.active {
		sum += a.frac * 0.98
		bytes += a.bytes
		x.AnimeActive = append(x.AnimeActive, AnimeActiveEp{File: a.ep.File, State: a.state, Progress: a.frac * 100})
		switch a.state {
		case "phone":
			parts, phone = append(parts, "等手机帮忙找"+a.ep.Label+"的视频地址"), true
		case "resolving":
			parts = append(parts, "正在找"+a.ep.Label+"的视频地址")
		case "mergewait":
			parts = append(parts, a.ep.Label+"下好了，等着拼接")
		case "merging":
			parts = append(parts, "正在把"+a.ep.Label+"拼成完整视频")
		default:
			parts = append(parts, fmt.Sprintf("正在下载%s · %d%%", a.ep.Label, int(a.frac*100)))
		}
		if a.state == "running" {
			downloading = true
		}
	}
	x.Progress = sum / float64(total) * 100
	x.BytesDone = bytes
	if len(parts) > 0 {
		x.Stage = strings.Join(parts, "，") + fmt.Sprintf("（已完成 %d/%d 集）", len(x.Files), total)
		if phone {
			x.Stage += "，请打开手机上的 App"
		}
	}
	now := time.Now()
	if !downloading {
		// 找地址、等拼接、拼接都不消耗下载带宽；重算基线，别让最后一次速度一直挂着。
		x.SpeedKBs = 0
		r.lastBytes, r.lastAt = bytes, now
	} else if el := now.Sub(r.lastAt); el >= time.Second {
		if bytes >= r.lastBytes {
			x.SpeedKBs = float64(bytes-r.lastBytes) / 1024 / el.Seconds()
		}
		r.lastBytes, r.lastAt = bytes, now
	}
}

// set 改工位状态并汇总（f 在锁里执行）
func (r *animeDLRun) set(f func()) {
	r.t.update(func(x *TorrentTask) {
		f()
		r.sync(x)
	})
}

type animeMergeJob struct {
	slot  *animeDLSlot
	work  string
	merge func() error
}

func runAnimeDLTask(t *animeDLTask) {
	cfg := currentConfig()
	workRoot := filepath.Join(cfg.CacheDir, "anime-dl", t.info.ID)
	defer os.RemoveAll(workRoot)
	rule := animeDL.api.Rule(t.rule)
	run := &animeDLRun{t: t, lastAt: time.Now()}

	var (
		mu    sync.Mutex // 管 next / fails / okN / stop
		next  int
		fails []string
		okN   int
		stop  bool // 规则没了、硬盘满了：后面的集都不下了
	)
	fail := func(sl *animeDLSlot, msg string) {
		mu.Lock()
		fails = append(fails, sl.ep.Label+"："+msg)
		mu.Unlock()
		run.set(func() {
			run.finished++
			run.active = slices.DeleteFunc(run.active, func(a *animeDLSlot) bool { return a == sl })
			t.info.AnimeFailedFiles = append(t.info.AnimeFailedFiles, sl.ep.File)
		})
	}
	// take 按顺序领下一集；该停了返回 -1
	take := func() int {
		mu.Lock()
		defer mu.Unlock()
		if stop || next >= len(t.eps) || t.ctx.Err() != nil {
			return -1
		}
		if rule == nil {
			fails, stop = append(fails, "片源规则不见了"), true
			return -1
		}
		if free := diskFree(); free >= 0 && free < animeDLMinFree {
			fails, stop = append(fails, "硬盘空间不够了（剩余不到 2GB）"), true
			return -1
		}
		next++
		return next - 1
	}
	// finish 是拼接完成后的统一收尾：入库、清临时文件、更新进度；失败的集不会影响其它集。
	finish := func(sl *animeDLSlot, work string, err error) {
		if err == nil && t.ctx.Err() == nil {
			err = placeFile(filepath.Join(work, "out.mp4"), filepath.Join(t.info.TargetDir, sl.ep.File))
		}
		os.RemoveAll(work)
		if err != nil || t.ctx.Err() != nil {
			if t.ctx.Err() == nil {
				fail(sl, err.Error())
			} else {
				run.set(func() {
					run.finished++
					run.active = slices.DeleteFunc(run.active, func(a *animeDLSlot) bool { return a == sl })
				})
			}
			return
		}
		var size int64
		if st, statErr := os.Stat(filepath.Join(t.info.TargetDir, sl.ep.File)); statErr == nil {
			size = st.Size()
		}
		mu.Lock()
		okN++
		mu.Unlock()
		run.set(func() {
			run.finished++
			run.bytesBase += size
			run.active = slices.DeleteFunc(run.active, func(a *animeDLSlot) bool { return a == sl })
			t.info.Files = append(t.info.Files, sl.ep.File)
		})
		animeDLScanMu.Lock()
		ScanLibrary()
		animeDLScanMu.Unlock()
	}

	// 拼接单独排队，下载工位把下载齐的一集交过来就能领下一集。
	// 队列只留一个等待位置，避免下载远快于硬盘时把整部的分段同时堆在硬盘上。
	merges := make(chan animeMergeJob, 1)
	var mergeWG sync.WaitGroup
	mergeWG.Add(1)
	go func() {
		defer mergeWG.Done()
		for job := range merges {
			err := t.ctx.Err()
			if err == nil {
				err = job.merge()
			}
			finish(job.slot, job.work, err)
		}
	}()

	worker := func() {
		for i := take(); i >= 0; i = take() {
			ep := t.eps[i]
			sl := &animeDLSlot{idx: i, ep: ep, state: "resolving"}
			run.set(func() { run.active = append(run.active, sl) })
			target, kind, err := resolveForDownload(t, ep, func() { run.set(func() { sl.state = "phone" }) })
			if err != nil {
				if t.ctx.Err() == nil {
					fail(sl, err.Error())
				}
				continue
			}
			run.set(func() { sl.state = "running" })
			work := filepath.Join(workRoot, strconv.Itoa(i))
			out := filepath.Join(work, "out.mp4")
			merge, err := dl.Download(t.ctx, dl.Options{
				Fetch: func(ctx context.Context, rawURL, rng string) (*http.Response, error) {
					return animeDL.api.Fetch(ctx, target, rawURL, rng)
				},
				URL: target.URL, Kind: kind, AdBlock: target.AdBlock, WorkDir: work, Out: out, Parallel: 3,
				MergeSlot: animeDLMergeSlot,
				OnProgress: func(p dl.Progress) {
					run.set(func() {
						sl.bytes = p.Bytes
						switch {
						case p.MergeWait:
							sl.state, sl.frac = "mergewait", 1
						case p.Merging:
							sl.state, sl.frac = "merging", 1
						case p.Total > 0:
							sl.frac = float64(p.Done) / float64(p.Total)
						}
					})
				},
			})
			if err != nil {
				os.RemoveAll(work)
				if t.ctx.Err() == nil {
					fail(sl, err.Error())
				}
				continue
			}
			if merge == nil {
				finish(sl, work, nil)
				continue
			}
			// 先写状态再交队列，避免拼得很快时又被“等着拼”覆盖回去。
			run.set(func() { sl.state, sl.frac = "mergewait", 1 })
			select {
			case merges <- animeMergeJob{slot: sl, work: work, merge: merge}:
			case <-t.ctx.Done():
				finish(sl, work, t.ctx.Err())
			}
		}
	}
	var wg sync.WaitGroup
	for range min(animeDLParallel, len(t.eps)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}
	wg.Wait()
	close(merges)
	mergeWG.Wait()

	t.update(func(x *TorrentTask) {
		x.SpeedKBs, x.FinishedAt, x.AnimeActive = 0, time.Now().Unix(), nil
		switch {
		case t.ctx.Err() != nil:
			x.Status, x.Stage = "stopped", ""
		case len(fails) == 0:
			x.Status, x.Stage, x.Progress = "done", "下载完成", 100
		default:
			x.Status, x.Stage = "error", ""
			x.Error = strings.Join(fails, "；")
			if okN > 0 {
				x.Error += fmt.Sprintf("（其余 %d 集已经下好了）", okN)
			}
		}
	})
	if okN > 0 {
		go ensureFolderCover(t.info.TargetDir) // 找封面最多要 45 秒，别拖着下一个任务
	}
}

// resolveForDownload 解析视频地址；要手机帮忙而手机不在线时，隔一会儿再试（onWait 通知界面"等手机"），最多等 animeDLWaitPhone
func resolveForDownload(t *animeDLTask, ep animeDLEpisode, onWait func()) (hls.Target, string, error) {
	rule := animeDL.api.Rule(t.rule)
	deadline := time.Now().Add(animeDLWaitPhone)
	for {
		ctx, cancel := context.WithTimeout(t.ctx, time.Minute)
		target, kind, err := animeDL.api.ResolveTarget(ctx, rule, ep.URL, refererOf(t.ref))
		cancel()
		if !errors.Is(err, resolve.ErrNeedWorker) {
			return target, kind, err
		}
		if time.Now().After(deadline) {
			return target, kind, errors.New("一直等不到手机 App 帮忙解析（这个站要打开手机 App 才能下）")
		}
		onWait()
		select {
		case <-time.After(15 * time.Second):
		case <-t.ctx.Done():
			return target, kind, t.ctx.Err()
		}
	}
}

// refererOf 作品来源是网址才当 Referer（接口模式规则可能只是个编号）
func refererOf(ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	return ""
}

func diskFree() int64 {
	d, err := DiskSpace()
	if err != nil {
		return -1
	}
	if v, ok := d["free"].(int64); ok {
		return v
	}
	return -1
}

// placeFile 把下好的文件挪进片库文件夹（用 BT 入库同一个 moveFile：同盘改名，跨盘复制并校验）
func placeFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return errors.New("文件夹里已经有同名文件了")
	}
	return moveFile(src, dst)
}

func animeDLJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// ensureFolderCover 文件夹还没封面就按片名自动找一张（和片库管理"补封面"同一套）
func ensureFolderCover(dir string) {
	items, _ := LibrarySnapshot("all")
	for _, it := range items {
		if it.Path != dir || it.Cover != "" {
			continue
		}
		if res := fetchCoverFor(it); res.Path != "" {
			ScanLibrary()
		}
		return
	}
}
