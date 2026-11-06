//go:build !linux && !windows

package main

import "fmt"

// DiskSpace 暂未支持的平台保留明确提示。
func DiskSpace() (map[string]any, error) {
	return nil, fmt.Errorf("当前平台不支持磁盘空间查询")
}
