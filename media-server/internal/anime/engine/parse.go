// Package engine 用 XPath 规则从视频站页面里取"搜索结果"和"选集列表"。
package engine

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"media-server/internal/anime/rules"
)

// Item 一条搜索结果（视频站里的一部番）
type Item struct {
	Name string `json:"name"`
	Src  string `json:"src"` // 详情页绝对地址
}

// Episode 一集
type Episode struct {
	Name string `json:"name"`
	URL  string `json:"url"` // 播放页绝对地址
}

// Road 一条播放线路（含若干集）
type Road struct {
	Name     string    `json:"name"`
	Episodes []Episode `json:"episodes"`
}

// decode 把页面字节按声明/探测到的编码转成 HTML 节点树。
// 站点不声明编码时 x/net 默认按 windows-1252 解，会把没有 meta 的 UTF-8 中文页面解坏，
// 这里在"没把握"且字节本身是合法 UTF-8 时直接按 UTF-8 处理。
func decode(body []byte, contentType string) (*html.Node, error) {
	head := body
	if len(head) > 2048 {
		head = head[:2048]
	}
	enc, _, certain := charset.DetermineEncoding(head, contentType)
	if !certain && utf8.Valid(body) {
		return htmlquery.Parse(bytes.NewReader(body))
	}
	out, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return nil, err
	}
	return htmlquery.Parse(bytes.NewReader(out))
}

// rel 规则里 "//xx" 形式的子选择器是相对于上级节点求值的，要写成 ".//xx"
func rel(path string) string {
	if strings.HasPrefix(path, "/") {
		return "." + path
	}
	return path
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// hrefOf 取节点指向的链接：元素取自身 href，没有则找最近的祖先/后代 <a>；
// 规则若直接选中属性/文本节点，节点内容本身就是链接。
func hrefOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type != html.ElementNode {
		return strings.TrimSpace(n.Data)
	}
	if v := htmlquery.SelectAttr(n, "href"); v != "" {
		return strings.TrimSpace(v)
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "a" {
			if v := htmlquery.SelectAttr(p, "href"); v != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	if a := htmlquery.FindOne(n, ".//a[@href]"); a != nil {
		return strings.TrimSpace(htmlquery.SelectAttr(a, "href"))
	}
	return ""
}

// absURL 把页面里的相对链接按页面地址补全成绝对地址
func absURL(pageURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "javascript:") || strings.HasPrefix(ref, "#") {
		return ""
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

// sameSiteHTTPS 有的站（如 AGE）在页面里写死 http:// 链接，而它的 http 端口根本不通。
// 链接与规则 baseURL 同域名、baseURL 是 https、链接却是 http 时，改用 https。
func sameSiteHTTPS(baseURL, link string) string {
	b, err1 := url.Parse(baseURL)
	l, err2 := url.Parse(link)
	if err1 != nil || err2 != nil || b.Scheme != "https" || l.Scheme != "http" || !strings.EqualFold(b.Host, l.Host) {
		return link
	}
	l.Scheme = "https"
	return l.String()
}

// ParseSearch 按规则解析搜索结果页
func ParseSearch(r *rules.Rule, body []byte, contentType, pageURL string) ([]Item, error) {
	doc, err := decode(body, contentType)
	if err != nil {
		return nil, err
	}
	nodes, err := htmlquery.QueryAll(doc, r.SearchList)
	if err != nil {
		return nil, fmt.Errorf("searchList 表达式无效: %w", err)
	}
	var items []Item
	seen := map[string]bool{}
	for _, n := range nodes {
		nameNode, err := htmlquery.Query(n, rel(r.SearchName))
		if err != nil {
			return nil, fmt.Errorf("searchName 表达式无效: %w", err)
		}
		linkNode, err := htmlquery.Query(n, rel(r.SearchResult))
		if err != nil {
			return nil, fmt.Errorf("searchResult 表达式无效: %w", err)
		}
		name := ""
		if nameNode != nil {
			name = cleanText(htmlquery.InnerText(nameNode))
		}
		src := sameSiteHTTPS(r.BaseURL, absURL(pageURL, hrefOf(linkNode)))
		if name == "" || src == "" || seen[src] {
			continue
		}
		seen[src] = true
		items = append(items, Item{Name: name, Src: src})
	}
	return items, nil
}

// ParseChapters 按规则解析详情页里的线路与选集
func ParseChapters(r *rules.Rule, body []byte, contentType, pageURL string) ([]Road, error) {
	doc, err := decode(body, contentType)
	if err != nil {
		return nil, err
	}
	var roadNodes []*html.Node
	if strings.Trim(strings.TrimSpace(r.ChapterRoads), "/") == "" {
		// 个别旧规则把 chapterRoads 写成 "//"，含义是"整个页面就是一条线路"
		roadNodes = []*html.Node{doc}
	} else if roadNodes, err = htmlquery.QueryAll(doc, r.ChapterRoads); err != nil {
		return nil, fmt.Errorf("chapterRoads 表达式无效: %w", err)
	}
	var roads []Road
	for _, rn := range roadNodes {
		epNodes, err := htmlquery.QueryAll(rn, rel(r.ChapterResult))
		if err != nil {
			return nil, fmt.Errorf("chapterResult 表达式无效: %w", err)
		}
		var eps []Episode
		for _, en := range epNodes {
			u := sameSiteHTTPS(r.BaseURL, absURL(pageURL, hrefOf(en)))
			if u == "" {
				continue
			}
			name := cleanText(htmlquery.InnerText(en))
			if name == "" {
				name = fmt.Sprintf("第%d集", len(eps)+1)
			}
			eps = append(eps, Episode{Name: name, URL: u})
		}
		if len(eps) == 0 {
			continue
		}
		roads = append(roads, Road{Name: fmt.Sprintf("线路 %d", len(roads)+1), Episodes: eps})
	}
	return roads, nil
}
