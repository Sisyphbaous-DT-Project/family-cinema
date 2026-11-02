package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errMediaProcStopped = errors.New("媒体处理已停止")

func (m *mediaProcManager) process(job *mediaProcJob) {
	var plan *mediaPlan
	fail := func(err error) {
		if errors.Is(err, errMediaProcStopped) || job.ctx.Err() != nil {
			m.finishStopped(job, plan)
		} else {
			m.finishError(job, plan, err)
		}
	}
	if err := m.waitStable(job); err != nil {
		fail(err)
		return
	}
	if err := m.waitBusy(job); err != nil {
		fail(err)
		return
	}
	probe, err := m.probeForJob(job)
	if err != nil {
		fail(err)
		return
	}
	built, err := buildMediaPlan(job.path, probe)
	if err != nil {
		fail(err)
		return
	}
	built.Subtitles = append(built.Subtitles, m.externalWork(job.path, job.force)...)
	plan = &built
	m.mu.Lock()
	job.plan = plan
	if plan.hasWork() && !job.visible {
		job.visible = true
		m.leaveCheckLocked(job, true)
	}
	m.mu.Unlock()
	if !plan.hasWork() {
		m.finishDone(job, plan, nil, nil)
		return
	}
	if plan.ConvertAudio {
		free, err := m.freeSpace(job.path)
		if err != nil {
			fail(fmt.Errorf("检查硬盘剩余空间失败: %v", err))
			return
		}
		required := job.size + 2<<30
		if free < required {
			fail(fmt.Errorf("硬盘剩余空间不足：需要约 %s（含预留 2 GB），当前只有 %s", humanSize(required), humanSize(free)))
			return
		}
		if !absEqual(plan.SourcePath, plan.VideoTarget) {
			if _, err := os.Stat(plan.VideoTarget); err == nil {
				fail(fmt.Errorf("目标 MP4 已经存在，为避免覆盖，原片已保留"))
				return
			}
		}
	}

	m.mu.Lock()
	stage := "正在提取字幕"
	if plan.ConvertAudio {
		stage = "正在转换声音 0%"
	}
	job.task.Status = "running"
	job.task.Stage = stage
	m.setActiveLocked(job, stage, plan.tempFiles())
	m.mu.Unlock()

	if err := m.runMainFFmpeg(job, plan); err != nil {
		fail(err)
		return
	}
	// 外挂字幕的原文件一直在硬盘上，转不成也不丢东西，所以不连累声音
	externalFailures, err := m.convertExternalSubtitles(job, plan)
	if err != nil {
		fail(err)
		return
	}
	m.dropEmptySubtitles(job, plan)
	if !plan.ConvertAudio && len(plan.Subtitles) == 0 {
		if len(externalFailures) != 0 {
			fail(fmt.Errorf("外挂字幕没能转换（原文件还在）：%s", strings.Join(externalFailures, "；")))
			return
		}
		m.finishDone(job, plan, nil, nil)
		return
	}
	outputProbe, err := m.validateOutputs(job, plan)
	if err != nil {
		fail(err)
		return
	}
	if plan.ConvertAudio {
		if err := m.waitNotStreaming(job); err != nil {
			fail(err)
			return
		}
	}
	published, err := m.publishOutputs(job, plan)
	if err != nil {
		fail(err)
		return
	}
	m.finishDone(job, plan, outputProbe, published)
}

func (m *mediaProcManager) waitStable(job *mediaProcJob) error {
	for {
		info, err := os.Stat(job.path)
		if err != nil {
			return fmt.Errorf("原片不存在或无法读取: %v", err)
		}
		if info.Size() != job.size || info.ModTime().UnixNano() != job.modTime {
			return fmt.Errorf("文件在排队期间发生变化，已保留原片；重新扫描后再处理")
		}
		age := m.now().Sub(info.ModTime())
		if age >= m.minAge {
			return nil
		}
		m.setJobStage(job, "processing", "等文件保存稳定后再处理", 0)
		if err := mediaProcWait(job.ctx, m.minAge-age); err != nil {
			return err
		}
	}
}

func (m *mediaProcManager) waitBusy(job *mediaProcJob) error {
	for {
		reason := mediaProcBusyReason()
		if reason == "" {
			return nil
		}
		m.setJobStage(job, "processing", reason, 0)
		if err := mediaProcWait(job.ctx, m.busyRetry); err != nil {
			return err
		}
	}
}

func (m *mediaProcManager) waitNotStreaming(job *mediaProcJob) error {
	for mediaStreamRecent(job.path, 10*time.Minute) {
		m.setJobStage(job, "processing", "等这部片子没人看了再换", 95)
		if err := mediaProcWait(job.ctx, m.streamRetry); err != nil {
			return err
		}
	}
	return nil
}

func mediaProcWait(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errMediaProcStopped
	case <-timer.C:
		return nil
	}
}

func (m *mediaProcManager) setJobStage(job *mediaProcJob, status, stage string, progress float64) {
	m.mu.Lock()
	job.task.Status = status
	job.task.Stage = stage
	if progress > 0 {
		job.task.Progress = progress
	}
	if m.state.Active != nil && m.state.Active.TaskID == job.id {
		m.state.Active.Stage = stage
		_ = m.saveStateLocked()
	}
	m.mu.Unlock()
}

func (m *mediaProcManager) probeForJob(job *mediaProcJob) (mediaProbe, error) {
	m.mu.Lock()
	cache, ok := m.state.Probes[job.path]
	m.mu.Unlock()
	if !job.force && ok && cache.Size == job.size && cache.ModTime == job.modTime {
		return cache.Probe, nil
	}
	m.setJobStage(job, "processing", "正在检查声音和字幕", 0)
	probe, err := m.probePath(job.ctx, job.path)
	if err != nil {
		return probe, err
	}
	m.mu.Lock()
	job.probed = true
	m.state.Probes[job.path] = mediaProbeCache{Size: job.size, ModTime: job.modTime, Probe: probe}
	delete(m.state.Attempts, job.path)
	_ = m.saveStateLocked()
	m.mu.Unlock()
	return probe, nil
}

// runMainFFmpeg 一次读原片，写出新 MP4、内嵌字幕和要另存的 .mks。
// 转声音时如果是内嵌字幕转不出来，就改成把内嵌字幕全部原样另存再转一次：声音照样修好，字幕一条不丢。
func (m *mediaProcManager) runMainFFmpeg(job *mediaProcJob, plan *mediaPlan) error {
	if !plan.ConvertAudio && !plan.hasInternalSubtitles() && plan.Keep == nil {
		return nil
	}
	err := m.runFFmpeg(job, buildMediaFFmpegArgs(*plan), plan.Duration, plan.ConvertAudio)
	if err == nil || errors.Is(err, errMediaProcStopped) || job.ctx.Err() != nil || !plan.hasInternalSubtitles() {
		return err
	}
	m.cleanupPlan(plan)
	m.mu.Lock()
	if !plan.keepAllSubtitles() {
		m.mu.Unlock()
		return err
	}
	log.Printf("内嵌字幕转换失败（%s），改为原样另存后重试: %v", filepath.Base(job.path), err)
	m.setActiveLocked(job, job.task.Stage, plan.tempFiles())
	m.mu.Unlock()
	return m.runFFmpeg(job, buildMediaFFmpegArgs(*plan), plan.Duration, plan.ConvertAudio)
}

// convertExternalSubtitles 逐个转外挂字幕，转不成的记下来、从计划里拿掉，返回失败说明。
func (m *mediaProcManager) convertExternalSubtitles(job *mediaProcJob, plan *mediaPlan) ([]string, error) {
	var kept []mediaSubtitlePlan
	var failures []string
	for _, sub := range plan.Subtitles {
		if !sub.External {
			kept = append(kept, sub)
			continue
		}
		err := m.convertExternalSubtitle(job, &sub)
		if sub.InputTemp != "" {
			_ = os.Remove(sub.InputTemp)
			sub.InputTemp = ""
		}
		if err != nil && (errors.Is(err, errMediaProcStopped) || job.ctx.Err() != nil) {
			return nil, errMediaProcStopped
		}
		info, statErr := os.Stat(sub.SourcePath)
		m.mu.Lock()
		if err == nil {
			delete(m.state.SubtitleFailures, sub.SourcePath)
		} else if statErr == nil {
			m.state.SubtitleFailures[sub.SourcePath] = mediaProcAttempt{Size: info.Size(), ModTime: info.ModTime().UnixNano(), Status: "error"}
			_ = m.saveStateLocked()
		}
		m.mu.Unlock()
		if err != nil {
			_ = os.Remove(sub.TempPath)
			log.Printf("外挂字幕转换失败（%s）: %v", filepath.Base(sub.SourcePath), err)
			failures = append(failures, fmt.Sprintf("%s（%v）", filepath.Base(sub.SourcePath), err))
			continue
		}
		kept = append(kept, sub)
	}
	m.mu.Lock()
	plan.Subtitles = kept
	if len(failures) != 0 && (plan.ConvertAudio || len(kept) != 0) {
		job.notes = append(job.notes, fmt.Sprintf("%d 条外挂字幕没能转换（原文件还在）", len(failures)))
	}
	m.mu.Unlock()
	return failures, nil
}

func (m *mediaProcManager) convertExternalSubtitle(job *mediaProcJob, sub *mediaSubtitlePlan) error {
	if err := mediaPrepareSubtitleInput(sub); err != nil {
		return err
	}
	if err := m.runFFmpeg(job, buildMediaExternalArgs(sub.SourcePath, *sub), 0, false); err != nil {
		return err
	}
	empty, err := mediaSubtitleOutputEmpty(sub.TempPath)
	if err != nil {
		return fmt.Errorf("没有生成字幕文件")
	}
	if empty {
		return fmt.Errorf("转出来是空的，可能不是字幕文件")
	}
	if sub.Lang == "und" {
		if lang := mediaGuessTextLanguage(sub.TempPath); lang != "" {
			sub.Lang = lang
		}
	}
	return nil
}

// dropEmptySubtitles 拿掉原轨本来就没内容的内嵌字幕（ffmpeg 正常结束但抽出来是空的），不算失败。
func (m *mediaProcManager) dropEmptySubtitles(job *mediaProcJob, plan *mediaPlan) {
	var kept []mediaSubtitlePlan
	empty := 0
	for _, sub := range plan.Subtitles {
		if !sub.External {
			if isEmpty, err := mediaSubtitleOutputEmpty(sub.TempPath); err == nil && isEmpty {
				_ = os.Remove(sub.TempPath)
				empty++
				continue
			}
		}
		kept = append(kept, sub)
	}
	m.mu.Lock()
	plan.Subtitles = kept
	if empty != 0 {
		job.notes = append(job.notes, fmt.Sprintf("%d 个字幕文件本来就是空的，已跳过", empty))
	}
	m.mu.Unlock()
}

func (m *mediaProcManager) probePath(ctx context.Context, path string) (mediaProbe, error) {
	var probe mediaProbe
	args := []string{"-v", "error", "-show_entries", "stream=index,codec_type,codec_name:stream_tags=language,title:stream_disposition=default,forced,attached_pic:format=duration", "-of", "json", path}
	cmd := m.probeCommand(ctx, m.ffprobePath, args...)
	var stdout bytes.Buffer
	stderr := newMediaTailBuffer(40, 64<<10)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return probe, errMediaProcStopped
		}
		log.Printf("ffprobe 失败（%s）:\n%s", filepath.Base(path), stderr.String())
		return probe, fmt.Errorf("读取视频信息失败: %s", mediaProcErrorSummary(stderr.String(), err))
	}
	if err := json.Unmarshal(stdout.Bytes(), &probe); err != nil {
		return probe, fmt.Errorf("视频信息格式不正确: %v", err)
	}
	return probe, nil
}

func (m *mediaProcManager) runFFmpeg(job *mediaProcJob, args []string, duration float64, converting bool) error {
	cmd := m.command(job.ctx, m.ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("启动 ffmpeg 失败: %v", err)
	}
	stderr := newMediaTailBuffer(40, 64<<10)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg 失败: %v", err)
	}
	mediaProcNice(cmd)
	m.mu.Lock()
	job.cmd = cmd
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	m.updateActiveLocked(job.task.Stage, pid)
	m.mu.Unlock()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "out_time_us=") {
			continue
		}
		micros, err := strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64)
		if err != nil || duration <= 0 {
			continue
		}
		progress := float64(micros) / 1e6 / duration * 100
		if progress > 99 {
			progress = 99
		}
		if progress < 0 {
			progress = 0
		}
		stage := "正在提取字幕"
		if converting {
			stage = fmt.Sprintf("正在转换声音 %d%%", int(progress))
		}
		m.mu.Lock()
		job.task.Progress = progress
		job.task.Stage = stage
		m.mu.Unlock()
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	m.mu.Lock()
	job.cmd = nil
	m.updateActiveLocked(job.task.Stage, 0)
	m.mu.Unlock()
	if job.ctx.Err() != nil {
		return errMediaProcStopped
	}
	if scanErr != nil && scanErr != io.EOF {
		return fmt.Errorf("读取 ffmpeg 进度失败: %v", scanErr)
	}
	if waitErr != nil {
		log.Printf("ffmpeg 失败（%s）:\n%s", filepath.Base(job.path), stderr.String())
		action := "提取字幕失败"
		if converting {
			action = "转换声音失败"
		}
		return fmt.Errorf("%s: %s", action, mediaProcErrorSummary(stderr.String(), waitErr))
	}
	return nil
}

func mediaProcErrorSummary(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	message := strings.TrimSpace(strings.Join(lines, "；"))
	if message == "" {
		message = err.Error()
	}
	if len(message) > 500 {
		message = message[len(message)-500:]
	}
	return message
}

type mediaTailBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
	bytes int
}

func newMediaTailBuffer(lines, bytes int) *mediaTailBuffer {
	return &mediaTailBuffer{max: lines, bytes: bytes}
}

func (b *mediaTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := string(p)
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		b.lines = append(b.lines, line)
	}
	if len(b.lines) > b.max {
		b.lines = append([]string(nil), b.lines[len(b.lines)-b.max:]...)
	}
	for len(strings.Join(b.lines, "\n")) > b.bytes && len(b.lines) > 1 {
		b.lines = b.lines[1:]
	}
	return len(p), nil
}

func (b *mediaTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

func (m *mediaProcManager) validateOutputs(job *mediaProcJob, plan *mediaPlan) (*mediaProbe, error) {
	var outputProbe *mediaProbe
	if plan.ConvertAudio {
		info, err := os.Stat(plan.VideoTemp)
		if err != nil || info.Size() == 0 {
			return nil, fmt.Errorf("转换后的 MP4 没有生成或内容为空，原片已保留")
		}
		probe, err := m.probePath(job.ctx, plan.VideoTemp)
		if err != nil {
			return nil, fmt.Errorf("校验转换后的 MP4 失败: %v", err)
		}
		audioCount := 0
		for _, stream := range probe.Streams {
			if stream.CodecType != "audio" {
				continue
			}
			audioCount++
			codec := strings.ToLower(stream.CodecName)
			if codec != "aac" && codec != "mp3" {
				return nil, fmt.Errorf("转换后的 MP4 仍有浏览器不支持的声音（%s），原片已保留", codec)
			}
		}
		if audioCount != len(plan.Audio) {
			return nil, fmt.Errorf("转换后的 MP4 音轨数量不对（原片 %d 条，新片 %d 条），原片已保留", len(plan.Audio), audioCount)
		}
		duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("读不到转换后视频的时长，原片已保留")
		}
		difference := duration - plan.Duration
		if difference < 0 {
			difference = -difference
		}
		if difference > 2 && difference > plan.Duration*0.01 {
			return nil, fmt.Errorf("转换前后时长相差 %.1f 秒，原片已保留", difference)
		}
		outputProbe = &probe
	}
	for _, sub := range plan.Subtitles {
		info, err := os.Stat(sub.TempPath)
		if err != nil || info.Size() == 0 {
			return nil, fmt.Errorf("字幕文件 %s 没有生成或内容为空，原片已保留", filepath.Base(sub.FinalPath))
		}
	}
	if plan.Keep != nil {
		info, err := os.Stat(plan.Keep.TempPath)
		if err != nil || info.Size() == 0 {
			return nil, fmt.Errorf("另存的字幕 %s 没有生成，原片已保留", filepath.Base(plan.Keep.FinalPath))
		}
	}
	if plan.ConvertAudio && !absEqual(plan.SourcePath, plan.VideoTarget) {
		if _, err := os.Stat(plan.VideoTarget); err == nil {
			return nil, fmt.Errorf("目标 MP4 已经存在，为避免覆盖，原片已保留")
		}
	}
	return outputProbe, nil
}

func (m *mediaProcManager) publishOutputs(job *mediaProcJob, plan *mediaPlan) ([]string, error) {
	var published []string
	var old *mediaProcOldFile
	oldMoved := false
	rollback := func() {
		for _, path := range published {
			_ = os.Remove(path)
		}
		// 只有原片确实已经移开，才能删正式位置上的新片再放回原片；没移开时正式位置上就是原片本身
		if old != nil && oldMoved {
			if _, err := os.Stat(old.FinalPath); err == nil {
				_ = os.Remove(old.FinalPath)
			}
			_ = os.Rename(old.OldPath, old.FinalPath)
		}
	}
	if plan.ConvertAudio && absEqual(plan.SourcePath, plan.VideoTarget) {
		old = &mediaProcOldFile{OldPath: plan.SourcePath + ".fcold", FinalPath: plan.SourcePath}
		m.mu.Lock()
		m.addOldFileLocked(old.OldPath, old.FinalPath)
		m.mu.Unlock()
		if err := os.Rename(plan.SourcePath, old.OldPath); err != nil {
			rollback()
			return nil, fmt.Errorf("暂时移开原片失败: %v", err)
		}
		oldMoved = true
	}
	if plan.ConvertAudio {
		if !absEqual(plan.SourcePath, plan.VideoTarget) {
			if _, err := os.Stat(plan.VideoTarget); err == nil {
				rollback()
				return nil, fmt.Errorf("目标 MP4 已经存在，为避免覆盖，原片已保留")
			}
		}
		if err := os.Rename(plan.VideoTemp, plan.VideoTarget); err != nil {
			rollback()
			return nil, fmt.Errorf("发布新 MP4 失败: %v", err)
		}
		published = append(published, plan.VideoTarget)
	}
	reserved := map[string]bool{}
	base := strings.TrimSuffix(plan.SourcePath, filepath.Ext(plan.SourcePath))
	for i := range plan.Subtitles {
		sub := &plan.Subtitles[i]
		if sub.External {
			if _, err := os.Stat(sub.FinalPath); err == nil {
				_ = os.Remove(sub.TempPath)
				continue
			}
		} else {
			sub.FinalPath = availableMediaSubtitlePath(base, sub.StreamIndex, sub.Lang, strings.TrimPrefix(filepath.Ext(sub.FinalPath), "."), reserved)
		}
		reserved[sub.FinalPath] = true
		if err := os.Rename(sub.TempPath, sub.FinalPath); err != nil {
			rollback()
			return nil, fmt.Errorf("发布字幕 %s 失败: %v", filepath.Base(sub.FinalPath), err)
		}
		published = append(published, sub.FinalPath)
	}
	if plan.Keep != nil {
		if _, err := os.Stat(plan.Keep.FinalPath); err == nil {
			plan.Keep.FinalPath = availableMediaKeepPath(base, reserved)
		}
		if err := os.Rename(plan.Keep.TempPath, plan.Keep.FinalPath); err != nil {
			rollback()
			return nil, fmt.Errorf("发布另存的字幕失败: %v", err)
		}
		published = append(published, plan.Keep.FinalPath)
	}
	if plan.ConvertAudio && old == nil {
		if err := os.Remove(plan.SourcePath); err != nil {
			rollback()
			return nil, fmt.Errorf("删除已替换的原片失败: %v", err)
		}
	}
	if old != nil {
		if err := os.Remove(old.OldPath); err != nil {
			rollback()
			return nil, fmt.Errorf("清理原片备份失败: %v", err)
		}
	}
	return published, nil
}

func (m *mediaProcManager) cleanupPlan(plan *mediaPlan) {
	if plan == nil {
		return
	}
	for _, temp := range plan.tempFiles() {
		if strings.HasSuffix(temp, ".fcwork") {
			_ = os.Remove(temp)
		}
	}
}

func (m *mediaProcManager) finishError(job *mediaProcJob, plan *mediaPlan, cause error) {
	m.cleanupPlan(plan)
	m.mu.Lock()
	job.finished = true
	job.cmd = nil
	job.task.Status = "error"
	job.task.Stage = ""
	job.task.Error = cause.Error()
	job.task.FinishedAt = time.Now().Unix()
	m.state.Attempts[job.path] = mediaProcAttempt{Size: job.size, ModTime: job.modTime, Status: "error"}
	m.state.Active = nil
	_ = m.saveStateLocked()
	// 出错的要让人看见，就算本来只是查一查
	if !job.visible {
		job.visible = true
		m.leaveCheckLocked(job, true)
	}
	rescan := m.afterJobLocked(job, false)
	m.mu.Unlock()
	if rescan {
		m.scanLibrary()
	}
}

func (m *mediaProcManager) finishStopped(job *mediaProcJob, plan *mediaPlan) {
	m.cleanupPlan(plan)
	m.mu.Lock()
	m.finishStoppedLocked(job)
	m.state.Active = nil
	_ = m.saveStateLocked()
	rescan := m.afterJobLocked(job, false)
	m.mu.Unlock()
	if rescan {
		m.scanLibrary()
	}
}

func (m *mediaProcManager) finishDone(job *mediaProcJob, plan *mediaPlan, outputProbe *mediaProbe, published []string) {
	m.mu.Lock()
	job.finished = true
	job.cmd = nil
	job.task.Status = "done"
	job.task.Stage = "无需处理"
	if len(plan.Subtitles) != 0 {
		job.task.Stage = "字幕已准备好"
	}
	if plan.ConvertAudio {
		job.task.Stage = "处理完成"
	}
	if plan.Keep != nil && len(published) != 0 {
		job.notes = append(job.notes, plan.Keep.note())
	}
	if len(job.notes) != 0 {
		job.task.Stage += " · " + strings.Join(job.notes, "；")
	}
	job.task.Error = ""
	job.task.Progress = 100
	job.task.BytesDone = job.size
	job.task.FinishedAt = time.Now().Unix()
	job.task.Files = nil
	for _, path := range published {
		job.task.Files = append(job.task.Files, filepath.Base(path))
	}
	delete(m.state.Attempts, job.path)
	if m.state.SubtitleMeta == nil {
		m.state.SubtitleMeta = map[string]map[string]Subtitle{}
	}
	videoPath := plan.SourcePath
	if plan.ConvertAudio {
		videoPath = plan.VideoTarget
	}
	meta := map[string]Subtitle{}
	// 转声音后视频路径变了：原片名下以前记的字幕标题、强制标记也要带过来
	for _, key := range []string{plan.SourcePath, videoPath} {
		for path, saved := range m.state.SubtitleMeta[key] {
			meta[path] = saved
		}
	}
	for _, sub := range plan.Subtitles {
		meta[sub.FinalPath] = Subtitle{Path: sub.FinalPath, Kind: sub.Kind, Lang: sub.Lang, Title: sub.Title, Forced: sub.Forced}
	}
	if videoPath != plan.SourcePath {
		delete(m.state.SubtitleMeta, plan.SourcePath)
	}
	if len(meta) != 0 {
		m.state.SubtitleMeta[videoPath] = meta
	}
	if plan.ConvertAudio {
		progressKey := plan.SourcePath
		if earlier := m.state.ProgressKeys[plan.SourcePath]; earlier != "" {
			progressKey = earlier
		}
		m.state.ProgressKeys[plan.VideoTarget] = progressKey
		delete(m.state.Probes, plan.SourcePath)
		if outputProbe != nil {
			if info, err := os.Stat(plan.VideoTarget); err == nil {
				m.state.Probes[plan.VideoTarget] = mediaProbeCache{Size: info.Size(), ModTime: info.ModTime().UnixNano(), Probe: *outputProbe}
			}
		}
	}
	m.state.Active = nil
	_ = m.saveStateLocked()
	if !job.visible {
		m.dropJobLocked(job)
		m.leaveCheckLocked(job, true)
	}
	rescan := m.afterJobLocked(job, len(published) != 0)
	m.mu.Unlock()
	if rescan {
		m.scanLibrary()
	}
}
