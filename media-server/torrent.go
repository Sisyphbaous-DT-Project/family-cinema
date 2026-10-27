package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	dht "github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

// 公共 tracker 池。tracker 越多，能捞到的 peer 越多，速度才上得去。
// 每个内层切片是一个「tier」，同一 tier 内按顺序尝试，不同 tier 全部并行 announce。
// 这里刻意把每个 tracker 单独成 tier，让它们同时干活而不是排队。
// UDP tracker 的主机名解析已经由 cc.LookupTrackerIp 接到自定义 DNS 上（Android 无本地 DNS）。
var extraTrackers = [][]string{
	{"http://tracker.opentrackr.org:1337/announce"},
	{"udp://tracker.opentrackr.org:1337/announce"},
	{"udp://open.tracker.cl:1337/announce"},
	{"udp://open.demonii.com:1337/announce"},
	{"udp://tracker.torrent.eu.org:451/announce"},
	{"udp://exodus.desync.com:6969/announce"},
	{"udp://tracker.dler.org:6969/announce"},
	{"udp://explodie.org:6969/announce"},
	{"udp://tracker1.bt.moack.co.kr:80/announce"},
	{"udp://tracker.tiny-vps.com:6969/announce"},
	{"udp://open.stealth.si:80/announce"},
	{"udp://tracker.theoks.net:6969/announce"},
	{"http://tracker.dler.org:6969/announce"},
	{"http://open.acgtracker.com:1096/announce"},
	{"http://t.nyaatracker.com/announce"},
	{"https://tr.bangumi.moe:9696/announce"},
}

// addTrackers 给种子追加直连 tracker，确保能拿到 peers
func addTrackers(t *torrent.Torrent) {
	t.AddTrackers(extraTrackers)
}

// SpeedResult 测速结果
type SpeedResult struct {
	MBps    float64 `json:"mbps"`
	KBps    float64 `json:"kbps"`
	Peers   int     `json:"peers"`
	Size    string  `json:"size"`
	TimeMS  int64   `json:"timeMs"`
	Error   string  `json:"error,omitempty"`
	Torrent string  `json:"torrent,omitempty"` // 种子标题
}

// TorrentTask 下载任务
type TorrentTask struct {
	ID         string   `json:"id"`
	Magnet     string   `json:"magnet"`
	Name       string   `json:"name"`
	Category   string   `json:"category"` // movie | series
	TargetDir  string   `json:"targetDir"`
	Status     string   `json:"status"`          // running | processing | done | error | stopped
	Stage      string   `json:"stage,omitempty"` // 当前阶段说明（下载中/解压中/校验中）
	Progress   float64  `json:"progress"`
	SpeedKBs   float64  `json:"speedKBs"`
	BytesDone  int64    `json:"bytesDone"`
	BytesTotal int64    `json:"bytesTotal"`
	Peers      int      `json:"peers"`
	Files      []string `json:"files,omitempty"`
	// 番剧下载的逐集状态：前端按片源和作品匹配当前页面，File 统一为"第NN集.mp4"
	AnimeRule        string          `json:"animeRule,omitempty"`
	AnimeRef         string          `json:"animeRef,omitempty"`
	AnimeFiles       []string        `json:"animeFiles,omitempty"`
	AnimeFailedFiles []string        `json:"animeFailedFiles,omitempty"`
	AnimeFailures    []AnimeFailure  `json:"animeFailures,omitempty"` // 失败的那几集和原因（片库页显示重试按钮用）
	AnimeActive      []AnimeActiveEp `json:"animeActive,omitempty"`   // 正在处理的几集（同时下 2 集）
	Cover            string          `json:"cover,omitempty"`         // 用户指定的封面（已落盘）
	Manual           bool            `json:"manual,omitempty"`
	Error            string          `json:"error,omitempty"`
	CreatedAt        int64           `json:"createdAt"`
	FinishedAt       int64           `json:"finishedAt,omitempty"`
}

// btManager BT 全局管理（测速与下载共用一个 client）
type btManager struct {
	mu       sync.Mutex
	client   *torrent.Client
	started  bool
	tasks    map[string]*TorrentTask
	active   map[string]*activeDownload
	shutdown chan struct{}
}

type activeDownload struct {
	task *TorrentTask
	t    *torrent.Torrent
	stop chan struct{}
}

var bt *btManager

func initBT(cfg Config) error {
	bt = &btManager{
		tasks:    map[string]*TorrentTask{},
		active:   map[string]*activeDownload{},
		shutdown: make(chan struct{}),
	}
	if err := os.MkdirAll(cfg.CacheDir, 0755); err != nil {
		return err
	}
	cc := torrent.NewDefaultClientConfig()
	cc.DataDir = cfg.CacheDir
	// 固定监听端口，便于在路由器做端口映射（随机端口没法映射）
	cc.ListenPort = cfg.BTPort
	cc.NoDHT = cfg.BTNoDHT
	// BT 是互惠协议：完全不上传会被对端的 choke 算法判定为吸血，连接被掐死、peer 数上不去。
	// 所以开启上传但限速，既换到对端的带宽又不占满家里的上行。
	cc.NoUpload = false
	cc.Seed = false // 下载完就撒手，不长期做种
	if cfg.BTUploadKBs > 0 {
		lim := float64(cfg.BTUploadKBs) * 1024
		cc.UploadRateLimiter = rate.NewLimiter(rate.Limit(lim), int(lim))
	}
	// 让路由器把 BT 端口映射进来，peer 才能主动连我们（否则只能单向外连，可用 peer 少一半以上）
	cc.NoDefaultPortForwarding = !cfg.BTUPnP
	if cfg.BTMaxConns > 0 {
		cc.EstablishedConnsPerTorrent = cfg.BTMaxConns
		cc.HalfOpenConnsPerTorrent = cfg.BTMaxConns / 2
	}
	cc.Debug = cfg.BTDebug
	// 强制 HTTP tracker 直连（不走系统代理），国内网络下 opentrackr 等直连可达
	cc.HTTPProxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	// ==== 关键：Android 上 Go 默认解析器会退化成查 [::1]:53（本机无 DNS 服务），
	// 导致所有 tracker 域名解析失败、announce 根本发不出去。
	// 这里把自定义公共 DNS 注入到 BT 客户端的三条解析路径上。====
	// 1) HTTP tracker 的拨号
	cc.TrackerDialContext = dialWithDNS
	// 2) 抓取 metainfo / webseed 的拨号
	cc.HTTPDialContext = dialWithDNS
	// 3) UDP tracker：先把主机名解析成 IP 再交给它
	cc.LookupTrackerIp = func(u *url.URL) ([]net.IP, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return customDNS.LookupIP(ctx, "ip4", u.Hostname())
	}

	// DHT 引导节点同样要先解析成 IP，否则 DHT 起不来、拿不到 peers
	cc.DhtStartingNodes = func(network string) dht.StartingNodesGetter {
		return func() ([]dht.Addr, error) {
			udps, err := ResolveDHTBootstrap(network)
			if err != nil {
				log.Printf("DHT 引导节点解析失败: %v", err)
				return nil, err
			}
			addrs := make([]dht.Addr, 0, len(udps))
			for _, u := range udps {
				addrs = append(addrs, dht.NewAddr(u))
			}
			log.Printf("DHT 引导节点已解析 %d 个", len(addrs))
			return addrs, nil
		}
	}
	cl, err := torrent.NewClient(cc)
	if err != nil {
		return err
	}
	bt.client = cl
	bt.started = true
	log.Printf("BT 引擎已启动 (anacrolix/torrent), 缓存目录 %s", cfg.CacheDir)
	return nil
}

func closeBT() {
	if bt != nil && bt.client != nil {
		close(bt.shutdown)
		bt.client.Close()
	}
}

// SpeedTest 对磁力测速：下载采样 N 秒计算速度
func SpeedTest(magnet string, secs int) SpeedResult {
	if bt == nil || !bt.started {
		return SpeedResult{Error: "BT 引擎未启动"}
	}
	if secs <= 0 {
		secs = currentConfig().Speedtest
	}
	t, err := bt.client.AddMagnet(magnet)
	if err != nil {
		return SpeedResult{Error: err.Error()}
	}
	addTrackers(t)
	defer func() {
		// 测速结束后丢弃并清理缓存
		hash := t.InfoHash().HexString()
		t.Drop()
		if hash != "" {
			os.RemoveAll(filepath.Join(currentConfig().CacheDir, hash))
		}
	}()
	select {
	case <-t.GotInfo():
	case <-time.After(50 * time.Second):
		return SpeedResult{Error: "获取种子信息超时（可能无做种源）"}
	}
	info := t.Info()
	// 全部下载以便测速
	t.DownloadAll()
	start := time.Now()
	before := t.BytesCompleted()
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if t.BytesCompleted() >= info.TotalLength() && info.TotalLength() > 0 {
			break
		}
	}
	elapsed := time.Since(start).Seconds()
	got := t.BytesCompleted() - before
	if got < 0 {
		got = 0
	}
	sr := SpeedResult{
		MBps:    float64(got) / 1048576 / elapsed,
		KBps:    float64(got) / 1024 / elapsed,
		Peers:   t.Stats().ActivePeers,
		Size:    humanSize(info.TotalLength()),
		TimeMS:  int64(elapsed * 1000),
		Torrent: info.Name,
	}
	return sr
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// StartDownload 用磁力开始下载（新建条目）
func StartDownload(magnet, title, category string) (*TorrentTask, error) {
	return startTask(magnet, nil, title, category, "", false, "")
}

// StartDownloadFromTorrent 用 .torrent 文件内容开始下载
func StartDownloadFromTorrent(data []byte, title, category, cover, targetDir string) (*TorrentTask, error) {
	return startTask("", data, title, category, cover, true, targetDir)
}

// StartManualMagnet 手动添加磁力（带可选封面）
func StartManualMagnet(magnet, title, category, cover, targetDir string) (*TorrentTask, error) {
	return startTask(magnet, nil, title, category, cover, true, targetDir)
}

// startTask 落盘规则：
//   - targetDir 为空：新建 电影电视剧/<片名>/ 或 番剧/<片名>/
//   - targetDir 非空：直接落进这个已有目录（追番加新集用）
func startTask(magnet string, torrentData []byte, title, category, cover string, manual bool, targetDir string) (*TorrentTask, error) {
	if bt == nil || !bt.started {
		return nil, fmt.Errorf("BT 引擎未启动")
	}
	dir := targetDir
	if dir == "" {
		cfg := currentConfig()
		root := cfg.SeriesRoot
		if category == "movie" {
			root = cfg.MovieRoot
		}
		dir = filepath.Join(root, sanitizeDirName(title))
	}

	task := &TorrentTask{
		ID:        fmt.Sprintf("t%d", time.Now().UnixNano()/1e6),
		Magnet:    magnet,
		Name:      title,
		Category:  category,
		TargetDir: dir,
		Status:    "running",
		Manual:    manual,
		CreatedAt: time.Now().Unix(),
	}

	var t *torrent.Torrent
	var err error
	if len(torrentData) > 0 {
		mi, perr := metainfo.Load(bytes.NewReader(torrentData))
		if perr != nil {
			return nil, fmt.Errorf("种子文件解析失败: %v", perr)
		}
		t, err = bt.client.AddTorrent(mi)
	} else {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(magnet)), "magnet:") {
			return nil, fmt.Errorf("magnet 链接格式不正确")
		}
		t, err = bt.client.AddMagnet(magnet)
	}
	if err != nil {
		return nil, err
	}
	addTrackers(t)

	// 封面先落盘到目标目录（下载中也能在媒体库里显示）
	if cover != "" {
		if p, cerr := saveCover(dir, cover); cerr == nil {
			task.Cover = p
		} else {
			log.Printf("封面保存失败: %v", cerr)
		}
	}

	bt.mu.Lock()
	bt.tasks[task.ID] = task
	ad := &activeDownload{task: task, t: t, stop: make(chan struct{})}
	bt.active[task.ID] = ad
	bt.mu.Unlock()

	go func() {
		select {
		case <-t.GotInfo():
		case <-time.After(50 * time.Second):
			bt.mu.Lock()
			task.Status = "error"
			task.Error = "获取种子信息超时"
			delete(bt.active, task.ID)
			bt.mu.Unlock()
			t.Drop()
			return
		case <-ad.stop:
			return
		}
		info := t.Info()
		bt.mu.Lock()
		task.BytesTotal = info.TotalLength()
		task.Peers = t.Stats().ActivePeers
		bt.mu.Unlock()
		t.DownloadAll()
		// 进度刷新
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		lastDone := int64(0)
		lastAt := time.Now()
		for {
			select {
			case <-ticker.C:
				done := t.BytesCompleted()
				now := time.Now()
				dt := now.Sub(lastAt).Seconds()
				if dt > 0 {
					speed := float64(done-lastDone) / 1024 / dt
					lastDone, lastAt = done, now
					progress := 0.0
					if task.BytesTotal > 0 {
						progress = float64(done) / float64(task.BytesTotal) * 100
					}
					bt.mu.Lock()
					task.BytesDone = done
					task.SpeedKBs = speed
					task.Progress = progress
					task.Peers = t.Stats().ActivePeers
					bt.mu.Unlock()
					if done >= task.BytesTotal && task.BytesTotal > 0 {
						finishDownload(task.ID, ad)
						return
					}
				}
			case <-ad.stop:
				return
			}
		}
	}()

	return task, nil
}

// finishDownload 下载完成：移动文件到目标目录
func finishDownload(id string, ad *activeDownload) {
	task := ad.task
	t := ad.t
	bt.mu.Lock()
	delete(bt.active, id)
	bt.mu.Unlock()

	if err := os.MkdirAll(task.TargetDir, 0755); err != nil {
		bt.mu.Lock()
		task.Status = "error"
		task.Error = "创建目标目录失败: " + err.Error()
		bt.mu.Unlock()
		t.Drop()
		return
	}
	var moved []string
	fail := ""
	for _, f := range t.Files() {
		rel := filepath.Base(f.Path())
		// 只保留视频与压缩包（压缩包留待后续自动解压），其余杂物丢弃
		lower := strings.ToLower(rel)
		ext := strings.ToLower(filepath.Ext(rel))
		keep := videoExts[ext] || archiveExts[ext] || isSecondaryVolume(rel)
		if !keep || strings.HasPrefix(lower, "sample") || strings.HasPrefix(lower, "www.") {
			continue
		}
		src := f.Path()
		if !filepath.IsAbs(src) {
			src = filepath.Join(currentConfig().CacheDir, t.InfoHash().HexString(), f.Path())
		}
		// 落进已有目录时可能撞名，自动避让而不是覆盖原有的集
		dst := uniquePath(filepath.Join(task.TargetDir, rel))
		if err := moveFile(src, dst); err != nil {
			fail = "移动文件失败: " + err.Error()
			break
		}
		moved = append(moved, filepath.Base(dst))
	}
	if fail != "" {
		bt.mu.Lock()
		task.Status = "error"
		task.Error = fail
		bt.mu.Unlock()
		t.Drop()
		os.RemoveAll(filepath.Join(currentConfig().CacheDir, t.InfoHash().HexString()))
		return
	}

	t.Drop()
	// 清理缓存残留
	os.RemoveAll(filepath.Join(currentConfig().CacheDir, t.InfoHash().HexString()))

	// 自动整理：解压压缩包 -> 校验视频 -> 清理压缩包
	bt.mu.Lock()
	task.Status = "processing"
	task.Stage = "正在整理文件"
	task.Progress = 100
	task.Files = moved
	bt.mu.Unlock()

	videoCount, note, perr := ProcessDownloaded(task.TargetDir, func(stage string) {
		bt.mu.Lock()
		task.Stage = stage
		bt.mu.Unlock()
	})

	// 整理后重新收集目录内的视频文件名，写回任务
	final := listVideoNames(task.TargetDir)

	bt.mu.Lock()
	task.Stage = ""
	if perr != nil {
		task.Status = "error"
		task.Error = "整理失败: " + perr.Error()
	} else if videoCount == 0 && len(final) == 0 {
		task.Status = "error"
		task.Error = "下载完成但未找到可播放的视频文件"
		if note != "" {
			task.Error += "（" + note + "）"
		}
	} else {
		task.Status = "done"
		task.Files = final
		task.FinishedAt = time.Now().Unix()
		if note != "" {
			task.Stage = note
		}
	}
	bt.mu.Unlock()

	// 通知媒体库刷新（此时集数索引会按新文件重建）
	go ScanLibrary()
}

// listVideoNames 列出目录内所有视频文件名（含子目录，只留文件名）
func listVideoNames(dir string) []string {
	var out []string
	walkVideos(dir, func(vpath string, size int64, modTime int64) {
		out = append(out, filepath.Base(vpath))
	})
	return out
}

// moveFile 把文件搬到目标位置。
// 缓存目录（/data，ext4）和媒体目录（外挂硬盘，NTFS）可能不在同一个文件系统上，
// os.Rename 跨设备会直接失败（EXDEV），所以这里退回“复制 + 删除源文件”。
// 复制后校验字节数，中途出错清掉半截文件，不留残骸。
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	si, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	written, cerr := io.Copy(out, in)
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	if cerr == nil && written != si.Size() {
		cerr = fmt.Errorf("复制不完整：预期 %d 字节，实际 %d 字节", si.Size(), written)
	}
	if cerr != nil {
		os.Remove(dst)
		return cerr
	}
	// 源文件删不掉不算失败（内容已经安全落到目标），只记一笔日志
	if err := os.Remove(src); err != nil {
		log.Printf("源文件清理失败（已忽略）: %v", err)
	}
	return nil
}

// uniquePath 目标已存在时追加序号，避免覆盖已有文件
func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return p
}

// StopTask 停止任务
func StopTask(id string) error {
	if stopAnimeTask(id) { // 番剧"下载到盒子"的任务（见 anime_download.go）
		return nil
	}
	if bt == nil {
		return fmt.Errorf("BT 引擎未启动")
	}
	bt.mu.Lock()
	defer bt.mu.Unlock()
	ad, ok := bt.active[id]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	task := ad.task
	close(ad.stop)
	task.Status = "stopped"
	delete(bt.active, id)
	ad.t.Drop()
	os.RemoveAll(filepath.Join(currentConfig().CacheDir, ad.t.InfoHash().HexString()))
	return nil
}

// ListTasks 任务列表（倒序）。番剧"下载到盒子"的任务排在前面（见 anime_download.go）
func ListTasks() []TorrentTask {
	anime := animeTaskList()
	if bt == nil {
		return anime
	}
	bt.mu.Lock()
	defer bt.mu.Unlock()
	out := make([]TorrentTask, 0, len(bt.tasks))
	for _, t := range bt.tasks {
		out = append(out, *t)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return append(anime, out...)
}

// saveCover 保存封面到目标目录下的 folder.jpg。
// cover 支持两种形式：http(s) URL，或 data:image/...;base64,xxx（前端上传的图片）
func saveCover(dir, cover string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	var data []byte
	var ext = ".jpg"

	if strings.HasPrefix(cover, "data:") {
		// data URL：data:image/png;base64,....
		idx := strings.Index(cover, ",")
		if idx < 0 {
			return "", fmt.Errorf("data URL 格式不正确")
		}
		meta, payload := cover[5:idx], cover[idx+1:]
		if !strings.Contains(meta, "base64") {
			return "", fmt.Errorf("仅支持 base64 编码的图片数据")
		}
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return "", err
		}
		data = b
		if strings.Contains(meta, "png") {
			ext = ".png"
		}
	} else if strings.HasPrefix(cover, "http://") || strings.HasPrefix(cover, "https://") {
		req, err := newReq("GET", cover, nil)
		if err != nil {
			return "", err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("下载封面失败: HTTP %d", resp.StatusCode)
		}
		b, err := readAllLimit(resp.Body, 12<<20) // 12MB 上限
		if err != nil {
			return "", err
		}
		data = b
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "png") {
			ext = ".png"
		}
	} else {
		return "", fmt.Errorf("封面只支持 http(s) 链接或 base64 图片数据")
	}

	if len(data) < 128 {
		return "", fmt.Errorf("封面数据过小，可能不是有效图片")
	}
	if !looksLikeImage(data) {
		return "", fmt.Errorf("封面不是有效的图片格式")
	}
	// 统一存成 folder.jpg/png，媒体库扫描时会自动识别
	p := filepath.Join(dir, "folder"+ext)
	if err := os.WriteFile(p, data, 0644); err != nil {
		return "", err
	}
	// 清掉其他封面变体：findCover 按固定顺序挑图（folder.jpg 排在 folder.png 前面），
	// 若新图是 png 而目录里还留着旧的 jpg，扫描会一直选中旧图，换封面就"没反应"
	for _, n := range coverNames {
		old := filepath.Join(dir, n)
		if !absEqual(old, p) {
			os.Remove(old)
		}
	}
	return p, nil
}

// looksLikeImage 按魔数校验图片格式（JPEG/PNG/GIF/WebP/BMP）
func looksLikeImage(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	switch {
	case b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF: // JPEG
		return true
	case b[0] == 0x89 && string(b[1:4]) == "PNG": // PNG
		return true
	case string(b[0:3]) == "GIF": // GIF
		return true
	case string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP": // WebP
		return true
	case b[0] == 'B' && b[1] == 'M': // BMP
		return true
	}
	return false
}

func sanitizeDirName(name string) string {
	name = strings.TrimSpace(name)
	repl := strings.NewReplacer("/", "", "\\", "", ":", "", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "")
	name = repl.Replace(name)
	if name == "" {
		name = "未命名"
	}
	return name
}

// PreloadMeta 预取磁力元数据（供详情页展示大小/名字，无需下载）
type metaInfo struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Hash  string `json:"hash"`
	Error string `json:"error,omitempty"`
}

func FetchMeta(magnet string) metaInfo {
	if bt == nil || !bt.started {
		return metaInfo{Error: "BT 引擎未启动"}
	}
	t, err := bt.client.AddMagnet(magnet)
	if err != nil {
		return metaInfo{Error: err.Error()}
	}
	addTrackers(t)
	defer func() {
		hash := t.InfoHash().HexString()
		t.Drop()
		if hash != "" {
			os.RemoveAll(filepath.Join(currentConfig().CacheDir, hash))
		}
	}()
	select {
	case <-t.GotInfo():
	case <-time.After(50 * time.Second):
		return metaInfo{Error: "获取元数据超时"}
	}
	info := t.Info()
	return metaInfo{Name: info.Name, Size: info.TotalLength(), Hash: t.InfoHash().HexString()}
}
