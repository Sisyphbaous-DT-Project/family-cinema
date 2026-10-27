package rules

import "testing"

func TestAPIConfigDefaults(t *testing.T) {
	// 缺省的字段用 Kazumi 的默认值；显式写了空串的保持空串
	r, err := ParseRule([]byte(`{"name":"a","searchMode":"api","chapterMode":"api",
		"searchApiConfig":{"request":{"url":"https://x.test/s"}},
		"chapterApiConfig":{"request":{"url":"https://x.test/c/@source"},"roadNamePath":"","format":"weird"}}`))
	if err != nil {
		t.Fatal(err)
	}
	s, c := r.SearchAPI, r.ChapterAPI
	if s.ListPath != "$.data[*]" || s.NamePath != "$.name" || s.SourcePath != "$.url" {
		t.Errorf("搜索默认值不对: %+v", s)
	}
	if c.Format != FormatNested || c.RoadsPath != "$.data.roads[*]" || c.RoadNamePath != "" || c.EpisodeURLPath != "$.url" {
		t.Errorf("选集默认值不对: %+v", c)
	}
	if c.RoadSeparator != "$$$" || c.EpisodeSeparator != "#" || c.FieldSeparator != "$" {
		t.Errorf("分隔符默认值不对: %+v", c)
	}
	if r.Unsupported() != "" {
		t.Errorf("完整的接口规则应可用: %q", r.Unsupported())
	}
}
