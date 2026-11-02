package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func newMediaProcRunningManager(t *testing.T, probe mediaProbe) (*mediaProcManager, *atomic.Int32) {
	t.Helper()
	t.Setenv("MEDIA_PROC_TEST_PROBE", mediaProbeJSON(t, probe))
	t.Setenv("MEDIA_PROC_TEST_OUT_PROBE", mediaProbeJSON(t, mediaTestProbe("hevc", []string{"aac"})))
	m := newMediaProcTestManager(t)
	m.command = mediaProcFakeCommand
	m.probeCommand = mediaProcFakeCommand
	var scans atomic.Int32
	m.scanLibrary = func() { scans.Add(1) }
	m.start()
	t.Cleanup(m.close)
	return m, &scans
}

// 99 个片子只查不改时：不刷 99 条任务，合并成一行；查完不留记录，整轮只重扫一次片库
func TestMediaProcQuietChecksDoNotFlood(t *testing.T) {
	resetUploadRegistry(t)
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"a.mp4", "b.mp4", "c.mp4"} {
		path := filepath.Join(dir, name)
		mediaWriteTestFile(t, path, "video "+name)
		paths = append(paths, path)
	}
	// 先挂一个正在进行的上传，让检查排队等着，好看清合并那一行
	rec, _, err := beginUpload(strings.NewReader("x"), "busy.mkv", dir, "movie", "upload-flood-test", "", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, scans := newMediaProcRunningManager(t, mediaTestProbe("h264", []string{"aac"}))
	for _, path := range paths {
		if id := m.enqueue(path, false, false); id == "" {
			t.Fatal("没有入队")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	var rows []TorrentTask
	for {
		rows = m.taskList()
		if len(rows) == 1 && strings.Contains(rows[0].Stage, "等正在上传") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("排队时应只显示一行合并的检查: %+v", rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rows[0].ID != mediaCheckTaskPrefix+"movie" || !strings.Contains(rows[0].Stage, "还剩 3 个") || rows[0].Status != "processing" {
		t.Fatalf("合并行内容不对: %+v", rows[0])
	}
	finishUpload(rec, 0, errUploadStopped, nil)
	deadline = time.Now().Add(5 * time.Second)
	for len(m.taskList()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("查完后不应留下任务记录: %+v", m.taskList())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := scans.Load(); got != 1 {
		t.Fatalf("一整轮检查应只重扫一次片库，实际 %d 次", got)
	}
	m.mu.Lock()
	tasks := len(m.tasks)
	m.libraryPaths = map[string]bool{paths[0]: true, paths[1]: true, paths[2]: true}
	m.mu.Unlock()
	if tasks != 0 {
		t.Fatalf("无需处理的检查不应留在任务表里: %d", tasks)
	}
	if status := m.status(); status.Checked != 3 || status.Pending != 0 {
		t.Fatalf("检查数应按状态记录算: %+v", status)
	}
}

// 停掉合并那一行 = 这一类里还没查的都不查了，也不留一串"已停止"
func TestMediaProcStopQuietChecks(t *testing.T) {
	dir := t.TempDir()
	m := newMediaProcTestManager(t)
	for _, name := range []string{"a.mp4", "b.mp4"} {
		path := filepath.Join(dir, name)
		mediaWriteTestFile(t, path, "video")
		m.enqueue(path, false, false)
	}
	if !m.stopTask(mediaCheckTaskPrefix + "movie") {
		t.Fatal("停止合并行应返回成功")
	}
	if rows := m.taskList(); len(rows) != 0 {
		t.Fatalf("停止后不应留下记录: %+v", rows)
	}
	if m.state.Attempts[filepath.Join(dir, "a.mp4")].Status != "stopped" {
		t.Fatal("停掉的检查平时不应自动再查")
	}
}

// 外挂字幕转不出来：只记一句，声音照样转好、原片照常替换
func TestMediaProcExternalSubtitleFailureKeepsAudio(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	external := filepath.Join(dir, "movie.chs.srt")
	mediaWriteTestFile(t, source, "original")
	mediaWriteTestFile(t, external, "坏掉的字幕")
	t.Setenv("MEDIA_PROC_TEST_FAIL_ON", "movie.chs.vtt.fcwork")
	m, _ := newMediaProcRunningManager(t, mediaTestProbe("hevc", []string{"ac3"}))
	job := waitMediaProcJob(t, m, m.enqueue(source, false, true))
	if job.task.Status != "done" || !strings.Contains(job.task.Stage, "外挂字幕没能转换") {
		t.Fatalf("外挂字幕失败不应拖垮声音转换: %+v", job.task)
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mp4")); err != nil {
		t.Fatal("声音没有转好")
	}
	if data, _ := os.ReadFile(external); string(data) != "坏掉的字幕" {
		t.Fatal("外挂字幕原件应保留")
	}
	if work := m.externalWork(filepath.Join(dir, "movie.mp4"), false); len(work) != 0 {
		t.Fatalf("转失败且没变过的外挂字幕平时不应反复尝试: %+v", work)
	}
	if work := m.externalWork(filepath.Join(dir, "movie.mp4"), true); len(work) != 1 {
		t.Fatal("「检查全部片子」时应再试一次")
	}
}

// 只有外挂字幕要转且失败：任务失败、说清原因，视频不动
func TestMediaProcOnlyExternalFailureReportsError(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mp4")
	mediaWriteTestFile(t, source, "video")
	mediaWriteTestFile(t, filepath.Join(dir, "movie.srt"), "bad")
	t.Setenv("MEDIA_PROC_TEST_FAIL_ON", "movie.vtt.fcwork")
	m, _ := newMediaProcRunningManager(t, mediaTestProbe("h264", []string{"aac"}))
	job := waitMediaProcJob(t, m, m.enqueue(source, false, true))
	if job.task.Status != "error" || !strings.Contains(job.task.Error, "movie.srt") {
		t.Fatalf("应报告哪个字幕没转成: %+v", job.task)
	}
	if data, _ := os.ReadFile(source); string(data) != "video" {
		t.Fatal("视频不应改动")
	}
}

// 一句字幕都没有的 PGS 轨导出来是 0 字节：跳过它，不连累声音
func TestMediaProcEmptyPGSTrackSkipped(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	mediaWriteTestFile(t, source, "original")
	t.Setenv("MEDIA_PROC_TEST_EMPTY_SUP", "1")
	m, _ := newMediaProcRunningManager(t, mediaTestProbe("hevc", []string{"ac3"}, "hdmv_pgs_subtitle"))
	job := waitMediaProcJob(t, m, m.enqueue(source, false, true))
	if job.task.Status != "done" || !strings.Contains(job.task.Stage, "本来就是空的") {
		t.Fatalf("空字幕轨应跳过: %+v", job.task)
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mp4")); err != nil {
		t.Fatal("声音没有转好")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.sup*")); len(matches) != 0 {
		t.Fatalf("空字幕不应发布: %v", matches)
	}
}

// 老 DVD 的图片字幕网页放不了：转声音时原样另存进 .mks，删原片也不丢
func TestMediaProcKeepsUnsupportedSubtitles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "dvd.mkv")
	probe := mediaTestProbe("h264", []string{"ac3"}, "dvd_subtitle", "hdmv_pgs_subtitle")
	plan, err := buildMediaPlan(source, probe)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Keep == nil || len(plan.Keep.Streams) != 1 || plan.Keep.Streams[0].Codec != "dvd_subtitle" {
		t.Fatalf("网页放不了的字幕应安排另存: %+v", plan.Keep)
	}
	args := strings.Join(buildMediaFFmpegArgs(plan), " ")
	if !strings.Contains(args, "-map 0:2 -c copy -max_muxing_queue_size 4096 -f matroska "+plan.Keep.TempPath) {
		t.Fatalf("另存参数不对: %s", args)
	}
	// 不转声音时原片不动，不需要另存
	if keep, _ := buildMediaPlan(source, mediaTestProbe("h264", []string{"aac"}, "dvd_subtitle")); keep.Keep != nil {
		t.Fatal("原片不删时不应另存")
	}

	mediaWriteTestFile(t, source, "original")
	m, _ := newMediaProcRunningManager(t, probe)
	job := waitMediaProcJob(t, m, m.enqueue(source, false, true))
	if job.task.Status != "done" || !strings.Contains(job.task.Stage, "dvd.subs.mks") {
		t.Fatalf("另存任务结果不对: %+v", job.task)
	}
	for _, name := range []string{"dvd.mp4", "dvd.subs.mks", "dvd.s3.zho.sup"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("缺少 %s", name)
		}
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("字幕保全后原片应正常替换")
	}
}

// ASS 带字体的片子：转声音删原片前，ASS 和字体一起另存，别的播放器显示不走样
func TestMediaProcKeepsFontsWithStyledSubtitles(t *testing.T) {
	probe := mediaTestProbe("h264", []string{"dts"}, "ass")
	probe.Streams = append(probe.Streams, mediaProbeStream{Index: 3, CodecType: "attachment", CodecName: "ttf"})
	plan, err := buildMediaPlan(filepath.Join(t.TempDir(), "anime.mkv"), probe)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Keep == nil || len(plan.Keep.Streams) != 2 {
		t.Fatalf("ASS 和字体应一起另存: %+v", plan.Keep)
	}
	if noFonts, _ := buildMediaPlan(filepath.Join(t.TempDir(), "anime.mkv"), mediaTestProbe("h264", []string{"dts"}, "ass")); noFonts.Keep != nil {
		t.Fatal("没有字体时 ASS 已经原样存了 .ass，不用再另存")
	}
}

// 内嵌字幕转不出来：改成原样另存再转一次，声音照样修好
func TestMediaProcFallbackKeepsSubtitlesWhenExtractionFails(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	mediaWriteTestFile(t, source, "original")
	t.Setenv("MEDIA_PROC_TEST_FAIL_ON", "-f webvtt")
	m, _ := newMediaProcRunningManager(t, mediaTestProbe("hevc", []string{"ac3"}, "subrip", "mov_text"))
	job := waitMediaProcJob(t, m, m.enqueue(source, false, true))
	if job.task.Status != "done" || !strings.Contains(job.task.Stage, "已原样另存") {
		t.Fatalf("应改为另存后转好声音: %+v", job.task)
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.subs.mks")); err != nil {
		t.Fatal("内嵌字幕没有另存")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.vtt*")); len(matches) != 0 {
		t.Fatalf("失败那次的临时字幕应清理: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mp4")); err != nil {
		t.Fatal("声音没有转好")
	}
}

func TestMediaProcFallbackConvertsMovTextForMatroska(t *testing.T) {
	plan, err := buildMediaPlan(filepath.Join(t.TempDir(), "phone.mp4"), mediaTestProbe("h264", []string{"ac3"}, "subrip", "mov_text"))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.keepAllSubtitles() || len(plan.Subtitles) != 0 || plan.Keep.Subtitles != 2 {
		t.Fatalf("退路应把内嵌字幕全部改为另存: %+v", plan)
	}
	args := strings.Join(buildMediaFFmpegArgs(plan), " ")
	if !strings.Contains(args, "-c copy -c:s:1 srt") || strings.Contains(args, "-f webvtt") {
		t.Fatalf("MKV 装不下 mov_text，应换成 SRT: %s", args)
	}
}

// 国内常见的 GBK 外挂字幕：先转成 UTF-8 副本再交给 ffmpeg，原文件不动
func TestMediaSubtitleGBKInput(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String("1\n00:00:01,000 --> 00:00:02,000\n你好，世界\n")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.srt")
	mediaWriteTestFile(t, source, gbk)
	sub := mediaSubtitlePlan{SourcePath: source, FinalPath: filepath.Join(dir, "movie.vtt"), TempPath: filepath.Join(dir, "movie.vtt.fcwork"), External: true}
	if err := mediaPrepareSubtitleInput(&sub); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sub.InputTemp)
	if err != nil || !strings.Contains(string(data), "你好，世界") {
		t.Fatalf("GBK 没有转成 UTF-8: %q %v", data, err)
	}
	args := strings.Join(buildMediaExternalArgs(source, sub), " ")
	if !strings.Contains(args, "-f srt -i "+sub.InputTemp) {
		t.Fatalf("应把 UTF-8 副本交给 ffmpeg: %s", args)
	}
	if raw, _ := os.ReadFile(source); string(raw) != gbk {
		t.Fatal("原文件不应改动")
	}
	plan := mediaPlan{Subtitles: []mediaSubtitlePlan{sub}}
	found := false
	for _, temp := range plan.tempFiles() {
		found = found || temp == sub.InputTemp
	}
	if !found {
		t.Fatal("编码副本要登记成临时文件，断电后能清掉")
	}

	utf := mediaSubtitlePlan{SourcePath: filepath.Join(dir, "utf8.srt"), FinalPath: filepath.Join(dir, "utf8.vtt")}
	mediaWriteTestFile(t, utf.SourcePath, "\xef\xbb\xbf1\n00:00:01,000 --> 00:00:02,000\n你好\n")
	if err := mediaPrepareSubtitleInput(&utf); err != nil || utf.InputTemp != "" {
		t.Fatalf("UTF-8 字幕不需要副本: %q %v", utf.InputTemp, err)
	}
}

func TestMediaGuessTextLanguage(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n我们明天一起去公园散步吧，天气很好\n":                            "chi",
		"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nLife was like a box of chocolates, Forrest\n":   "",
		"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n人生就像一盒巧克力\nLife was like a box of chocolates\n": "chi",
		"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nこんにちは、今日はいい天気ですね\n":                             "jpn",
	}
	i := 0
	for text, want := range cases {
		i++
		path := filepath.Join(dir, strings.Repeat("x", i)+".vtt")
		mediaWriteTestFile(t, path, text)
		if got := mediaGuessTextLanguage(path); got != want {
			t.Errorf("%q 认成 %q，应为 %q", text, got, want)
		}
	}
}

// 同目录有 Film 和 Film.Extended：各自的字幕各归各，片名本身也不能被当成语言
func TestMediaSidecarOwnershipAndLanguage(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Film.mp4", "Film.Extended.mp4", "Film.chs.srt", "Film.Extended.chs.srt", "Film.Extended.srt", "Film.s2.eng.vtt", "Film.Extended.s3.chi.sup"} {
		mediaWriteTestFile(t, filepath.Join(dir, name), "x")
	}
	work := mediaExternalSubtitleWork(filepath.Join(dir, "Film.mp4"))
	if len(work) != 1 || filepath.Base(work[0].SourcePath) != "Film.chs.srt" || work[0].Lang != "chs" {
		t.Fatalf("Film 的外挂字幕认错: %+v", work)
	}
	subs := mediaScanSubtitles(filepath.Join(dir, "Film.mp4"), nil)
	if len(subs) != 1 || filepath.Base(subs[0].Path) != "Film.s2.eng.vtt" {
		t.Fatalf("Film 的字幕列表认错: %+v", subs)
	}
	sidecars := sidecarSubtitleFiles(filepath.Join(dir, "Film.mp4"))
	for _, sub := range sidecars {
		if strings.Contains(sub.path, "Extended") {
			t.Fatalf("删改 Film 时不应带走 Film.Extended 的字幕: %s", sub.path)
		}
	}
	if len(sidecars) != 2 {
		t.Fatalf("Film 的旁挂字幕数 = %d", len(sidecars))
	}
	extended := mediaExternalSubtitleWork(filepath.Join(dir, "Film.Extended.mp4"))
	if len(extended) != 2 {
		t.Fatalf("Film.Extended 应有两条外挂字幕: %+v", extended)
	}
	for _, sub := range extended {
		if filepath.Base(sub.SourcePath) == "Film.Extended.srt" && sub.Lang != "und" {
			t.Fatalf("没写语言的外挂字幕应是 und，实际 %q", sub.Lang)
		}
	}

	for _, test := range []struct{ video, name, want string }{
		{"阿甘正传.mkv", "阿甘正传.srt", "und"},
		{"Forrest.Gump.mkv", "Forrest.Gump.srt", "und"},
		{"forrest.gump.mkv", "Forrest.Gump.ENG.forced.srt", "eng"},
		{"movie.mkv", "movie.s4.chi.sup", "chi"},
	} {
		base := strings.TrimSuffix(test.video, filepath.Ext(test.video))
		rest, ok := mediaSidecarRest(base, test.name, []string{base})
		if !ok || mediaSidecarLanguage(rest) != test.want {
			t.Errorf("%s 的语言认成 %q（ok=%v），应为 %q", test.name, mediaSidecarLanguage(rest), ok, test.want)
		}
	}
}

func TestMediaNaturalSubtitleOrder(t *testing.T) {
	dir := t.TempDir()
	mediaWriteTestFile(t, filepath.Join(dir, "m.mkv"), "x")
	for _, name := range []string{"m.s10.chi.sup", "m.s3.chi.sup", "m.s2.eng.sup"} {
		mediaWriteTestFile(t, filepath.Join(dir, name), "x")
	}
	subs := mediaScanSubtitles(filepath.Join(dir, "m.mkv"), nil)
	var got []string
	for _, sub := range subs {
		got = append(got, filepath.Base(sub.Path))
	}
	if strings.Join(got, ",") != "m.s2.eng.sup,m.s3.chi.sup,m.s10.chi.sup" {
		t.Fatalf("字幕顺序 = %v", got)
	}
}

// 同名 MP4 替换时，原片挪开那一步就失败：回滚绝不能把还在原位的原片删掉
func TestMediaProcRollbackWhenMoveFails(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mp4")
	mediaWriteTestFile(t, source, "original")
	if err := os.MkdirAll(filepath.Join(source+".fcold", "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := mediaPlan{SourcePath: source, ConvertAudio: true, VideoTarget: source, VideoTemp: source + ".fcwork"}
	mediaWriteTestFile(t, plan.VideoTemp, "replacement")
	m := newMediaProcTestManager(t)
	if _, err := m.publishOutputs(newMediaProcTestJob(source), &plan); err == nil {
		t.Fatal("挪不开原片时应失败")
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "original" {
		t.Fatalf("原片丢了: %q %v", data, err)
	}
}

// 配置里关掉媒体处理时，也要先把上次打断时移开的原片放回去，进度对照表照常读出来
func TestMediaProcDisabledStillRecovers(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mp4")
	old := source + ".fcold"
	mediaWriteTestFile(t, old, "original")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := mediaProcState{ProgressKeys: map[string]string{filepath.Join(dir, "other.mp4"): filepath.Join(dir, "other.mkv")}, Active: &mediaProcActive{
		TaskID: "media-off", SourcePath: source, OldFiles: []mediaProcOldFile{{OldPath: old, FinalPath: source}},
	}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMediaProcManager(Config{MovieRoot: dir}, MediaConfig{Enabled: false, StateDir: stateDir})
	m.start()
	if data, _ := os.ReadFile(source); string(data) != "original" {
		t.Fatal("功能关着也应放回原片")
	}
	if m.isEnabled() || m.state.ProgressKeys[filepath.Join(dir, "other.mp4")] == "" {
		t.Fatal("应保持关闭，但要读出进度对照表")
	}
}

// 上次解压到一半断电留下的 .extracting 不能挡住重新解压
func TestExtractLeftoverTempIsReplaced(t *testing.T) {
	dir := t.TempDir()
	mediaWriteTestFile(t, filepath.Join(dir, "movie.mkv.extracting"), "half")
	var res extractResult
	if err := writeExtracted(strings.NewReader("whole"), dir, "movie.mkv", &res); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "movie.mkv")); string(data) != "whole" {
		t.Fatal("没有重新解压出完整文件")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mkv.extracting")); !os.IsNotExist(err) {
		t.Fatal("临时文件应已改名")
	}
}
