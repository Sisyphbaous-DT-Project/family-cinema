// Package rules 定义番剧播放源规则（兼容 Kazumi 的 KazumiRules JSON 格式）并负责加载。
package rules

import (
	"bytes"
	"encoding/json"
)

// flexString 兼容规则里 "api": "1" 与 "api": 1 两种写法
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	// 数字等其它字面量，原样转成字符串
	*f = flexString(bytes.Trim(b, `"`))
	return nil
}

// AntiCrawler 反爬（验证码）配置。和 Kazumi 一样只对"搜索走网页（xpath）"的规则生效；
// 盒子上没有浏览器，暂不做验证码流程，这类规则标记为不支持（社区规则已陆续改成走接口搜索绕开验证码）。
type AntiCrawler struct {
	Enabled bool `json:"enabled"`
}

// Rule 一条播放源规则。字段名与 Kazumi 规则 JSON 保持一致，方便直接复用社区规则。
type Rule struct {
	API             flexString  `json:"api"`
	Type            string      `json:"type"`
	Name            string      `json:"name"`
	Version         flexString  `json:"version"`
	MuliSources     bool        `json:"muliSources"`
	UseWebview      bool        `json:"useWebview"`
	UseNativePlayer bool        `json:"useNativePlayer"`
	UsePost         bool        `json:"usePost"`
	UseLegacyParser bool        `json:"useLegacyParser"`
	AdBlocker       bool        `json:"adBlocker"`
	UserAgent       string      `json:"userAgent"`
	BaseURL         string      `json:"baseURL"`
	SearchURL       string      `json:"searchURL"`
	SearchList      string      `json:"searchList"`
	SearchName      string      `json:"searchName"`
	SearchResult    string      `json:"searchResult"`
	ChapterRoads    string      `json:"chapterRoads"`
	ChapterResult   string      `json:"chapterResult"`
	Referer         string      `json:"referer"`
	SearchMode      string      `json:"searchMode"`
	ChapterMode     string      `json:"chapterMode"`
	SearchAPI       APISearch   `json:"searchApiConfig"`
	ChapterAPI      APIChapter  `json:"chapterApiConfig"`
	AntiCrawler     AntiCrawler `json:"antiCrawlerConfig"`
}

// SearchByAPI / ChaptersByAPI 搜索、选集是否走接口模式（其它值都按 xpath）
func (r *Rule) SearchByAPI() bool   { return r.SearchMode == ModeAPI }
func (r *Rule) ChaptersByAPI() bool { return r.ChapterMode == ModeAPI }

// Unsupported 返回不支持该规则的原因；空串表示可用。
func (r *Rule) Unsupported() string {
	if r.SearchByAPI() {
		if r.SearchAPI.Request.URL == "" {
			return "incomplete"
		}
	} else {
		if r.AntiCrawler.Enabled {
			return "captcha"
		}
		if r.SearchURL == "" || r.SearchList == "" {
			return "incomplete"
		}
	}
	if r.ChaptersByAPI() {
		if r.ChapterAPI.Request.URL == "" {
			return "incomplete"
		}
	} else if r.ChapterRoads == "" {
		return "incomplete"
	}
	return ""
}

// Info 给前端展示的规则摘要
type Info struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Unsupported string `json:"unsupported,omitempty"`
	AdBlocker   bool   `json:"adBlocker"`
}

func (r *Rule) Info() Info {
	return Info{
		Name:        r.Name,
		Version:     string(r.Version),
		Unsupported: r.Unsupported(),
		AdBlocker:   r.AdBlocker,
	}
}

// ParseRule 解析单条规则 JSON
func ParseRule(data []byte) (*Rule, error) {
	var r Rule
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Name == "" {
		return nil, errNoName
	}
	return &r, nil
}

var errNoName = &ruleError{"规则缺少 name 字段"}

type ruleError struct{ msg string }

func (e *ruleError) Error() string { return e.msg }
