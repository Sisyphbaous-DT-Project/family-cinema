package main

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// UploadResult 单个文件的上传结果
type UploadResult struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// resolveUploadDir 根据前端选的去向算出目标目录。
// dest=new：在对应分类根目录下按 title 建（或复用）文件夹
// dest=existing：并入已有条目的文件夹
func resolveUploadDir(cat, dest, title, targetID string) (string, error) {
	cfg := currentConfig()
	switch dest {
	case "new":
		root := cfg.MovieRoot
		if cat == "series" {
			root = cfg.SeriesRoot
		}
		name := sanitizeDirName(title)
		if name == "未命名" {
			return "", fmt.Errorf("请填写保存名称")
		}
		return filepath.Join(root, name), nil
	case "existing":
		it := FindItem(targetID)
		if it == nil {
			return "", fmt.Errorf("目标条目不存在，请刷新后重试")
		}
		if !it.IsDir {
			return "", fmt.Errorf("目标是单文件条目，无法并入")
		}
		return it.Path, nil
	}
	return "", fmt.Errorf("未知的保存方式")
}

// HandleUpload 流式上传视频。绝不能走 ParseMultipartForm——那会把整个文件读进内存，
// 盒子内存小，几个 GB 直接 OOM。MultipartReader 收一块写一块，内存恒定在缓冲大小。
// 先写 .uploading 临时名，传完才改正名：断网留下的半截文件不会冒充正片被扫进库。
func HandleUpload(w http.ResponseWriter, r *http.Request) (*UploadResult, error) {
	if r.Method != http.MethodPost {
		return nil, fmt.Errorf("method not allowed")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("请求格式错误: %v", err)
	}

	// 前端按顺序 append：文本字段在前，文件在最后，流式边读边定目标
	var cat, dest, title, targetID string
	var saved *UploadResult
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取上传内容失败: %v", err)
		}
		name := part.FormName()
		if part.FileName() == "" {
			// 文本字段
			v, _ := readAllLimit(part, 1<<20)
			switch name {
			case "cat":
				cat = strings.TrimSpace(string(v))
			case "dest":
				dest = strings.TrimSpace(string(v))
			case "title":
				title = strings.TrimSpace(string(v))
			case "targetId":
				targetID = strings.TrimSpace(string(v))
			}
			part.Close()
			continue
		}
		// 文件字段：前面的文本字段必须已经读完
		saved, err = saveUploadPart(part, cat, dest, title, targetID)
		part.Close()
		if err != nil {
			return nil, err
		}
	}
	if saved == nil {
		return nil, fmt.Errorf("没有收到文件")
	}
	// 入库后触发扫库，媒体库立刻能看到
	go ScanLibrary()
	return saved, nil
}

// saveUploadPart 把一个文件流式写进目标目录
func saveUploadPart(part *multipart.Part, cat, dest, title, targetID string) (*UploadResult, error) {
	fname := filepath.Base(part.FileName()) // 去掉浏览器可能带的路径成分
	ext := strings.ToLower(filepath.Ext(fname))
	if !videoExts[ext] {
		return nil, fmt.Errorf("只接受视频文件，%s 不支持", ext)
	}
	dir, err := resolveUploadDir(cat, dest, title, targetID)
	if err != nil {
		return nil, err
	}
	if !safeMediaPath(filepath.Join(dir, fname)) {
		return nil, fmt.Errorf("目标路径不在媒体目录内，拒绝写入")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建目录失败: %v", err)
	}
	final := filepath.Join(dir, fname)
	tmp := final + ".uploading"
	// 同名文件已存在时不覆盖，自动加序号
	for i := 2; ; i++ {
		if _, err := os.Stat(final); os.IsNotExist(err) {
			break
		}
		base := strings.TrimSuffix(fname, filepath.Ext(fname))
		final = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, filepath.Ext(fname)))
		tmp = final + ".uploading"
	}

	f, err := os.Create(tmp)
	if err != nil {
		return nil, fmt.Errorf("创建文件失败: %v", err)
	}
	n, copyErr := io.Copy(f, part)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		// 中途断开（断网/关页面）：删掉半截文件，不留垃圾
		os.Remove(tmp)
		return nil, fmt.Errorf("上传中断，已清理未完成文件")
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("落盘改名失败: %v", err)
	}
	return &UploadResult{OK: true, Path: final, Size: n}, nil
}
