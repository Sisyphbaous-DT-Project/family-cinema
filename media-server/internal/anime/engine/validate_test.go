package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 仿 TvTFun 1.3 的写法：接口搜索 + 网页选集，选集表达式用 *=
const tvtfunRule = `{"api":"1","name":"TvTFun","version":"1.3","useWebview":true,"baseURL":"https://tvt.test/",
 "searchMode":"api","chapterMode":"xpath",
 "searchApiConfig":{"request":{"url":"https://tvt.test/index.php/ajax/suggest","query":{"mid":1,"wd":"@keyword"}},
   "listPath":"$.list[*]","namePath":"$.name","sourcePath":"$.id","sourceTemplate":"https://tvt.test/detail/@source"},
 "searchList":"","searchName":"","searchResult":"",
 "chapterRoads":"//ol[@class*='tvt-watch-episodes']","chapterResult":"//li/a"}`

func wantProblem(t *testing.T, err error, reason, field string) {
	t.Helper()
	var p *RuleProblem
	if !errors.As(err, &p) {
		t.Fatalf("应报规则问题（%s），实际 %v", reason, err)
	}
	if p.Reason != reason || p.Field != field {
		t.Errorf("问题不对: %q / %q，想要 %q / %q（%s）", p.Reason, p.Field, reason, field, p.Detail())
	}
}

func TestValidateRule(t *testing.T) {
	// TvTFun：接口搜索时没用到的空 XPath 字段不算错；*= 经兼容改写后能用
	if err := ValidateRule(mustRule(t, tvtfunRule)); err != nil {
		t.Fatalf("TvTFun 应通过: %v", err)
	}
	// 搜索、选集都走接口：XPath 字段全空也没事；地址里的 @keyword/@source 按样例值展开后再判断
	allAPI := mustRule(t, `{"name":"a","baseURL":"https://a.test/","searchMode":"api","chapterMode":"api",
	 "searchApiConfig":{"request":{"method":"POST","url":"https://a.test/s/@keyword","bodyType":"json","body":{"kw":"@keyword"}}},
	 "chapterApiConfig":{"request":{"url":"https://a.test/v/@source"},"format":"delimited",
	   "roadNamesPath":"$.from","roadEpisodesPath":"$.url","variables":{"vid":"$.id"},
	   "episodePage":{"url":"/play/@vid-@roadNumber-@episodeNumber"}}}`)
	if err := ValidateRule(allAPI); err != nil {
		t.Fatalf("全接口规则应通过: %v", err)
	}

	bad := func(mod func(m map[string]string)) error {
		fields := map[string]string{
			"searchURL": "https://x.test/s?wd=@keyword", "searchList": "//ul/li", "searchName": "//a", "searchResult": "//a",
			"chapterRoads": "//div[@class='src']", "chapterResult": "//li/a",
		}
		mod(fields)
		var parts []string
		for k, v := range fields {
			parts = append(parts, `"`+k+`":`+jsonQuote(v))
		}
		return ValidateRule(mustRule(t, `{"name":"x",`+strings.Join(parts, ",")+`}`))
	}
	if err := bad(func(map[string]string) {}); err != nil {
		t.Fatalf("正常的网页规则应通过: %v", err)
	}
	wantProblem(t, bad(func(m map[string]string) { m["chapterRoads"] = "//ol[@class*=tvt]" }), "选集表达式暂未兼容", "chapterRoads") // 值不带引号：不认
	wantProblem(t, bad(func(m map[string]string) { m["chapterResult"] = "//li/a[" }), "选集表达式暂未兼容", "chapterResult")
	wantProblem(t, bad(func(m map[string]string) { m["searchName"] = "" }), "搜索表达式暂未兼容", "searchName")
	wantProblem(t, bad(func(m map[string]string) { m["searchList"] = "//div/@class*='x'" }), "搜索表达式暂未兼容", "searchList")
	wantProblem(t, bad(func(m map[string]string) { m["searchURL"] = "/s?wd=@keyword" }), "搜索地址写法不对", "searchURL")
	// 整页一条线路（"//"）照旧不用查
	if err := bad(func(m map[string]string) { m["chapterRoads"] = "//" }); err != nil {
		t.Errorf("chapterRoads 写 // 应通过: %v", err)
	}

	api := func(search, chapter string) error {
		return ValidateRule(mustRule(t, `{"name":"y","baseURL":"https://y.test/","searchMode":"api","chapterMode":"api",
		 "searchApiConfig":`+search+`,"chapterApiConfig":`+chapter+`}`))
	}
	okSearch := `{"request":{"url":"https://y.test/s?wd=@keyword"}}`
	okChapter := `{"request":{"url":"https://y.test/v?id=@source"}}`
	wantProblem(t, api(`{"request":{"url":"https://y.test/s?wd=@keyword&p=@page"}}`, okChapter), "搜索接口地址写法不对", "searchApiConfig.request")
	wantProblem(t, api(`{"request":{"url":"https://y.test/s","method":"PUT"}}`, okChapter), "搜索接口地址写法不对", "searchApiConfig.request")
	wantProblem(t, api(`{"request":{"url":"https://y.test/s"},"listPath":"$..list"}`, okChapter), "搜索接口取值路径暂未兼容", "searchApiConfig.listPath")
	wantProblem(t, api(okSearch, `{"request":{"url":"y.test/v/@source"}}`), "选集接口地址写法不对", "chapterApiConfig.request")
	wantProblem(t, api(okSearch, `{"request":{"url":"https://y.test/v"},"episodesPath":"data.eps"}`), "选集接口取值路径暂未兼容", "chapterApiConfig.episodesPath")
	wantProblem(t, api(okSearch, `{"request":{"url":"https://y.test/v"},"format":"delimited","roadNamesPath":"$.a","roadEpisodesPath":"$.b","fieldSeparator":""}`), "选集分隔符是空的", "chapterApiConfig")
	wantProblem(t, api(okSearch, `{"request":{"url":"https://y.test/v"},"episodePage":{"url":"/p/@nope"}}`), "播放页地址模板写法不对", "chapterApiConfig.episodePage")
	// 留空表示"不用"的字段不查
	if err := api(okSearch, `{"request":{"url":"https://y.test/v"},"roadsPath":"","episodesPath":"$.eps[*]","episodeUrlPath":""}`); err != nil {
		t.Errorf("不分线路、不取剧集地址的接口选集应通过: %v", err)
	}
}

// XPath 编译通过后，谓词遇到真实节点仍可能 panic：更新前要拦住，实际解析也不能把 panic 抛给调用方。
func TestXPathEvaluationPanicIsRuleError(t *testing.T) {
	r := testRule()
	r.SearchList = "//div[ends-with(name(), 0)]"
	wantProblem(t, ValidateRule(r), "搜索表达式暂未兼容", "searchList")
	if _, err := ParseSearch(r, []byte(attrHTML), "text/html", "https://site.test/"); err == nil || !strings.Contains(err.Error(), "XPath 求值失败") {
		t.Errorf("真实搜索页面的 XPath panic 应变成普通错误: %v", err)
	}

	r = testRule()
	r.SearchName = "//a[ends-with(name(), 0)]"
	wantProblem(t, ValidateRule(r), "搜索表达式暂未兼容", "searchName")
	page := `<ul class="list"><li><a href="/detail">名字</a></li></ul>`
	if _, err := ParseSearch(r, []byte(page), "text/html", "https://site.test/"); err == nil || !strings.Contains(err.Error(), "XPath 求值失败") {
		t.Errorf("单节点查询的 XPath panic 也应变成普通错误: %v", err)
	}

	r = testRule()
	r.ChapterRoads = "//ol[ends-with(name(), 0)]"
	wantProblem(t, ValidateRule(r), "选集表达式暂未兼容", "chapterRoads")
	if _, err := ParseChapters(r, []byte(tvtfunHTML), "text/html", "https://site.test/"); err == nil || !strings.Contains(err.Error(), "XPath 求值失败") {
		t.Errorf("真实选集页面的 XPath panic 应变成普通错误: %v", err)
	}
}

func TestValidateSourceTemplate(t *testing.T) {
	const original = "https://tvt.test/detail/@source"
	for _, c := range []struct{ name, tpl string }{
		{"非法地址", "://bad/@source"},
		{"未知变量", "https://tvt.test/detail/@sourc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := mustRule(t, strings.Replace(tvtfunRule, original, c.tpl, 1))
			wantProblem(t, ValidateRule(r), "搜索详情页地址写法不对", "searchApiConfig.sourceTemplate")
			if c.name == "未知变量" {
				if _, err := parseSearchAPI(r.SearchAPI, mustDoc(t, `{"list":[{"id":1,"name":"测试"}]}`)); err == nil || !strings.Contains(err.Error(), "@sourc") {
					t.Errorf("运行时也应明确报出写错的模板变量: %v", err)
				}
			}
		})
	}

	// 相对地址按 baseURL 补全；首尾空格沿用旧行为去掉，不能因抽出共用函数而改变结果。
	for _, tpl := range []string{"/detail/@source", "  " + original + "  "} {
		r := mustRule(t, strings.Replace(tvtfunRule, original, tpl, 1))
		if err := ValidateRule(r); err != nil {
			t.Errorf("正常模板 %q 应通过: %v", tpl, err)
		}
	}
	r := mustRule(t, strings.Replace(tvtfunRule, original, "  "+original+"  ", 1))
	items, err := parseSearchAPI(r.SearchAPI, mustDoc(t, `{"list":[{"id":1,"name":"测试"}]}`))
	if err != nil || len(items) != 1 || items[0].Src != "https://tvt.test/detail/1" {
		t.Errorf("模板首尾空格应照旧去掉: %+v, %v", items, err)
	}
}

func jsonQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// 项目里内嵌的社区规则：能用的都要通过检查，而且兼容改写不改动其中任何一个表达式
func TestValidateEmbeddedRules(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "rules")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip("找不到内嵌规则目录:", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "index.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		r := mustRule(t, string(data))
		for _, x := range []string{r.SearchList, r.SearchName, r.SearchResult, r.ChapterRoads, r.ChapterResult} {
			if compatXPath(x) != x {
				t.Errorf("%s: 兼容改写动了本来能用的表达式 %q → %q", r.Name, x, compatXPath(x))
			}
		}
		if r.Unsupported() != "" {
			continue
		}
		n++
		if err := ValidateRule(r); err != nil {
			detail := ""
			if p := (*RuleProblem)(nil); errors.As(err, &p) {
				detail = p.Detail()
			}
			t.Errorf("%s 应通过检查: %v（%s）", r.Name, err, detail)
		}
	}
	if n == 0 {
		t.Fatal("一条可用的内嵌规则都没读到")
	}
}
