// Package dl 把番剧的一集从片源站下载到盒子：分段视频（m3u8）逐段下载、解密、拼成一个 mp4，
// 直接是 mp4 的整个下载。下载流程参考 Kazumi 的 download_manager.dart（分段并发、失败退避重试、
// 断点续下、密钥、去广告段、嵌套清单、非 m3u8 退回整文件下载），只借鉴做法，没拷代码；
// 比 Kazumi 多一步"拼成 mp4"：Kazumi 用自带播放器直接播本地分段，我们的片库、手机缓存、
// 浏览器播放都只认单个视频文件。
package dl

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Segment 媒体清单里的一个分段
type Segment struct {
	URI string  // 绝对地址
	Seq int64   // 媒体序号（加密没给 IV 时用它当 IV）
	Key *Key    // 解密密钥，nil 表示不加密
	Map string  // fMP4 的初始化段地址（EXT-X-MAP），TS 分段为空
	Dur float64 // 时长（秒，来自 #EXTINF；量大小时用）
}

// Key EXT-X-KEY
type Key struct {
	Method string // AES-128（其它加密方式不支持）
	URI    string
	IV     []byte // 16 字节；nil 表示用分段序号
}

// isMaster 是不是主清单（列的是不同清晰度的子清单）
func isMaster(body string) bool {
	return strings.Contains(body, "#EXT-X-STREAM-INF")
}

// bestVariant 从主清单里挑带宽最高的子清单（和 Kazumi 一样下最清楚的）
func bestVariant(body string, base *url.URL) (string, error) {
	best, bestBW := "", int64(-1)
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	bw := int64(-1)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "#EXT-X-STREAM-INF"):
			bw = 0
			if v, ok := attrs(l)["BANDWIDTH"]; ok {
				bw, _ = strconv.ParseInt(v, 10, 64)
			}
		case l != "" && !strings.HasPrefix(l, "#") && bw >= 0:
			if u, err := base.Parse(l); err == nil && bw > bestBW {
				best, bestBW = u.String(), bw
			}
			bw = -1
		}
	}
	if best == "" {
		return "", errors.New("清单里没有可下载的清晰度")
	}
	return best, nil
}

// parseMedia 解析媒体清单，分段地址都转成绝对地址
func parseMedia(body string, base *url.URL) ([]Segment, error) {
	var segs []Segment
	var key *Key
	var mapURI string
	seq := int64(0)
	dur := 0.0
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "#EXTINF:"):
			v := strings.TrimPrefix(l, "#EXTINF:")
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			dur, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
		case strings.HasPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"):
			seq, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(l, "#EXT-X-MEDIA-SEQUENCE:")), 10, 64)
		case strings.HasPrefix(l, "#EXT-X-KEY:"):
			a := attrs(l)
			switch strings.ToUpper(a["METHOD"]) {
			case "", "NONE":
				key = nil
			case "AES-128":
				u, err := base.Parse(a["URI"])
				if err != nil || a["URI"] == "" {
					return nil, errors.New("加密视频缺少密钥地址")
				}
				k := &Key{Method: "AES-128", URI: u.String()}
				if iv := a["IV"]; iv != "" {
					b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(iv, "0x"), "0X"))
					if err != nil || len(b) != 16 {
						return nil, errors.New("加密视频的 IV 格式不对")
					}
					k.IV = b
				}
				key = k
			default:
				return nil, fmt.Errorf("这个视频用了盒子不支持的加密方式（%s）", a["METHOD"])
			}
		case strings.HasPrefix(l, "#EXT-X-MAP:"):
			if u, err := base.Parse(attrs(l)["URI"]); err == nil {
				mapURI = u.String()
			}
		case l != "" && !strings.HasPrefix(l, "#"):
			u, err := base.Parse(l)
			if err != nil {
				continue
			}
			segs = append(segs, Segment{URI: u.String(), Seq: seq, Key: key, Map: mapURI, Dur: dur})
			seq++
			dur = 0
		}
	}
	return segs, nil
}

// attrs 解析 m3u8 标签里的 KEY=VALUE 属性（值可以带引号，引号里可以有逗号）
func attrs(line string) map[string]string {
	out := map[string]string{}
	if i := strings.IndexByte(line, ':'); i >= 0 {
		line = line[i+1:]
	}
	for len(line) > 0 {
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			break
		}
		k := strings.ToUpper(strings.TrimSpace(line[:eq]))
		line = line[eq+1:]
		var v string
		if strings.HasPrefix(line, `"`) {
			end := strings.IndexByte(line[1:], '"')
			if end < 0 {
				v, line = line[1:], ""
			} else {
				v, line = line[1:1+end], line[2+end:]
			}
		} else {
			end := strings.IndexByte(line, ',')
			if end < 0 {
				v, line = line, ""
			} else {
				v, line = line[:end], line[end:]
			}
		}
		out[k] = strings.TrimSpace(v)
		line = strings.TrimPrefix(strings.TrimSpace(line), ",")
	}
	return out
}
