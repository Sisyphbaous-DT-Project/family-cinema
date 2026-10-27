package main

import (
	"context"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SearchHit 聚合搜索结果：本地命中 + 各网站来源合并成一条
type SearchHit struct {
	Key     string         `json:"key"`
	Title   string         `json:"title"`
	Local   *LocalRef      `json:"local,omitempty"` // 本地媒体库命中
	Results []SourceResult `json:"results"`         // 各站来源
	Image   string         `json:"image,omitempty"`
	From    []string       `json:"from"` // 来源站名列表（前端徽标）
}

// LocalRef 本地命中引用
type LocalRef struct {
	ID       string    `json:"id"`
	Category string    `json:"category"`
	Path     string    `json:"-"`
	Cover    string    `json:"cover,omitempty"`
	Episodes []Episode `json:"episodes,omitempty"`
}

// 缓存
var (
	searchCacheMu sync.Mutex
	searchCache   = map[string]cacheEntry{}
)

type cacheEntry struct {
	hits []SearchHit
	at   time.Time
}

// normalizeTitle 标题规范化，用于跨站/本地合并
func normalizeTitle(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[\[\]【】()（）{}<>_\-—–|:：.·、&/\\]`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?i)\s+(?:第\s*\d+\s*[季期集話话]|s\d{1,2}(?:e\d{1,2})?|\d{1,3}话)\s*`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?i)\b(?:bd|hd|dvd|web|bluray|remux|1080p|720p|2160p|4k|2k|x264|x265|hevc|h264|h265|aac|ac3|flac|opus|dts|hdr|10bit|8bit|简繁|繁体|简体|双语|字幕组|comic|anime)\b`).ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), "")
	// 去掉尾部常见后缀（冒号后内容如"再次冒险"保留，只清理分隔符残留）
	return s
}

// 尾部可接受的后缀（季度/年份/版本号/合集修饰词等），用于前缀合并判断
var reTailOK = regexp.MustCompile(`^(?:\d{4}|第\d+[季部]|s\d{1,2}|e\d{1,2}|v\d{1,2}|sp\d*|系列|全集|合集|三部曲|重制版|加长版)$`)

// sameTitle 判断两条标题是否同一部片子：
//  1. 归一化后完全相等
//  2. 一方是另一方前缀，且剩余部分是季度/年份/版本等合法后缀
//
// 避免把"流浪地球"与"流浪地球2：再次冒险"这类系列续作误合并
func sameTitle(a, b string) bool {
	if a == b {
		return true
	}
	na, nb := normalizeTitle(a), normalizeTitle(b)
	if na == nb {
		return true
	}
	short, long := na, nb
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	if len([]rune(short)) < 4 {
		return false
	}
	// 纯拉丁字母的片名（罗马音译名）作为子串出现即认定同片。
	// 站点标题常写成 "葬送的芙莉莲_Sousou no Frieren"，而本地目录可能只有罗马音名。
	if reLatinOnly.MatchString(short) && len(short) >= 8 && strings.Contains(long, short) {
		return true
	}
	if !strings.HasPrefix(long, short) {
		return false
	}
	tail := strings.TrimPrefix(long, short)
	return reTailOK.MatchString(strings.ToLower(tail))
}

// reLatinOnly 判断归一化后的标题是否为纯拉丁字母数字（罗马音/英文名）
var reLatinOnly = regexp.MustCompile(`^[a-z0-9]+$`)

// mergeTorrents 合并磁力列表并按 magnet 去重（同站多条结果常有重复种子）
func mergeTorrents(dst, src []TorrentLink) []TorrentLink {
	seen := map[string]bool{}
	for _, t := range dst {
		seen[t.Magnet] = true
	}
	for _, t := range src {
		if t.Magnet == "" || seen[t.Magnet] {
			continue
		}
		seen[t.Magnet] = true
		dst = append(dst, t)
	}
	return dst
}

// pickLocal 在关键词命中的本地条目里找与站点标题同片的那个
func pickLocal(localHits []MediaItem, title string) *MediaItem {
	for i := range localHits {
		it := &localHits[i]
		if sameTitle(it.Title, title) || sameTitle(it.RawTitle, title) {
			return it
		}
	}
	return nil
}

func newLocalRef(it *MediaItem) *LocalRef {
	return &LocalRef{
		ID:       it.ID,
		Category: it.Category,
		Path:     it.Path,
		Cover:    it.Cover,
		Episodes: it.Episodes,
	}
}

// AggregateSearch 聚合搜索：本地优先 + 四站并行
func AggregateSearch(q string) []SearchHit {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	// 缓存
	searchCacheMu.Lock()
	if e, ok := searchCache[q]; ok && time.Since(e.at) < time.Duration(currentConfig().SearchTTL)*time.Second {
		searchCacheMu.Unlock()
		return e.hits
	}
	searchCacheMu.Unlock()

	cfg := currentConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 远程搜索并行
	type srcOut struct {
		site string
		res  []SourceResult
		err  error
	}
	ch := make(chan srcOut, len(sources))
	var wg sync.WaitGroup
	for _, s := range sources {
		if !cfg.SourcesEnabled(s.Name()) {
			continue
		}
		wg.Add(1)
		go func(s searchSource) {
			defer wg.Done()
			res, err := s.Search(ctx, q)
			ch <- srcOut{site: s.Name(), res: res, err: err}
		}(s)
	}
	go func() { wg.Wait(); close(ch) }()

	// 按片名分组；组内按站点分开保存，保证"哪些站有这个资源"完整可见
	type siteGroup struct {
		key    string
		title  string
		bySite map[string]*SourceResult
		order  []string // 站点出现顺序，保证输出稳定
		image  string
	}
	var groups []*siteGroup
	findGroup := func(title string) *siteGroup {
		for _, g := range groups {
			if sameTitle(g.title, title) {
				return g
			}
		}
		return nil
	}

	for out := range ch {
		if out.err != nil {
			log.Printf("搜索源 %s 失败: %v", out.site, out.err)
			continue
		}
		for i := range out.res {
			r := out.res[i]
			g := findGroup(r.Title)
			if g == nil {
				g = &siteGroup{
					key:    normalizeTitle(r.Title),
					title:  r.Title,
					bySite: map[string]*SourceResult{},
					image:  r.Image,
				}
				groups = append(groups, g)
			}
			if g.image == "" {
				g.image = r.Image
			}
			if ex, ok := g.bySite[r.Site]; ok {
				// 同站同片的多条结果：合并磁力并去重
				ex.Torrents = mergeTorrents(ex.Torrents, r.Torrents)
				if ex.Image == "" {
					ex.Image = r.Image
				}
				if ex.URL == "" {
					ex.URL = r.URL
				}
			} else {
				clone := r
				clone.Torrents = mergeTorrents(nil, r.Torrents)
				g.bySite[r.Site] = &clone
				g.order = append(g.order, r.Site)
			}
		}
	}

	// 本地命中
	localHits := SearchLocal(q)
	usedLocal := map[string]bool{}

	// 组装：每个分组输出一条，组内各站点独立成块
	var hits []SearchHit
	for _, g := range groups {
		hit := SearchHit{Key: g.key, Title: g.title, Image: g.image}
		for _, site := range g.order {
			sr := g.bySite[site]
			hit.Results = append(hit.Results, *sr)
			if n := siteNames[site]; n != "" {
				hit.From = append(hit.From, n)
			}
		}
		// 关联本地条目：先看关键词直接命中的，再用站点标题反查全库（跨语言）
		if it := pickLocal(localHits, g.title); it != nil {
			hit.Local = newLocalRef(it)
			usedLocal[it.ID] = true
		} else if extra := MatchLocalByTitle(g.title, sameTitle); extra != nil {
			hit.Local = newLocalRef(extra)
			usedLocal[extra.ID] = true
		}
		hits = append(hits, hit)
	}

	// 纯本地（关键词命中但没有任何站点匹配上）
	for i := range localHits {
		it := &localHits[i]
		if usedLocal[it.ID] {
			continue
		}
		hits = append(hits, SearchHit{
			Key:   normalizeTitle(it.Title),
			Title: it.Title,
			Local: newLocalRef(it),
			From:  []string{"本地资源"},
		})
	}

	// 排序：本地命中 > 标题前缀匹配 > 字母序
	qL := strings.ToLower(q)
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		la, lb := a.Local != nil, b.Local != nil
		if la != lb {
			return la
		}
		pa, pb := strings.HasPrefix(strings.ToLower(a.Title), qL), strings.HasPrefix(strings.ToLower(b.Title), qL)
		if pa != pb {
			return pa
		}
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	})

	// 截断时本地条目全部保留，联网条目补足到上限，
	// 避免本地命中多的时候把联网结果全挤掉
	const maxHits = 40
	if len(hits) > maxHits {
		var locals, remotes []SearchHit
		for _, h := range hits {
			if h.Local != nil {
				locals = append(locals, h)
			} else {
				remotes = append(remotes, h)
			}
		}
		room := maxHits - len(locals)
		if room < 0 {
			room = 0
		}
		if len(remotes) > room {
			remotes = remotes[:room]
		}
		hits = append(locals, remotes...)
		if len(hits) > maxHits {
			hits = hits[:maxHits]
		}
	}

	// 写缓存
	searchCacheMu.Lock()
	searchCache[q] = cacheEntry{hits: hits, at: time.Now()}
	searchCacheMu.Unlock()
	return hits
}

// GetDetail 抓某站详情页磁力列表（mikan 返回空，直接用搜索阶段数据）
func GetDetail(site, pageURL string) ([]TorrentLink, string) {
	for _, s := range sources {
		if s.Name() == site {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tls, cover, err := s.FetchDetail(ctx, pageURL)
			if err != nil {
				log.Printf("详情抓取 %s 失败: %v", site, err)
			}
			return tls, cover
		}
	}
	return nil, ""
}

func (c Config) SourcesEnabled(name string) bool {
	switch name {
	case "dytt8899":
		return c.Sources.Dytt8899
	case "dygangs":
		return c.Sources.Dygangs
	case "mp4z6":
		return c.Sources.Mp4z6
	case "mikan":
		return c.Sources.Mikan
	}
	return true
}
