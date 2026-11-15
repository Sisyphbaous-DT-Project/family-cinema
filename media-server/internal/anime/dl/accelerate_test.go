package dl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func accelVideo(size int) []byte {
	b := make([]byte, size)
	copy(b, []byte("xxxxftypisom"))
	for i := 12; i < len(b); i++ {
		b[i] = byte(i*31 + i/97)
	}
	return b
}

func rangeServer(t *testing.T, body []byte, support bool, changeETag bool) *httptest.Server {
	t.Helper()
	var requests atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		if changeETag && requests.Add(1) == 1 {
			w.Header().Set("ETag", `"old"`)
		} else {
			w.Header().Set("ETag", `"same"`)
		}
		rng := r.Header.Get("Range")
		if !support || rng == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		end = min(end, int64(len(body))-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start : end+1])
	}))
}

func TestDirectRangedMultipleChannelsAndResume(t *testing.T) {
	body := accelVideo(35<<20 + 123)
	srv := rangeServer(t, body, true, false)
	defer srv.Close()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	part := out + ".part"
	prefix := 3<<20 + 17
	if err := os.WriteFile(part, body[:prefix], 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	phoneBytes, accelBytes := int64(0), int64(0)
	enabled := true
	base := func(ctx context.Context, u, rng string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		return http.DefaultClient.Do(req)
	}
	_, err := Download(context.Background(), Options{
		Fetch: base, URL: srv.URL, Kind: "mp4", WorkDir: dir, Out: out,
		Accel: &Acceleration{
			Enabled: func() bool { return enabled },
			Channels: func() []Channel {
				return []Channel{{ID: "box", Fetch: base}, {ID: "phone-a", Phone: true, Fetch: base}, {ID: "phone-b", Phone: true, Fetch: base}}
			},
			OnTraffic: func(tr Traffic) {
				mu.Lock()
				if tr.Phone {
					phoneBytes += tr.Bytes
				}
				if tr.Accel {
					accelBytes += tr.Bytes
				}
				mu.Unlock()
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("分段结果不一致: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if phoneBytes == 0 || accelBytes == 0 {
		t.Fatalf("没有分别统计到手机和加速流量: phone=%d accel=%d", phoneBytes, accelBytes)
	}
}

func TestDirectRangedFallbackAndIdentity(t *testing.T) {
	for _, support := range []bool{false, true} {
		t.Run(fmt.Sprintf("support=%v", support), func(t *testing.T) {
			body := accelVideo(2<<20 + 31)
			srv := rangeServer(t, body, support, support)
			defer srv.Close()
			dir := t.TempDir()
			out := filepath.Join(dir, "out.mp4")
			base := func(ctx context.Context, u, rng string) (*http.Response, error) {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
				if rng != "" {
					req.Header.Set("Range", rng)
				}
				return http.DefaultClient.Do(req)
			}
			var supported atomic.Bool
			supported.Store(true)
			_, err := Download(context.Background(), Options{Fetch: base, URL: srv.URL, Kind: "mp4", WorkDir: dir, Out: out,
				Accel: &Acceleration{Enabled: func() bool { return true }, OnSupport: supported.Store}})
			if !support {
				if err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(out)
				if !bytes.Equal(got, body) || supported.Load() {
					t.Fatal("不支持 Range 时应回退并明确标记")
				}
			} else if err == nil || !strings.Contains(err.Error(), "版本变了") {
				t.Fatalf("文件指纹变化应拒绝拼接: %v", err)
			}
		})
	}
}

func TestDynamicPhoneFailureMovesChunk(t *testing.T) {
	body := accelVideo(10 << 20)
	srv := rangeServer(t, body, true, false)
	defer srv.Close()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	base := func(ctx context.Context, u, rng string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		req.Header.Set("Range", rng)
		return http.DefaultClient.Do(req)
	}
	_, err := Download(context.Background(), Options{Fetch: base, URL: srv.URL, Kind: "mp4", WorkDir: dir, Out: out,
		Accel: &Acceleration{Enabled: func() bool { return true }, Channels: func() []Channel {
			return []Channel{{ID: "box", Fetch: base}, {ID: "phone-a", Phone: true, Fetch: func(context.Context, string, string) (*http.Response, error) {
				return nil, io.ErrUnexpectedEOF
			}}}
		}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, body) {
		t.Fatal("手机失败后换通道的结果不一致")
	}
}

func TestDirectAccelerationCanEnableMidDownload(t *testing.T) {
	body := accelVideo(40 << 20)
	var enabled atomic.Bool
	var phoneBytes atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var current, maxActive atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		end = min(end, int64(len(body))-1)
		if start == 0 && end > 1024 {
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		n := current.Add(1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		defer current.Add(-1)
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("ETag", `"same"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start : end+1])
	}))
	defer srv.Close()
	base := func(ctx context.Context, u, rng string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		req.Header.Set("Range", rng)
		return http.DefaultClient.Do(req)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	done := make(chan error, 1)
	go func() {
		_, err := Download(context.Background(), Options{Fetch: base, URL: srv.URL, Kind: "mp4", WorkDir: dir, Out: out,
			Accel: &Acceleration{Enabled: enabled.Load, Channels: func() []Channel {
				return []Channel{{ID: "box", Fetch: base}, {ID: "phone-a", Phone: true, Fetch: base}}
			}, OnTraffic: func(tr Traffic) {
				if tr.Phone {
					phoneBytes.Add(tr.Bytes)
				}
			}}})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("下载没有开始")
	}
	enabled.Store(true)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("动态加速没有及时结束")
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, body) || maxActive.Load() < 2 || phoneBytes.Load() == 0 {
		t.Fatalf("中途加速没有正确加入: max=%d phone=%d equal=%v", maxActive.Load(), phoneBytes.Load(), bytes.Equal(got, body))
	}
}

func TestDynamicSharesTaskSlots(t *testing.T) {
	slots := make(chan struct{}, 4)
	var current, maxActive atomic.Int64
	work := func(context.Context, int, Fetcher) error {
		n := current.Add(1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		time.Sleep(10 * time.Millisecond)
		current.Add(-1)
		return nil
	}
	newAccel := func() *Acceleration {
		return &Acceleration{Enabled: func() bool { return true }, Slots: slots}
	}
	items := make([]int, 30)
	for i := range items {
		items[i] = i
	}
	done := make(chan error, 2)
	go func() { done <- runDynamic(context.Background(), newAccel(), items, nil, work) }()
	go func() { done <- runDynamic(context.Background(), newAccel(), items, nil, work) }()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if maxActive.Load() > 4 {
		t.Fatalf("两集共享额度被突破: %d", maxActive.Load())
	}
}
