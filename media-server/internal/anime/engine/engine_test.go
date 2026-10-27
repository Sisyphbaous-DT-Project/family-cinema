package engine

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"media-server/internal/anime/rules"
)

const searchHTML = `<html><body>
<ul class="list">
  <li><a class="t" href="/detail/1.html"> 孤独摇滚！ </a></li>
  <li><a class="t" href="detail/2.html">孤独摇滚！ 总集篇</a></li>
  <li><a class="t" href="/detail/1.html">重复项</a></li>
  <li><a class="t" href="#">没有链接</a></li>
</ul>
</body></html>`

const detailHTML = `<html><body>
<div class="src"><ul><li><a href="/play/1-1.html">第01集</a></li><li><a href="/play/1-2.html">第02集</a></li></ul></div>
<div class="src"><ul><li><a href="/play/2-1.html">1</a></li></ul></div>
<div class="src"><ul></ul></div>
</body></html>`

func testRule() *rules.Rule {
	return &rules.Rule{
		Name:          "t",
		BaseURL:       "https://site.test/",
		SearchURL:     "https://site.test/s?q=@keyword",
		SearchList:    "//ul[@class='list']/li",
		SearchName:    "//a",
		SearchResult:  "//a",
		ChapterRoads:  "//div[@class='src']",
		ChapterResult: "//ul/li/a",
	}
}

func TestParseSearch(t *testing.T) {
	items, err := ParseSearch(testRule(), []byte(searchHTML), "text/html; charset=utf-8", "https://site.test/s?q=x")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("期望 2 条（重复和空链接被过滤），实际 %d: %+v", len(items), items)
	}
	if items[0].Name != "孤独摇滚！" || items[0].Src != "https://site.test/detail/1.html" {
		t.Errorf("第一条不对: %+v", items[0])
	}
	// 页面相对路径按当前页面补全（/s?q=x 的目录是 /）
	if items[1].Src != "https://site.test/detail/2.html" {
		t.Errorf("相对链接补全不对: %s", items[1].Src)
	}
}

// 页面里写死 http:// 的同站链接要升级成 https（AGE 的 http 端口不通）；别的域名不动
func TestSameSiteHTTPS(t *testing.T) {
	cases := []struct{ base, in, want string }{
		{"https://www.agedm.io/", "http://www.agedm.io/detail/1", "https://www.agedm.io/detail/1"},
		{"https://www.agedm.io/", "http://other.test/detail/1", "http://other.test/detail/1"},
		{"http://old.test/", "http://old.test/a", "http://old.test/a"},
		{"https://a.test/", "https://a.test/x", "https://a.test/x"},
	}
	for _, c := range cases {
		if got := sameSiteHTTPS(c.base, c.in); got != c.want {
			t.Errorf("sameSiteHTTPS(%q,%q)=%q 期望 %q", c.base, c.in, got, c.want)
		}
	}
}

func TestParseChapters(t *testing.T) {
	roads, err := ParseChapters(testRule(), []byte(detailHTML), "text/html", "https://site.test/detail/1.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(roads) != 2 {
		t.Fatalf("期望 2 条线路（空线路被跳过），实际 %d", len(roads))
	}
	// 每条线路的集只能取自己内部的（"//ul/li/a" 必须当相对路径用）
	if len(roads[0].Episodes) != 2 || len(roads[1].Episodes) != 1 {
		t.Fatalf("各线路集数不对: %d / %d", len(roads[0].Episodes), len(roads[1].Episodes))
	}
	if roads[0].Name != "线路 1" || roads[0].Episodes[1].URL != "https://site.test/play/1-2.html" {
		t.Errorf("线路/集解析不对: %+v", roads[0])
	}
}

func TestParseChaptersWholePageRoad(t *testing.T) {
	r := testRule()
	r.ChapterRoads = "//"
	roads, err := ParseChapters(r, []byte(detailHTML), "text/html", "https://site.test/detail/1.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(roads) != 1 || len(roads[0].Episodes) != 3 {
		t.Fatalf(`chapterRoads="//" 应把整页当一条线路: %+v`, roads)
	}
}

func TestDecodeGBK(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String(searchHTML)
	if err != nil {
		t.Fatal(err)
	}
	items, err := ParseSearch(testRule(), []byte(gbk), "text/html; charset=gbk", "https://site.test/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || items[0].Name != "孤独摇滚！" {
		t.Fatalf("GBK 页面解码失败: %+v", items)
	}
}

func TestDecodeUTF8WithoutDeclaration(t *testing.T) {
	// 没声明编码的 UTF-8 页面不能被当成 windows-1252 解坏
	items, err := ParseSearch(testRule(), []byte(searchHTML), "text/html", "https://site.test/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || items[0].Name != "孤独摇滚！" {
		t.Fatalf("未声明编码的 UTF-8 解码失败: %+v", items)
	}
}

func TestBuildSearch(t *testing.T) {
	base, q := buildSearch("https://a.test/s?q=@keyword&p=1", "孤独 摇滚")
	if base != "https://a.test/s" || q != "q=%E5%AD%A4%E7%8B%AC+%E6%91%87%E6%BB%9A&p=1" {
		t.Errorf("查询串里的关键词应 QueryEscape: %q %q", base, q)
	}
	base, q = buildSearch("https://a.test/search/@keyword/", "a b")
	if base != "https://a.test/search/a%20b/" || q != "" {
		t.Errorf("路径里的关键词应 PathEscape: %q %q", base, q)
	}
}

func TestInvalidExpression(t *testing.T) {
	r := testRule()
	r.SearchList = "//li["
	if _, err := ParseSearch(r, []byte(searchHTML), "text/html", "https://site.test/"); err == nil {
		t.Fatal("无效 XPath 应返回错误")
	}
}
