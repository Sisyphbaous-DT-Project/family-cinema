package main

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Episode 单个视频文件（一集或一部电影）
type Episode struct {
	Index    int    `json:"index"` // 集序号，0 表示无法解析（显示为单文件）
	Title    string `json:"title"` // 干净的文件标题（"第1集" 等）
	Filename string `json:"filename"`
	Path     string `json:"path"` // 服务器本地绝对路径
	Size     int64  `json:"size"`
}

// MediaItem 一个"片"（目录或根级单文件）
type MediaItem struct {
	ID       string    `json:"id"`
	Category string    `json:"category"` // movie | series
	Title    string    `json:"title"`    // 展示名（已清洗字幕组/画质标签）
	RawTitle string    `json:"rawTitle"` // 原始目录名，搜索匹配用
	Path     string    `json:"-"`
	IsDir    bool      `json:"isDir"`
	Episodes []Episode `json:"episodes"`
	Size     int64     `json:"size"`
	Modified int64     `json:"modified"`
	Cover    string    `json:"cover,omitempty"` // 本地封面（folder.jpg / cover.jpg / poster.jpg）
}

type Category struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Count int    `json:"count"`
}

type Library struct {
	mu        sync.RWMutex
	items     []MediaItem
	lastScan  time.Time
	scanError string
}

var library Library

var videoExts = map[string]bool{
	".avi": true, ".flv": true, ".m2ts": true, ".m4v": true, ".mkv": true,
	".mov": true, ".mp4": true, ".mpeg": true, ".mpg": true, ".rm": true,
	".rmvb": true, ".ts": true, ".webm": true, ".wmv": true, ".iso": true,
}

var coverNames = []string{"folder.jpg", "folder.png", "cover.jpg", "cover.png", "poster.jpg", "poster.png", "fanart.jpg", "fanart.png"}

// 集数解析正则："第01集"/"第1話"/"EP01"/"E01"/"S01E01"/"[01]"/"01" 等
var (
	reEpCN      = regexp.MustCompile(`第\s*(\d+)\s*[集話话话]`)
	reEpEN      = regexp.MustCompile(`(?i)(?:ep|e)\s*(\d{1,3})`)
	reEpBracket = regexp.MustCompile(`[\[\(【]\s*(\d{1,2})\s*[\]\)】]`)
	reEpDash    = regexp.MustCompile(`(?i)\b(\d{1,3})\s*[vx]\s*(\d{1,3})\b`)
	reEpBare    = regexp.MustCompile(`(?i)\b(?:第)?(\d{1,3})\b`)
)

func parseEpisodeIndex(filename string) int {
	name := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	// S01E01 这种优先：取 E 后数字
	if m := reEpDash.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			return n
		}
	}
	if m := reEpCN.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if m := reEpEN.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if m := reEpBracket.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	// 裸数字（恰好 2 位的数字才认，避免误伤标题里的单个数字）
	if m := reEpBare.FindStringSubmatch(name); m != nil {
		s := m[1]
		if len(s) == 2 {
			if n, err := strconv.Atoi(s); err == nil {
				return n
			}
		}
	}
	return 0
}

// episodeLabel 集数标签。番剧用"第N集"，电影不套集数概念
func episodeLabel(idx int, category string) string {
	if idx <= 0 {
		return ""
	}
	if category == "movie" {
		return ""
	}
	return "第" + strconv.Itoa(idx) + "集"
}

// labelUnnumbered 给解析不出集数的文件补可读标签。
// 电影不套"第N集"：单文件不加标签（前端显示文件名），多文件标为"版本 N"。
// 番剧按顺序续编"第N集"，编号紧接已识别的最大集数，不再用 900 这类魔法值。
func labelUnnumbered(eps []Episode, category string) {
	maxIdx, unnumbered := 0, 0
	for _, e := range eps {
		if e.Index > maxIdx {
			maxIdx = e.Index
		}
		if e.Index == 0 {
			unnumbered++
		}
	}
	if unnumbered == 0 {
		return
	}
	n := 0
	for i := range eps {
		if eps[i].Index != 0 {
			continue
		}
		n++
		if category == "movie" {
			// 目录里只有一个视频时不加标签，多个才标注版本序号
			if len(eps) > 1 {
				eps[i].Title = "版本 " + strconv.Itoa(n)
			}
			continue
		}
		eps[i].Index = maxIdx + n
		eps[i].Title = "第" + strconv.Itoa(maxIdx+n) + "集"
	}
}

var (
	reTagBlock  = regexp.MustCompile(`[\[\(【][^\[\]\(\)【】]*[\]\)】]`)
	reTechNoise = regexp.MustCompile(`(?i)\b(?:web-?rip|blu-?ray|bd-?rip|bdrip|hdrip|dvdrip|remux|hevc|avc|x26[45]|h\.?26[45]|10bit|8bit|aac|ac3|flac|opus|dts|hdr|sdr|2160p|1080p|720p|480p|4k|60fps|ma\d?\.?\d?)\b`)
	reCJK       = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]`)
	reSpaces    = regexp.MustCompile(`\s{2,}`)
)

// displayTitle 把原始目录名清洗成适合展示的片名
// "[Sakurato] Sousou no Frieren [01-28][AVC-8bit 1080p AAC]" -> "Sousou no Frieren"
// "别当欧尼酱了! Onii-chan wa Oshimai! [简中]" -> "别当欧尼酱了!"
func displayTitle(raw string) string {
	s := reTagBlock.ReplaceAllString(raw, " ")
	s = reTechNoise.ReplaceAllString(s, " ")
	s = strings.Trim(reSpaces.ReplaceAllString(s, " "), " -_.·")

	// 中英混排时，优先保留中文段（更符合国内观看习惯）
	if reCJK.MatchString(s) {
		if cn := extractCJKSegment(s); cn != "" {
			s = cn
		}
	}
	s = strings.Trim(reSpaces.ReplaceAllString(s, " "), " -_.·")
	if s == "" {
		// 整个名字都由标签组成时（如 "[组名][片名][01-11][画质]"），挑最像片名的标签
		if pick := pickTitleFromTags(raw); pick != "" {
			return pick
		}
		return raw
	}
	return s
}

var (
	reEpRange  = regexp.MustCompile(`^\d{1,4}(?:\s*[-~]\s*\d{1,4})?(?:\s*END)?$`)
	reLangTag  = regexp.MustCompile(`(?i)^(?:chs|cht|gb|big5|jp|jpsc|jptc|sc|tc|简[中体繁]?|繁[中体]?|双语|内封|外挂|无字)$`)
	reFansub   = regexp.MustCompile(`(?i)(?:字幕|subs?|raws?|sub-?group|工作室|压制|发布组|扫图|监修)`)
	reUpperOrg = regexp.MustCompile(`^[A-Z0-9]+(?:[-_&][A-Z0-9]+)+$`) // 形如 UHA-WINGS
)

// pickTitleFromTags 在全标签名字里挑出最像片名的一段
func pickTitleFromTags(raw string) string {
	var cands []string
	for _, m := range reTagBlock.FindAllString(raw, -1) {
		inner := strings.TrimSpace(strings.Trim(m, "[]()【】"))
		if inner == "" {
			continue
		}
		if reEpRange.MatchString(inner) || reLangTag.MatchString(inner) {
			continue
		}
		// 去掉技术噪声后为空的，说明整段都是画质信息
		if strings.TrimSpace(reTechNoise.ReplaceAllString(inner, " ")) == "" {
			continue
		}
		cands = append(cands, inner)
	}
	// 优先取非字幕组的候选
	for _, c := range cands {
		if !reFansub.MatchString(c) && !reUpperOrg.MatchString(c) {
			return c
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// extractCJKSegment 取出以中日文为主的片名段，去掉尾随的罗马音/英文译名
func extractCJKSegment(s string) string {
	runes := []rune(s)
	end := 0
	for i, r := range runes {
		if reCJK.MatchString(string(r)) {
			end = i + 1
		}
	}
	if end == 0 {
		return ""
	}
	// 保留中文最后一个字之后紧跟的标点（如"别当欧尼酱了!"的感叹号），
	// 以及季度序号（如"无职转生3""刀剑神域Ⅱ"——数字不是中日文字符，不补上会被切掉）
	for end < len(runes) {
		r := runes[end]
		if strings.ContainsRune("!！?？。~～", r) || (r >= '0' && r <= '9') ||
			strings.ContainsRune("ⅠⅡⅢⅣⅤⅥⅦⅧⅨⅩ", r) {
			end++
			continue
		}
		break
	}
	return strings.TrimSpace(string(runes[:end]))
}

// scanDir 扫描一个根目录，返回 MediaItem 列表
func scanDir(root string, category string) []MediaItem {
	var items []MediaItem
	entries, err := os.ReadDir(root)
	if err != nil {
		return items
	}
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if e.IsDir() {
			// 目录 = 一个"片"，内部所有视频文件（含子目录） = 集
			var eps []Episode
			var total int64
			var newest int64
			walkVideos(full, func(vpath string, size int64, modTime int64) {
				idx := parseEpisodeIndex(vpath)
				eps = append(eps, Episode{
					Index:    idx,
					Title:    episodeLabel(idx, category),
					Filename: filepath.Base(vpath),
					Path:     vpath,
					Size:     size,
				})
				total += size
				if modTime > newest {
					newest = modTime
				}
			})
			if len(eps) == 0 {
				continue
			}
			// 解析不出集数的（Index=0）排到末尾，内部保持文件名顺序
			sort.Slice(eps, func(i, j int) bool {
				ai, aj := eps[i].Index, eps[j].Index
				if (ai == 0) != (aj == 0) {
					return aj == 0
				}
				if ai != aj {
					return ai < aj
				}
				return eps[i].Filename < eps[j].Filename
			})
			labelUnnumbered(eps, category)
			items = append(items, MediaItem{
				ID:       category + ":" + e.Name(),
				Category: category,
				Title:    displayTitle(e.Name()),
				RawTitle: e.Name(),
				Path:     full,
				IsDir:    true,
				Episodes: eps,
				Size:     total,
				Modified: newest,
				Cover:    findCover(full),
			})
		} else if videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			idx := parseEpisodeIndex(e.Name())
			base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			items = append(items, MediaItem{
				ID:       category + ":" + e.Name(),
				Category: category,
				Title:    displayTitle(base),
				RawTitle: base,
				Path:     full,
				IsDir:    false,
				Episodes: []Episode{{
					Index:    idx,
					Title:    episodeLabel(idx, category),
					Filename: e.Name(),
					Path:     full,
					Size:     info.Size(),
				}},
				Size:     info.Size(),
				Modified: info.ModTime().Unix(),
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Title) < strings.ToLower(items[j].Title)
	})
	return items
}

// walkVideos 递归收集目录内所有视频文件
func walkVideos(dir string, fn func(vpath string, size int64, modTime int64)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			walkVideos(full, fn)
			continue
		}
		if !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fn(full, info.Size(), info.ModTime().Unix())
	}
}

func findCover(dir string) string {
	for _, n := range coverNames {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	// 常见扩展名大小写变体
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		lower := strings.ToLower(e.Name())
		for _, n := range coverNames {
			if lower == n {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return ""
}

// ScanLibrary 全量扫描媒体库
func ScanLibrary() {
	cfg := currentConfig()
	start := time.Now()
	movieItems := scanDir(cfg.MovieRoot, "movie")
	seriesItems := scanDir(cfg.SeriesRoot, "series")
	all := append(movieItems, seriesItems...)
	library.mu.Lock()
	library.items = all
	library.lastScan = time.Now()
	library.scanError = ""
	library.mu.Unlock()
	log.Printf("媒体库扫描完成: 共 %d 个条目 (电影电视剧 %d, 番剧 %d), 耗时 %v",
		len(all), len(movieItems), len(seriesItems), time.Since(start))
}

// LibrarySnapshot 返回媒体库快照
func LibrarySnapshot(category string) ([]MediaItem, []Category) {
	library.mu.RLock()
	defer library.mu.RUnlock()
	items := library.items
	catCount := map[string]int{}
	var out []MediaItem
	for _, it := range items {
		if category == "" || category == "all" || it.Category == category {
			out = append(out, it)
		}
		catCount[it.Category]++
	}
	cats := []Category{
		{ID: "all", Title: "全部", Count: len(items)},
		{ID: "movie", Title: "电影电视剧", Count: catCount["movie"]},
		{ID: "series", Title: "番剧", Count: catCount["series"]},
	}
	return out, cats
}

// FindItem 通过 ID 找条目
func FindItem(id string) *MediaItem {
	library.mu.RLock()
	defer library.mu.RUnlock()
	for i := range library.items {
		if library.items[i].ID == id {
			return &library.items[i]
		}
	}
	return nil
}

// MatchLocalByTitle 用站点标题反查本地库（跨语言匹配用）
// 场景：用户搜"葬送"，本地目录名是罗马音 "Sousou no Frieren"，
// 站点标题 "葬送的芙莉莲_Sousou no Frieren" 可作为桥梁把两者关联起来。
func MatchLocalByTitle(remoteTitle string, same func(a, b string) bool) *MediaItem {
	library.mu.RLock()
	defer library.mu.RUnlock()
	for i := range library.items {
		it := &library.items[i]
		if same(it.Title, remoteTitle) || same(it.RawTitle, remoteTitle) {
			return it
		}
	}
	return nil
}

// SearchLocal 本地检索：标题模糊匹配（按字面包含）
func SearchLocal(q string) []MediaItem {
	if q == "" {
		return nil
	}
	q = strings.ToLower(strings.TrimSpace(q))
	library.mu.RLock()
	defer library.mu.RUnlock()
	var hits []MediaItem
	for _, it := range library.items {
		// 展示名和原始目录名都参与匹配（原始名含罗马音/字幕组，便于按英文名搜索）
		if strings.Contains(strings.ToLower(it.Title), q) ||
			strings.Contains(strings.ToLower(it.RawTitle), q) {
			hits = append(hits, it)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := strings.ToLower(hits[i].Title), strings.ToLower(hits[j].Title)
		// 前缀命中优先
		ap, bp := strings.HasPrefix(a, q), strings.HasPrefix(b, q)
		if ap != bp {
			return ap
		}
		return a < b
	})
	if len(hits) > 20 {
		hits = hits[:20]
	}
	return hits
}
