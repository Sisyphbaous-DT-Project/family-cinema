package hls

import (
	"bytes"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// IsM3U8 按内容判断是不是 m3u8（比看 Content-Type 可靠，很多 CDN 把它标成 text/plain）
func IsM3U8(head []byte) bool {
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	return bytes.HasPrefix(bytes.TrimSpace(head), []byte("#EXTM3U"))
}

var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// Rewrite 把清单里的所有地址（分片、子清单、密钥、init 段等）解析成绝对地址后交给 wrap 改写。
func Rewrite(body []byte, base *url.URL, wrap func(abs string) string) []byte {
	resolve := func(ref string) string {
		u, err := url.Parse(strings.TrimSpace(ref))
		if err != nil {
			return ref
		}
		return base.ResolveReference(u).String()
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			if strings.Contains(t, "URI=\"") {
				lines[i] = uriAttr.ReplaceAllStringFunc(t, func(m string) string {
					sub := uriAttr.FindStringSubmatch(m)
					if sub[1] == "" || strings.HasPrefix(sub[1], "data:") {
						return m
					}
					return `URI="` + wrap(resolve(sub[1])) + `"`
				})
			}
		default:
			lines[i] = wrap(resolve(t))
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// segment 媒体清单里的一个分片及其前面的标签行
type segment struct {
	lines []string // 含 #EXTINF 等标签行与最后的地址行
	dur   float64
	group int // 所属的"间断组"，每遇到一个 #EXT-X-DISCONTINUITY 加 1
}

// FilterAds 过滤媒体清单里的广告段。思路：广告通常被 #EXT-X-DISCONTINUITY 隔成独立的小段，
// 时长最长的一组是正片；明显比正片短的组、首尾很短的组、不足 10 秒的组都当广告剔除。
// 主清单（没有 #EXTINF）或没有间断分组的清单原样返回。
func FilterAds(body []byte) []byte {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	var header, tail []string
	var segs []segment
	var pending []string
	group := 0
	var curDur float64
	inSegments := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case t == "#EXT-X-DISCONTINUITY": // 注意不能前缀匹配，否则会误中 #EXT-X-DISCONTINUITY-SEQUENCE
			group++
			pending = append(pending, line)
			inSegments = true
		case strings.HasPrefix(t, "#EXTINF:"):
			curDur = parseDur(t)
			pending = append(pending, line)
			inSegments = true
		case t == "#EXT-X-ENDLIST":
			tail = append(tail, line)
		case strings.HasPrefix(t, "#"):
			if inSegments {
				pending = append(pending, line)
			} else {
				header = append(header, line)
			}
		default: // 分片地址行
			pending = append(pending, line)
			segs = append(segs, segment{lines: pending, dur: curDur, group: group})
			pending, curDur = nil, 0
		}
	}
	if len(segs) == 0 || group == 0 {
		return body
	}

	dur := map[int]float64{}
	for _, s := range segs {
		dur[s.group] += s.dur
	}
	var maxDur float64
	minG, maxG := segs[0].group, segs[0].group
	for g, d := range dur {
		if d > maxDur {
			maxDur = d
		}
		if g < minG {
			minG = g
		}
		if g > maxG {
			maxG = g
		}
	}
	ads := map[int]bool{}
	for g, d := range dur {
		if d == maxDur {
			continue
		}
		if d < maxDur*0.3 || d < 10 || ((g == minG || g == maxG) && d < 30) {
			ads[g] = true
		}
	}
	if len(ads) == 0 {
		return body
	}

	out := append([]string{}, header...)
	for _, s := range segs {
		if !ads[s.group] {
			out = append(out, s.lines...)
		}
	}
	out = append(out, pending...) // 末尾可能残留的标签
	out = append(out, tail...)
	return []byte(strings.Join(out, "\n") + "\n")
}

func parseDur(extinf string) float64 {
	v := strings.TrimPrefix(extinf, "#EXTINF:")
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f
}
