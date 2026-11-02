package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediaProcHelperProcess(t *testing.T) {
	if os.Getenv("MEDIA_PROC_TEST_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	if separator == 0 || separator >= len(os.Args) {
		os.Exit(2)
	}
	args := os.Args[separator+1:]
	isProbe := false
	for _, arg := range args {
		if arg == "-show_entries" {
			isProbe = true
		}
	}
	if isProbe {
		input := args[len(args)-1]
		content := os.Getenv("MEDIA_PROC_TEST_PROBE")
		if strings.HasSuffix(input, ".fcwork") {
			content = os.Getenv("MEDIA_PROC_TEST_OUT_PROBE")
		}
		_, _ = fmt.Fprint(os.Stdout, content)
		os.Exit(0)
	}
	if os.Getenv("MEDIA_PROC_TEST_FFMPEG_FAIL") == "1" {
		_, _ = fmt.Fprintln(os.Stderr, "fake ffmpeg failed")
		os.Exit(1)
	}
	// 参数里含这段文字就失败，用来模拟某一条字幕转不出来
	if failOn := os.Getenv("MEDIA_PROC_TEST_FAIL_ON"); failOn != "" && strings.Contains(strings.Join(args, " "), failOn) {
		_, _ = fmt.Fprintln(os.Stderr, "fake ffmpeg failed on "+failOn)
		os.Exit(1)
	}
	for i, arg := range args {
		if !strings.HasSuffix(arg, ".fcwork") || (i > 0 && args[i-1] == "-i") {
			continue
		}
		content := "fake output"
		switch filepath.Ext(strings.TrimSuffix(arg, ".fcwork")) {
		case ".vtt":
			content = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n字幕\n"
		case ".ass":
			content = "[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,字幕\n"
		case ".sup":
			if os.Getenv("MEDIA_PROC_TEST_EMPTY_SUP") == "1" {
				content = ""
			}
		}
		if err := os.WriteFile(arg, []byte(content), 0o644); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	_, _ = fmt.Fprintln(os.Stdout, "out_time_us=50000000")
	_, _ = fmt.Fprintln(os.Stdout, "out_time_us=100000000")
	os.Exit(0)
}

func mediaProcFakeCommand(ctx context.Context, tool string, args ...string) *exec.Cmd {
	all := []string{"-test.run=TestMediaProcHelperProcess", "--", tool}
	all = append(all, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], all...)
	cmd.Env = append(os.Environ(), "MEDIA_PROC_TEST_HELPER=1")
	return cmd
}

func newMediaProcTestManager(t *testing.T) *mediaProcManager {
	t.Helper()
	dir := t.TempDir()
	m := newMediaProcManager(Config{MovieRoot: dir, SeriesRoot: filepath.Join(dir, "series")}, MediaConfig{Enabled: true, FFmpegPath: os.Args[0], FFprobePath: os.Args[0], StateDir: filepath.Join(dir, "state")})
	m.enabled = true
	m.stateDir = m.opts.StateDir
	m.statePath = filepath.Join(m.stateDir, "state.json")
	m.minAge = 0
	m.busyRetry = 5 * time.Millisecond
	m.streamRetry = 5 * time.Millisecond
	m.freeSpace = func(string) (int64, error) { return 1 << 62, nil }
	m.scanLibrary = func() {}
	return m
}

func newMediaProcTestJob(path string) *mediaProcJob {
	info, _ := os.Stat(path)
	ctx, cancel := context.WithCancel(context.Background())
	job := &mediaProcJob{id: "media-test", path: path, ctx: ctx, cancel: cancel}
	if info != nil {
		job.size = info.Size()
		job.modTime = info.ModTime().UnixNano()
	}
	job.task = TorrentTask{ID: job.id, Name: filepath.Base(path), Status: "running", Kind: "media"}
	return job
}

func mediaProbeJSON(t *testing.T, probe mediaProbe) string {
	t.Helper()
	data, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMediaProcPublishMKVAndReplaceMP4(t *testing.T) {
	t.Run("MKV 转 MP4", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "movie.mkv")
		mediaWriteTestFile(t, source, "original")
		plan := mediaPlan{SourcePath: source, ConvertAudio: true, VideoTarget: filepath.Join(dir, "movie.mp4"), VideoTemp: filepath.Join(dir, "movie.mp4.fcwork")}
		plan.Subtitles = []mediaSubtitlePlan{{StreamIndex: 2, Lang: "zho", FinalPath: filepath.Join(dir, "movie.s2.zho.sup"), TempPath: filepath.Join(dir, "movie.s2.zho.sup.fcwork")}}
		mediaWriteTestFile(t, plan.VideoTemp, "new video")
		mediaWriteTestFile(t, plan.Subtitles[0].TempPath, "subtitle")
		m := newMediaProcTestManager(t)
		published, err := m.publishOutputs(newMediaProcTestJob(source), &plan)
		if err != nil {
			t.Fatal(err)
		}
		if len(published) != 2 {
			t.Fatalf("发布文件数 = %d", len(published))
		}
		if _, err := os.Stat(source); !os.IsNotExist(err) {
			t.Fatal("MKV 成功后原片应删除")
		}
		if data, _ := os.ReadFile(plan.VideoTarget); string(data) != "new video" {
			t.Fatal("新 MP4 内容不对")
		}
		if _, err := os.Stat(plan.Subtitles[0].FinalPath); err != nil {
			t.Fatal("字幕没有发布")
		}
	})

	t.Run("MP4 同名替换", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "movie.mp4")
		mediaWriteTestFile(t, source, "original")
		plan := mediaPlan{SourcePath: source, ConvertAudio: true, VideoTarget: source, VideoTemp: source + ".fcwork"}
		mediaWriteTestFile(t, plan.VideoTemp, "replacement")
		m := newMediaProcTestManager(t)
		job := newMediaProcTestJob(source)
		m.mu.Lock()
		m.setActiveLocked(job, "正在转换声音 99%", plan.tempFiles())
		m.mu.Unlock()
		if _, err := m.publishOutputs(job, &plan); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(source); string(data) != "replacement" {
			t.Fatal("同名 MP4 没有换成新内容")
		}
		if _, err := os.Stat(source + ".fcold"); !os.IsNotExist(err) {
			t.Fatal("成功后不应留下 .fcold")
		}
	})
}

func TestMediaProcPublishCollisionKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	target := filepath.Join(dir, "movie.mp4")
	mediaWriteTestFile(t, source, "original")
	mediaWriteTestFile(t, target, "do not overwrite")
	plan := mediaPlan{SourcePath: source, ConvertAudio: true, VideoTarget: target, VideoTemp: target + ".fcwork"}
	mediaWriteTestFile(t, plan.VideoTemp, "new")
	m := newMediaProcTestManager(t)
	if _, err := m.publishOutputs(newMediaProcTestJob(source), &plan); err == nil {
		t.Fatal("目标已存在必须失败")
	}
	m.cleanupPlan(&plan)
	if data, _ := os.ReadFile(source); string(data) != "original" {
		t.Fatal("失败后原片被改动")
	}
	if data, _ := os.ReadFile(target); string(data) != "do not overwrite" {
		t.Fatal("已有 MP4 被覆盖")
	}
	if _, err := os.Stat(plan.VideoTemp); !os.IsNotExist(err) {
		t.Fatal("失败后临时文件未清理")
	}
}

func TestMediaProcValidationFailuresKeepOriginal(t *testing.T) {
	tests := []struct {
		name         string
		outputLength string
		missingSub   bool
	}{
		{name: "时长不对", outputLength: "120.0"},
		{name: "字幕缺失", outputLength: "100.0", missingSub: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "movie.mkv")
			mediaWriteTestFile(t, source, "original")
			outProbe := mediaTestProbe("hevc", []string{"aac"})
			outProbe.Format.Duration = test.outputLength
			t.Setenv("MEDIA_PROC_TEST_OUT_PROBE", mediaProbeJSON(t, outProbe))
			m := newMediaProcTestManager(t)
			m.ffprobePath = os.Args[0]
			m.probeCommand = mediaProcFakeCommand
			plan := mediaPlan{SourcePath: source, Probe: mediaTestProbe("hevc", []string{"ac3"}), Duration: 100, ConvertAudio: true,
				Audio: []mediaAudioPlan{{StreamIndex: 1}}, VideoTarget: filepath.Join(dir, "movie.mp4"), VideoTemp: filepath.Join(dir, "movie.mp4.fcwork")}
			mediaWriteTestFile(t, plan.VideoTemp, "new")
			if !test.missingSub {
				plan.Subtitles = []mediaSubtitlePlan{{FinalPath: filepath.Join(dir, "movie.s2.zho.sup"), TempPath: filepath.Join(dir, "movie.s2.zho.sup.fcwork")}}
				mediaWriteTestFile(t, plan.Subtitles[0].TempPath, "subtitle")
			} else {
				plan.Subtitles = []mediaSubtitlePlan{{FinalPath: filepath.Join(dir, "missing.sup"), TempPath: filepath.Join(dir, "missing.sup.fcwork")}}
			}
			job := newMediaProcTestJob(source)
			if _, err := m.validateOutputs(job, &plan); err == nil {
				t.Fatal("校验应当失败")
			} else {
				m.finishError(job, &plan, err)
			}
			if data, _ := os.ReadFile(source); string(data) != "original" {
				t.Fatal("校验失败后原片被改动")
			}
			for _, temp := range plan.tempFiles() {
				if _, err := os.Stat(temp); !os.IsNotExist(err) {
					t.Fatalf("临时文件未清理: %s", temp)
				}
			}
			if job.task.Status != "error" || job.task.Error == "" {
				t.Fatalf("失败任务没有原因: %+v", job.task)
			}
		})
	}
}

func TestMediaProcQueueEndToEnd(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	mediaWriteTestFile(t, source, "original")
	sourceProbe := mediaTestProbe("hevc", []string{"ac3"}, "hdmv_pgs_subtitle")
	outProbe := mediaTestProbe("hevc", []string{"aac"})
	t.Setenv("MEDIA_PROC_TEST_PROBE", mediaProbeJSON(t, sourceProbe))
	t.Setenv("MEDIA_PROC_TEST_OUT_PROBE", mediaProbeJSON(t, outProbe))
	m := newMediaProcTestManager(t)
	m.ffmpegPath = os.Args[0]
	m.ffprobePath = os.Args[0]
	m.command = mediaProcFakeCommand
	m.probeCommand = mediaProcFakeCommand
	var scans atomic.Int32
	m.scanLibrary = func() { scans.Add(1) }
	m.start()
	defer m.close()
	id := m.enqueue(source, false, true)
	if !strings.HasPrefix(id, "media-") {
		t.Fatalf("任务 ID = %q", id)
	}
	job := waitMediaProcJob(t, m, id)
	if job.task.Status != "done" {
		t.Fatalf("任务失败: %+v", job.task)
	}
	if job.task.Kind != "media" || job.task.Progress != 100 {
		t.Fatalf("任务字段不对: %+v", job.task)
	}
	target := filepath.Join(dir, "movie.mp4")
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("转换成功后 MKV 应删除")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("新 MP4 不存在")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.s2.zho.sup")); err != nil {
		t.Fatal("内嵌 PGS 没有抽成 SUP")
	}
	m.mu.Lock()
	progressKey := m.state.ProgressKeys[target]
	_, cached := m.state.Probes[target]
	subtitle := m.state.SubtitleMeta[target][filepath.Join(dir, "movie.s2.zho.sup")]
	m.mu.Unlock()
	if progressKey != source || !cached || scans.Load() == 0 {
		t.Fatalf("状态未衔接: progressKey=%q cached=%v scans=%d", progressKey, cached, scans.Load())
	}
	if subtitle.Title != "中文字幕" || subtitle.Lang != "zho" || subtitle.Kind != "pgs" {
		t.Fatalf("转换后的字幕信息没有保留: %+v", subtitle)
	}
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved mediaProcState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ProgressKeys[target] != source || saved.Active != nil {
		t.Fatalf("落盘状态不对: %+v", saved)
	}
}

func TestMediaProcSubtitleOnlyKeepsVideo(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	external := filepath.Join(dir, "movie.chs.ass")
	mediaWriteTestFile(t, source, "original video")
	mediaWriteTestFile(t, external, "original subtitle")
	t.Setenv("MEDIA_PROC_TEST_PROBE", mediaProbeJSON(t, mediaTestProbe("h264", []string{"aac"}, "subrip")))
	m := newMediaProcTestManager(t)
	m.command = mediaProcFakeCommand
	m.probeCommand = mediaProcFakeCommand
	m.start()
	defer m.close()
	id := m.enqueue(source, false, true)
	job := waitMediaProcJob(t, m, id)
	if job.task.Status != "done" {
		t.Fatalf("字幕任务失败: %+v", job.task)
	}
	if data, _ := os.ReadFile(source); string(data) != "original video" {
		t.Fatal("只抽字幕不应改动视频")
	}
	if data, _ := os.ReadFile(external); string(data) != "original subtitle" {
		t.Fatal("外挂 ASS 原件应保留")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mp4")); !os.IsNotExist(err) {
		t.Fatal("AAC 视频不应生成新 MP4")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.s2.zho.vtt")); err != nil {
		t.Fatal("内嵌文字字幕没有转成 VTT")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.chs.vtt")); err != nil {
		t.Fatal("外挂 ASS 没有另转 VTT")
	}
}

func TestMediaProcRecoverWorkAndOldFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mp4")
	old := source + ".fcold"
	work := source + ".fcwork"
	unrelated := filepath.Join(dir, "unrelated.fcwork")
	mediaWriteTestFile(t, old, "original")
	mediaWriteTestFile(t, work, "partial")
	mediaWriteTestFile(t, unrelated, "keep")
	m := newMediaProcTestManager(t)
	m.ffmpegPath = os.Args[0]
	m.ffprobePath = os.Args[0]
	if err := os.MkdirAll(m.stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := mediaProcState{Probes: map[string]mediaProbeCache{}, ProgressKeys: map[string]string{}, Attempts: map[string]mediaProcAttempt{}, Active: &mediaProcActive{
		TaskID: "media-recover", SourcePath: source, Size: 8, ModTime: 1, TempFiles: []string{work}, Stage: "正在转换声音 40%",
		OldFiles: []mediaProcOldFile{{OldPath: old, FinalPath: source}},
	}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(m.statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	m.start()
	defer m.close()
	if data, _ := os.ReadFile(source); string(data) != "original" {
		t.Fatal(".fcold 没有恢复原片")
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatal("登记的 .fcwork 没有清理")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("未登记文件不应删除")
	}
	job := m.tasks["media-recover"]
	if job == nil || job.task.Status != "error" || !strings.Contains(job.task.Error, "检查全部片子") {
		t.Fatalf("恢复任务状态不对: %+v", job)
	}
}

func TestMediaProcRetryStopAndClear(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mp4")
	mediaWriteTestFile(t, source, "video")
	m := newMediaProcTestManager(t)
	first := m.enqueue(source, false, true)
	if again := m.enqueue(source, false, true); again != first {
		t.Fatal("同一路径应去重")
	}
	job := m.tasks[first]
	job.finished = true
	job.task.Status = "error"
	m.state.Attempts[source] = mediaProcAttempt{Size: job.size, ModTime: job.modTime, Status: "error"}
	if got := m.enqueue(source, false, true); got != "" {
		t.Fatal("失败任务平时不应自动重试")
	}
	second := m.enqueue(source, true, true)
	if second == "" || second == first {
		t.Fatal("强制检查应允许重新入队")
	}
	if !m.stopTask(second) || m.stopTask(second) {
		t.Fatal("停止接口返回值不对")
	}
	if got := m.clearFinished(); got != 2 {
		t.Fatalf("清理完成任务数 = %d", got)
	}
}

func TestMediaProcDonePathAllowsNewSubtitleWork(t *testing.T) {
	source := filepath.Join(t.TempDir(), "movie.mkv")
	mediaWriteTestFile(t, source, "video")
	m := newMediaProcTestManager(t)
	first := m.enqueue(source, false, true)
	job := m.tasks[first]
	job.finished = true
	job.task.Status = "done"
	second := m.enqueue(source, false, true)
	if second == "" || second == first {
		t.Fatal("已完成不能挡住后来新增的外挂字幕")
	}
}

func waitMediaProcJob(t *testing.T, m *mediaProcManager, id string) *mediaProcJob {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		job := m.tasks[id]
		if job != nil && job.finished {
			m.mu.Unlock()
			return job
		}
		m.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待媒体处理任务超时")
	return nil
}
