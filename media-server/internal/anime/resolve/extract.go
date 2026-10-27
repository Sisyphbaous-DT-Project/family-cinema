// Package resolve 把"某一集的播放页地址"解析成可播放的视频地址：
// 先静态读播放页（快车道），读不出来再交给手机解析助手嗅探。
package resolve

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var (
	// 页面里直接写出来的 m3u8/mp4 地址（\/ 转义要先还原）
	mediaURLRe = regexp.MustCompile(`https?://[^\s"'<>\\)]+?\.(?:m3u8|mp4)(?:\?[^\s"'<>\\)]*)?`)
	iframeRe   = regexp.MustCompile(`(?is)<iframe[^>]+?src\s*=\s*["']([^"']+)["']`)
	playerVar  = regexp.MustCompile(`player_\w+\s*=\s*\{`)
)

// Kind 按地址判断媒体类型
func Kind(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if strings.HasSuffix(strings.ToLower(u), ".mp4") {
		return "mp4"
	}
	return "hls"
}

// IsDirectMedia 地址本身就是视频（路径以 .m3u8/.mp4 结尾），而不是播放页
func IsDirectMedia(u string) bool {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.ToLower(u)
	return strings.HasSuffix(u, ".m3u8") || strings.HasSuffix(u, ".mp4")
}

// extractBraced 从 s[start]（必须是 '{'）开始取出配平的 JSON 对象文本，字符串里的括号不算
func extractBraced(s string, start int) string {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// decodeMacCMS 还原苹果 CMS 播放器变量里的 url：encrypt=1 为 escape 编码，2 为 base64 后再 escape
func decodeMacCMS(encrypt int, raw string) string {
	switch encrypt {
	case 1:
		if v, err := url.PathUnescape(raw); err == nil {
			return v
		}
	case 2:
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			b, err = base64.RawStdEncoding.DecodeString(raw) // 有的站不补 =
		}
		if err == nil {
			if v, err := url.PathUnescape(string(b)); err == nil {
				return v
			}
		}
	}
	return raw
}

// Extract 在播放页里找候选视频地址与 iframe 地址。pageURL 用来补全相对地址。
func Extract(page, pageURL string) (cands, iframes []string) {
	base, _ := url.Parse(pageURL)
	abs := func(ref string) string {
		u, err := url.Parse(strings.TrimSpace(ref))
		if err != nil || base == nil {
			return ""
		}
		return base.ResolveReference(u).String()
	}
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			cands = append(cands, u)
		}
	}

	// 1) 苹果 CMS 的 player_aaaa = {...,"url":"..."}
	for _, loc := range playerVar.FindAllStringIndex(page, -1) {
		obj := extractBraced(page, loc[1]-1)
		if obj == "" {
			continue
		}
		var pv struct {
			Encrypt int    `json:"encrypt"`
			URL     string `json:"url"`
		}
		if json.Unmarshal([]byte(obj), &pv) != nil || pv.URL == "" {
			continue
		}
		u := decodeMacCMS(pv.Encrypt, pv.URL)
		lower := strings.ToLower(u)
		if strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mp4") {
			add(abs(u))
		}
	}

	// 2) 页面里直接出现的媒体地址
	flat := strings.ReplaceAll(page, `\/`, `/`)
	for _, m := range mediaURLRe.FindAllString(flat, -1) {
		add(m)
	}

	// 3) iframe（很多站把播放器放在第三方解析页里）
	for _, m := range iframeRe.FindAllStringSubmatch(page, -1) {
		src := strings.TrimSpace(m[1])
		if src == "" || strings.HasPrefix(src, "about:") || strings.HasPrefix(src, "javascript:") {
			continue
		}
		if u := abs(src); strings.HasPrefix(u, "http") {
			iframes = append(iframes, u)
		}
	}
	return cands, iframes
}
