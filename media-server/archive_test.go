package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMKV 造一个带合法 EBML 头、超过 1MB 的假视频，用于走通校验逻辑
func fakeMKV() []byte {
	b := make([]byte, 1<<20+4096)
	copy(b, []byte{0x1A, 0x45, 0xDF, 0xA3})
	return b
}

func TestVerifyVideoHeaders(t *testing.T) {
	dir := t.TempDir()

	ok := filepath.Join(dir, "good.mkv")
	if err := os.WriteFile(ok, fakeMKV(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyVideo(ok); err != nil {
		t.Errorf("合法 mkv 应通过校验, got %v", err)
	}

	// 体积过小
	small := filepath.Join(dir, "small.mkv")
	os.WriteFile(small, []byte{0x1A, 0x45, 0xDF, 0xA3, 0, 0, 0, 0, 0, 0, 0, 0}, 0644)
	if err := VerifyVideo(small); err == nil {
		t.Error("过小文件应校验失败")
	}

	// 头部不是任何已知容器
	bad := make([]byte, 1<<20+16)
	copy(bad, []byte("this is plain text not a video!!"))
	badPath := filepath.Join(dir, "bad.mkv")
	os.WriteFile(badPath, bad, 0644)
	if err := VerifyVideo(badPath); err == nil {
		t.Error("非视频文件头应校验失败")
	}

	// mp4 的 ftyp 魔数
	mp4 := make([]byte, 1<<20+16)
	copy(mp4[4:], []byte("ftypisom"))
	mp4Path := filepath.Join(dir, "movie.mp4")
	os.WriteFile(mp4Path, mp4, 0644)
	if err := VerifyVideo(mp4Path); err != nil {
		t.Errorf("合法 mp4 应通过校验, got %v", err)
	}
}

func TestExtractZipAndProcess(t *testing.T) {
	work := t.TempDir()

	// 造一个含视频与垃圾文件的 zip
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w1, _ := zw.Create("第01集.mkv")
	w1.Write(fakeMKV())
	w2, _ := zw.Create("说明.txt")
	w2.Write([]byte("readme"))
	zw.Close()

	zipPath := filepath.Join(work, "pack.zip")
	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	n, note, err := ProcessDownloaded(work, nil)
	if err != nil {
		t.Fatalf("ProcessDownloaded err: %v", err)
	}
	if n != 1 {
		t.Errorf("视频数 = %d, want 1", n)
	}
	// 校验通过后压缩包应被删除
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Error("校验通过后压缩包应被删除")
	}
	if _, err := os.Stat(filepath.Join(work, "第01集.mkv")); err != nil {
		t.Errorf("解出的视频不存在: %v", err)
	}
	// 非视频文件不应被解出
	if _, err := os.Stat(filepath.Join(work, "说明.txt")); err == nil {
		t.Error("非视频文件不应解出")
	}
	if !strings.Contains(note, "已解压") {
		t.Errorf("处理说明异常: %q", note)
	}
}

func TestExtractTarGz(t *testing.T) {
	work := t.TempDir()

	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	body := fakeMKV()
	tw.WriteHeader(&tar.Header{Name: "show/第02集.mkv", Size: int64(len(body)), Mode: 0644})
	tw.Write(body)
	tw.Close()

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	gw.Write(raw.Bytes())
	gw.Close()

	p := filepath.Join(work, "pack.tar.gz")
	os.WriteFile(p, gzBuf.Bytes(), 0644)

	n, _, err := ProcessDownloaded(work, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Errorf("视频数 = %d, want 1", n)
	}
	// tar 内的目录层级被摊平，只取文件名
	if _, err := os.Stat(filepath.Join(work, "第02集.mkv")); err != nil {
		t.Errorf("解出的视频不存在: %v", err)
	}
}

func TestBadArchiveKeepsFile(t *testing.T) {
	work := t.TempDir()
	// 一个坏 zip，解压必失败
	p := filepath.Join(work, "broken.zip")
	os.WriteFile(p, []byte("not a real zip file at all"), 0644)

	_, note, err := ProcessDownloaded(work, nil)
	if err != nil {
		t.Fatalf("不应返回硬错误: %v", err)
	}
	// 坏包必须保留，便于人工排查
	if _, err := os.Stat(p); err != nil {
		t.Error("解压失败时应保留压缩包")
	}
	if !strings.Contains(note, "解压失败") {
		t.Errorf("应报告解压失败, got %q", note)
	}
}

func TestSecondaryVolumeDetection(t *testing.T) {
	cases := map[string]bool{
		"movie.part1.rar":  false, // 首卷
		"movie.part2.rar":  true,
		"movie.part10.rar": true,
		"movie.rar":        false,
		"movie.r00":        true,
		"movie.r01":        true,
		"movie.zip":        false,
		"movie.z01":        true,
		"movie.001":        true,
		"movie.mkv":        false,
	}
	for name, want := range cases {
		if got := isSecondaryVolume(name); got != want {
			t.Errorf("isSecondaryVolume(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestArchiveStemGroupsVolumes(t *testing.T) {
	if a, b := archiveStem("movie.part1.rar"), archiveStem("movie.part2.rar"); a != b {
		t.Errorf("分卷主干名应一致: %q vs %q", a, b)
	}
	if a, b := archiveStem("movie.rar"), archiveStem("movie.r00"); a != b {
		t.Errorf("分卷主干名应一致: %q vs %q", a, b)
	}
}
