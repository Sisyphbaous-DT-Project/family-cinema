package rulesync

import (
	"regexp"
	"strconv"
	"strings"
)

// compareVersion 比较 "1.6" 这类点分数字版本：a<b 返回 -1，相等 0，a>b 1。
// 短的那个缺的段按 0 算（"1" 与 "1.0" 相等）；任何一段不是纯数字就返回 ok=false。
func compareVersion(a, b string) (int, bool) {
	pa, ok1 := parseVersion(a)
	pb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return nil, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// newer 远端版本是否比本地新。远端看不懂就不算新（落后的镜像在，不能"不一样就换"）；
// 本地看不懂而远端看得懂，算新（换成一个看得懂的版本）。
func newer(remote, local string) bool {
	if _, ok := parseVersion(remote); !ok {
		return false
	}
	if c, ok := compareVersion(remote, local); ok {
		return c > 0
	}
	return true
}

// apiLevel 规则要求的引擎等级："api" 缺省按 1；写了却看不懂返回 ok=false
func apiLevel(api string) (int, bool) {
	api = strings.TrimSpace(api)
	if api == "" {
		return 1, true
	}
	n, err := strconv.Atoi(api)
	return n, err == nil
}

// 规则名要拼成文件名写盘，只收字母数字和 . _ -，也不能以点开头
var safeName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)
