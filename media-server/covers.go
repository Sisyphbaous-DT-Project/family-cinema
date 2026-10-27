package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// CoverResult 单个条目的封面抓取结果
type CoverResult struct {
	Title   string `json:"title"`
	Path    string `json:"path,omitempty"`   // 落盘位置
	Source  string `json:"source,omitempty"` // 来源站
	Skipped bool   `json:"skipped"`          // 已有封面，跳过
	Error   string `json:"error,omitempty"`
}

var (
	coverJobMu   sync.Mutex
	coverRunning bool
	coverStatus  struct {
		Running bool          `json:"running"`
		Total   int           `json:"total"`
		Done    int           `json:"done"`
		Found   int           `json:"found"`
		Results []CoverResult `json:"results"`
	}
)

// CoverJobStatus 抓取进度（供前端轮询）
func CoverJobStatus() any {
	coverJobMu.Lock()
	defer coverJobMu.Unlock()
	return coverStatus
}

// FetchCoversForLibrary 给媒体库里没有封面的条目抓取封面。
// force=true 时连已有封面的也重新抓一遍。
func FetchCoversForLibrary(force bool) bool {
	coverJobMu.Lock()
	if coverRunning {
		coverJobMu.Unlock()
		return false
	}
	coverRunning = true
	items, _ := LibrarySnapshot("all")
	var todo []MediaItem
	for _, it := range items {
		if it.Cover == "" || force {
			todo = append(todo, it)
		}
	}
	coverStatus.Running = true
	coverStatus.Total = len(todo)
	coverStatus.Done = 0
	coverStatus.Found = 0
	coverStatus.Results = nil
	coverJobMu.Unlock()

	go func() {
		defer func() {
			coverJobMu.Lock()
			coverRunning = false
			coverStatus.Running = false
			coverJobMu.Unlock()
			// 抓完刷新媒体库，让新封面立即生效
			ScanLibrary()
		}()

		for _, it := range todo {
			res := fetchCoverFor(it)
			coverJobMu.Lock()
			coverStatus.Done++
			if res.Path != "" {
				coverStatus.Found++
			}
			coverStatus.Results = append(coverStatus.Results, res)
			coverJobMu.Unlock()
			// 抓取之间留间隔，避免把站点请求打太密
			time.Sleep(600 * time.Millisecond)
		}
		log.Printf("封面抓取完成: %d/%d 成功", coverStatus.Found, coverStatus.Total)
	}()
	return true
}

// fetchCoverFor 为单个条目找封面：番剧优先 mikan 官方图，电影走电影港/MP4资源
func fetchCoverFor(it MediaItem) CoverResult {
	out := CoverResult{Title: it.Title}
	if !it.IsDir {
		// 根级单文件没有独立目录可放封面，跳过
		out.Skipped = true
		out.Error = "单文件条目无独立目录"
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// 关键词候选：展示名优先，再退到原始目录名里的外文名
	// （"肖申克的救赎 The Shawshank Redemption 1080P" 这类中英混排，
	//   站点可能只按其中一种写法收录）
	keywords := coverKeywords(it)
	if len(keywords) == 0 {
		out.Error = "无法提取搜索关键词"
		return out
	}

	// 按分类决定源的优先级
	order := []string{"mikan", "dygangs", "mp4z6"}
	if it.Category == "movie" {
		order = []string{"dygangs", "mp4z6", "dytt8899"}
	}

	for _, kw := range keywords {
		for _, siteName := range order {
			var src searchSource
			for _, s := range sources {
				if s.Name() == siteName {
					src = s
					break
				}
			}
			if src == nil {
				continue
			}
			results, err := src.Search(ctx, kw)
			if err != nil || len(results) == 0 {
				continue
			}
			// 找标题最贴近的那条，取它的封面
			img := pickCoverImage(results, it)
			if img == "" {
				continue
			}
			// mikan 的图要经代理取，其余直连
			data, err := downloadImage(ctx, img)
			if err != nil || len(data) < 512 || !looksLikeImage(data) {
				continue
			}
			ext := ".jpg"
			if data[0] == 0x89 {
				ext = ".png"
			}
			p := filepath.Join(it.Path, "folder"+ext)
			if err := os.WriteFile(p, data, 0644); err != nil {
				out.Error = "写入失败: " + err.Error()
				return out
			}
			out.Path = p
			out.Source = siteNames[siteName]
			return out
		}
	}
	out.Error = "未找到封面"
	return out
}

// coverKeywords 生成搜索关键词候选，按命中概率从高到低
func coverKeywords(it MediaItem) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len([]rune(s)) < 2 {
			return
		}
		for _, x := range out {
			if x == s {
				return
			}
		}
		out = append(out, s)
	}

	add(coverKeyword(it.Title))
	// 原始名里的外文段（去掉标签与画质噪声后剩下的拉丁字母部分）
	if it.RawTitle != "" && it.RawTitle != it.Title {
		clean := reTagBlock.ReplaceAllString(it.RawTitle, " ")
		clean = reTechNoise.ReplaceAllString(clean, " ")
		clean = strings.Join(strings.Fields(clean), " ")
		if latin := extractLatinSegment(clean); latin != "" {
			add(coverKeyword(latin))
		}
		add(coverKeyword(clean))
	}
	return out
}

// extractLatinSegment 取出以拉丁字母为主的片名段（罗马音/英文译名）
func extractLatinSegment(s string) string {
	// 按中日文与拉丁的边界切分，取最长的拉丁片段
	parts := regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]+`).Split(s, -1)
	best := ""
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), "-_.·!！?？:：")
		// 至少两个单词、总长够，才当作片名
		if len(p) > len(best) && len(strings.Fields(p)) >= 2 {
			best = p
		}
	}
	return best
}

// coverKeyword 从展示名提取搜索关键词
func coverKeyword(title string) string {
	t := strings.TrimSpace(title)
	// 去掉"系列/全集/第N季"等尾巴，站点通常按本名收录
	t = reCoverTail.ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	if t == "" {
		return strings.TrimSpace(title)
	}
	return t
}

var reCoverTail = regexp.MustCompile(`(?i)[\s·]*(?:系列|全集|合集|三部曲|第[一二三四五六七八九十\d]+[季部期]|S\d{1,2})\s*$`)

// pickCoverImage 在搜索结果里挑与本地条目最匹配的封面
func pickCoverImage(results []SourceResult, it MediaItem) string {
	// 先找标题判定为同片的
	for _, r := range results {
		if r.Image == "" {
			continue
		}
		if sameTitle(it.Title, r.Title) || sameTitle(it.RawTitle, r.Title) {
			return r.Image
		}
	}
	// 退一步：取第一个带图的结果
	for _, r := range results {
		if r.Image != "" {
			return r.Image
		}
	}
	return ""
}

// downloadImage 抓图片字节，mikan 域名走配置的自托管代理
func downloadImage(ctx context.Context, imgURL string) ([]byte, error) {
	if strings.Contains(imgURL, "mikanani.me") {
		body, err := fetchViaProxy(ctx, imgURL)
		if err != nil {
			return nil, err
		}
		return []byte(body), nil
	}
	req, err := newReq("GET", imgURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/jpeg,image/png,*/*")
	resp, err := httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readAllLimit(resp.Body, 12<<20)
}
