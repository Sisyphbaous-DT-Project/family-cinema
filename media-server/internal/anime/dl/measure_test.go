package dl

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMeasure(t *testing.T) {
	video := bytes.Repeat([]byte{1}, 123456)
	mux := http.NewServeMux()
	mux.HandleFunc("/v.mp4", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v.mp4", time.Time{}, bytes.NewReader(video))
	})
	// 主清单 → 子清单：10 段，第 0 段 2 秒，其余 4 秒；第 i 段大小 (i+1)*1000 字节（大小差别大，抽样会偏）
	playlist := func(dir string) string {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:2.0,\n" + dir + "/s0.ts\n")
		for i := 1; i < 10; i++ {
			fmt.Fprintf(&b, "#EXTINF:4.000,\n%s/s%d.ts\n", dir, i)
		}
		return b.String() + "#EXT-X-ENDLIST\n"
	}
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=900\nhigh.m3u8\n")
	})
	mux.HandleFunc("/high.m3u8", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, playlist("ok")) })
	mux.HandleFunc("/nolen.m3u8", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, playlist("nolen")) })
	seg := func(r *http.Request) []byte {
		i, _ := strconv.Atoi(strings.TrimSuffix(r.URL.Path[strings.LastIndex(r.URL.Path, "/s")+2:], ".ts"))
		return bytes.Repeat([]byte{0x47}, (i+1)*1000)
	}
	mux.HandleFunc("/ok/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "s.ts", time.Time{}, bytes.NewReader(seg(r)))
	})
	mux.HandleFunc("/nolen/", func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush() // 不认 Range、也不写 Content-Length
		w.Write(seg(r))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	fetch := httpFetch(srv)
	ctx := context.Background()

	s, err := Measure(ctx, fetch, srv.URL+"/v.mp4", "mp4", false, 0)
	if err != nil || s.Bytes != int64(len(video)) || !s.Exact {
		t.Fatalf("mp4: %+v %v", s, err)
	}
	// 解析时以为是 hls、实际是 mp4 文件
	if s, err = Measure(ctx, fetch, srv.URL+"/v.mp4", "hls", false, 0); err != nil || s.Bytes != int64(len(video)) {
		t.Fatalf("误判的 mp4: %+v %v", s, err)
	}
	// 每段都问得出大小：加起来是准确值 1000+2000+…+10000
	s, err = Measure(ctx, fetch, srv.URL+"/master.m3u8", "hls", false, 0)
	if err != nil || !s.Exact || s.Seconds != 38 || s.Bytes != 55000 {
		t.Fatalf("逐段相加: %+v %v", s, err)
	}
	// 同一部番量过别的集：清单时长 × 码率
	if s, err = Measure(ctx, fetch, srv.URL+"/high.m3u8", "hls", false, 1000); err != nil || s.Exact || s.Bytes != 38000 {
		t.Fatalf("按码率: %+v %v", s, err)
	}
	// 问不出每段大小：退回抽样（10 段全抽到）
	if s, err = Measure(ctx, fetch, srv.URL+"/nolen.m3u8", "hls", false, 0); err != nil || s.Exact || s.Bytes != 55000 {
		t.Fatalf("抽样: %+v %v", s, err)
	}
}

// 不加密的分段边收边写：伪装成图片的假文件头要去掉
func TestStreamSegmentStripsFakeHeader(t *testing.T) {
	ts := bytes.Repeat(append([]byte{0x47}, make([]byte, 187)...), 400) // 75KB，比读开头那一小块大
	fake := append([]byte("\x89PNG\r\n\x1a\nfakeheader-------"), ts...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fake) }))
	defer srv.Close()
	dst := t.TempDir() + "/seg"
	n, err := streamSegment(context.Background(), httpFetch(srv), srv.URL+"/s", dst, true)
	got, _ := os.ReadFile(dst)
	if err != nil || n != int64(len(ts)) || !bytes.Equal(got, ts) {
		t.Fatalf("n=%d len=%d err=%v", n, len(got), err)
	}
}

// 拼接排队：前一集在拼时后一集先报"等着拼"，轮到了才拼；停止任务时不会一直卡在排队上
func TestMergeTurnQueues(t *testing.T) {
	slot := make(chan struct{}, 1)
	var waits int
	opt := Options{MergeSlot: slot, OnProgress: func(p Progress) {
		if p.MergeWait {
			waits++
		}
	}}
	release, err := opt.mergeTurn(context.Background(), Progress{})
	if err != nil || waits != 0 {
		t.Fatal("第一个应该直接轮到")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := opt.mergeTurn(ctx, Progress{}); err == nil || waits != 1 {
		t.Fatalf("排队中被停止应返回错误: %v waits=%d", err, waits)
	}
	release()
	if r2, err := opt.mergeTurn(context.Background(), Progress{}); err != nil {
		t.Fatal(err)
	} else {
		r2()
	}
}
