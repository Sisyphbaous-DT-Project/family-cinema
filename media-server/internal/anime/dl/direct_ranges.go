package dl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const directChunkSize = 8 << 20

// directRanged 把完整视频切成若干 8MiB 小块。没开加速时只有盒子一个工人；
// 打开加速后，同一集的未完成小块会动态分给盒子和在线手机，不需要停掉原任务。
func directRanged(ctx context.Context, opt Options, u string) (func() error, error) {
	part := opt.Out + ".part"
	probeCtx, cancelProbe := context.WithTimeout(ctx, 30*time.Second)
	probe, err := opt.Fetch(probeCtx, u, "bytes=0-0")
	cancelProbe()
	if err != nil {
		opt.Accel.support(false)
		return direct(ctx, opt, u)
	}
	total := rangeTotal(probe.Header.Get("Content-Range"))
	etag := probe.Header.Get("ETag")
	html := strings.Contains(strings.ToLower(probe.Header.Get("Content-Type")), "text/html")
	probe.Body.Close()
	if probe.StatusCode != http.StatusPartialContent || total <= 0 || html {
		opt.Accel.support(false)
		return direct(ctx, opt, u)
	}
	opt.Accel.support(true)

	var have int64
	if st, statErr := os.Stat(part); statErr == nil {
		have = st.Size()
	}
	if have >= total {
		if have > total {
			if err := os.Truncate(part, 0); err != nil {
				return nil, err
			}
			have = 0
		} else {
			return finishDirect(ctx, opt, part, total)
		}
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := f.Truncate(total); err != nil {
		return nil, err
	}

	type chunk struct {
		start, end int64
		done       atomic.Int64
	}
	chunks := []*chunk{{start: 0, end: min(have, total) - 1}}
	chunks[0].done.Store(have)
	for start := have; start < total; {
		end := min(start+directChunkSize, total) - 1
		chunks = append(chunks, &chunk{start: start, end: end})
		start = end + 1
	}
	items := make([]int, 0, len(chunks))
	var bytesDone atomic.Int64
	for i, c := range chunks {
		if c.done.Load() < c.end-c.start+1 {
			items = append(items, i)
		}
		bytesDone.Add(c.done.Load())
	}
	report := func() {
		n := bytesDone.Load()
		opt.OnProgress(Progress{Done: int(n >> 20), Total: int(total >> 20), Bytes: n})
	}
	report()

	downloadChunk := func(cctx context.Context, index int, fetch Fetcher) error {
		c := chunks[index]
		want := c.end - c.start + 1
		var lastErr error
		for attempt := 0; ; attempt++ {
			if err := cctx.Err(); err != nil {
				return err
			}
			done := c.done.Load()
			if done >= want {
				return nil
			}
			rng := fmt.Sprintf("bytes=%d-%d", c.start+done, c.end)
			actx, cancelAttempt := context.WithCancel(cctx)
			resp, err := fetch(actx, u, rng)
			if err == nil && etag != "" && resp.Header.Get("ETag") != etag {
				resp.Body.Close()
				err = errors.New("分段文件的版本变了，为避免拼错，请重新下载这一集")
				resp = nil
			}
			if err == nil && (resp.StatusCode != http.StatusPartialContent || resp.ContentLength != want-done) {
				resp.Body.Close()
				err = fmt.Errorf("分段响应不对（HTTP %d）", resp.StatusCode)
				resp = nil
			}
			if err == nil {
				written := done
				lastReport := time.Now()
				idle := time.AfterFunc(time.Minute, cancelAttempt)
				var slow *time.Timer
				if opt.Accel.enabled() && attempt < 2 {
					slow = time.AfterFunc(8*time.Second, func() {
						if c.done.Load()-done < 1<<20 {
							cancelAttempt()
						}
					})
				}
				buf := make([]byte, 256<<10)
				for written < want {
					n, readErr := resp.Body.Read(buf)
					if n > 0 {
						idle.Reset(time.Minute)
						wn, writeErr := f.WriteAt(buf[:n], c.start+written)
						written += int64(wn)
						c.done.Store(written)
						bytesDone.Add(int64(wn))
						if writeErr != nil {
							err = writeErr
							break
						}
						if wn != n {
							err = io.ErrShortWrite
							break
						}
						if time.Since(lastReport) > 500*time.Millisecond {
							lastReport = time.Now()
							report()
						}
					}
					if readErr != nil {
						if readErr != io.EOF {
							err = readErr
						}
						break
					}
				}
				resp.Body.Close()
				idle.Stop()
				if slow != nil {
					slow.Stop()
				}
				if err == nil && written != want {
					err = errors.New("分段下载不完整")
				}
			}
			cancelAttempt()
			report()
			if err == nil {
				return nil
			}
			lastErr = err
			if cctx.Err() != nil {
				return cctx.Err()
			}
			if attempt >= len(retryDelays) {
				return fmt.Errorf("第 %d 块下载失败：%w", index+1, lastErr)
			}
			select {
			case <-time.After(retryDelays[attempt]):
			case <-cctx.Done():
				return cctx.Err()
			}
		}
	}
	if err := runDynamic(ctx, opt.Accel, items, opt.Fetch, downloadChunk); err != nil {
		return nil, err
	}
	report()
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return finishDirect(ctx, opt, part, total)
}

// rangeTotal 从 "bytes 0-0/362127172" 取完整文件大小。
func rangeTotal(contentRange string) int64 {
	i := strings.LastIndexByte(contentRange, '/')
	if i < 0 {
		return 0
	}
	n, _ := strconv.ParseInt(contentRange[i+1:], 10, 64)
	return n
}

func finishDirect(ctx context.Context, opt Options, part string, got int64) (func() error, error) {
	switch videoKind(part) {
	case "mp4":
		if err := os.Rename(part, opt.Out); err != nil {
			return nil, err
		}
		return nil, nil
	case "ts":
		return func() error {
			p := Progress{Done: int(got >> 20), Total: int(got >> 20), Bytes: got}
			release, err := opt.mergeTurn(ctx, p)
			if err != nil {
				return err
			}
			defer release()
			p.Merging = true
			opt.OnProgress(p)
			if err := remuxTS([]string{part}, opt.Out, mergeReporter(opt, p)); err != nil {
				return err
			}
			return os.Remove(part)
		}, nil
	case "flv":
		return nil, errors.New("这个站给的是 flv 格式的视频，浏览器和手机都放不了，暂时不支持下载")
	default:
		return nil, errors.New("下载下来的不是视频文件")
	}
}
