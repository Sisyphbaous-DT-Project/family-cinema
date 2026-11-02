package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UploadResult 单个文件的上传结果
type UploadResult struct {
	OK         bool   `json:"ok"`
	ID         string `json:"id"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	NextTaskID string `json:"nextTaskId,omitempty"`
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

// HandleUpload 流式上传视频。MultipartReader 收一块写一块，不把整片读进内存。
// 接收期间登记成任务，其它设备也能看到进度；中断或停止会清理半截文件。
func HandleUpload(w http.ResponseWriter, r *http.Request) (*UploadResult, error) {
	if r.Method != http.MethodPost {
		return nil, fmt.Errorf("method not allowed")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("请求格式错误: %v", err)
	}

	var cat, dest, title, targetID, uploadID string
	var declaredSize int64
	var saved *UploadResult
	rc := http.NewResponseController(w)
	interrupt := func() { _ = rc.SetReadDeadline(time.Now()) }
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
			case "size":
				declaredSize, _ = strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
			case "uploadId":
				uploadID = strings.TrimSpace(string(v))
			}
			part.Close()
			continue
		}
		saved, err = saveUploadPartTracked(part, cat, dest, title, targetID, uploadID, uploadClientIP(r.RemoteAddr), declaredSize, interrupt)
		if err != nil {
			return nil, err
		}
		part.Close()
	}
	if saved == nil {
		return nil, fmt.Errorf("没有收到文件")
	}
	go ScanLibrary()
	return saved, nil
}

// saveUploadPart 保留给旧的内部调用；新请求会带大小、任务编号和来源地址。
func saveUploadPart(part *multipart.Part, cat, dest, title, targetID string) (*UploadResult, error) {
	return saveUploadPartTracked(part, cat, dest, title, targetID, "", "", 0, nil)
}

// uploadTarget 算出这个文件最终放进哪个目录，并检查扩展名和路径。
func uploadTarget(fileName, cat, dest, title, targetID string) (string, string, error) {
	fname := filepath.Base(fileName)
	ext := strings.ToLower(filepath.Ext(fname))
	if !videoExts[ext] {
		return "", "", fmt.Errorf("只接受视频文件，%s 不支持", ext)
	}
	dir, err := resolveUploadDir(cat, dest, title, targetID)
	if err != nil {
		return "", "", err
	}
	if !safeMediaPath(filepath.Join(dir, fname)) {
		return "", "", fmt.Errorf("目标路径不在媒体目录内，拒绝写入")
	}
	return dir, fname, nil
}

// HandleUploadCheck 正式上传前先问一句：同名文件是不是正在别处上传。
// 浏览器上传途中被拒只会看到"网络中断"，先问就能给出看得懂的提示。
func HandleUploadCheck(r *http.Request) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method not allowed")
	}
	var req struct {
		Cat      string `json:"cat"`
		Dest     string `json:"dest"`
		Title    string `json:"title"`
		TargetID string `json:"targetId"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		return fmt.Errorf("请求格式错误: %v", err)
	}
	dir, fname, err := uploadTarget(req.Name, strings.TrimSpace(req.Cat), strings.TrimSpace(req.Dest), strings.TrimSpace(req.Title), strings.TrimSpace(req.TargetID))
	if err != nil {
		return err
	}
	return checkUploadFree(dir, fname)
}

func saveUploadPartTracked(part *multipart.Part, cat, dest, title, targetID, uploadID, sourceIP string, size int64, interrupt func()) (*UploadResult, error) {
	dir, fname, err := uploadTarget(part.FileName(), cat, dest, title, targetID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建目录失败: %v", err)
	}

	rec, reader, err := beginUpload(part, fname, dir, cat, uploadID, sourceIP, size, interrupt)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(rec.tmpPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		finishUpload(rec, 0, err, nil)
		return nil, fmt.Errorf("创建文件失败: %v", err)
	}
	n, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr == nil && closeErr != nil {
		copyErr = closeErr
	}
	var renameErr error
	if copyErr == nil {
		renameErr = os.Rename(rec.tmpPath, rec.finalPath)
	}
	finishUpload(rec, n, copyErr, renameErr)
	if copyErr != nil {
		if copyErr == errUploadStopped {
			return nil, errUploadStopped
		}
		return nil, fmt.Errorf("上传中断，已清理未完成文件")
	}
	if renameErr != nil {
		return nil, fmt.Errorf("落盘改名失败: %v", renameErr)
	}

	nextID := mediaProcEnqueue(rec.finalPath, false)
	setUploadNextTask(rec.task.ID, nextID)
	return &UploadResult{OK: true, ID: rec.task.ID, Path: rec.finalPath, Size: n, NextTaskID: nextID}, nil
}
