package main

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 上传：dest=new 应在分类根下建目录并落盘
func TestUploadNewFolder(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	setTestRoots(t, movieRoot, seriesRoot)
	ScanLibrary()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("cat", "movie")
	mw.WriteField("dest", "new")
	mw.WriteField("title", "测试影片")
	fw, _ := mw.CreateFormFile("file", "正片.mp4")
	fw.Write([]byte("fake video data"))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := HandleUpload(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(movieRoot, "测试影片", "正片.mp4")
	if res.Path != want {
		t.Fatalf("path = %q, want %q", res.Path, want)
	}
	data, err := os.ReadFile(want)
	if err != nil || string(data) != "fake video data" {
		t.Fatalf("content mismatch: %v %q", err, data)
	}
	// 不应残留临时文件
	if _, err := os.Stat(want + ".uploading"); !os.IsNotExist(err) {
		t.Fatal("临时文件未清理")
	}
}

// 上传：拒绝非视频扩展名
func TestUploadRejectsNonVideo(t *testing.T) {
	setTestRoots(t, t.TempDir(), t.TempDir())
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("cat", "movie")
	mw.WriteField("dest", "new")
	mw.WriteField("title", "测试")
	fw, _ := mw.CreateFormFile("file", "木马.exe")
	fw.Write([]byte("x"))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if _, err := HandleUpload(httptest.NewRecorder(), req); err == nil {
		t.Fatal("非视频文件应该被拒绝")
	}
}

// 上传：dest=existing 并入已有条目目录
func TestUploadIntoExisting(t *testing.T) {
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	writeTestFile(t, filepath.Join(movieRoot, "某剧集", "第01集.mp4"), "ep1")
	ScanLibrary()

	var targetID string
	items, _ := LibrarySnapshot("all")
	for _, it := range items {
		if it.Title == "某剧集" {
			targetID = it.ID
		}
	}
	if targetID == "" {
		t.Fatal("条目未入库")
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("dest", "existing")
	mw.WriteField("targetId", targetID)
	fw, _ := mw.CreateFormFile("file", "第02集.mp4")
	fw.Write([]byte("ep2"))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := HandleUpload(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(movieRoot, "某剧集", "第02集.mp4") {
		t.Fatalf("path = %q", res.Path)
	}
}

// 上传：同名文件不覆盖，自动加序号
func TestUploadNoOverwrite(t *testing.T) {
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	writeTestFile(t, filepath.Join(movieRoot, "测试", "正片.mp4"), "old")
	ScanLibrary()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("cat", "movie")
	mw.WriteField("dest", "new")
	mw.WriteField("title", "测试")
	fw, _ := mw.CreateFormFile("file", "正片.mp4")
	fw.Write([]byte("new"))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := HandleUpload(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Path, "正片-2.mp4") {
		t.Fatalf("应加序号而不是覆盖: %q", res.Path)
	}
	// 旧文件内容不能被覆盖
	data, _ := os.ReadFile(filepath.Join(movieRoot, "测试", "正片.mp4"))
	if string(data) != "old" {
		t.Fatal("原文件被覆盖了")
	}
}

// 文件管理：列出、新建、改名、删除全流程
func TestFsLifecycle(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	setTestRoots(t, movieRoot, seriesRoot)
	// 让 diskRoot 指向临时盘
	disk := filepath.Dir(movieRoot)

	// 列出根
	out, err := FsList("")
	if err != nil {
		t.Fatal(err)
	}
	if out["root"] != disk {
		t.Fatalf("root = %v, want %v", out["root"], disk)
	}

	// 新建文件夹
	if err := FsMkdir(disk, "新建目录"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(disk, "新建目录")); err != nil {
		t.Fatal("目录未创建")
	}

	// 改名
	if err := FsRename(filepath.Join(disk, "新建目录"), "改名后"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(disk, "改名后")); err != nil {
		t.Fatal("改名未生效")
	}

	// 非空目录要先返回 needConfirm
	writeTestFile(t, filepath.Join(disk, "改名后", "视频.mkv"), "data")
	r, err := FsDelete(filepath.Join(disk, "改名后"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !r.NeedConfirm || r.DirFiles != 1 {
		t.Fatalf("应要求二次确认: %+v", r)
	}
	// 带 force 才真正删
	r, err = FsDelete(filepath.Join(disk, "改名后"), true)
	if err != nil || !r.OK {
		t.Fatalf("force 删除失败: %v %+v", err, r)
	}
	if _, err := os.Stat(filepath.Join(disk, "改名后")); !os.IsNotExist(err) {
		t.Fatal("目录未被删除")
	}
}

// 文件管理：三道保护
func TestFsProtections(t *testing.T) {
	movieRoot := t.TempDir()
	seriesRoot := t.TempDir()
	setTestRoots(t, movieRoot, seriesRoot)
	disk := filepath.Dir(movieRoot)

	// 根目录不可删
	if _, err := FsDelete(disk, true); err == nil {
		t.Fatal("根目录应该被拒绝删除")
	}
	// 媒体根目录本身不可删
	if _, err := FsDelete(movieRoot, true); err == nil {
		t.Fatal("媒体根目录应该被拒绝删除")
	}
	// 路径穿越：硬盘外（另起一棵完全独立的临时树，别跟 diskRoot 共享父目录）
	outside, _ := os.MkdirTemp("", "fs-outside")
	defer os.RemoveAll(outside)
	writeTestFile(t, filepath.Join(outside, "秘密.txt"), "secret")
	if _, err := FsDelete(filepath.Join(outside, "秘密.txt"), true); err == nil {
		t.Fatal("硬盘外路径应该被拒绝")
	}
	if _, err := os.Stat(filepath.Join(outside, "秘密.txt")); err != nil {
		t.Fatal("硬盘外文件被删了！")
	}
	if _, err := FsList(outside); err == nil {
		t.Fatal("硬盘外目录应该被拒绝列出")
	}
	if err := FsMkdir(outside, "渗透"); err == nil {
		t.Fatal("硬盘外 mkdir 应该被拒绝")
	}
}

// 文件管理：视频文件标记 video，排序文件夹在前
func TestFsListOrder(t *testing.T) {
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	disk := filepath.Dir(movieRoot)
	writeTestFile(t, filepath.Join(disk, "zzz文件夹", "a.mp4"), "x")
	writeTestFile(t, filepath.Join(disk, "aaa视频.mp4"), "x")
	writeTestFile(t, filepath.Join(disk, "说明.txt"), "x")

	out, err := FsList("")
	if err != nil {
		t.Fatal(err)
	}
	entries := out["entries"].([]FsEntry)
	if len(entries) < 3 {
		t.Fatalf("entries = %d", len(entries))
	}
	// 找 zzz文件夹 和 aaa视频.mp4 的相对位置（跳过媒体根目录项）
	var dirIdx, vidIdx, vidMarked = -1, -1, false
	for i, e := range entries {
		if e.Name == "zzz文件夹" {
			dirIdx = i
		}
		if e.Name == "aaa视频.mp4" {
			vidIdx = i
			vidMarked = e.Video
		}
	}
	if dirIdx < 0 || vidIdx < 0 || dirIdx > vidIdx {
		t.Fatalf("文件夹应排在文件前: dir=%d vid=%d", dirIdx, vidIdx)
	}
	if !vidMarked {
		t.Fatal("视频文件应标记 video")
	}
}

func resetUploadRegistry(t *testing.T) {
	t.Helper()
	uploads.Lock()
	uploads.byID = map[string]*uploadRec{}
	uploads.activeKey = map[string]string{}
	uploads.Unlock()
	t.Cleanup(func() {
		uploads.Lock()
		uploads.byID = map[string]*uploadRec{}
		uploads.activeKey = map[string]string{}
		uploads.Unlock()
	})
}

func findUploadTask(id string) (TorrentTask, bool) {
	for _, task := range uploadTasks() {
		if task.ID == id {
			return task, true
		}
	}
	return TorrentTask{}, false
}

func TestUploadWithSizeCreatesFinishedTask(t *testing.T) {
	resetUploadRegistry(t)
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	ScanLibrary()
	payload := []byte("video bytes")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("cat", "movie")
	mw.WriteField("dest", "new")
	mw.WriteField("title", "任务测试")
	mw.WriteField("size", "11")
	mw.WriteField("uploadId", "upload-size-test")
	fw, _ := mw.CreateFormFile("file", "正片.mp4")
	fw.Write(payload)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := HandleUpload(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "upload-size-test" {
		t.Fatalf("上传编号 = %q", res.ID)
	}
	task, ok := findUploadTask(res.ID)
	if !ok || task.Kind != "upload" || task.Status != "done" {
		t.Fatalf("上传任务状态不对: %+v", task)
	}
	if task.BytesDone != 11 || task.BytesTotal != 11 || task.Progress != 100 {
		t.Fatalf("上传进度不对: %+v", task)
	}
	if task.SourceIP != "192.0.2.1" {
		t.Fatalf("来源地址 = %q", task.SourceIP)
	}
}

func TestConcurrentSameNameUploadIsRejected(t *testing.T) {
	resetUploadRegistry(t)
	dir := t.TempDir()
	const count = 8
	start := make(chan struct{})
	type outcome struct {
		rec *uploadRec
		err error
	}
	results := make(chan outcome, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := "upload-race-" + strings.Repeat("x", i)
			rec, _, err := beginUpload(strings.NewReader("x"), "同名片.mkv", dir, "movie", id, "", 1, nil)
			results <- outcome{rec: rec, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner *uploadRec
	succeeded := 0
	for result := range results {
		if result.err == nil {
			succeeded++
			winner = result.rec
			continue
		}
		if statusErr, ok := result.err.(*uploadHTTPError); !ok || statusErr.status != 409 {
			t.Fatalf("第二个同名上传应返回 409，实际: %v", result.err)
		}
	}
	if succeeded != 1 || winner == nil {
		t.Fatalf("同时上传成功数 = %d，应为 1", succeeded)
	}
	finishUpload(winner, 0, errUploadStopped, nil)
	matches, _ := filepath.Glob(filepath.Join(dir, "*.uploading"))
	if len(matches) != 0 {
		t.Fatalf("临时文件没有清理: %v", matches)
	}
}

// 真实 TCP 连接：对方传到一半网络卡住（不再发数据也不断开），停止也要马上生效、删掉半截文件，
// 浏览器随后自动重发同一个请求时也不能再收。
func TestStopUploadInterruptsReadAndRemovesPartial(t *testing.T) {
	resetUploadRegistry(t)
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	ScanLibrary()
	srv := httptest.NewServer(NewServer())
	defer srv.Close()

	uploadBody := func(id string, complete bool) (*bytes.Buffer, string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("cat", "movie")
		mw.WriteField("dest", "new")
		mw.WriteField("title", "停止测试")
		mw.WriteField("size", "1048576")
		mw.WriteField("uploadId", id)
		fw, _ := mw.CreateFormFile("file", "半截.mkv")
		fw.Write([]byte("partial video"))
		if complete {
			mw.Close()
		}
		return &body, mw.FormDataContentType()
	}
	head, contentType := uploadBody("upload-stop-test", false)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /api/upload HTTP/1.1\r\nHost: box\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", contentType, head.Len()+1<<20)
	conn.Write(head.Bytes())

	deadline := time.Now().Add(2 * time.Second)
	for {
		task, ok := findUploadTask("upload-stop-test")
		if ok && task.Status == "running" && task.BytesDone > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("上传没有进入接收状态")
		}
		time.Sleep(10 * time.Millisecond)
	}
	duplicate, duplicateType := uploadBody("upload-duplicate-test", true)
	resp, err := srv.Client().Post(srv.URL+"/api/upload", duplicateType, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("同名上传应立刻返回 409，实际 %d", resp.StatusCode)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- StopTask("upload-stop-test") }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("对方网络卡住时，停止接口被卡住了")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		task, _ := findUploadTask("upload-stop-test")
		if task.Status == "stopped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("停止没有叫醒阻塞中的接收，状态 = %q", task.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir := filepath.Join(movieRoot, "停止测试")
	matches, _ := filepath.Glob(filepath.Join(dir, "*.uploading"))
	if len(matches) != 0 {
		t.Fatalf("停止后仍有半截文件: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(dir, "半截.mkv")); !os.IsNotExist(err) {
		t.Fatal("停止后不应出现正式视频文件")
	}

	// 浏览器把同一个请求重发一遍：已停止的编号不能再收
	replay, replayType := uploadBody("upload-stop-test", true)
	resp, err = srv.Client().Post(srv.URL+"/api/upload", replayType, replay)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("已停止的上传被重发时应拒收，实际 %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "半截.mkv")); !os.IsNotExist(err) {
		t.Fatal("重发的请求不应落盘")
	}
}

// 正式上传前先问：同名文件正在上传时直接给出看得懂的冲突提示
func TestUploadCheckReportsBusyName(t *testing.T) {
	resetUploadRegistry(t)
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	ScanLibrary()
	srv := httptest.NewServer(NewServer())
	defer srv.Close()
	check := func(name string) int {
		body := strings.NewReader(`{"cat":"movie","dest":"new","title":"冲突测试","targetId":"","name":"` + name + `"}`)
		resp, err := srv.Client().Post(srv.URL+"/api/upload/check", "application/json", body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := check("片子.mkv"); code != 200 {
		t.Fatalf("没人在传时应允许，实际 %d", code)
	}
	dir := filepath.Join(movieRoot, "冲突测试")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec, _, err := beginUpload(strings.NewReader("x"), "片子.mkv", dir, "movie", "upload-check-busy", "", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer finishUpload(rec, 0, errUploadStopped, nil)
	if code := check("片子.mkv"); code != 409 {
		t.Fatalf("同名正在上传时应返回 409，实际 %d", code)
	}
	if code := check("别的.mkv"); code != 200 {
		t.Fatalf("不同名字不应冲突，实际 %d", code)
	}
}
