//go:build linux

package main

import (
	"fmt"
	"syscall"
)

// DiskSpace 磁盘空间（给上传前的"放不放得下"检查用）
func DiskSpace() (map[string]any, error) {
	var st syscall.Statfs_t
	// 三个媒体根在同一块硬盘上，查哪个都一样
	if err := syscall.Statfs(currentConfig().MovieRoot, &st); err != nil {
		return nil, fmt.Errorf("读取磁盘信息失败: %v", err)
	}
	total := int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	return map[string]any{
		"total": total, "free": free,
		"totalH": humanSize(total), "freeH": humanSize(free),
	}, nil
}
