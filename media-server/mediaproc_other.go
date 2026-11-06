//go:build !linux && !windows

package main

import (
	"fmt"
	"os/exec"
)

func mediaProcFreeSpace(path string) (int64, error) {
	return 0, fmt.Errorf("当前平台不支持按目录检查剩余空间")
}

func mediaProcNice(cmd *exec.Cmd) {}

func mediaProcPrepare(cmd *exec.Cmd) {}

func mediaProcKillRecorded(pid int, temps []string) error { return nil }

func mediaProcRecordedCommand(pid int, temps []string) bool { return false }
