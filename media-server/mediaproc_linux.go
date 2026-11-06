//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func mediaProcFreeSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func mediaProcNice(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
	}
}

func mediaProcPrepare(cmd *exec.Cmd) {}

func mediaProcKillRecorded(pid int, temps []string) error {
	if pid <= 0 || !mediaProcRecordedCommand(pid, temps) {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

func mediaProcRecordedCommand(pid int, temps []string) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	args := bytes.Split(data, []byte{0})
	if len(args) == 0 || !strings.Contains(strings.ToLower(filepath.Base(string(args[0]))), "ffmpeg") {
		return false
	}
	for _, temp := range temps {
		for _, arg := range args[1:] {
			if string(arg) == temp {
				return true
			}
		}
	}
	return false
}
