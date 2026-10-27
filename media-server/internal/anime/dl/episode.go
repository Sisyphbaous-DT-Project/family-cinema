package dl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"media-server/internal/anime/hls"
)

// Fetcher 取一个地址（自动带上该站要的 Referer/UA/Cookie，并挡掉内网地址）。rng 为空表示取整个。
type Fetcher func(ctx context.Context, rawURL, rng string) (*http.Response, error)

// Progress 一集的下载进度
type Progress struct {
	MergeWait       bool  // 下完了，排队等着拼（同一时间只拼一集）
	Merging         bool  // 下完了，正在拼成 mp4
	MergeDone       int64 // 拼接已读取的分段字节
	MergeTotal      int64 // 分段总字节
	MergeFinalizing bool  // 正在写 MP4 索引并落盘
	Done            int   // 已下好的分段数（整文件下载时：已下字节）
	Total           int   // 分段总数（整文件下载时：总字节，0 表示不知道）
	Bytes           int64 // 已下载的字节数
}

// Options 一集的下载参数
type Options struct {
	Fetch      Fetcher
	URL        string // 解析出来的视频地址（m3u8 或 mp4）
	Kind       string // 解析时判断的类型：hls / mp4（只是提示，以实际内容为准）
	AdBlock    bool   // 规则开了去广告：过滤 m3u8 里的广告段
	WorkDir    string // 这一集的临时目录（分段存这里，重下时已下好的分段不再下）
	Out        string // 下好的 mp4 写到这里
	Parallel   int    // 同时下几段，默认 3（和 Kazumi 一样）
	OnProgress func(Progress)
	// MergeSlot 拼接要排队时传一个容量为 1 的通道（几集同时下载、共用一个）：
	// 拼接主要在读写硬盘，盒子的移动硬盘两集同时拼反而更慢、整台盒子都会卡；nil 表示不排队
	MergeSlot chan struct{}
}

// mergeTurn 轮到自己拼接（排队时先报"等着拼"）；返回的函数用完要调，把位置让给下一集
func (opt Options) mergeTurn(ctx context.Context, p Progress) (func(), error) {
	if opt.MergeSlot == nil {
		return func() {}, nil
	}
	select {
	case opt.MergeSlot <- struct{}{}:
	default:
		p.MergeWait = true
		opt.OnProgress(p)
		select {
		case opt.MergeSlot <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return func() { <-opt.MergeSlot }, nil
}

const (
	maxPlaylistBytes = 2 << 20
	segTimeout       = 90 * time.Second
)

// retryDelays 每段最多试 3 次，失败后分别等 1、3 秒再试（和 Kazumi 的 1/3/9 秒同思路，最后一次失败就放弃）
var retryDelays = []time.Duration{time.Second, 3 * time.Second}

// Episode 下载并拼好一集，成功后 opt.Out 是一个完整的 mp4。
func Episode(ctx context.Context, opt Options) error {
	merge, err := Download(ctx, opt)
	if err != nil || merge == nil {
		return err
	}
	return merge()
}

// Download 只负责把一集下载齐，返回随后要做的拼接；整文件 MP4 不需要拼接，返回 nil。
// 多集同时下载时，调用方可以把拼接交给另一个队列，下载工位就能直接领下一集。
func Download(ctx context.Context, opt Options) (func() error, error) {
	if opt.Parallel <= 0 {
		opt.Parallel = 3
	}
	if opt.OnProgress == nil {
		opt.OnProgress = func(Progress) {}
	}
	if err := os.MkdirAll(opt.WorkDir, 0o755); err != nil {
		return nil, err
	}
	if opt.Kind == "mp4" {
		return direct(ctx, opt, opt.URL)
	}
	body, err := fetchPlaylist(ctx, opt.Fetch, opt.URL)
	if errors.Is(err, errNotM3U8) {
		// 解析时以为是 m3u8，实际是整个视频文件：退回整文件下载（Kazumi 也是这样）
		return direct(ctx, opt, opt.URL)
	}
	if err != nil {
		return nil, err
	}
	return hlsDownload(ctx, opt, body)
}

var errNotM3U8 = errors.New("不是 m3u8")

// fetchPlaylist 取 m3u8 文本；内容不是 m3u8（或大得不像清单）就返回 errNotM3U8
func fetchPlaylist(ctx context.Context, fetch Fetcher, u string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := fetch(cctx, u, "")
	if err != nil {
		return "", fmt.Errorf("取播放清单失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("取播放清单失败（HTTP %d）", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistBytes+1))
	if err != nil {
		return "", fmt.Errorf("取播放清单失败：%w", err)
	}
	if len(b) > maxPlaylistBytes || !hls.IsM3U8(b) {
		return "", errNotM3U8
	}
	return string(b), nil
}

func hlsDownload(ctx context.Context, opt Options, body string) (func() error, error) {
	base, err := url.Parse(opt.URL)
	if err != nil {
		return nil, err
	}
	if isMaster(body) {
		sub, err := bestVariant(body, base)
		if err != nil {
			return nil, err
		}
		if body, err = fetchPlaylist(ctx, opt.Fetch, sub); err != nil {
			return nil, err
		}
		base, _ = url.Parse(sub)
	}
	segs, err := parseMedia(body, base)
	if err != nil {
		return nil, err
	}
	if segs, err = expandNested(ctx, opt.Fetch, segs); err != nil {
		return nil, err
	}
	if opt.AdBlock {
		segs = filterAds(body, base, segs)
	}
	if len(segs) == 0 {
		return nil, errors.New("播放清单里没有可下载的视频分段")
	}

	keys, err := fetchKeys(ctx, opt.Fetch, segs)
	if err != nil {
		return nil, err
	}
	fmp4 := segs[0].Map != ""
	paths := make([]string, len(segs))
	for i := range segs {
		paths[i] = filepath.Join(opt.WorkDir, fmt.Sprintf("seg_%05d", i))
	}

	// 已下好的分段不再下（Kazumi 的断点续下思路）
	var done atomic.Int64
	var bytesDone atomic.Int64
	var todo []int
	for i, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			done.Add(1)
			bytesDone.Add(st.Size())
		} else {
			todo = append(todo, i)
		}
	}
	report := func() {
		opt.OnProgress(Progress{Done: int(done.Load()), Total: len(segs), Bytes: bytesDone.Load()})
	}
	report()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, opt.Parallel)
	)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, i := range todo {
		select {
		case sem <- struct{}{}:
		case <-cctx.Done():
		}
		if cctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			n, err := fetchSegment(cctx, opt.Fetch, segs[i], keys, paths[i], !fmp4)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("第 %d 段下载失败：%w", i+1, err)
				}
				mu.Unlock()
				cancel() // 有一段怎么都下不下来，整集就算失败，别的段也不用下了
				return
			}
			done.Add(1)
			bytesDone.Add(n)
			report()
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}

	p := Progress{Done: len(segs), Total: len(segs), Bytes: bytesDone.Load()}
	return func() error {
		release, err := opt.mergeTurn(ctx, p)
		if err != nil {
			return err
		}
		defer release()
		p.Merging = true
		opt.OnProgress(p)
		if fmp4 {
			return concatFMP4(ctx, opt, segs[0].Map, paths, mergeReporter(opt, p))
		}
		return remuxTS(paths, opt.Out, mergeReporter(opt, p))
	}, nil
}

// expandNested 有的源把真正的内容又套了一层 m3u8（分段地址本身是 .m3u8），展开一层
func expandNested(ctx context.Context, fetch Fetcher, segs []Segment) ([]Segment, error) {
	var out []Segment
	for _, s := range segs {
		u, err := url.Parse(s.URI)
		if err != nil || !strings.HasSuffix(strings.ToLower(path.Ext(u.Path)), ".m3u8") {
			out = append(out, s)
			continue
		}
		body, err := fetchPlaylist(ctx, fetch, s.URI)
		if err != nil {
			return nil, err
		}
		inner, err := parseMedia(body, u)
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// filterAds 用播放代理同一套去广告逻辑（hls.FilterAds）过滤，只留下正片分段；
// 按地址对回原来解析出的分段，保住每段的序号和密钥（解密要用）
func filterAds(body string, base *url.URL, segs []Segment) []Segment {
	kept, err := parseMedia(string(hls.FilterAds([]byte(body))), base)
	if err != nil || len(kept) == 0 || len(kept) == len(segs) {
		return segs
	}
	keep := make(map[string]bool, len(kept))
	for _, s := range kept {
		keep[s.URI] = true
	}
	var out []Segment
	for _, s := range segs {
		if keep[s.URI] {
			out = append(out, s)
		}
	}
	return out
}

// fetchKeys 把用到的密钥都先下好（一般整集就一个）
func fetchKeys(ctx context.Context, fetch Fetcher, segs []Segment) (map[string][]byte, error) {
	keys := map[string][]byte{}
	for _, s := range segs {
		if s.Key == nil || keys[s.Key.URI] != nil {
			continue
		}
		b, err := fetchAll(ctx, fetch, s.Key.URI, 4096)
		if err != nil {
			return nil, fmt.Errorf("取解密密钥失败：%w", err)
		}
		if len(b) != 16 {
			return nil, errors.New("解密密钥格式不对")
		}
		keys[s.Key.URI] = b
	}
	return keys, nil
}

// fetchSegment 下一段（失败按 retryDelays 重试），解密、去伪装头后写到 dst，返回写入的字节数
func fetchSegment(ctx context.Context, fetch Fetcher, s Segment, keys map[string][]byte, dst string, ts bool) (int64, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		var n int64
		var err error
		if s.Key == nil {
			n, err = streamSegment(ctx, fetch, s.URI, dst, ts)
		} else {
			var b []byte
			if b, err = fetchAll(ctx, fetch, s.URI, maxSegBytes); err == nil {
				b, err = decryptAES128(b, keys[s.Key.URI], s.Key.IV, s.Seq)
			}
			if err == nil && ts {
				b = stripToTS(b)
			}
			if err == nil {
				n, err = int64(len(b)), writeAtomic(dst, b)
			}
		}
		if err == nil {
			return n, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if attempt >= len(retryDelays) {
			return 0, lastErr
		}
		select {
		case <-time.After(retryDelays[attempt]):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

const maxSegBytes = 64 << 20

// streamSegment 不加密的分段边收边写进临时文件，写完再改名。不在内存里攒整段：
// 有的片源一段就二十多 MB，两集同时下、每集同时拉几段，全读进内存会把盒子（剩余内存一百多 MB）吃光
func streamSegment(ctx context.Context, fetch Fetcher, u, dst string, ts bool) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, segTimeout)
	defer cancel()
	resp, err := fetch(cctx, u, "")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body := io.LimitReader(resp.Body, maxSegBytes+1)
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	var n int64
	if ts {
		// 伪装成图片的分段前面有一小段假文件头：先读开头一小块，从 TS 同步字节那里开始写
		head := make([]byte, 64<<10)
		hn, err := io.ReadFull(body, head)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			f.Close()
			os.Remove(tmp)
			return 0, err
		}
		h := stripToTS(head[:hn])
		w, err := f.Write(h)
		n += int64(w)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return 0, err
		}
	}
	m, err := io.Copy(f, body)
	n += m
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case n > maxSegBytes:
		err = errors.New("内容大得不正常")
	case n == 0:
		err = errors.New("内容是空的")
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, os.Rename(tmp, dst)
}

// fetchAll 取整个内容（单次请求限时 segTimeout，最多 limit 字节）
func fetchAll(ctx context.Context, fetch Fetcher, u string, limit int64) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, segTimeout)
	defer cancel()
	resp, err := fetch(cctx, u, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var b []byte
	if cl := resp.ContentLength; cl > 0 && cl <= limit {
		// 知道多大就一次分配好（io.ReadAll 边读边翻倍扩容，大分段会多占一倍内存）
		b = make([]byte, cl)
		_, err = io.ReadFull(resp.Body, b)
	} else {
		b, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	}
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("内容大得不正常")
	}
	if len(b) == 0 {
		return nil, errors.New("内容是空的")
	}
	return b, nil
}

// writeAtomic 先写临时文件再改名：下到一半断了不会留下"看着下好了其实是半截"的分段
func writeAtomic(dst string, b []byte) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// concatFMP4 fMP4 分段：初始化段 + 各分段直接首尾相接就是一个能播的 mp4，不用重新封装
func concatFMP4(ctx context.Context, opt Options, mapURI string, paths []string, report func(MergeProgress)) error {
	init, err := fetchAll(ctx, opt.Fetch, mapURI, 16<<20)
	if err != nil {
		return fmt.Errorf("取视频头失败：%w", err)
	}
	f, err := os.Create(opt.Out)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(init); err != nil {
		return err
	}
	r := &filesReader{paths: paths}
	defer r.Close()
	total := mergeBytes(paths)
	pr := &mergeProgressReader{r: r, total: total, report: report}
	if _, err := io.CopyBuffer(f, pr, make([]byte, 1<<20)); err != nil {
		return err
	}
	reportMerge(report, MergeProgress{Done: total, Total: total, Finalizing: true})
	return f.Sync()
}

// direct 整个视频文件下载：支持断点续下（.part 已有的部分用 Range 接着下），一分钟没有新数据算卡住。
// 如果实际是整个 TS，下载齐后返回拼接函数；MP4 不需要拼接，返回 nil。
func direct(ctx context.Context, opt Options, u string) (func() error, error) {
	part := opt.Out + ".part"
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rng := ""
	if have > 0 {
		rng = fmt.Sprintf("bytes=%d-", have)
	}
	resp, err := opt.Fetch(cctx, u, rng)
	if err != nil {
		return nil, fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	flag := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flag |= os.O_APPEND
	case http.StatusOK:
		have = 0 // 站点不支持续传，从头下
		flag |= os.O_TRUNC
	case http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part) // 已下的比文件还大，删了重下（Kazumi 遇到 416 也是这样）
		return direct(ctx, opt, u)
	default:
		return nil, fmt.Errorf("下载失败（HTTP %d）", resp.StatusCode)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "text/html") {
		return nil, errors.New("拿到的是网页不是视频")
	}
	total := have + resp.ContentLength
	if resp.ContentLength < 0 {
		total = 0
	}
	f, err := os.OpenFile(part, flag, 0o644)
	if err != nil {
		return nil, err
	}
	// 看门狗：一分钟没有新数据就断开
	idle := time.AfterFunc(time.Minute, cancel)
	defer idle.Stop()
	buf := make([]byte, 256<<10)
	got := have
	lastReport := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(time.Minute)
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return nil, err
			}
			got += int64(n)
			if time.Since(lastReport) > 500*time.Millisecond {
				lastReport = time.Now()
				opt.OnProgress(Progress{Done: int(got >> 20), Total: int(total >> 20), Bytes: got})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("下载中断：%w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if total > 0 && got < total {
		return nil, errors.New("下载不完整")
	}
	switch videoKind(part) {
	case "mp4":
		if err := os.Rename(part, opt.Out); err != nil {
			return nil, err
		}
		return nil, nil
	case "ts":
		// 整个文件其实是 TS：也拼成 mp4，浏览器和手机才放得了
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

// videoKind 看文件开头判断容器格式：mp4 / ts / flv，都不像就返回空（多半是报错网页）
func videoKind(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case n >= 8 && bytes.Equal(head[4:8], []byte("ftyp")):
		return "mp4"
	case n > 0 && head[0] == 0x47:
		return "ts"
	case bytes.HasPrefix(head, []byte("FLV")):
		return "flv"
	}
	return ""
}
