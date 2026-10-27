package main

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// ---------------- 电影天堂 dytt8899 ----------------
// 页面 GB18030 编码；POST /e/search/index.php 后 302 到 /e/search/result/?searchid=SID

const dyttBase = "https://www.dytt8899.com"

type dyttSource struct{}

func (s *dyttSource) Name() string { return "dytt8899" }

func gb18030ToUTF8(b []byte) string {
	if out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b); err == nil {
		return string(out)
	}
	return string(b)
}

func gbkToUTF8(b []byte) string {
	if out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
		return string(out)
	}
	return string(b)
}

// gbSafeDecode 自适应解码：页面为合法 UTF-8 时直接使用，否则按 GB18030 解
func gbSafeDecode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return gb18030ToUTF8(b)
}

func (s *dyttSource) Search(ctx context.Context, kw string) ([]SourceResult, error) {
	// 搜索接口被站点 WAF 按 TLS 指纹拦截（Go/OpenSSL 系返回"没有搜索到相关的内容"），
	// 降级方案：抓首页与分类列表页，本地关键词过滤（详情页不受限，磁力可正常获取）
	kwL := strings.ToLower(kw)
	pages := []string{dyttBase + "/"}
	home, err := fetchWith(ctx, httpClient, dyttBase+"/", nil)
	if err != nil {
		return nil, err
	}
	// 首页分类导航
	reCat := regexp.MustCompile(`(?i)href="(/html/[a-z0-9]+/)"`)
	seenCat := map[string]bool{}
	outPages := []string{home}
	for _, m := range reCat.FindAllStringSubmatch(home, -1) {
		if len(seenCat) >= 8 {
			break
		}
		if !seenCat[m[1]] {
			seenCat[m[1]] = true
			outPages = append(outPages, dyttBase+m[1])
		}
	}
	pages = append(pages, outPages...)

	var results []SourceResult
	seen := map[string]bool{}
	reUlink := regexp.MustCompile(`(?is)<a href="(/i/\d+\.html)"[^>]*class="ulink"[^>]*>([^<]+)</a>`)
	for _, pg := range outPages {
		// 列表页编码 gb18030
		body, err := fetchRaw(ctx, httpClient, pg, nil)
		if err != nil {
			continue
		}
		html := gb18030ToUTF8(body)
		for _, m := range reUlink.FindAllStringSubmatch(html, -1) {
			href, title := m[1], strings.TrimSpace(m[2])
			if len(title) < 4 || !strings.Contains(strings.ToLower(title), kwL) {
				continue
			}
			if seen[href] {
				continue
			}
			seen[href] = true
			results = append(results, SourceResult{
				Site:     s.Name(),
				SiteName: siteNames[s.Name()],
				Title:    title,
				URL:      dyttBase + href,
			})
			if len(results) >= 20 {
				break
			}
		}
		if len(results) >= 20 {
			break
		}
	}
	return results, nil
}

// dytt 详情页：#Zoom 第一张 img 为封面；#downlist 内磁力
func (s *dyttSource) FetchDetail(ctx context.Context, pageURL string) ([]TorrentLink, string, error) {
	body, err := fetchRaw(ctx, httpClient, pageURL, nil)
	if err != nil {
		return nil, "", err
	}
	html := gb18030ToUTF8(body)
	// 封面
	covert := ""
	reZoom := regexp.MustCompile(`(?is)<div[^>]*id="Zoom"[^>]*>(.*?)</div>`)
	if zm := reZoom.FindStringSubmatch(html); zm != nil {
		reImg := regexp.MustCompile(`(?is)<img[^>]+src="([^"]+)"`)
		if im := reImg.FindStringSubmatch(zm[1]); im != nil {
			covert = absURL(dyttBase, im[1])
		}
	}
	return extractMagnets(html), covert, nil
}

// ---------------- 电影港 dygangs ----------------
// 页面 GBK 编码；关键词必须 GBK 编码提交，结果无分页（20 条）

const dygangsBase = "https://www.dygangs.net"

type dygangsSource struct{}

func (s *dygangsSource) Name() string { return "dygangs" }

func (s *dygangsSource) Search(ctx context.Context, kw string) ([]SourceResult, error) {
	// 关键词 GBK 编码
	gbkKW, err := simplifiedchinese.GBK.NewEncoder().String(kw)
	if err != nil {
		gbkKW = kw
	}
	form := url.Values{
		"tempid":   {"1"},
		"tbname":   {"article"},
		"show":     {"title"},
		"keyboard": {gbkKW},
	}
	body, err := postFormRaw(ctx, httpClient, dygangsBase+"/e/search/index.php", form)
	if err != nil {
		return nil, err
	}
	html := gbSafeDecode(body)
	// 结果行：<a href="/article/2024/0101/12345.htm"><img src="封面" alt="标题">
	reRow := regexp.MustCompile(`(?is)<a href="(/\w+/\d{8}/\d+\.htm)"[^>]*>\s*<img src="([^"]+)"[^>]*alt="([^"]*)"`)
	var results []SourceResult
	seen := map[string]bool{}
	for _, m := range reRow.FindAllStringSubmatch(html, -1) {
		href, img, title := m[1], m[2], strings.TrimSpace(m[3])
		key := href + "|" + title
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, SourceResult{
			Site:     s.Name(),
			SiteName: siteNames[s.Name()],
			Title:    title,
			URL:      dygangsBase + href,
			Image:    absURL(dygangsBase, img),
		})
	}
	if len(results) == 0 {
		if d := degradeKeyword(kw); d != kw {
			return s.Search(ctx, d)
		}
	}
	return results, nil
}

// dygangs 详情页磁力格式：磁力：<a href="magnet:..." >标注</a>
func (s *dygangsSource) FetchDetail(ctx context.Context, pageURL string) ([]TorrentLink, string, error) {
	body, err := fetchRaw(ctx, httpClient, pageURL, nil)
	if err != nil {
		return nil, "", err
	}
	html := gbSafeDecode(body)
	covert := ""
	reArt := regexp.MustCompile(`(?is)<div[^>]*id="article"[^>]*>(.*?)</div>`)
	if zm := reArt.FindStringSubmatch(html); zm != nil {
		reImg := regexp.MustCompile(`(?is)<img[^>]+src="([^"]+)"`)
		if im := reImg.FindStringSubmatch(zm[1]); im != nil {
			covert = absURL(dygangsBase, im[1])
		}
	}
	// 磁力行，链接文本是版本标注
	reMag := regexp.MustCompile(`(?is)<a href="(magnet:[^"]+)"[^>]*>([^<]*)</a>`)
	var tls []TorrentLink
	seen := map[string]bool{}
	for _, m := range reMag.FindAllStringSubmatch(html, -1) {
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
		tls = extractMagnets(html)
	}
	return tls, covert, nil
}

// degradeKeyword 降级关键词：去掉 "第X季/第X集/SXX" 后缀，供无结果时重试
func degradeKeyword(kw string) string {
	k := strings.TrimSpace(kw)
	reSeason := regexp.MustCompile(`(?i)[\s_\-]*(?:第\s*\d+\s*[季期]|S\d{1,2})\s*$`)
	out := reSeason.ReplaceAllString(k, "")
	out = strings.TrimSpace(out)
	if out == "" {
		return k
	}
	return out
}
