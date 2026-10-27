package dl

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	mpeg2 "github.com/yapingcat/gomedia/go-mpeg2"
)

func TestRemuxTSProgressAndOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "seg_00000")
	data := makeTS(t, mpeg2.TS_STREAM_H264)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.mp4")
	var progress []MergeProgress
	if err := remuxTS([]string{src}, out, func(p MergeProgress) { progress = append(progress, p) }); err != nil {
		t.Fatal(err)
	}
	if len(progress) < 2 {
		t.Fatalf("进度上报太少: %+v", progress)
	}
	for i, p := range progress {
		if p.Total != int64(len(data)) || p.Done > p.Total || (i > 0 && p.Done < progress[i-1].Done) {
			t.Fatalf("进度倒退或总量不对: %+v", progress)
		}
	}
	last := progress[len(progress)-1]
	if !last.Finalizing || last.Done != last.Total {
		t.Fatalf("没有进入收尾: %+v", last)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 16 || !bytes.Equal(got[4:8], []byte("ftyp")) || !bytes.Contains(got, []byte("mdat")) || !bytes.Contains(got, []byte("moov")) {
		t.Fatal("输出不是完整的 MP4")
	}
}
