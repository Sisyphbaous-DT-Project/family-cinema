package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type MediaConfig struct {
	Enabled     bool   `json:"enabled"`
	FFmpegPath  string `json:"ffmpegPath"`
	FFprobePath string `json:"ffprobePath"`
	StateDir    string `json:"stateDir"`
}

type Subtitle struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Lang   string `json:"lang,omitempty"`
	Title  string `json:"title,omitempty"`
	Forced bool   `json:"forced,omitempty"`
}

type MediaProcStatus struct {
	Enabled bool         `json:"enabled"`
	Reason  string       `json:"reason"`
	Checked int          `json:"checked"`
	Pending int          `json:"pending"`
	Current *TorrentTask `json:"current"`
}

type mediaProcJob struct {
	id       string
	path     string
	size     int64
	modTime  int64
	force    bool
	seq      uint64
	task     TorrentTask
	ctx      context.Context
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	plan     *mediaPlan
	finished bool
	// visible：真要转声音/抽字幕、出了错，或是刚上传的片子，才单独显示成任务；
	// 只是查一查的合并成一行"还剩 N 个"，查完什么都不用做的不留记录。
	visible bool
	probed  bool
	notes   []string
}

type mediaProcManager struct {
	mu          sync.Mutex
	cfg         Config
	opts        MediaConfig
	enabled     bool
	reason      string
	stateDir    string
	statePath   string
	ffmpegPath  string
	ffprobePath string
	state       mediaProcState
	tasks       map[string]*mediaProcJob
	byPath      map[string]string
	queue       []string
	current     *mediaProcJob
	wake        chan struct{}
	stop        chan struct{}
	workerDone  chan struct{}
	seq         uint64
	// checkDone 按分类记这一轮已经查完几个，用来算合并那一行的进度
	checkDone map[string]int
	// dirtyScan 表示有新查到的声音语言还没进片库，等这一轮都查完再统一重扫一次
	dirtyScan bool
	// libraryPaths 是最近一次扫库看到的视频，用来算"已检查 N 个"
	libraryPaths map[string]bool

	minAge       time.Duration
	busyRetry    time.Duration
	streamRetry  time.Duration
	now          func() time.Time
	freeSpace    func(string) (int64, error)
	command      func(context.Context, string, ...string) *exec.Cmd
	probeCommand func(context.Context, string, ...string) *exec.Cmd
	scanLibrary  func()
}

var mediaProcGlobal = &mediaProcManager{}

func newMediaProcManager(cfg Config, opts MediaConfig) *mediaProcManager {
	return &mediaProcManager{
		cfg: cfg, opts: opts,
		state: mediaProcState{Probes: map[string]mediaProbeCache{}, ProgressKeys: map[string]string{}, Attempts: map[string]mediaProcAttempt{}, SubtitleFailures: map[string]mediaProcAttempt{}},
		tasks: map[string]*mediaProcJob{}, byPath: map[string]string{}, checkDone: map[string]int{},
		wake: make(chan struct{}, 1), stop: make(chan struct{}), workerDone: make(chan struct{}),
		minAge: time.Minute, busyRetry: 30 * time.Second, streamRetry: time.Minute,
		now: time.Now, freeSpace: mediaProcFreeSpace,
		command: func(ctx context.Context, tool string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, tool, args...)
		},
		probeCommand: func(ctx context.Context, tool string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, tool, args...)
		},
		scanLibrary: func() { go ScanLibrary() },
	}
}

func initMediaProc(cfg Config) {
	m := newMediaProcManager(cfg, mediaProcReadConfig(cfg))
	mediaProcGlobal = m
	m.start()
}

func mediaProcReadConfig(cfg Config) MediaConfig {
	return cfg.Media
}

func (m *mediaProcManager) start() {
	m.mu.Lock()
	m.stateDir = m.opts.StateDir
	if m.stateDir == "" {
		m.stateDir = filepath.Join(exeDir(), "media-proc")
	}
	m.statePath = filepath.Join(m.stateDir, "state.json")
	m.ffmpegPath = m.opts.FFmpegPath
	m.ffprobePath = m.opts.FFprobePath
	if m.ffmpegPath == "" {
		m.ffmpegPath = filepath.Join(exeDir(), "ffmpeg")
	}
	if m.ffprobePath == "" {
		m.ffprobePath = filepath.Join(exeDir(), "ffprobe")
	}
	m.mu.Unlock()

	// 功能关着也要先读状态、把上次打断时移开的原片放回去；转换过的片子还要靠状态里的对照表接上观看进度
	if err := os.MkdirAll(m.stateDir, 0o755); err != nil {
		m.disable("状态目录不可用: " + err.Error())
		return
	}
	if err := m.loadState(); err != nil {
		log.Printf("媒体处理状态读取失败，将重新建立: %v", err)
	}
	m.recoverState()
	if !m.opts.Enabled {
		m.disable("已在配置中关闭")
		return
	}
	if !mediaProcExecutable(m.ffmpegPath) || !mediaProcExecutable(m.ffprobePath) {
		m.disable("缺少可执行的 ffmpeg 或 ffprobe")
		return
	}
	m.mu.Lock()
	m.enabled = true
	m.reason = ""
	m.mu.Unlock()
	go m.worker()
	log.Printf("媒体处理已启用，状态目录: %s", m.stateDir)
}

func (m *mediaProcManager) disable(reason string) {
	m.mu.Lock()
	m.enabled = false
	m.reason = reason
	m.mu.Unlock()
	log.Printf("媒体处理未启用: %s", reason)
}

func mediaProcExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

func mediaProcScan(items []MediaItem) {
	m := mediaProcGlobal
	paths := map[string]bool{}
	defer func() {
		m.mu.Lock()
		m.libraryPaths = paths
		m.mu.Unlock()
	}()
	for i := range items {
		for j := range items[i].Episodes {
			ep := &items[i].Episodes[j]
			info, err := os.Stat(ep.Path)
			if err != nil || info.IsDir() {
				continue
			}
			paths[ep.Path] = true
			m.mu.Lock()
			progressKey := m.state.ProgressKeys[ep.Path]
			cache, cached := m.state.Probes[ep.Path]
			failed := m.state.Attempts[ep.Path]
			subtitleMeta := m.state.SubtitleMeta[ep.Path]
			m.mu.Unlock()
			if progressKey == "" {
				progressKey = ep.Path
			}
			var probe *mediaProbe
			if cached && cache.Size == info.Size() && cache.ModTime == info.ModTime().UnixNano() {
				probe = &cache.Probe
			}
			subs := mediaScanSubtitles(ep.Path, probe)
			for i := range subs {
				if saved, ok := subtitleMeta[subs[i].Path]; ok {
					subs[i] = saved
				}
			}
			mediaProcSetEpisode(ep, progressKey, mediaFirstAudioLanguage(probe), subs)
			if !m.isEnabled() {
				continue
			}
			needsProbe := probe == nil
			needsExternal := len(m.externalWork(ep.Path, false)) != 0
			if (needsProbe || needsExternal) && !(failed.Size == info.Size() && failed.ModTime == info.ModTime().UnixNano() && (failed.Status == "error" || failed.Status == "stopped")) {
				m.enqueue(ep.Path, false, false)
			}
		}
	}
}

func mediaProcSetEpisode(ep *Episode, progressKey, audioLang string, subtitles []Subtitle) {
	ep.ProgressKey = progressKey
	ep.AudioLang = audioLang
	ep.Subtitles = subtitles
}

func mediaProcSetTaskKind(task *TorrentTask) {
	task.Kind = "media"
}

// mediaProcEnqueue 给上传完的片子用：这一单会单独显示，上传卡片能接着显示检查和转换状态。
func mediaProcEnqueue(path string, force bool) string {
	return mediaProcGlobal.enqueue(path, force, true)
}

// externalWork 列出还要转的外挂字幕；以前转失败、文件也没变过的，平时不再反复尝试。
func (m *mediaProcManager) externalWork(path string, force bool) []mediaSubtitlePlan {
	work := mediaExternalSubtitleWork(path)
	if force || len(work) == 0 {
		return work
	}
	m.mu.Lock()
	failures := make(map[string]mediaProcAttempt, len(work))
	for _, sub := range work {
		if failed, ok := m.state.SubtitleFailures[sub.SourcePath]; ok {
			failures[sub.SourcePath] = failed
		}
	}
	m.mu.Unlock()
	out := work[:0]
	for _, sub := range work {
		if failed, ok := failures[sub.SourcePath]; ok {
			if info, err := os.Stat(sub.SourcePath); err == nil && info.Size() == failed.Size && info.ModTime().UnixNano() == failed.ModTime {
				continue
			}
		}
		out = append(out, sub)
	}
	return out
}

func (m *mediaProcManager) enqueue(path string, force, visible bool) string {
	if !m.isEnabled() {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || !videoExts[strings.ToLower(filepath.Ext(path))] {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if oldID := m.byPath[path]; oldID != "" {
		if old := m.tasks[oldID]; old != nil && old.size == info.Size() && old.modTime == info.ModTime().UnixNano() {
			if old.task.Status == "running" || old.task.Status == "processing" {
				if visible && !old.visible {
					old.visible = true
					m.leaveCheckLocked(old, false)
				}
				return oldID
			}
		}
	}
	if !force {
		if failed := m.state.Attempts[path]; failed.Size == info.Size() && failed.ModTime == info.ModTime().UnixNano() && (failed.Status == "error" || failed.Status == "stopped") {
			return ""
		}
	}
	m.seq++
	id := fmt.Sprintf("media-%d-%d", time.Now().UnixNano(), m.seq)
	ctx, cancel := context.WithCancel(context.Background())
	job := &mediaProcJob{id: id, path: path, size: info.Size(), modTime: info.ModTime().UnixNano(), force: force, seq: m.seq, ctx: ctx, cancel: cancel, visible: visible}
	job.task = TorrentTask{
		ID: id, Name: filepath.Base(path), Category: mediaProcCategory(m.cfg, path), TargetDir: filepath.Dir(path),
		Status: "processing", Stage: "等待处理", BytesTotal: info.Size(), CreatedAt: time.Now().Unix(),
	}
	mediaProcSetTaskKind(&job.task)
	m.tasks[id] = job
	m.byPath[path] = id
	m.queue = append(m.queue, id)
	m.signal()
	return id
}

func mediaProcCategory(cfg Config, path string) string {
	for _, root := range []struct{ path, category string }{{cfg.SeriesRoot, "series"}, {cfg.MovieRoot, "movie"}} {
		if root.path == "" {
			continue
		}
		rel, err := filepath.Rel(root.path, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return root.category
		}
	}
	return "movie"
}

func (m *mediaProcManager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *mediaProcManager) isEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

func mediaProcStatus() MediaProcStatus {
	return mediaProcGlobal.status()
}

func (m *mediaProcManager) status() MediaProcStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := MediaProcStatus{Enabled: m.enabled, Reason: m.reason}
	// 按状态文件里的检查记录算，重启也不会归零
	for path := range m.libraryPaths {
		if _, ok := m.state.Probes[path]; ok {
			status.Checked++
		}
	}
	for _, job := range m.tasks {
		if job.task.Status == "running" || job.task.Status == "processing" {
			status.Pending++
		}
	}
	if m.current != nil && (m.current.task.Status == "running" || m.current.task.Status == "processing") {
		task := m.current.task
		status.Current = &task
	}
	return status
}

func mediaProcCheckAll() error {
	m := mediaProcGlobal
	if !m.isEnabled() {
		m.mu.Lock()
		reason := m.reason
		m.mu.Unlock()
		return fmt.Errorf("媒体处理不可用: %s", reason)
	}
	items, _ := LibrarySnapshot("all")
	for _, item := range items {
		for _, ep := range item.Episodes {
			m.enqueue(ep.Path, true, false)
		}
	}
	return nil
}

func mediaProcTasks() []TorrentTask {
	return mediaProcGlobal.taskList()
}

const mediaCheckTaskPrefix = "media-check-"

func (m *mediaProcManager) taskList() []TorrentTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs := make([]*mediaProcJob, 0, len(m.tasks))
	for _, job := range m.tasks {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].seq > jobs[j].seq })
	out := make([]TorrentTask, 0, len(jobs))
	type checkRow struct {
		pending int
		created int64
		waiting string
	}
	checks := map[string]*checkRow{}
	for _, job := range jobs {
		if job.visible {
			out = append(out, job.task)
			continue
		}
		if job.finished {
			continue
		}
		row := checks[job.task.Category]
		if row == nil {
			row = &checkRow{created: job.task.CreatedAt}
			checks[job.task.Category] = row
		}
		row.pending++
		if job.task.CreatedAt < row.created {
			row.created = job.task.CreatedAt
		}
		// 正在等番剧下完、等文件存稳这类原因，写在合并那一行后面
		if m.current == job && strings.HasPrefix(job.task.Stage, "等") && job.task.Stage != "等待处理" {
			row.waiting = job.task.Stage
		}
	}
	for _, category := range []string{"movie", "series"} {
		row := checks[category]
		if row == nil {
			continue
		}
		done := m.checkDone[category]
		stage := fmt.Sprintf("正在检查声音和字幕，还剩 %d 个", row.pending)
		if row.waiting != "" {
			stage += " · " + row.waiting
		}
		out = append(out, TorrentTask{
			ID: mediaCheckTaskPrefix + category, Kind: "media", Name: "检查片子的声音和字幕", Category: category,
			Status: "processing", Stage: stage, Progress: float64(done) * 100 / float64(done+row.pending), CreatedAt: row.created,
		})
	}
	return out
}

// leaveCheckLocked 在一个没单独显示的检查结束或转成单独任务时调用，更新合并那一行的进度。
func (m *mediaProcManager) leaveCheckLocked(job *mediaProcJob, counted bool) {
	category := job.task.Category
	if counted {
		m.checkDone[category]++
	}
	for _, other := range m.tasks {
		if !other.visible && !other.finished && other.task.Category == category {
			return
		}
	}
	delete(m.checkDone, category)
}

// dropJobLocked 删掉查完什么都不用做（或被停掉）的检查记录，免得任务页留下一长串。
func (m *mediaProcManager) dropJobLocked(job *mediaProcJob) {
	delete(m.tasks, job.id)
	if m.byPath[job.path] == job.id {
		delete(m.byPath, job.path)
	}
}

// afterJobLocked 决定这一单结束后要不要重扫片库：发布了新文件马上扫；
// 只是查到了声音语言，就等这一轮全部查完再扫一次，不再每查一个扫一遍。
func (m *mediaProcManager) afterJobLocked(job *mediaProcJob, published bool) bool {
	if published {
		m.dirtyScan = false
		return true
	}
	if job.probed {
		m.dirtyScan = true
	}
	if !m.dirtyScan {
		return false
	}
	for _, other := range m.tasks {
		if !other.finished {
			return false
		}
	}
	m.dirtyScan = false
	return true
}

func stopMediaProcTask(id string) bool {
	return mediaProcGlobal.stopTask(id)
}

func (m *mediaProcManager) stopTask(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if category, ok := strings.CutPrefix(id, mediaCheckTaskPrefix); ok {
		// 停掉合并那一行 = 这个分类里还没查的都不查了（之后点「检查全部片子」还能再查）
		stopped := false
		for _, job := range m.tasks {
			if job.visible || job.finished || job.task.Category != category {
				continue
			}
			job.cancel()
			stopped = true
			if m.current != job {
				m.finishStoppedLocked(job)
			} else {
				job.task.Stage = "正在停止"
			}
		}
		return stopped
	}
	job := m.tasks[id]
	if job == nil || job.finished || (job.task.Status != "running" && job.task.Status != "processing") {
		return false
	}
	job.cancel()
	if m.current != job {
		m.finishStoppedLocked(job)
	} else {
		job.task.Stage = "正在停止"
	}
	return true
}

func clearFinishedMediaProcTasks() int {
	return mediaProcGlobal.clearFinished()
}

func (m *mediaProcManager) clearFinished() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, job := range m.tasks {
		if job.task.Status != "done" && job.task.Status != "error" && job.task.Status != "stopped" {
			continue
		}
		delete(m.tasks, id)
		if m.byPath[job.path] == id {
			delete(m.byPath, job.path)
		}
		n++
	}
	return n
}

func (m *mediaProcManager) worker() {
	defer close(m.workerDone)
	for {
		select {
		case <-m.stop:
			return
		case <-m.wake:
		}
		for {
			m.mu.Lock()
			var job *mediaProcJob
			for len(m.queue) > 0 {
				id := m.queue[0]
				m.queue = m.queue[1:]
				candidate := m.tasks[id]
				if candidate != nil && !candidate.finished && candidate.ctx.Err() == nil {
					job = candidate
					break
				}
			}
			m.current = job
			m.mu.Unlock()
			if job == nil {
				break
			}
			m.process(job)
			m.mu.Lock()
			m.current = nil
			m.mu.Unlock()
		}
	}
}

func (m *mediaProcManager) close() {
	if !m.isEnabled() {
		return
	}
	close(m.stop)
	m.mu.Lock()
	current := m.current
	m.mu.Unlock()
	if current != nil {
		current.cancel()
	}
	<-m.workerDone
}
