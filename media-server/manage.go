package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeleteResult 删除结果
type DeleteResult struct {
	OK      bool   `json:"ok"`
	Removed string `json:"removed"`
	Freed   int64  `json:"freed"` // 释放的字节数
	FreedH  string `json:"freedHuman"`
}

// DeleteMedia 删除媒体：传 id 删整部（目录或单文件），传 path 删单个文件。
// 为了安全，只允许删媒体根目录内的路径。
func DeleteMedia(id, path string, force bool) (*DeleteResult, error) {
	// 删单个文件（某一集）
	if path != "" {
		if !safeMediaPath(path) {
			return nil, fmt.Errorf("路径不在媒体目录内，拒绝删除")
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("文件不存在")
		}
		if fi.IsDir() {
			return nil, fmt.Errorf("这是目录，请用条目删除")
		}
		size := fi.Size()
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("删除失败: %v", err)
		}
		return &DeleteResult{OK: true, Removed: filepath.Base(path), Freed: size, FreedH: humanSize(size)}, nil
	}

	// 删整部
	if id == "" {
		return nil, fmt.Errorf("请指定要删除的条目")
	}
	it := FindItem(id)
	if it == nil {
		return nil, fmt.Errorf("条目不存在")
	}
	if !safeMediaPath(it.Path) {
		return nil, fmt.Errorf("路径不在媒体目录内，拒绝删除")
	}
	// 不允许删到媒体根目录本身
	cfg := currentConfig()
	for _, root := range []string{cfg.MovieRoot, cfg.SeriesRoot, cfg.DownloadDir} {
		if absEqual(it.Path, root) {
			return nil, fmt.Errorf("不能删除媒体根目录")
		}
	}

	size := it.Size
	if it.IsDir {
		// 目录里若有非视频的额外内容，需要显式 force 才删
		if !force {
			extra, err := hasForeignFiles(it.Path)
			if err == nil && extra != "" {
				return nil, fmt.Errorf("目录内含非视频文件（%s），如确认删除请勾选强制删除", extra)
			}
		}
		if err := os.RemoveAll(it.Path); err != nil {
			return nil, fmt.Errorf("删除失败: %v", err)
		}
	} else {
		if err := os.Remove(it.Path); err != nil {
			return nil, fmt.Errorf("删除失败: %v", err)
		}
	}
	return &DeleteResult{OK: true, Removed: it.Title, Freed: size, FreedH: humanSize(size)}, nil
}

// hasForeignFiles 检查目录里是否有视频/封面之外的文件，返回第一个的名字
func hasForeignFiles(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		lower := strings.ToLower(n)
		ext := strings.ToLower(filepath.Ext(n))
		if videoExts[ext] || archiveExts[ext] {
			continue
		}
		// 封面与常见元数据文件不算"额外内容"
		isCover := false
		for _, c := range coverNames {
			if lower == c {
				isCover = true
				break
			}
		}
		if isCover || ext == ".nfo" || ext == ".srt" || ext == ".ass" || ext == ".txt" {
			continue
		}
		return n, nil
	}
	return "", nil
}

// RenameMedia 重命名条目（改目录名/文件名）
func RenameMedia(id, newName string) error {
	if id == "" {
		return fmt.Errorf("请指定条目")
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("新名称不能为空")
	}
	clean := sanitizeDirName(newName)
	it := FindItem(id)
	if it == nil {
		return fmt.Errorf("条目不存在")
	}
	if !safeMediaPath(it.Path) {
		return fmt.Errorf("路径不在媒体目录内，拒绝操作")
	}
	dir := filepath.Dir(it.Path)
	var target string
	if it.IsDir {
		target = filepath.Join(dir, clean)
	} else {
		// 单文件保留原扩展名
		target = filepath.Join(dir, clean+filepath.Ext(it.Path))
	}
	if absEqual(target, it.Path) {
		return nil
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("已存在同名条目：%s", clean)
	}
	if err := os.Rename(it.Path, target); err != nil {
		return fmt.Errorf("重命名失败: %v", err)
	}
	return nil
}

// StatItem 管理页的条目概览
type StatItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	RawTitle string `json:"rawTitle"`
	Category string `json:"category"`
	Episodes int    `json:"episodes"`
	Size     int64  `json:"size"`
	SizeH    string `json:"sizeHuman"`
	HasCover bool   `json:"hasCover"`
	Cover    string `json:"cover,omitempty"`
	IsDir    bool   `json:"isDir"`
	Modified int64  `json:"modified"`
}

// ManageStats 管理页数据：分类统计 + 条目明细
func ManageStats() map[string]any {
	items, cats := LibrarySnapshot("all")
	var total int64
	var noCover int
	list := make([]StatItem, 0, len(items))
	for _, it := range items {
		total += it.Size
		if it.Cover == "" {
			noCover++
		}
		list = append(list, StatItem{
			ID:       it.ID,
			Title:    it.Title,
			RawTitle: it.RawTitle,
			Category: it.Category,
			Episodes: len(it.Episodes),
			Size:     it.Size,
			SizeH:    humanSize(it.Size),
			HasCover: it.Cover != "",
			Cover:    it.Cover,
			IsDir:    it.IsDir,
			Modified: it.Modified,
		})
	}
	return map[string]any{
		"categories":   cats,
		"items":        list,
		"totalSize":    total,
		"totalSizeH":   humanSize(total),
		"missingCover": noCover,
		"taskCount":    len(ListTasks()),
	}
}

// CacheEntry 一份 BT 缓存（按 infohash 分目录，也可能是散落的单文件）
type CacheEntry struct {
	Name     string `json:"name"`  // 目录/文件名（infohash 或文件名）
	Title    string `json:"title"` // 里面最大那个文件的名字，方便认出是什么片
	Size     int64  `json:"size"`
	SizeH    string `json:"sizeHuman"`
	Modified int64  `json:"modified"`
	Partial  bool   `json:"partial"` // 是否含未下载完的 .part 文件
	InUse    bool   `json:"inUse"`   // 是否有正在跑的任务在用它
	TaskName string `json:"taskName,omitempty"`
}

// ListCache 列出缓存目录里的内容。正在下载的任务会标记 inUse，避免误删活跃下载。
func ListCache() (entries []CacheEntry, total int64) {
	cacheDir := currentConfig().CacheDir
	items, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, 0
	}
	// 收集活跃任务占用的 infohash
	inUse := map[string]string{}
	if bt != nil {
		bt.mu.Lock()
		for _, ad := range bt.active {
			inUse[ad.t.InfoHash().HexString()] = ad.task.Name
		}
		bt.mu.Unlock()
	}

	entries = make([]CacheEntry, 0, len(items))
	for _, e := range items {
		full := filepath.Join(cacheDir, e.Name())
		ce := CacheEntry{Name: e.Name()}
		if e.IsDir() {
			ce.Size, ce.Title, ce.Partial, ce.Modified = scanCacheDir(full)
		} else {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			ce.Size = fi.Size()
			ce.Modified = fi.ModTime().Unix()
			ce.Title = e.Name()
			ce.Partial = strings.HasSuffix(strings.ToLower(e.Name()), ".part")
		}
		if name, ok := inUse[e.Name()]; ok {
			ce.InUse = true
			ce.TaskName = name
		}
		ce.SizeH = humanSize(ce.Size)
		total += ce.Size
		entries = append(entries, ce)
	}
	// 大的排前面，方便先清占地方的
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].Size > entries[i].Size {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	return entries, total
}

// scanCacheDir 统计一个缓存子目录：总大小、最大文件名、是否有未完成分片、最新修改时间
func scanCacheDir(dir string) (size int64, title string, partial bool, modified int64) {
	var biggest int64
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil || fi.IsDir() {
			return nil
		}
		size += fi.Size()
		if fi.ModTime().Unix() > modified {
			modified = fi.ModTime().Unix()
		}
		name := fi.Name()
		if strings.HasSuffix(strings.ToLower(name), ".part") {
			partial = true
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		if fi.Size() > biggest {
			biggest = fi.Size()
			title = name
		}
		return nil
	})
	return
}

// DeleteCache 删除缓存。name 为空时清空整个缓存目录（都跳过正在下载的）。
// 安全：只允许操作缓存目录内的一层子项，不接受路径分隔符。
func DeleteCache(name string, all bool) (*DeleteResult, error) {
	cacheDir := currentConfig().CacheDir
	entries, _ := ListCache()

	del := func(e CacheEntry) (int64, error) {
		if e.InUse {
			return 0, fmt.Errorf("「%s」正在下载中，请先停止任务", e.TaskName)
		}
		full := filepath.Join(cacheDir, e.Name)
		// 双保险：确认算出来的路径真的在缓存目录下
		if !withinDir(full, cacheDir) {
			return 0, fmt.Errorf("路径不在缓存目录内，拒绝删除")
		}
		if err := os.RemoveAll(full); err != nil {
			return 0, fmt.Errorf("删除失败: %v", err)
		}
		return e.Size, nil
	}

	if all {
		var freed int64
		var skipped int
		for _, e := range entries {
			if e.InUse {
				skipped++
				continue
			}
			n, err := del(e)
			if err != nil {
				return nil, err
			}
			freed += n
		}
		removed := fmt.Sprintf("%d 项缓存", len(entries)-skipped)
		if skipped > 0 {
			removed += fmt.Sprintf("（跳过 %d 项正在下载）", skipped)
		}
		return &DeleteResult{OK: true, Removed: removed, Freed: freed, FreedH: humanSize(freed)}, nil
	}

	if name == "" {
		return nil, fmt.Errorf("请指定要删除的缓存")
	}
	// 不接受任何路径成分，杜绝 ../ 穿越
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return nil, fmt.Errorf("缓存名称不合法")
	}
	for _, e := range entries {
		if e.Name == name {
			freed, err := del(e)
			if err != nil {
				return nil, err
			}
			title := e.Title
			if title == "" {
				title = e.Name
			}
			return &DeleteResult{OK: true, Removed: title, Freed: freed, FreedH: humanSize(freed)}, nil
		}
	}
	return nil, fmt.Errorf("缓存不存在")
}

// withinDir 判断 p 是否位于 dir 之内（含 dir 本身的直接子项）
func withinDir(p, dir string) bool {
	ap, err1 := filepath.Abs(p)
	ad, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(ad, ap)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ClearFinishedTasks 清掉已结束的任务记录（不动磁盘文件）
func ClearFinishedTasks() int {
	cleared := clearFinishedAnimeTasks()
	if bt == nil {
		return cleared
	}
	bt.mu.Lock()
	defer bt.mu.Unlock()
	n := 0
	for id, t := range bt.tasks {
		if t.Status == "done" || t.Status == "error" || t.Status == "stopped" {
			delete(bt.tasks, id)
			n++
		}
	}
	return cleared + n
}

func absEqual(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return aa == bb
}
