//go:build live

package dl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"
)

// 联网实测：在盒子上真下一集（看速度、内存、拼出来的文件对不对）。默认不跑。
//
//	GOOS=linux GOARCH=arm GOARM=7 go test -c -tags live -o dl.arm ./internal/anime/dl
//	盒子上：LIVE_URL=<视频地址> LIVE_KIND=hls|mp4 LIVE_REF=<Referer> LIVE_OUT=<输出 mp4> ./dl.arm -test.run Live -test.v -test.timeout 2h
func TestLiveEpisode(t *testing.T) {
	u, out := os.Getenv("LIVE_URL"), os.Getenv("LIVE_OUT")
	if u == "" || out == "" {
		t.Skip("没给 LIVE_URL / LIVE_OUT")
	}
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "udp", "223.5.5.5:53")
		}}
	}
	pool := x509.NewCertPool()
	if b, err := os.ReadFile("/data/server/pansou/cacert.pem"); err == nil {
		pool.AppendCertsFromPEM(b)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	ref := os.Getenv("LIVE_REF")
	fetch := func(ctx context.Context, rawURL, rng string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		if ref != "" {
			req.Header.Set("Referer", ref)
		}
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		return client.Do(req)
	}
	start := time.Now()
	var last time.Time
	var peak uint64
	var mergeAt time.Time
	err := Episode(context.Background(), Options{
		Fetch: fetch, URL: u, Kind: os.Getenv("LIVE_KIND"), WorkDir: out + ".work", Out: out,
		OnProgress: func(p Progress) {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.Sys > peak {
				peak = ms.Sys
			}
			if p.Merging && mergeAt.IsZero() {
				mergeAt = time.Now()
				t.Logf("下载完 %.0f 秒，%.1fMB，开始拼接", time.Since(start).Seconds(), float64(p.Bytes)/1e6)
			}
			if time.Since(last) > 20*time.Second {
				last = time.Now()
				t.Logf("%.0fs 进度 %d/%d %.1fMB 平均 %.2fMB/s 内存 %.0fMB", time.Since(start).Seconds(), p.Done, p.Total,
					float64(p.Bytes)/1e6, float64(p.Bytes)/1e6/time.Since(start).Seconds(), float64(ms.Sys)/1e6)
			}
		},
	})
	if err != nil {
		t.Fatalf("失败：%v", err)
	}
	st, _ := os.Stat(out)
	merge := 0.0
	if !mergeAt.IsZero() {
		merge = time.Since(mergeAt).Seconds()
	}
	t.Logf("完成：总 %.0f 秒（拼接 %.0f 秒），文件 %.1fMB，内存峰值约 %.0fMB", time.Since(start).Seconds(), merge, float64(st.Size())/1e6, float64(peak)/1e6)
	os.RemoveAll(out + ".work")
}
