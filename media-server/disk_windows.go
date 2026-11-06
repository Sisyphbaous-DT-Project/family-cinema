//go:build windows

package main

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func windowsDiskSpace(path string) (total, free int64, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, 0, err
	}
	dir, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return 0, 0, err
	}
	var available, capacity, unused uint64
	if err := windows.GetDiskFreeSpaceEx(dir, &available, &capacity, &unused); err != nil {
		return 0, 0, err
	}
	return int64(capacity), int64(available), nil
}

func DiskSpace() (map[string]any, error) {
	total, free, err := windowsDiskSpace(currentConfig().MovieRoot)
	if err != nil {
		return nil, fmt.Errorf("读取磁盘信息失败: %v", err)
	}
	return map[string]any{
		"total": total, "free": free,
		"totalH": humanSize(total), "freeH": humanSize(free),
	}, nil
}
