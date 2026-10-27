package bangumi

import (
	"encoding/json"
	"sort"
	"strings"
)

// Images Bangumi 的封面多档尺寸
type Images struct {
	Large  string `json:"large"`
	Common string `json:"common"`
	Medium string `json:"medium"`
	Small  string `json:"small"`
	Grid   string `json:"grid"`
}

// Card 列表里的一张番剧卡片（日历/搜索/热门共用）
type Card struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	NameCN  string  `json:"name_cn"`
	Date    string  `json:"date"`
	Summary string  `json:"summary"`
	Eps     int     `json:"eps"`
	Score   float64 `json:"score"`
	Rank    int     `json:"rank"`
	Images  Images  `json:"images"`
}

// Title 优先中文名
func (c Card) Title() string {
	if c.NameCN != "" {
		return c.NameCN
	}
	return c.Name
}

// CalendarDay 一天的放送（Weekday 1=周一 … 7=周日）
type CalendarDay struct {
	Weekday int    `json:"weekday"`
	Items   []Card `json:"items"`
}

// SearchResult 分页的搜索/热门结果
type SearchResult struct {
	Total  int    `json:"total"`
	Offset int    `json:"offset"`
	Items  []Card `json:"items"`
}

type Tag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type InfoItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Rating struct {
	Rank  int     `json:"rank"`
	Total int     `json:"total"`
	Score float64 `json:"score"`
}

// Subject 番剧详情
type Subject struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	NameCN   string     `json:"name_cn"`
	Summary  string     `json:"summary"`
	Date     string     `json:"date"`
	Platform string     `json:"platform"`
	Eps      int        `json:"eps"`
	Images   Images     `json:"images"`
	Tags     []Tag      `json:"tags"`
	Infobox  []InfoItem `json:"infobox"`
	Rating   Rating     `json:"rating"`
}

// Episode 一集（只取本篇 type=0）
type Episode struct {
	ID       int     `json:"id"`
	Sort     float64 `json:"sort"` // 集序号，偶尔有 12.5 这种
	Name     string  `json:"name"`
	NameCN   string  `json:"name_cn"`
	AirDate  string  `json:"airdate"`
	Duration string  `json:"duration"`
	Desc     string  `json:"desc"`
}

// ---- 上游原始结构（只声明用到的字段） ----

type rawRating struct {
	Rank  int     `json:"rank"`
	Total int     `json:"total"`
	Score float64 `json:"score"`
}

// 日历条目与 v0 条目字段名略有出入（air_date / date），统一转成 Card
type rawCard struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	NameCN       string    `json:"name_cn"`
	Summary      string    `json:"summary"`
	AirDate      string    `json:"air_date"`
	Date         string    `json:"date"`
	Eps          int       `json:"eps"`
	TotalEpisode int       `json:"total_episodes"`
	Rating       rawRating `json:"rating"`
	Images       *Images   `json:"images"`
}

func (r rawCard) card() Card {
	c := Card{
		ID: r.ID, Name: r.Name, NameCN: r.NameCN, Summary: r.Summary,
		Date: r.Date, Eps: r.Eps, Score: r.Rating.Score, Rank: r.Rating.Rank,
	}
	if c.Date == "" {
		c.Date = r.AirDate
	}
	if c.Eps == 0 {
		c.Eps = r.TotalEpisode
	}
	if r.Images != nil {
		c.Images = *r.Images
	}
	return c
}

type rawInfoItem struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// infoValue infobox 的 value 可能是字符串，也可能是 [{"v":"..."}] 数组
func infoValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []struct {
		K string `json:"k"`
		V string `json:"v"`
	}
	if json.Unmarshal(raw, &arr) == nil {
		parts := make([]string, 0, len(arr))
		for _, a := range arr {
			if a.V != "" {
				parts = append(parts, a.V)
			}
		}
		return strings.Join(parts, "、")
	}
	return ""
}

type rawSubject struct {
	rawCard
	Platform string        `json:"platform"`
	Tags     []Tag         `json:"tags"`
	Infobox  []rawInfoItem `json:"infobox"`
}

func (r rawSubject) subject() Subject {
	c := r.rawCard.card()
	s := Subject{
		ID: c.ID, Name: c.Name, NameCN: c.NameCN, Summary: c.Summary, Date: c.Date,
		Platform: r.Platform, Eps: c.Eps, Images: c.Images,
		Rating: Rating{Rank: r.Rating.Rank, Total: r.Rating.Total, Score: r.Rating.Score},
	}
	tags := append([]Tag(nil), r.Tags...)
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].Count > tags[j].Count })
	if len(tags) > 12 {
		tags = tags[:12]
	}
	s.Tags = tags
	for _, it := range r.Infobox {
		if v := infoValue(it.Value); v != "" {
			s.Infobox = append(s.Infobox, InfoItem{Key: it.Key, Value: v})
		}
	}
	return s
}
