package rules

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestParseRuleFlexAndUnsupported(t *testing.T) {
	// api 与 version 既可能是字符串也可能是数字
	r, err := ParseRule([]byte(`{"api":8,"name":"x","version":1.5,"searchURL":"u","searchList":"//a","chapterRoads":"//b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.API != "8" || r.Version != "1.5" {
		t.Errorf("数字字段转字符串不对: %q %q", r.API, r.Version)
	}
	if r.Unsupported() != "" {
		t.Errorf("完整的 xpath 规则应可用: %s", r.Unsupported())
	}
	r2, _ := ParseRule([]byte(`{"name":"y","searchMode":"api"}`))
	if r2.Unsupported() != "incomplete" {
		t.Errorf("api 模式缺请求地址应标记不完整: %q", r2.Unsupported())
	}
	r3, _ := ParseRule([]byte(`{"name":"z","searchURL":"u","searchList":"//a","chapterRoads":"//b","antiCrawlerConfig":{"enabled":true}}`))
	if r3.Unsupported() != "captcha" {
		t.Errorf("网页搜索 + 验证码的规则应标记不支持: %q", r3.Unsupported())
	}
	// 和 Kazumi 一样：验证码只管网页搜索，搜索改走接口的就可用（社区规则 dalvdm/mgnacg/mutefun 都是这样）
	r4, _ := ParseRule([]byte(`{"name":"w","searchMode":"api","chapterMode":"xpath","chapterRoads":"//b",
		"searchApiConfig":{"request":{"url":"https://x.test/s"}},"antiCrawlerConfig":{"enabled":true}}`))
	if r4.Unsupported() != "" {
		t.Errorf("接口搜索的规则应可用: %q", r4.Unsupported())
	}
	if _, err := ParseRule([]byte(`{}`)); err == nil {
		t.Error("缺 name 应报错")
	}
}

func TestLoadDiskFirstThenEmbed(t *testing.T) {
	embedded := fstest.MapFS{
		"b.json":     {Data: []byte(`{"name":"B-embed"}`)},
		"index.json": {Data: []byte(`[]`)},
	}
	// 磁盘目录不存在 → 用 embed
	s := Load(filepath.Join(t.TempDir(), "nope"), embedded)
	if s.Get("b-embed") == nil || len(s.All()) != 1 {
		t.Fatalf("应回退到 embed: %+v", s.Infos())
	}
	// 磁盘目录有规则 → 以磁盘为准（忽略 embed）
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"name":"A-disk"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s = Load(dir, embedded)
	if s.Get("A-DISK") == nil || s.Get("b-embed") != nil {
		t.Fatalf("应优先用磁盘规则且大小写不敏感: %+v", s.Infos())
	}
	// 磁盘目录存在但没有 json → 仍回退 embed
	s = Load(t.TempDir(), embedded)
	if s.Get("b-embed") == nil {
		t.Fatal("空目录应回退到 embed")
	}
}
