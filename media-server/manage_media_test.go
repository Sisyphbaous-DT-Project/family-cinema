package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSubtitleFilesAreNotForeign(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"电影.mkv", "电影.vtt", "电影.sup", "电影.srt", "电影.ass", "电影.ssa"} {
		writeTestFile(t, filepath.Join(dir, name), "data")
	}
	extra, err := hasForeignFiles(dir)
	if err != nil || extra != "" {
		t.Fatalf("字幕被当成额外文件: %q %v", extra, err)
	}
}

func TestDeleteSingleVideoRemovesSidecarSubtitles(t *testing.T) {
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	video := filepath.Join(movieRoot, "电影.mkv")
	writeTestFile(t, video, "video")
	writeTestFile(t, filepath.Join(movieRoot, "电影.s0.chi.vtt"), "vtt")
	writeTestFile(t, filepath.Join(movieRoot, "电影.chs.sup"), "pgs")
	ScanLibrary()
	if _, err := DeleteMedia("", video, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"电影.mkv", "电影.s0.chi.vtt", "电影.chs.sup"} {
		if _, err := os.Stat(filepath.Join(movieRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 没有一起删除", name)
		}
	}
}

func TestRenameSingleVideoMovesSidecarSubtitles(t *testing.T) {
	movieRoot := t.TempDir()
	setTestRoots(t, movieRoot, t.TempDir())
	writeTestFile(t, filepath.Join(movieRoot, "旧名字.mkv"), "video")
	writeTestFile(t, filepath.Join(movieRoot, "旧名字.s0.chi.vtt"), "vtt")
	writeTestFile(t, filepath.Join(movieRoot, "旧名字.chs.ass"), "ass")
	ScanLibrary()
	items, _ := LibrarySnapshot("all")
	if len(items) != 1 {
		t.Fatalf("条目数 = %d", len(items))
	}
	if err := RenameMedia(items[0].ID, "新名字"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"新名字.mkv", "新名字.s0.chi.vtt", "新名字.chs.ass"} {
		if _, err := os.Stat(filepath.Join(movieRoot, name)); err != nil {
			t.Fatalf("%s 没有一起改名: %v", name, err)
		}
	}
}
