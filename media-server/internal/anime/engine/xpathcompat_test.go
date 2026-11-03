package engine

import (
	"strings"
	"testing"

	"github.com/antchfx/htmlquery"
)

const tvtfunExpr = "//ol[@class*='tvt-watch-episodes']"

const tvtfunHTML = `<html><body>
<ol class="tvt-watch-tabs"><li><a href="/tab/1">线路一</a></li></ol>
<ol class="tvt-watch-episodes"><li><a href="/play/1-1">第1集</a></li><li><a href="/play/1-2">第2集</a></li></ol>
<ol class="list tvt-watch-episodes active"><li><a href="/play/2-1">第1集</a></li></ol>
</body></html>`

// ids 按表达式选中的节点，取出它们的 id（按文档顺序拼起来）
func ids(t *testing.T, doc string, expr string) string {
	t.Helper()
	root, err := htmlquery.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := htmlquery.QueryAll(root, compatXPath(expr))
	if err != nil {
		t.Fatalf("%s → %s 编译失败: %v", expr, compatXPath(expr), err)
	}
	var out []string
	for _, n := range nodes {
		out = append(out, htmlquery.SelectAttr(n, "id"))
	}
	return strings.Join(out, ",")
}

// TvTFun 1.3 的选集表达式：原样交给 XPath 库会报错（这就是这次的根因），改写后能选中两个 ol
func TestCompatTvTFunExpression(t *testing.T) {
	root, _ := htmlquery.Parse(strings.NewReader(tvtfunHTML))
	if _, err := htmlquery.QueryAll(root, tvtfunExpr); err == nil {
		t.Fatal("原表达式应该编译不过（不然这个兼容层就没必要了）")
	}
	if got, want := compatXPath(tvtfunExpr), "//ol[contains(@class, 'tvt-watch-episodes')]"; got != want {
		t.Fatalf("改写结果 %q 想要 %q", got, want)
	}
	nodes, err := htmlquery.QueryAll(root, compatXPath(tvtfunExpr))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || htmlquery.SelectAttr(nodes[0], "class") != "tvt-watch-episodes" ||
		htmlquery.SelectAttr(nodes[1], "class") != "list tvt-watch-episodes active" {
		t.Fatalf("应只选中两个选集 ol，实际 %d 个", len(nodes))
	}
}

const attrHTML = `<html><body>
<div id="d1" class="nav main-nav" lang="en-US" data-k="abc-def"></div>
<div id="d2" class="main" lang="en" data-k="def"></div>
<div id="d3" class="mainline" lang="english" data-k="xabc"></div>
<div id="d4" lang="fr" w="2"></div>
<div id="d5" class=" foo  main	bar " title="it's"></div>
</body></html>`

// 五种写法的选择语义与 CSS 属性选择器（也就是 Kazumi 的 xpath_selector）一致
func TestCompatAttrOperators(t *testing.T) {
	cases := []struct{ expr, want string }{
		{"//div[@class*='main']", "d1,d2,d3,d5"},
		{"//div[@class^='main']", "d2,d3"},
		{"//div[@class$='nav']", "d1"},
		{"//div[@class~='main']", "d2,d5"}, // main-nav、mainline 不算 main 这一项
		{"//div[@lang|='en']", "d1,d2"},    // en、en-US 算，english 不算
		{`//div[@data-k*="abc"]`, "d1,d3"}, // 双引号
		{"//div[ @class *= 'main' ]", "d1,d2,d3,d5"},
		{"//div[@class^='main' and @lang|='en']", "d2"},
		{"//div[@id='d4' or @class$='line']", "d3,d4"},
		{"//div[not(@class*='main')]", "d4"},
		{"//*[@class*='main'][@lang]", "d1,d2,d3"},
		{`//div[@title*="it's"]`, "d5"},
		{"//div[@class*='']", ""},    // 空值什么都不匹配（CSS 规定）
		{"//div[@class~='a b']", ""}, // 带空白的值不可能是"一项"
		{"//div[@class~='']", ""},
		{"//div[@missing*='x']", ""},
	}
	for _, c := range cases {
		if got := ids(t, attrHTML, c.expr); got != c.want {
			t.Errorf("%s → %s 选中 [%s] 想要 [%s]", c.expr, compatXPath(c.expr), got, c.want)
		}
	}
}

// 标准 XPath 里本来就合法的写法一个字都不能动：引号里的字、已有的函数、通配符、乘法、!=、并集
func TestCompatLeavesValidXPathAlone(t *testing.T) {
	keep := []string{
		"//div[contains(@class, 'main')]",
		"//div[contains(@class,'module-play-list-content')]",
		"//*[@id='a']/*",
		"//div[@w * 2 = 4]",
		"//div[@w*2=4]",
		`//a[text()="@class*='x'"]`,
		"//a[@title='a*=b']",
		"//a[@x!='y']",
		"//div[@class='a'] | //span",
		"//div[starts-with(@id, 'playlist')][contains(@class, 'lists-box')]",
		"/self::*[@class='line_button_ban']/following-sibling::a[@class='circuit_switch_ban'] | /self::*[@class='line_button1']/following-sibling::a",
		"//div[2]/text()",
		"//",
		"",
	}
	for _, e := range keep {
		if got := compatXPath(e); got != e {
			t.Errorf("不该改写 %q，结果变成了 %q", e, got)
		}
	}
	// 真乘法照常算：d4 的 w=2
	if got := ids(t, attrHTML, "//div[@w * 2 = 4]"); got != "d4" {
		t.Errorf("乘法表达式结果不对: %s", got)
	}
	// 引号里的字原样保留，引号外的照常改写
	if got, want := compatXPath(`//div[@title="it's *= x"][@class*='main']`), `//div[@title="it's *= x"][contains(@class, 'main')]`; got != want {
		t.Errorf("引号处理不对: %q 想要 %q", got, want)
	}
	// 路径中间的 /@属性 不是独立条件，不认（留给 XPath 库报错，更新前就会被拦下）
	if got := compatXPath("//div/@class*='x'"); got != "//div/@class*='x'" {
		t.Errorf("路径里的属性步不该改写: %q", got)
	}
}

// 搜索和选集两个入口都走同一套兼容改写，子选择器照旧相对于上级节点
func TestParseUsesCompatXPath(t *testing.T) {
	r := testRule()
	r.SearchList = "//ul[@class*='result']/li"
	r.SearchName = "//a[@class^='tit']"
	r.SearchResult = "//a[@href$='.html']"
	page := `<html><body>
<a class="title" href="/outside.html">外面的链接</a>
<ul class="result-list"><li><a class="title" href="/detail/1.html">孤独摇滚！</a></li><li><a class="title" href="/detail/2.html">总集篇</a></li></ul>
</body></html>`
	items, err := ParseSearch(r, []byte(page), "text/html; charset=utf-8", "https://site.test/s")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "孤独摇滚！" || items[1].Src != "https://site.test/detail/2.html" {
		t.Fatalf("搜索结果不对（子选择器应相对于每一条 li）: %+v", items)
	}

	r.ChapterRoads = tvtfunExpr
	r.ChapterResult = "//li/a"
	roads, err := ParseChapters(r, []byte(tvtfunHTML), "text/html; charset=utf-8", "https://site.test/detail/1.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(roads) != 2 || len(roads[0].Episodes) != 2 || len(roads[1].Episodes) != 1 ||
		roads[1].Episodes[0].URL != "https://site.test/play/2-1" {
		t.Fatalf("选集不对（每条线路只取自己 ol 里的集）: %+v", roads)
	}
}
