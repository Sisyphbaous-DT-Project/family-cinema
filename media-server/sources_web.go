package main

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// ---------------- 蜜柑计划 mikanani.me ----------------
// 无法直连时，可通过自托管的 mikan-proxy /fetch 端点转发
// 搜索：GET /Home/Search?searchstr=<UTF-8>

type mikanSource struct{}

// fetchViaProxy 通过自托管代理的 /fetch 端点抓取 mikan 页面
func fetchViaProxy(ctx context.Context, target string) (string, error) {
	cfg := currentConfig()
	if cfg.Proxy.Base == "" {
		return "", errNoProxy
	}
	full := cfg.Proxy.Base + "/fetch?url=" + url.QueryEscape(target)
	req, err := newReq("GET", full, nil)
	if err != nil {
		return "", err
	}
	if cfg.Proxy.Token != "" {
		req.Header.Set("X-Proxy-Token", cfg.Proxy.Token)
	}
	resp, err := httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := readAllLimit(resp.Body, 16<<20)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var errNoProxy = &proxyError{}

type proxyError struct{}

func (e *proxyError) Error() string { return "mikan 代理未配置 (config.json proxy.base)" }

const mikanBase = "https://mikanani.me"

func (s *mikanSource) Name() string { return "mikan" }

// bangumiRef mikan 官方番剧条目（来自搜索页"相关推荐"区块，是权威分组依据）
type bangumiRef struct {
	ID      string
	Name    string
	Image   string
	norm    string   // 规范化后的官方番剧名
	learned []string // 从种子标题学到的别名（罗马音/日文原名）
}

// aliases 官方名 + 学到的别名
func (b *bangumiRef) aliases() []string {
	return append([]string{b.norm}, b.learned...)
}

func appendUniq(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// extractAliases 从种子标题里提取与官方名并列的其他语种写法。
// 例："[7³ACG] 孤独摇滚！/Bocchi the Rock S01 | 01-12+SPx1 [简繁字幕] BDrip"
// 官方名归一化为 "孤独摇滚"，可学到别名 "bocchitherock"。
func extractAliases(title, officialNorm string) []string {
	// 去掉开头字幕组标签，再按常见分隔符切段
	t := regexp.MustCompile(`^\s*(?:[\[【][^\]】]*[\]】]\s*)+`).ReplaceAllString(title, "")
	if i := strings.IndexAny(t, "|【["); i > 0 {
		t = t[:i]
	}
	var out []string
	for _, seg := range regexp.MustCompile(`[/／_、]`).Split(t, -1) {
		n := normalizeTitle(seg)
		if n == "" || n == officialNorm || len(n) < 6 {
			continue
		}
		// 只学纯拉丁或纯日文的写法，避免把集数范围之类的当别名
		if reLatinOnly.MatchString(n) || reCJKOnly.MatchString(n) {
			out = append(out, n)
		}
	}
	return out
}

// reCJKOnly 判断是否纯中日文（含长音符），用于识别日文原名别名
var reCJKOnly = regexp.MustCompile(`^[\p{Han}\p{Hiragana}\p{Katakana}ー]+$`)

var (
	reMikanRow     = regexp.MustCompile(`(?is)<tr class="js-search-results-row".*?</tr>`)
	reMikanMagnet  = regexp.MustCompile(`(?is)data-magnet="([^"]+)"`)
	reMikanTitle   = regexp.MustCompile(`(?is)<a[^>]+class="magnet-link-wrap"[^>]*>([^<]+)</a>`)
	reMikanSize    = regexp.MustCompile(`(?is)<td>\s*([0-9.]+\s*[GMK]B)\s*</td>`)
	reMikanDate    = regexp.MustCompile(`(?is)<td>\s*(\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2})\s*</td>`)
	reMikanBgBlock = regexp.MustCompile(`(?is)<li>\s*<a href="/Home/Bangumi/(\d+)"[^>]*>(.*?)</a>\s*</li>`)
	reMikanBgImg   = regexp.MustCompile(`(?is)data-src="([^"]+)"`)
	reMikanBgName  = regexp.MustCompile(`(?is)class="an-text"[^>]*title="([^"]*)"`)
)

// parseBangumiList 抽取搜索页里的官方番剧列表
func parseBangumiList(htmlStr string) []bangumiRef {
	var out []bangumiRef
	for _, m := range reMikanBgBlock.FindAllStringSubmatch(htmlStr, -1) {
		id, inner := m[1], m[2]
		nm := reMikanBgName.FindStringSubmatch(inner)
		if nm == nil {
			continue
		}
		name := strings.TrimSpace(html.UnescapeString(nm[1]))
		if name == "" {
			continue
		}
		ref := bangumiRef{ID: id, Name: name, norm: normalizeTitle(name)}
		if im := reMikanBgImg.FindStringSubmatch(inner); im != nil {
			// 官方封面统一转 jpg（webp 在旧内核上不显示）
			img := html.UnescapeString(im[1])
			img = strings.Replace(img, "format=webp", "format=jpg", 1)
			ref.Image = absURL(mikanBase, img)
		}
		out = append(out, ref)
	}
	return out
}

// mikan 搜索：以官方番剧列表为骨架分组，避免把字幕组/标签当成片名
func (s *mikanSource) Search(ctx context.Context, kw string) ([]SourceResult, error) {
	searchURL := mikanBase + "/Home/Search?searchstr=" + url.QueryEscape(kw)
	htmlStr, err := fetchViaProxy(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	type row struct {
		title, magnet, size, date string
	}
	var rows []row
	for _, tr := range reMikanRow.FindAllString(htmlStr, -1) {
		var r row
		if m := reMikanMagnet.FindStringSubmatch(tr); m != nil {
			r.magnet = html.UnescapeString(m[1])
		}
		if m := reMikanTitle.FindStringSubmatch(tr); m != nil {
			r.title = html.UnescapeString(strings.TrimSpace(m[1]))
		}
		if m := reMikanSize.FindStringSubmatch(tr); m != nil {
			r.size = strings.TrimSpace(m[1])
		}
		if m := reMikanDate.FindStringSubmatch(tr); m != nil {
			r.date = m[1]
		}
		if r.title == "" || r.magnet == "" {
			continue
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		if d := degradeKeyword(kw); d != kw {
			return s.Search(ctx, d)
		}
		return nil, nil
	}

	bangumis := parseBangumiList(htmlStr)
	groups := map[string]*SourceResult{}
	var order []string

	addTo := func(key, title, image string, r row) {
		g, ok := groups[key]
		if !ok {
			g = &SourceResult{
				Site:     s.Name(),
				SiteName: siteNames[s.Name()],
				Title:    title,
				Image:    image,
			}
			groups[key] = g
			order = append(order, key)
		}
		g.Torrents = append(g.Torrents, TorrentLink{
			Name:   r.title,
			Magnet: r.magnet,
			Size:   r.size,
		})
	}

	// 第一轮：按官方番剧名匹配，并从命中的标题里学习别名（罗马音/日文原名）
	matchBangumi := func(title string) int {
		nt := normalizeTitle(title)
		best, bestLen := -1, 0
		for i := range bangumis {
			b := &bangumis[i]
			for _, alias := range b.aliases() {
				if alias == "" || len(alias) < 4 {
					continue
				}
				if strings.Contains(nt, alias) && len(alias) > bestLen {
					best, bestLen = i, len(alias)
				}
			}
		}
		return best
	}

	assigned := make([]int, len(rows))
	for i := range assigned {
		assigned[i] = -1
	}
	for i, r := range rows {
		if bi := matchBangumi(r.title); bi >= 0 {
			assigned[i] = bi
			// 学习别名：标题里与官方名并列的其他语种写法
			for _, a := range extractAliases(r.title, bangumis[bi].norm) {
				bangumis[bi].learned = appendUniq(bangumis[bi].learned, a)
			}
		}
	}
	// 第二轮：用学到的别名再收一遍漏掉的
	for i, r := range rows {
		if assigned[i] < 0 {
			if bi := matchBangumi(r.title); bi >= 0 {
				assigned[i] = bi
			}
		}
	}

	for i, r := range rows {
		if bi := assigned[i]; bi >= 0 {
			b := bangumis[bi]
			addTo("bg:"+b.ID, b.Name, b.Image, r)
			continue
		}
		// 官方列表没覆盖到：退回按标题猜片名，猜不出就并入"其他资源"
		name := mikanGroupName(r.title)
		if name == "" || !looksLikeTitle(name) {
			addTo("misc", kw+" · 其他资源", "", r)
			continue
		}
		addTo("nm:"+normalizeTitle(name), name, "", r)
	}

	var results []SourceResult
	for _, k := range order {
		results = append(results, *groups[k])
	}
	return results, nil
}

// looksLikeTitle 过滤掉明显不是片名的碎片（纯标签、日期、单个词的字幕组等）
func looksLikeTitle(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	// 去掉零宽字符后为空
	if strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\u200b' || r == '\ufeff' || r == '\u200e' {
			return -1
		}
		return r
	}, t)) == "" {
		return false
	}
	if len([]rune(t)) < 3 {
		return false
	}
	// "10月新番" / "2024年10月" 这类档期标签
	if regexp.MustCompile(`^\d{1,2}月新番$|^\d{4}年\d{1,2}月`).MatchString(t) {
		return false
	}
	// 纯 ASCII 且像字幕组代号（含 Raws/Sub/Lab 等）
	if reFansub.MatchString(t) || reUpperOrg.MatchString(t) {
		return false
	}
	// 残留的半个括号标签
	if strings.HasPrefix(t, "【") || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "(") {
		return false
	}
	return true
}

var reMikanGroup = regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)*([^/|]+)`)

// mikanGroupName 从种子标题提取番剧名：
// "[7³ACG] 葬送的芙莉莲/Sousou no Frieren S01 | 01-28" -> "葬送的芙莉莲"
// "葬送的芙莉莲 Sousou no Frieren [01-28][BD]" -> "葬送的芙莉莲"
func mikanGroupName(title string) string {
	t := title
	// 去掉开头所有 [xxx] 字幕组/标签
	t = regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)+`).ReplaceAllString(t, "")
	// 取第一个 / 或 | 前的部分（去罗马音、集数范围）
	if i := strings.IndexAny(t, "/|"); i > 0 {
		t = t[:i]
	}
	// 取第一个 [ 前的部分
	if i := strings.Index(t, "["); i > 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	// 去季数尾巴（阿拉伯/中文数字）
	t = regexp.MustCompile(`(?i)\s*(?:S\d{1,2}|第\s*[一二三四五六七八九十\d]+\s*[季期])\s*$`).ReplaceAllString(t, "")
	// 去尾部集数（"- 01"、"01"、"-SPx1" 等）
	t = regexp.MustCompile(`[-—–\s]*(?:\d{1,3}|SP\d*|s\d+e\d+)\s*$`).ReplaceAllString(t, "")
	// 去尾部罗马音（空格+大写字母开头序列）
	t = regexp.MustCompile(`\s+[A-Za-z][A-Za-z0-9'\.\- ]*$`).ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	if t == "" {
		// 全是标签时取一个像番剧名的标签（跳过尺寸/画质等描述性标签）
		tagRe := regexp.MustCompile(`\[([^\[\]]{2,40})\]`)
		tags := tagRe.FindAllStringSubmatch(title, -1)
		for _, m := range tags {
			cand := strings.TrimSpace(m[1])
			if mikanTagIsName(cand) {
				return cand
			}
		}
		if len(tags) > 0 {
			return strings.TrimSpace(tags[len(tags)-1][1])
		}
		return strings.TrimSpace(title)
	}
	return t
}

// mikanTagIsName 判断标签内容是否是番剧名（排除尺寸、画质、状态类描述）
func mikanTagIsName(s string) bool {
	if s == "" {
		return false
	}
	if regexp.MustCompile(`(?i)^\d+(\.\d+)?\s*(gb|mb|kb|g|m|k)$`).MatchString(s) {
		return false
	}
	for _, bad := range []string{
		"4k", "1080p", "720p", "2160p", "x264", "x265", "hevc", "h264", "h265", "10bit", "8bit",
		"mkv", "mp4", "合集", "完", "rev", "gb", "简中", "繁中", "简体", "繁体", "双语", "字幕",
		"收藏级", "画质", "双版本", "招募", "新人", "内封", "外挂", "无水印",
	} {
		if strings.Contains(strings.ToLower(s), bad) {
			return false
		}
	}
	return true
}

// mikan 详情页就是搜索结果本身，直接用传入的页面（无独立详情抓取）
func (s *mikanSource) FetchDetail(ctx context.Context, pageURL string) ([]TorrentLink, string, error) {
	// pageURL 实际是种子标题，无详情页可抓；返回空让上层用搜索阶段数据
	return nil, "", nil
}

// ---------------- MP4资源 mp4.z6.net.cn ----------------
// 搜索接口已损坏（SQL 1064），降级方案：抓分类列表页 + 更新页，本地关键词过滤

const mp4z6Base = "https://mp4.z6.net.cn"

type mp4z6Source struct{}

func (s *mp4z6Source) Name() string { return "mp4z6" }

var reZ6Item = regexp.MustCompile(`(?is)<a[^>]+href="(/detail/\d+\.html)"[^>]*>\s*<img[^>]+data-original="\s*([^"]+)"[^>]*>\s*(?:</a>)?\s*</?(?:a|div|p)[^>]*>\s*(?:<span[^>]*>)?([^<]{2,60}?)\s*`)
var reZ6Title = regexp.MustCompile(`(?is)<a[^>]+href="(/detail/\d+\.html)"[^>]*title="([^"]+)"`)

func (s *mp4z6Source) Search(ctx context.Context, kw string) ([]SourceResult, error) {
	// 抓首页（含分类入口）+ 更新页，过滤关键词
	var pages []string
	home, err := fetchWith(ctx, httpClient, mp4z6Base+"/", map[string]string{"Referer": mp4z6Base + "/"})
	if err != nil {
		return nil, err
	}
	// 分类页 /list/{id}-1.html
	reCat := regexp.MustCompile(`(?i)href="(/list/\d+-1\.html)"`)
	seenCat := map[string]bool{}
	pages = append(pages, home)
	for _, m := range reCat.FindAllStringSubmatch(home, -1) {
		if len(seenCat) >= 6 {
			break
		}
		if !seenCat[m[1]] {
			seenCat[m[1]] = true
			pages = append(pages, mp4z6Base+m[1])
		}
	}
	// 还有更新页面（不分页则单独抓，最多 2 页试）
	for _, p := range []string{"/update.html"} {
		if v, err := fetchWith(ctx, httpClient, mp4z6Base+p, map[string]string{"Referer": mp4z6Base + "/"}); err == nil {
			pages = append(pages, v)
		}
	}

	kwL := strings.ToLower(kw)
	seen := map[string]bool{}
	var results []SourceResult
	reLink := regexp.MustCompile(`(?is)<a[^>]+href="(/detail/\d+\.html)"[^>]*>([^<]{2,80})</a>`)
	imgOf := map[string]string{}
	for _, pg := range pages {
		for _, m := range reZ6Item.FindAllStringSubmatch(pg, -1) {
			imgOf[m[1]] = strings.TrimSpace(m[2])
		}
		for _, m := range reLink.FindAllStringSubmatch(pg, -1) {
			href, title := m[1], strings.TrimSpace(m[2])
			if len(title) < 2 || !strings.Contains(strings.ToLower(title), kwL) {
				continue
			}
			if seen[href] {
				continue
			}
			seen[href] = true
			img := imgOf[href]
			if img == "" {
				// 从 <a> 段落附近找 img
				if im := regexp.MustCompile(`(?is)<a[^>]+href="` + regexp.QuoteMeta(href) + `"[^>]*>.*?<img[^>]+data-original="\s*([^"]+)"`).FindStringSubmatch(pg); im != nil {
					img = strings.TrimSpace(im[1])
				}
			}
			results = append(results, SourceResult{
				Site:     s.Name(),
				SiteName: siteNames[s.Name()],
				Title:    title,
				URL:      mp4z6Base + href,
				Image:    absURL(mp4z6Base, img),
			})
			if len(results) >= 30 {
				break
			}
		}
		if len(results) >= 30 {
			break
		}
	}
	return results, nil
}

// mp4z6 详情页：magnet 链接（过滤 jianpian:// / ftp:// 废链）
func (s *mp4z6Source) FetchDetail(ctx context.Context, pageURL string) ([]TorrentLink, string, error) {
	pg, err := fetchWith(ctx, httpClient, pageURL, map[string]string{"Referer": mp4z6Base + "/"})
	if err != nil {
		return nil, "", err
	}
	// 封面：正文第一张原图
	cover := ""
	reImg := regexp.MustCompile(`(?is)<img[^>]+data-original="\s*([^"]+)"[^>]*>`)
	if m := reImg.FindStringSubmatch(pg); m != nil {
		cover = absURL(mp4z6Base, strings.TrimSpace(m[1]))
	}
	var tls []TorrentLink
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?is)<a[^>]+href="(magnet:[^"]+)"[^>]*>([^<]*)</a>`).FindAllStringSubmatch(pg, -1) {
		mag, label := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if seen[mag] {
			continue
		}
		seen[mag] = true
		if len(label) > 60 {
			label = ""
		}
		tls = append(tls, TorrentLink{Name: label, Magnet: mag})
	}
	if len(tls) == 0 {
		for _, mag := range reMagnet.FindAllString(pg, -1) {
			mag = strings.TrimSuffix(mag, `"`)
			if !seen[mag] {
				seen[mag] = true
				tls = append(tls, TorrentLink{Magnet: mag})
			}
		}
	}
	return tls, cover, nil
}
