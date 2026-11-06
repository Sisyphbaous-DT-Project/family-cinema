//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsDiskSpaceAndMediaSpace(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	dir := t.TempDir()
	cfg = defaultConfig()
	cfg.MovieRoot = dir
	disk, err := DiskSpace()
	if err != nil {
		t.Fatal(err)
	}
	if disk["total"].(int64) <= 0 || disk["free"].(int64) <= 0 || disk["free"].(int64) > disk["total"].(int64) {
		t.Fatalf("invalid disk space: %#v", disk)
	}
	free, err := mediaProcFreeSpace(filepath.Join(dir, "测试影片.mkv"))
	if err != nil || free <= 0 {
		t.Fatalf("media free space = %d, err=%v", free, err)
	}
}

func TestWindowsMediaProcessHelper(t *testing.T) {
	if os.Getenv("WINDOWS_MEDIA_PROCESS_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestWindowsMediaProcessOwnershipAndRecovery(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	dir := t.TempDir()
	exe := filepath.Join(dir, "ffmpeg.exe")
	bytes, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, "测试输出.mp4.fcwork")
	cmd := exec.Command(exe, "-test.run=^TestWindowsMediaProcessHelper$", "--", temp)
	cmd.Env = append(os.Environ(), "WINDOWS_MEDIA_PROCESS_HELPER=1")
	mediaProcPrepare(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("media tool console window was not hidden")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	cfg = defaultConfig()
	cfg.Media.FFmpegPath = exe
	if !mediaProcRecordedCommand(cmd.Process.Pid, []string{temp}) {
		t.Fatal("recorded process not recognized by image path and exact temporary output")
	}
	if mediaProcRecordedCommand(cmd.Process.Pid, []string{filepath.Join(dir, "other.fcwork")}) {
		t.Fatal("unrelated temporary output recognized")
	}
	if err := mediaProcKillRecorded(cmd.Process.Pid, []string{filepath.Join(dir, "other.fcwork")}); err != nil {
		t.Fatal(err)
	}
	if !mediaProcRecordedCommand(cmd.Process.Pid, []string{temp}) {
		t.Fatal("unrelated process was terminated")
	}
	if err := mediaProcKillRecorded(cmd.Process.Pid, []string{temp}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("recorded process was not stopped")
	}
	if mediaProcRecordedCommand(os.Getpid(), []string{temp}) {
		t.Fatal("current test process must not be recognized as ffmpeg")
	}
}
