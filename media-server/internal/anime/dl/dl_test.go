package dl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() { retryDelays = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond} }

func encrypt(t *testing.T, plain, key []byte, seq int64) []byte {
	t.Helper()
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	data := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := make([]byte, 16)
	binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return out
}

func httpFetch(srv *httptest.Server) Fetcher {
	return func(ctx context.Context, u, rng string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		return srv.Client().Do(req)
	}
}

func TestPlaylistParsing(t *testing.T) {
	base, _ := url.Parse("https://cdn.test/v/index.m3u8")
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\nlow/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2800000,CODECS=\"avc1.64001f,mp4a.40.2\"\nhigh/index.m3u8\n"
	if !isMaster(master) {
		t.Fatal("应认出主清单")
	}
	if u, err := bestVariant(master, base); err != nil || u != "https://cdn.test/v/high/index.m3u8" {
		t.Errorf("应挑带宽最高的: %q %v", u, err)
	}
	media := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:7\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXTINF:4,\na.ts\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"/k2\",IV=0x000102030405060708090a0b0c0d0e0f\n#EXTINF:4,\nhttps://other.test/b.ts\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:4,\nc.ts\n#EXT-X-ENDLIST\n"
	segs, err := parseMedia(media, base)
	if err != nil || len(segs) != 3 {
		t.Fatalf("分段数不对: %+v %v", segs, err)
	}
	if segs[0].URI != "https://cdn.test/v/a.ts" || segs[0].Seq != 7 || segs[0].Key.URI != "https://cdn.test/v/key.bin" || segs[0].Key.IV != nil {
		t.Errorf("第一段不对: %+v %+v", segs[0], segs[0].Key)
	}
	if segs[1].Seq != 8 || segs[1].Key.URI != "https://cdn.test/k2" || len(segs[1].Key.IV) != 16 || segs[1].Key.IV[15] != 0x0f {
		t.Errorf("第二段不对: %+v %+v", segs[1], segs[1].Key)
	}
	if segs[2].Key != nil {
		t.Error("METHOD=NONE 之后不加密")
	}
	if _, err := parseMedia("#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\nx.ts\n", base); err == nil {
		t.Error("不支持的加密应报错")
	}
	a := attrs(`#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS="a,b",NAME="x y"`)
	if a["CODECS"] != "a,b" || a["NAME"] != "x y" || a["BANDWIDTH"] != "1" {
		t.Errorf("属性解析不对: %v", a)
	}
}

func TestDecryptAndStrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("hello segment data")
	got, err := decryptAES128(encrypt(t, plain, key, 42), key, nil, 42)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("没给 IV 时应用序号解密: %q %v", got, err)
	}
	if _, err := decryptAES128(encrypt(t, plain, key, 42), []byte("wrong-key-123456"), nil, 42); err == nil {
		t.Log("错误密钥偶尔也能通过填充检查，不强求报错")
	}
	ts := bytes.Repeat(append([]byte{0x47}, make([]byte, 187)...), 4)
	fake := append([]byte("\x89PNG\r\n\x1a\nfakeheader-------"), ts...)
	if out := stripToTS(fake); !bytes.Equal(out, ts) {
		t.Errorf("应去掉伪装的图片头，剩 %d 字节", len(out))
	}
	if out := stripToTS(ts); &out[0] != &ts[0] {
		t.Error("本来就是 TS 的应原样返回")
	}
	other := []byte("....ftypisom......")
	if out := stripToTS(other); !bytes.Equal(out, other) {
		t.Error("找不到 TS 包的应原样返回")
	}
}

// 模拟站：主清单 → 子清单（fMP4 + AES-128 加密），第 2 段第一次请求失败，验证重试、解密、拼接
func TestEpisodeFMP4EncryptedWithRetry(t *testing.T) {
	key := []byte("fedcba9876543210")
	segPlain := [][]byte{[]byte("moof-0..."), []byte("moof-1..."), []byte("moof-2...")}
	var mu sync.Mutex
	hits := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=900\nhi.m3u8\n")
	})
	mux.HandleFunc("/hi.m3u8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:4,\ns0\n#EXTINF:4,\ns1\n#EXTINF:4,\ns2\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "....ftypINIT") })
	mux.HandleFunc("/key", func(w http.ResponseWriter, r *http.Request) { w.Write(key) })
	for i := range segPlain {
		i := i
		mux.HandleFunc(fmt.Sprintf("/s%d", i), func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[r.URL.Path]++
			n := hits[r.URL.Path]
			mu.Unlock()
			if i == 1 && n == 1 {
				http.Error(w, "boom", 500)
				return
			}
			w.Write(encrypt(t, segPlain[i], key, int64(10+i)))
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	var last Progress
	err := Episode(context.Background(), Options{
		Fetch: httpFetch(srv), URL: srv.URL + "/master.m3u8", Kind: "hls",
		WorkDir: filepath.Join(dir, "w"), Out: out, Parallel: 2,
		OnProgress: func(p Progress) { last = p },
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	want := "....ftypINIT" + string(bytes.Join(segPlain, nil))
	if string(got) != want {
		t.Errorf("拼接结果不对:\n%q\n%q", got, want)
	}
	if hits["/s1"] != 2 {
		t.Errorf("第 2 段应重试一次，实际请求 %d 次", hits["/s1"])
	}
	if !last.Merging || last.Done != 3 || last.Total != 3 {
		t.Errorf("最后的进度不对: %+v", last)
	}
}

// Download 下载齐后先不拼接，调用方可以把拼接交给别的工位；Episode 仍会一次做完。
func TestDownloadDefersFMP4Concat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/p.m3u8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init\"\n#EXTINF:1,\na\n#EXTINF:1,\nb\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/init", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "....ftypINIT") })
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "AAA") })
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "BBB") })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	merge, err := Download(context.Background(), Options{
		Fetch: httpFetch(srv), URL: srv.URL + "/p.m3u8", Kind: "hls",
		WorkDir: filepath.Join(dir, "work"), Out: out,
	})
	if err != nil || merge == nil {
		t.Fatalf("下载失败或没有返回拼接: merge=%v err=%v", merge != nil, err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("返回 Download 时不应已经拼接: %v", err)
	}
	if err := merge(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "....ftypINITAAABBB" {
		t.Fatalf("拼接结果不对: %q", got)
	}
}

// 断点续下：已经下好的分段不再请求
func TestEpisodeResumeSkipsDoneSegments(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/p.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXT-X-MAP:URI=\"i\"\n#EXTINF:1,\na\n#EXTINF:1,\nb\n#EXT-X-ENDLIST\n")
		case "/i":
			io.WriteString(w, "I")
		default:
			io.WriteString(w, strings.TrimPrefix(r.URL.Path, "/"))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	work := filepath.Join(dir, "w")
	os.MkdirAll(work, 0o755)
	os.WriteFile(filepath.Join(work, "seg_00000"), []byte("A"), 0o644) // 上次已下好第 1 段
	out := filepath.Join(dir, "o.mp4")
	if err := Episode(context.Background(), Options{Fetch: httpFetch(srv), URL: srv.URL + "/p.m3u8", WorkDir: work, Out: out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "IAb" || hits["/a"] != 0 || hits["/b"] != 1 {
		t.Errorf("续下不对: out=%q hits=%v", got, hits)
	}
}

// 整文件下载：已有 .part 用 Range 接着下；以为是 m3u8 其实是 mp4 时退回整文件下载
func TestEpisodeDirectResumeAndFallback(t *testing.T) {
	video := append([]byte("\x00\x00\x00\x18ftypisom"), bytes.Repeat([]byte("v"), 5000)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/page" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>not video</html>")
			return
		}
		http.ServeContent(w, r, "v.mp4", time.Time{}, bytes.NewReader(video))
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "o.mp4")
	os.WriteFile(out+".part", video[:1000], 0o644)
	if err := Episode(context.Background(), Options{Fetch: httpFetch(srv), URL: srv.URL + "/v.mp4", Kind: "mp4", WorkDir: dir, Out: out}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, video) {
		t.Errorf("续传结果不对: %d 字节", len(got))
	}

	out2 := filepath.Join(dir, "o2.mp4")
	if err := Episode(context.Background(), Options{Fetch: httpFetch(srv), URL: srv.URL + "/v.mp4", Kind: "hls", WorkDir: dir, Out: out2}); err != nil {
		t.Fatalf("不是 m3u8 应退回整文件下载: %v", err)
	}
	if got, _ := os.ReadFile(out2); !bytes.Equal(got, video) {
		t.Error("退回整文件下载的结果不对")
	}

	if err := Episode(context.Background(), Options{Fetch: httpFetch(srv), URL: srv.URL + "/page", Kind: "mp4", WorkDir: dir, Out: filepath.Join(dir, "o3.mp4")}); err == nil {
		t.Error("拿到网页应报错")
	}
}

// 查询当前写入位置不能把缓冲冲掉；真要移动文件位置时才落盘。
func TestBufferedWriteSeeker(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "video")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &bufWriteSeeker{f: f, w: bufio.NewWriterSize(f, 1024)}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	pos, err := w.Seek(0, io.SeekCurrent)
	if err != nil || pos != 3 {
		t.Fatalf("当前位置不对: %d %v", pos, err)
	}
	if st, _ := f.Stat(); st.Size() != 0 {
		t.Fatalf("只查询位置不应触发写盘: %d", st.Size())
	}
	if _, err := w.Seek(1, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("Z")); err != nil {
		t.Fatal(err)
	}
	if err := w.w.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(f.Name())
	if string(got) != "aZc" {
		t.Fatalf("缓冲写入的位置不对: %q", got)
	}
}

func TestTrackClockKeepsTimeContinuous(t *testing.T) {
	var c trackClock
	var got []uint64
	// 正常 40ms 一帧 → 中间倒退回 0（广告接缝）→ 再往后跳很远（时间戳回绕之类）
	for _, d := range []uint64{1000, 1040, 1080, 0, 40, 80, 900000, 900040} {
		_, dd := c.fix(d, d)
		got = append(got, dd)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] || got[i]-got[i-1] > 1000 {
			t.Fatalf("时间戳应连续递增: %v", got)
		}
	}
}
