package dl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Size 一集有多大（下载前给用户看的参考值）
type Size struct {
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds,omitempty"` // 视频时长，分段视频才有
	Exact   bool    `json:"exact"`             // true = 服务器给的准确大小；false = 抽样估算
}

const (
	sampleSegs  = 32  // 问不出每段大小时抽几段估
	maxFullSegs = 900 // 分段太多（两三个小时的总集篇）就不挨个问了，抽样
	sizeWorkers = 8   // 挨个问分段大小时同时问几个
)

// Measure 不下载整集，量出一集大概多大。
//   - mp4：直接问服务器文件多大（准确）。
//   - m3u8：rate 为 0（这部番还没量过）时把每个分段的大小都问一遍加起来（准确，只问大小不下载）；
//     rate > 0（同一部番量过别的集，每秒多少字节）时只读清单拿时长，乘码率估算——
//     实测同一部番各集码率只差几个百分点，而分段大小能差上百倍，抽几段估反而不准。
func Measure(ctx context.Context, fetch Fetcher, u, kind string, adBlock bool, rate float64) (Size, error) {
	if kind == "mp4" {
		return remoteSize(ctx, fetch, u, false)
	}
	body, err := fetchPlaylist(ctx, fetch, u)
	if errors.Is(err, errNotM3U8) {
		return remoteSize(ctx, fetch, u, false)
	}
	if err != nil {
		return Size{}, err
	}
	base, err := url.Parse(u)
	if err != nil {
		return Size{}, err
	}
	if isMaster(body) {
		sub, err := bestVariant(body, base)
		if err != nil {
			return Size{}, err
		}
		if body, err = fetchPlaylist(ctx, fetch, sub); err != nil {
			return Size{}, err
		}
		base, _ = url.Parse(sub)
	}
	segs, err := parseMedia(body, base)
	if err != nil {
		return Size{}, err
	}
	if segs, err = expandNested(ctx, fetch, segs); err != nil {
		return Size{}, err
	}
	if adBlock {
		segs = filterAds(body, base, segs)
	}
	if len(segs) == 0 {
		return Size{}, errors.New("播放清单里没有视频分段")
	}
	total := 0.0
	for _, s := range segs {
		total += s.Dur
	}
	if rate > 0 && total > 0 {
		return Size{Bytes: int64(rate * total), Seconds: total}, nil
	}
	if len(segs) <= maxFullSegs {
		if n, err := sumSegments(ctx, fetch, segs); err == nil {
			return Size{Bytes: n, Seconds: total, Exact: true}, nil
		} else if ctx.Err() != nil {
			return Size{}, err
		}
	}
	return sampleSize(ctx, fetch, segs, total)
}

// sumSegments 把每个分段的大小都问一遍（只要第 1 个字节，看总长）；有一段问不出就放弃
func sumSegments(ctx context.Context, fetch Fetcher, segs []Segment) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		sum      int64
		firstErr error
		wg       sync.WaitGroup
		next     atomic.Int64
	)
	for range min(sizeWorkers, len(segs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(segs) || ctx.Err() != nil {
					return
				}
				b, err := remoteSize(ctx, fetch, segs[i].URI, false)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				sum += b.Bytes
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return 0, firstErr
	}
	return sum, nil
}

// sampleSize 均匀抽几段量大小，按时长折算（问不出每段大小、或分段太多时用）
func sampleSize(ctx context.Context, fetch Fetcher, segs []Segment, total float64) (Size, error) {
	var bytes int64
	var secs float64
	n := min(sampleSegs, len(segs))
	for i := 0; i < n; i++ {
		s := segs[(i+1)*len(segs)/(n+1)]
		b, err := remoteSize(ctx, fetch, s.URI, true)
		if err != nil {
			return Size{}, fmt.Errorf("量分段大小失败：%w", err)
		}
		bytes += b.Bytes
		secs += s.Dur
	}
	est := bytes * int64(len(segs)) / int64(n) // 清单没写时长时按段数折算
	if total > 0 && secs > 0 {
		est = int64(float64(bytes) / secs * total)
	}
	return Size{Bytes: est, Seconds: total}, nil
}

// remoteSize 问服务器一个文件多大：先要第 1 个字节看 Content-Range 里的总长，
// 服务器不认 Range 就看 Content-Length；都没有时，分段（count=true，一般只有一两 MB）就读完数一下，整集视频不读
func remoteSize(ctx context.Context, fetch Fetcher, u string, count bool) (Size, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := fetch(cctx, u, "bytes=0-0")
	if err != nil {
		return Size{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.Header.Get("Content-Range")
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if n, err := strconv.ParseInt(strings.TrimSpace(cr[i+1:]), 10, 64); err == nil && n > 0 {
				return Size{Bytes: n, Exact: true}, nil
			}
		}
		return Size{}, errSizeUnknown
	case http.StatusOK:
		if resp.ContentLength > 0 {
			return Size{Bytes: resp.ContentLength, Exact: true}, nil
		}
	default:
		return Size{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !count {
		return Size{}, errSizeUnknown
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return Size{}, err
	}
	if n == 0 {
		return Size{}, errSizeUnknown
	}
	return Size{Bytes: n, Exact: true}, nil
}

var errSizeUnknown = errors.New("服务器没说文件多大")
