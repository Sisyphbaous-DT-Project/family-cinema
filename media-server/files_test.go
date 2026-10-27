package main

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
