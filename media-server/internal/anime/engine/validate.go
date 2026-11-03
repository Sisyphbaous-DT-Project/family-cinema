package engine

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"

	"media-server/internal/anime/rules"
)

// RuleProblem 规则在这个引擎上用不了的地方。Reason 是给人看的一句话（设置页显示），
// Field 和 Err 是具体哪个字段、库报的什么错（记日志用）。
type RuleProblem struct {
	Reason string
	Field  string
	Err    error
}

func (p *RuleProblem) Error() string { return p.Reason }
func (p *RuleProblem) Unwrap() error { return p.Err }

// Detail 字段和底层原因，排查用
func (p *RuleProblem) Detail() string {
	if p.Err == nil {
		return p.Field
	}
	return p.Field + ": " + p.Err.Error()
}

func problem(reason, field string, err error) *RuleProblem {
	return &RuleProblem{Reason: reason, Field: field, Err: err}
}

// 更新前检查用的样例值：只是让模板能展开，不会真的拿去请求
const (
	sampleKeyword = "测试"
	sampleSource  = "1"
)

// ValidateRule 不联网检查一条规则在本引擎上能不能用：当前模式实际要用的 XPath 都按运行时
// 同一套写法（兼容改写 + 相对化）真跑一遍、JSONPath 解析一遍、地址模板用样例值展开一遍。
// 站点连不上、限流这些不在这里管——它只回答"这份规则本身引擎认不认得"。
// 验证码、缺字段这类由 Rule.Unsupported 判断，调用方先看那个。
func ValidateRule(r *rules.Rule) error {
	if r.SearchByAPI() {
		if err := validateSearchAPI(r.SearchAPI); err != nil {
			return err
		}
		if err := validateSourceTemplate(r); err != nil {
			return err
		}
	} else if err := validateSearchXPath(r); err != nil {
		return err
	}
	if r.ChaptersByAPI() {
		return validateChapterAPI(r.BaseURL, r.ChapterAPI)
	}
	return validateChapterXPath(r)
}

func validateSearchXPath(r *rules.Rule) error {
	base, query := buildSearch(r.SearchURL, sampleKeyword)
	target := base
	if !r.UsePost && query != "" {
		target += "?" + query
	}
	if u, err := url.Parse(target); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return problem("搜索地址写法不对", "searchURL", fmt.Errorf("展开后是 %q", target))
	}
	x := searchXPath(r)
	if err := tryXPath(x.list, true); err != nil {
		return problem("搜索表达式暂未兼容", "searchList", err)
	}
	if err := tryXPath(x.name, false); err != nil {
		return problem("搜索表达式暂未兼容", "searchName", err)
	}
	if err := tryXPath(x.result, false); err != nil {
		return problem("搜索表达式暂未兼容", "searchResult", err)
	}
	return nil
}

func validateChapterXPath(r *rules.Rule) error {
	x := chapterXPath(r)
	if !x.wholePage {
		if err := tryXPath(x.roads, true); err != nil {
			return problem("选集表达式暂未兼容", "chapterRoads", err)
		}
	}
	if err := tryXPath(x.result, true); err != nil {
		return problem("选集表达式暂未兼容", "chapterResult", err)
	}
	return nil
}

// tryXPath 和解析页面时使用同一套安全查询，在包含常见节点的样例页面上跑一次。
// 空页面不会执行谓词，可能漏掉函数参数类型错误；运行时的安全查询仍作为最后保护。
func tryXPath(expr string, all bool) error {
	var doc *html.Node
	doc, err := htmlquery.Parse(strings.NewReader(`<html><body>
	<div><ul><li><a href="/1">1</a></li></ul></div>
	<ol><li><a href="/2">2</a></li></ol><span></span><p></p><b></b>
	</body></html>`))
	if err != nil {
		return err
	}
	if all {
		_, err = queryAllXPath(doc, expr)
	} else {
		_, err = queryOneXPath(doc, expr)
	}
	return err
}

func validateSearchAPI(cfg rules.APISearch) error {
	if _, err := buildAPIRequest(cfg.Request, map[string]any{"keyword": sampleKeyword}); err != nil {
		return problem("搜索接口地址写法不对", "searchApiConfig.request", err)
	}
	for _, p := range []struct{ field, path string }{
		{"listPath", cfg.ListPath}, {"namePath", cfg.NamePath}, {"sourcePath", cfg.SourcePath},
	} {
		if _, err := parsePath(p.path); err != nil {
			return problem("搜索接口取值路径暂未兼容", "searchApiConfig."+p.field, err)
		}
	}
	return nil
}

// validateSourceTemplate 检查接口搜索的详情页模板。模板为空时来源可能是编号，也可能是完整地址，
// 不做额外限制；网页选集需要地址时，带模板的结果必须能补全成 HTTP(S) URL。
func validateSourceTemplate(r *rules.Rule) error {
	if strings.TrimSpace(r.SearchAPI.SourceTemplate) == "" {
		return nil
	}
	src, err := buildSourceURL(r.SearchAPI.SourceTemplate, sampleSource)
	if err != nil {
		return problem("搜索详情页地址写法不对", "searchApiConfig.sourceTemplate", err)
	}
	if r.ChaptersByAPI() {
		return nil
	}
	target := normalizeURL(r.BaseURL, src)
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return problem("搜索详情页地址写法不对", "searchApiConfig.sourceTemplate", fmt.Errorf("展开后是 %q", target))
	}
	return nil
}

// validateChapterAPI 和 parseChaptersAPI 用到的字段一一对应：运行时一定会读的都要能解析，
// 留空表示"不用"的（线路路径、线路名、剧集地址）留空就不查
func validateChapterAPI(baseURL string, cfg rules.APIChapter) error {
	if _, err := buildAPIRequest(cfg.Request, map[string]any{"source": sampleSource}); err != nil {
		return problem("选集接口地址写法不对", "chapterApiConfig.request", err)
	}
	type pathField struct{ field, path string }
	var paths []pathField
	for k, p := range cfg.Variables {
		paths = append(paths, pathField{"variables." + k, p})
	}
	if cfg.Format == rules.FormatDelimited {
		if cfg.RoadSeparator == "" || cfg.EpisodeSeparator == "" || cfg.FieldSeparator == "" {
			return problem("选集分隔符是空的", "chapterApiConfig", nil)
		}
		paths = append(paths, pathField{"roadNamesPath", cfg.RoadNamesPath}, pathField{"roadEpisodesPath", cfg.RoadEpisodesPath})
	} else {
		hasRoads := strings.TrimSpace(cfg.RoadsPath) != ""
		if hasRoads {
			paths = append(paths, pathField{"roadsPath", cfg.RoadsPath})
			if strings.TrimSpace(cfg.RoadNamePath) != "" {
				paths = append(paths, pathField{"roadNamePath", cfg.RoadNamePath})
			}
		}
		paths = append(paths, pathField{"episodesPath", cfg.EpisodesPath}, pathField{"episodeNamePath", cfg.EpisodeNamePath})
		if strings.TrimSpace(cfg.EpisodeURLPath) != "" {
			paths = append(paths, pathField{"episodeUrlPath", cfg.EpisodeURLPath})
		}
	}
	for _, p := range paths {
		if _, err := parsePath(p.path); err != nil {
			return problem("选集接口取值路径暂未兼容", "chapterApiConfig."+p.field, err)
		}
	}
	if cfg.EpisodePage != nil {
		root := map[string]any{"source": sampleSource}
		for k := range cfg.Variables {
			root[k] = sampleSource
		}
		if _, err := episodeURL(baseURL, cfg, root, "/play/1", 0, 0); err != nil {
			return problem("播放页地址模板写法不对", "chapterApiConfig.episodePage", err)
		}
	}
	return nil
}
