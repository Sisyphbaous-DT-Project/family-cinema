package engine

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 受限 JSONPath：只支持 Kazumi 规则里允许的写法（见其 RestrictedJsonPath）——
// 以 $ 开头，后面接 .字段、[数字]、[*]、['字段'] / ["字段"]。JSON 用 UseNumber 解码，数字是 json.Number。

type pathSeg struct {
	key  string // 取对象字段
	idx  int    // 取数组下标（key 为空、all 为 false 时）
	all  bool   // [*]：数组的每一项 / 对象的每个值
	isIx bool
}

func parsePath(expr string) ([]pathSeg, error) {
	if !strings.HasPrefix(expr, "$") {
		return nil, fmt.Errorf("JSONPath 必须以 $ 开头: %s", expr)
	}
	var segs []pathSeg
	for i := 1; i < len(expr); {
		switch expr[i] {
		case '.':
			i++
			start := i
			for i < len(expr) && isPathKeyChar(expr[i]) {
				i++
			}
			if i == start {
				return nil, fmt.Errorf("不支持的 JSONPath: %s", expr)
			}
			segs = append(segs, pathSeg{key: expr[start:i]})
		case '[':
			end := bracketEnd(expr, i)
			if end < 0 {
				return nil, fmt.Errorf("JSONPath 缺少 ]: %s", expr)
			}
			content := strings.TrimSpace(expr[i+1 : end])
			switch {
			case content == "*":
				segs = append(segs, pathSeg{all: true})
			case len(content) >= 2 && (content[0] == '\'' || content[0] == '"') && content[len(content)-1] == content[0]:
				segs = append(segs, pathSeg{key: content[1 : len(content)-1]})
			default:
				n, err := strconv.Atoi(content)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("不支持的 JSONPath 片段 [%s]", content)
				}
				segs = append(segs, pathSeg{idx: n, isIx: true})
			}
			i = end + 1
		default:
			return nil, fmt.Errorf("不支持的 JSONPath: %s", expr)
		}
	}
	return segs, nil
}

func isPathKeyChar(c byte) bool {
	return c == '_' || c == '$' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// bracketEnd 找到与 expr[start]（'['）配对的 ']'，引号里的 ']' 不算
func bracketEnd(expr string, start int) int {
	var quote byte
	for i := start + 1; i < len(expr); i++ {
		c := expr[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ']':
			return i
		}
	}
	return -1
}

// jsonRead 按路径取出所有匹配的值
func jsonRead(doc any, expr string) ([]any, error) {
	segs, err := parsePath(expr)
	if err != nil {
		return nil, err
	}
	cur := []any{doc}
	for _, s := range segs {
		var next []any
		for _, v := range cur {
			switch {
			case s.all:
				switch t := v.(type) {
				case []any:
					next = append(next, t...)
				case map[string]any:
					for _, x := range t {
						next = append(next, x)
					}
				}
			case s.isIx:
				if arr, ok := v.([]any); ok && s.idx < len(arr) {
					next = append(next, arr[s.idx])
				}
			default:
				if m, ok := v.(map[string]any); ok {
					if x, ok := m[s.key]; ok {
						next = append(next, x)
					}
				}
			}
		}
		cur = next
	}
	return cur, nil
}

// jsonFirst 取第一个匹配的值，没有就是 nil
func jsonFirst(doc any, expr string) (any, error) {
	vs, err := jsonRead(doc, expr)
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	return vs[0], nil
}

// jsonString 把取到的值变成字符串：字符串去首尾空白；数字按 Kazumi（Dart）的写法——
// 整数原样（30222），带小数点的是浮点数，整值也写成 "1.0"（接口给 1.000 也是 "1.0"），
// 规则作者是照着 Kazumi 拼出来的地址测的（如 sorani 的 /episode/1.0），这里必须一致。
func jsonString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return dartNumber(t)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// dartNumber 把 JSON 数字写成 Dart 的 toString 形式：没有小数点/指数的是整数，原样；否则是浮点数，
// 整值补 ".0"（1.000 → 1.0），非整值用最短表示（1.50 → 1.5）
func dartNumber(n json.Number) string {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		return s
	}
	f, err := n.Float64()
	if err != nil {
		return s
	}
	out := strconv.FormatFloat(f, 'f', -1, 64)
	if f == float64(int64(f)) && !strings.Contains(out, ".") {
		out += ".0"
	}
	return out
}
