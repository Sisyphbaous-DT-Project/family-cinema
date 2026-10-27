package rules

import "encoding/json"

// 接口模式（searchMode/chapterMode = "api"）的配置，字段与默认值对齐 Kazumi 的 api_rule_config.dart。
// 默认值只在字段缺省时生效：规则里显式写了空串（如 "roadNamePath": ""）就保持空串，和 Kazumi 一致。

// ModeAPI 接口模式；其它值（含空）都按 xpath 处理
const ModeAPI = "api"

// APIRequest 一次接口请求的模板。url/query/headers/body 里的 @keyword、@source 等会被替换。
type APIRequest struct {
	Method   string         `json:"method"`
	URL      string         `json:"url"`
	Headers  map[string]any `json:"headers"`
	Query    map[string]any `json:"query"`
	BodyType string         `json:"bodyType"` // none / json / form
	Body     any            `json:"body"`
}

// APISearch 接口搜索：listPath 取结果列表，namePath/sourcePath 在每条结果上取名字和来源
type APISearch struct {
	Request    APIRequest `json:"request"`
	ListPath   string     `json:"listPath"`
	NamePath   string     `json:"namePath"`
	SourcePath string     `json:"sourcePath"`
	// SourceTemplate 用 @source 拼出详情页地址；为空时来源原样交给选集那一步
	SourceTemplate string `json:"sourceTemplate"`
}

func (s *APISearch) UnmarshalJSON(b []byte) error {
	type plain APISearch
	v := plain{ListPath: "$.data[*]", NamePath: "$.name", SourcePath: "$.url"}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = APISearch(v)
	return nil
}

// APIEpisodePage 播放页地址模板：可用 @source、@episodeUrl、@roadIndex/@roadNumber、@episodeIndex/@episodeNumber
type APIEpisodePage struct {
	URL   string         `json:"url"`
	Query map[string]any `json:"query"`
}

// 选集返回格式
const (
	FormatNested    = "nested"    // 线路、剧集都是 JSON 数组
	FormatDelimited = "delimited" // 苹果 CMS 风格："线路1$$$线路2" + "第1集$地址#第2集$地址$$$..."
)

// APIChapter 接口选集
type APIChapter struct {
	Request APIRequest `json:"request"`
	Format  string     `json:"format"`

	// nested 格式
	RoadsPath       string `json:"roadsPath"`
	RoadNamePath    string `json:"roadNamePath"`
	EpisodesPath    string `json:"episodesPath"`
	EpisodeNamePath string `json:"episodeNamePath"`
	EpisodeURLPath  string `json:"episodeUrlPath"`

	// delimited 格式
	RoadNamesPath    string `json:"roadNamesPath"`
	RoadEpisodesPath string `json:"roadEpisodesPath"`
	RoadSeparator    string `json:"roadSeparator"`
	EpisodeSeparator string `json:"episodeSeparator"`
	FieldSeparator   string `json:"fieldSeparator"`

	// Variables 从选集响应里取出的具名值，给 EpisodePage 模板用
	Variables   map[string]string `json:"variables"`
	EpisodePage *APIEpisodePage   `json:"episodePage"`
}

func (c *APIChapter) UnmarshalJSON(b []byte) error {
	type plain APIChapter
	v := plain{
		Format:           FormatNested,
		RoadsPath:        "$.data.roads[*]",
		RoadNamePath:     "$.name",
		EpisodesPath:     "$.episodes[*]",
		EpisodeNamePath:  "$.name",
		EpisodeURLPath:   "$.url",
		RoadSeparator:    "$$$",
		EpisodeSeparator: "#",
		FieldSeparator:   "$",
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v.Format != FormatDelimited {
		v.Format = FormatNested
	}
	*c = APIChapter(v)
	return nil
}
