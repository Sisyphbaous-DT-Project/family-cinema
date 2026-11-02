package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type mediaProcAttempt struct {
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	Status  string `json:"status"`
}

type mediaProcOldFile struct {
	OldPath   string `json:"oldPath"`
	FinalPath string `json:"finalPath"`
}

type mediaProcActive struct {
	TaskID     string             `json:"taskId"`
	SourcePath string             `json:"sourcePath"`
	Size       int64              `json:"size"`
	ModTime    int64              `json:"modTime"`
	TempFiles  []string           `json:"tempFiles"`
	FFmpegPID  int                `json:"ffmpegPid"`
	Stage      string             `json:"stage"`
	OldFiles   []mediaProcOldFile `json:"oldFiles,omitempty"`
}

type mediaProcState struct {
	Probes       map[string]mediaProbeCache     `json:"probes"`
	ProgressKeys map[string]string              `json:"progressKeys"`
	Attempts     map[string]mediaProcAttempt    `json:"attempts"`
	SubtitleMeta map[string]map[string]Subtitle `json:"subtitleMeta,omitempty"`
	// SubtitleFailures 记住转不成功的外挂字幕（按大小和修改时间），文件不变就不反复尝试
	SubtitleFailures map[string]mediaProcAttempt `json:"subtitleFailures,omitempty"`
	Active           *mediaProcActive            `json:"active,omitempty"`
}

func (m *mediaProcManager) loadState() error {
	data, err := os.ReadFile(m.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state mediaProcState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Probes == nil {
		state.Probes = map[string]mediaProbeCache{}
	}
	if state.ProgressKeys == nil {
		state.ProgressKeys = map[string]string{}
	}
	if state.Attempts == nil {
		state.Attempts = map[string]mediaProcAttempt{}
	}
	if state.SubtitleMeta == nil {
		state.SubtitleMeta = map[string]map[string]Subtitle{}
	}
	if state.SubtitleFailures == nil {
		state.SubtitleFailures = map[string]mediaProcAttempt{}
	}
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
	return nil
}

func (m *mediaProcManager) saveStateLocked() error {
	if m.statePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.stateDir, 0o755); err != nil {
		return err
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.statePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (m *mediaProcManager) setActiveLocked(job *mediaProcJob, stage string, temps []string) {
	m.state.Active = &mediaProcActive{
		TaskID: job.id, SourcePath: job.path, Size: job.size, ModTime: job.modTime,
		TempFiles: append([]string(nil), temps...), Stage: stage,
	}
	_ = m.saveStateLocked()
}

func (m *mediaProcManager) updateActiveLocked(stage string, pid int) {
	if m.state.Active == nil {
		return
	}
	m.state.Active.Stage = stage
	m.state.Active.FFmpegPID = pid
	_ = m.saveStateLocked()
}

func (m *mediaProcManager) addOldFileLocked(oldPath, finalPath string) {
	if m.state.Active == nil {
		return
	}
	m.state.Active.OldFiles = append(m.state.Active.OldFiles, mediaProcOldFile{OldPath: oldPath, FinalPath: finalPath})
	_ = m.saveStateLocked()
}

func (m *mediaProcManager) clearActiveLocked() {
	m.state.Active = nil
	_ = m.saveStateLocked()
}

func (m *mediaProcManager) recoverState() {
	m.mu.Lock()
	active := m.state.Active
	m.mu.Unlock()
	if active == nil {
		return
	}
	if active.FFmpegPID > 0 {
		_ = mediaProcKillRecorded(active.FFmpegPID, active.TempFiles)
	}
	for _, old := range active.OldFiles {
		if !strings.HasSuffix(old.OldPath, ".fcold") {
			continue
		}
		if _, err := os.Stat(old.OldPath); err != nil {
			continue
		}
		if _, err := os.Stat(old.FinalPath); os.IsNotExist(err) {
			_ = os.Rename(old.OldPath, old.FinalPath)
		}
	}
	for _, temp := range active.TempFiles {
		if strings.HasSuffix(temp, ".fcwork") {
			_ = os.Remove(temp)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	id := active.TaskID
	if id == "" {
		m.seq++
		id = fmt.Sprintf("media-recovered-%d", m.seq)
	}
	job := &mediaProcJob{id: id, path: active.SourcePath, size: active.Size, modTime: active.ModTime, finished: true, visible: true}
	job.task = TorrentTask{
		ID: id, Name: filepath.Base(active.SourcePath), Category: mediaProcCategory(m.cfg, active.SourcePath),
		TargetDir: filepath.Dir(active.SourcePath), Status: "error", Stage: "",
		Error: "盒子重启打断了，点「检查全部片子」可以重新转", CreatedAt: time.Now().Unix(), FinishedAt: time.Now().Unix(),
	}
	mediaProcSetTaskKind(&job.task)
	m.tasks[id] = job
	m.byPath[active.SourcePath] = id
	m.state.Attempts[active.SourcePath] = mediaProcAttempt{Size: active.Size, ModTime: active.ModTime, Status: "error"}
	m.state.Active = nil
	_ = m.saveStateLocked()
}

func (m *mediaProcManager) finishStoppedLocked(job *mediaProcJob) {
	job.finished = true
	job.task.Status = "stopped"
	job.task.Stage = ""
	job.task.Error = ""
	job.task.FinishedAt = time.Now().Unix()
	m.state.Attempts[job.path] = mediaProcAttempt{Size: job.size, ModTime: job.modTime, Status: "stopped"}
	_ = m.saveStateLocked()
	if !job.visible {
		m.dropJobLocked(job)
		m.leaveCheckLocked(job, true)
	}
}
