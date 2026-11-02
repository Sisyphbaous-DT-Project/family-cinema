package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var errUploadStopped = errors.New("上传已停止")

type uploadHTTPError struct {
	status int
	text   string
}

func (e *uploadHTTPError) Error() string   { return e.text }
func (e *uploadHTTPError) StatusCode() int { return e.status }

type uploadRec struct {
	mu        sync.Mutex
	task      TorrentTask
	original  string
	key       string
	tmpPath   string
	finalPath string
	// interrupt 叫醒正卡在读网络数据上的接收（给连接设一个立刻到期的读超时）
	interrupt func()
	stop      chan struct{}
	stopOnce  sync.Once
	lastDone  int64
	lastAt    time.Time
}

type uploadRegistry struct {
	sync.Mutex
	byID      map[string]*uploadRec
	activeKey map[string]string
}

var uploads = uploadRegistry{byID: map[string]*uploadRec{}, activeKey: map[string]string{}}

func uploadClientIP(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

func validUploadID(id string) bool {
	if id == "" {
		return false
	}
	if len(id) > 100 || !strings.HasPrefix(id, "upload-") {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

var errUploadBusy = &uploadHTTPError{status: http.StatusConflict, text: "这个文件正在上传，请等它传完"}

// checkUploadFree 在真正传之前问一句：同一个位置的同名文件是不是正在上传。
// 真正接收时 beginUpload 还会在锁里再查一遍，这里只是为了让浏览器拿到看得懂的提示。
func checkUploadFree(dir, fname string) error {
	uploads.Lock()
	defer uploads.Unlock()
	if _, ok := uploads.activeKey[dir+"\x00"+fname]; ok {
		return errUploadBusy
	}
	return nil
}

func beginUpload(part io.Reader, fname, dir, cat, uploadID, sourceIP string, size int64, interrupt func()) (*uploadRec, io.Reader, error) {
	if !validUploadID(uploadID) {
		uploadID = fmt.Sprintf("upload-%d", time.Now().UnixNano())
	}
	key := dir + "\x00" + fname
	uploads.Lock()
	defer uploads.Unlock()
	if _, ok := uploads.activeKey[key]; ok {
		return nil, nil, errUploadBusy
	}
	if old, exists := uploads.byID[uploadID]; exists {
		old.mu.Lock()
		status := old.task.Status
		old.mu.Unlock()
		// 浏览器在连接被掐断后会自动把同一个请求重发一遍：停掉的、传完的不能再收，否则停止无效或多出一份
		if status == "stopped" || status == "done" {
			return nil, nil, &uploadHTTPError{status: http.StatusConflict, text: "这次上传已经结束，如需再传请重新开始"}
		}
		uploadID = fmt.Sprintf("upload-%d", time.Now().UnixNano())
	}

	ext := filepath.Ext(fname)
	base := strings.TrimSuffix(fname, ext)
	var final, tmp string
	var f *os.File
	for i := 1; ; i++ {
		name := fname
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		final = filepath.Join(dir, name)
		tmp = final + ".uploading"
		if _, err := os.Stat(final); err == nil {
			continue
		}
		var err error
		f, err = os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			break
		}
		if os.IsExist(err) {
			continue
		}
		return nil, nil, fmt.Errorf("创建文件失败: %v", err)
	}
	f.Close()

	now := time.Now()
	rec := &uploadRec{
		original:  fname,
		key:       key,
		tmpPath:   tmp,
		finalPath: final,
		interrupt: interrupt,
		stop:      make(chan struct{}),
		lastAt:    now,
		task: TorrentTask{
			ID: uploadID, Kind: "upload", Name: fname, Category: cat, TargetDir: dir,
			Status: "running", Stage: "正在上传", BytesTotal: size, SourceIP: sourceIP,
			CreatedAt: now.Unix(),
		},
	}
	uploads.byID[rec.task.ID] = rec
	uploads.activeKey[key] = rec.task.ID
	return rec, &uploadReadCounter{part: part, rec: rec}, nil
}

type uploadReadCounter struct {
	part io.Reader
	rec  *uploadRec
}

func (r *uploadReadCounter) Read(p []byte) (int, error) {
	select {
	case <-r.rec.stop:
		return 0, errUploadStopped
	default:
	}
	n, err := r.part.Read(p)
	if n > 0 {
		r.rec.mu.Lock()
		now := time.Now()
		r.rec.task.BytesDone += int64(n)
		if r.rec.task.BytesTotal > 0 {
			r.rec.task.Progress = float64(r.rec.task.BytesDone) * 100 / float64(r.rec.task.BytesTotal)
			if r.rec.task.Progress > 99 {
				r.rec.task.Progress = 99
			}
		}
		if dt := now.Sub(r.rec.lastAt).Seconds(); dt >= 0.5 {
			r.rec.task.SpeedKBs = float64(r.rec.task.BytesDone-r.rec.lastDone) / 1024 / dt
			r.rec.lastDone = r.rec.task.BytesDone
			r.rec.lastAt = now
		}
		r.rec.mu.Unlock()
	}
	if err != nil {
		select {
		case <-r.rec.stop:
			return n, errUploadStopped
		default:
		}
	}
	return n, err
}

func finishUpload(rec *uploadRec, n int64, copyErr error, renameErr error) {
	uploads.Lock()
	delete(uploads.activeKey, rec.key)
	uploads.Unlock()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.task.SpeedKBs = 0
	rec.task.FinishedAt = time.Now().Unix()
	if copyErr != nil {
		os.Remove(rec.tmpPath)
		rec.task.Progress = 0
		if errors.Is(copyErr, errUploadStopped) {
			rec.task.Status = "stopped"
			rec.task.Stage = "已停止"
		} else {
			rec.task.Status = "error"
			rec.task.Error = "上传中断，已清理未完成文件"
			rec.task.Stage = "上传中断"
		}
		return
	}
	if renameErr != nil {
		os.Remove(rec.tmpPath)
		rec.task.Status = "error"
		rec.task.Error = "落盘改名失败: " + renameErr.Error()
		rec.task.Stage = "保存失败"
		return
	}
	rec.task.Status = "done"
	rec.task.Stage = "上传完成"
	rec.task.Progress = 100
	rec.task.BytesDone = n
	if rec.task.BytesTotal == 0 {
		rec.task.BytesTotal = n
	}
	rec.task.Error = ""
}

func stopUpload(id string) error {
	uploads.Lock()
	rec := uploads.byID[id]
	uploads.Unlock()
	if rec == nil {
		return fmt.Errorf("任务不存在")
	}
	rec.mu.Lock()
	running := rec.task.Status == "running"
	rec.mu.Unlock()
	if !running {
		return fmt.Errorf("任务已经结束")
	}
	rec.stopOnce.Do(func() {
		close(rec.stop)
		// 不能关请求体：Go 里关请求体要等正在进行的那次读取结束，对方网络卡住时停止按钮会跟着卡住
		if rec.interrupt != nil {
			rec.interrupt()
		}
	})
	return nil
}

func uploadTasks() []TorrentTask {
	uploads.Lock()
	defer uploads.Unlock()
	out := make([]TorrentTask, 0, len(uploads.byID))
	for _, rec := range uploads.byID {
		rec.mu.Lock()
		out = append(out, rec.task)
		rec.mu.Unlock()
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func clearFinishedUploads() int {
	uploads.Lock()
	defer uploads.Unlock()
	n := 0
	for id, rec := range uploads.byID {
		rec.mu.Lock()
		finished := rec.task.Status == "done" || rec.task.Status == "error" || rec.task.Status == "stopped"
		rec.mu.Unlock()
		if finished {
			delete(uploads.byID, id)
			n++
		}
	}
	return n
}

func uploadsActive() bool {
	uploads.Lock()
	defer uploads.Unlock()
	return len(uploads.activeKey) > 0
}

func setUploadNextTask(id, next string) {
	uploads.Lock()
	rec := uploads.byID[id]
	uploads.Unlock()
	if rec == nil || next == "" {
		return
	}
	rec.mu.Lock()
	rec.task.NextTaskID = next
	rec.mu.Unlock()
}
