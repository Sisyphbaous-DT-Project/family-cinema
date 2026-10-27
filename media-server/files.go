package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 文件管理器：浏览/整理整块硬盘（MovieRoot 的上级，即 /mnt/sda/sda1）。
// 三道保护：根目录不可删、下载中占用的文件不可删、删非空目录要 force 二次确认。

// FsEntry 一个文件/目录条目
type FsEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	Size      int64  `json:"size"`
	SizeH     string `json:"sizeHuman"`
	Modified  int64  `json:"modified"`
	Video     bool   `json:"video"`     // 是否视频文件（决定前端显示什么图标）
	InUse     bool   `json:"inUse"`     // 被活跃下载任务占用，禁止删除
	Protected bool   `json:"protected"` // 根级关键目录，本身不可删（内容可以整理）
}

// diskRoot 整块硬盘的挂载点
func diskRoot() string {
	return filepath.Dir(currentConfig().MovieRoot)
}

// safeFsPath 路径必须在硬盘根内（防 ../ 穿越摸到系统盘）
func safeFsPath(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(diskRoot())
	if err != nil {
		return false
	}
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// protectedDir 根级关键目录：本身不许删，免得服务瘫痪（里面的内容可以正常整理）
func protectedDir(abs string) bool {
	cfg := currentConfig()
	for _, p := range []string{diskRoot(), cfg.MovieRoot, cfg.SeriesRoot, cfg.CacheDir, cfg.DownloadDir} {
		if absEqual(abs, p) {
			return true
		}
	}
	return false
}

// activeCacheDirs 正在下载的任务占用的缓存目录集合（infohash 目录）
func activeCacheDirs() map[string]bool {
	out := map[string]bool{}
	if bt == nil {
		return out
	}
	bt.mu.Lock()
	defer bt.mu.Unlock()
	for _, ad := range bt.active {
		out[filepath.Join(currentConfig().CacheDir, ad.t.InfoHash().HexString())] = true
	}
	return out
}

// pathInUse 路径本身或它内部含有活跃下载的缓存目录
func pathInUse(abs string, active map[string]bool) bool {
	for dir := range active {
		if abs == dir || strings.HasPrefix(abs, dir+string(filepath.Separator)) ||
			strings.HasPrefix(dir, abs+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// FsList 列目录：文件夹在前，各自按名称排序
func FsList(path string) (map[string]any, error) {
	if path == "" {
		path = diskRoot()
	}
	abs, err := filepath.Abs(path)
	if err != nil || !safeFsPath(abs) {
		return nil, fmt.Errorf("路径不在硬盘范围内")
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("目录不存在")
	}
	active := activeCacheDirs()
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %v", err)
	}
	dirs := []FsEntry{}
	files := []FsEntry{}
	for _, e := range entries {
		full := filepath.Join(abs, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		fe := FsEntry{
			Name: e.Name(), Path: full, IsDir: e.IsDir(),
			Size: info.Size(), Modified: info.ModTime().Unix(),
		}
		if e.IsDir() {
			fe.Protected = protectedDir(full)
			fe.InUse = pathInUse(full, active)
			dirs = append(dirs, fe)
		} else {
			fe.SizeH = humanSize(fe.Size)
			fe.Video = videoExts[strings.ToLower(filepath.Ext(e.Name()))]
			fe.InUse = pathInUse(full, active)
			files = append(files, fe)
		}
	}
	// 中文名按字符串排（Go 的 sort 对 UTF-8 稳定够用）
	sortFs(dirs)
	sortFs(files)
	return map[string]any{
		"path":    abs,
		"root":    diskRoot(),
		"entries": append(dirs, files...),
	}, nil
}

func sortFs(list []FsEntry) {
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if strings.ToLower(list[j].Name) < strings.ToLower(list[i].Name) {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
}

// FsMkdir 新建文件夹
func FsMkdir(parent, name string) error {
	name = sanitizeDirName(name)
	if name == "未命名" {
		return fmt.Errorf("请填写文件夹名称")
	}
	abs, err := filepath.Abs(parent)
	if err != nil || !safeFsPath(abs) {
		return fmt.Errorf("路径不在硬盘范围内")
	}
	target := filepath.Join(abs, name)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("已存在同名文件或文件夹")
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return fmt.Errorf("创建失败: %v", err)
	}
	return nil
}

// FsDeleteResult 删除结果；NeedConfirm 时前端拿 stats 弹窗再问一次
type FsDeleteResult struct {
	OK          bool   `json:"ok"`
	Removed     string `json:"removed"`
	Freed       int64  `json:"freed"`
	FreedH      string `json:"freedHuman"`
	NeedConfirm bool   `json:"needConfirm,omitempty"` // 非空目录需二次确认
	DirFiles    int    `json:"dirFiles,omitempty"`
	DirSize     int64  `json:"dirSize,omitempty"`
	DirSizeH    string `json:"dirSizeHuman,omitempty"`
}

// FsDelete 删除文件/目录，带三道保护
func FsDelete(path string, force bool) (*FsDeleteResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil || !safeFsPath(abs) {
		return nil, fmt.Errorf("路径不在硬盘范围内，拒绝删除")
	}
	if protectedDir(abs) {
		return nil, fmt.Errorf("这是系统关键目录，不能删除")
	}
	if pathInUse(abs, activeCacheDirs()) {
		return nil, fmt.Errorf("有下载任务正在使用它，请先停止任务")
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("不存在或已被删除")
	}
	if fi.IsDir() {
		files, size := dirStats(abs)
		if files > 0 && !force {
			return &FsDeleteResult{
				NeedConfirm: true, DirFiles: files, DirSize: size, DirSizeH: humanSize(size),
			}, nil
		}
		if err := os.RemoveAll(abs); err != nil {
			return nil, fmt.Errorf("删除失败: %v", err)
		}
		return &FsDeleteResult{OK: true, Removed: fi.Name(), Freed: size, FreedH: humanSize(size)}, nil
	}
	size := fi.Size()
	if err := os.Remove(abs); err != nil {
		return nil, fmt.Errorf("删除失败: %v", err)
	}
	return &FsDeleteResult{OK: true, Removed: fi.Name(), Freed: size, FreedH: humanSize(size)}, nil
}

// dirStats 统计目录里的条目数（文件+子目录）和文件总大小（递归）。
// 只装了一个空子目录也算"非空"，照样要二次确认。
func dirStats(dir string) (entries int, size int64) {
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil || p == dir {
			return nil
		}
		entries++
		if !fi.IsDir() {
			size += fi.Size()
		}
		return nil
	})
	return
}

// FsRename 改名（同目录内，视频文件保留扩展名）
func FsRename(path, newName string) error {
	abs, err := filepath.Abs(path)
	if err != nil || !safeFsPath(abs) {
		return fmt.Errorf("路径不在硬盘范围内")
	}
	if protectedDir(abs) {
		return fmt.Errorf("这是系统关键目录，不能改名")
	}
	if pathInUse(abs, activeCacheDirs()) {
		return fmt.Errorf("有下载任务正在使用它，请先停止任务")
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("不存在或已被删除")
	}
	clean := sanitizeDirName(newName)
	if clean == "未命名" {
		return fmt.Errorf("新名称不能为空")
	}
	if !fi.IsDir() {
		clean += filepath.Ext(abs) // 文件保留原扩展名
	}
	target := filepath.Join(filepath.Dir(abs), clean)
	if absEqual(target, abs) {
		return nil
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("已存在同名文件或文件夹")
	}
	if err := os.Rename(abs, target); err != nil {
		return fmt.Errorf("改名失败: %v", err)
	}
	// 媒体目录里的改动要让媒体库立刻反映出来
	go ScanLibrary()
	return nil
}
