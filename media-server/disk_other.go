//go:build !linux

package main

import "fmt"

// DiskSpace 非 Linux 平台的占位实现（本服务只部署在 Linux 盒子上，
// 这里只是让 Windows 开发机能编译跑测试）
func DiskSpace() (map[string]any, error) {
	return nil, fmt.Errorf("当前平台不支持磁盘空间查询")
}
