package bangumi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultBase Bangumi 公开 API（国内直连可用）
const DefaultBase = "https://api.bgm.tv"

// DefaultUA Bangumi 要求带可识别的 User-Agent
const DefaultUA = "FamilyCinema/1.0 (home-box)"

// 各类数据的缓存时长
const (
	ttlCalendar = 6 * time.Hour
	ttlSubject  = 24 * time.Hour
	ttlEpisodes = 6 * time.Hour
	ttlSearch   = 1 * time.Hour
	ttlHot      = 3 * time.Hour
)

// Client Bangumi 客户端。HTTP 由外部注入（盒子上需要自定义 DNS 的那个 client）。
type Client struct {
	HTTP  *http.Client
	Base  string
	UA    string
	Cache *Cache
	now   func() time.Time // 测试里可替换
}

func NewClient(httpc *http.Client, ua string, cache *Cache) *Client {
	if ua == "" {
		ua = DefaultUA
	}
	return &Client{HTTP: httpc, Base: DefaultBase, UA: ua, Cache: cache, now: time.Now}
}

// do 请求 Bangumi。网络错误和 5xx/429 是上游偶发抖动，自动重试一次；其它错误（404 等）直接返回。
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	var data []byte
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var retry bool
		data, retry, err = c.once(ctx, method, path, payload)
		if err == nil || !retry {
			return data, err
		}
		select {
		case <-time.After(400 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, err
}

func (c *Client) once(ctx context.Context, method, path string, payload []byte) (data []byte, retry bool, err error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.UA)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
			fmt.Errorf("bangumi HTTP %d", resp.StatusCode)
	}
	if !json.Valid(data) {
		return nil, true, fmt.Errorf("bangumi 返回的不是 JSON")
	}
	return data, false, nil
}

func (c *Client) cached(ctx context.Context, key string, ttl time.Duration, method, path string, body any) ([]byte, error) {
	return c.Cache.Get(ctx, key, ttl, func(ctx context.Context) ([]byte, error) {
		return c.do(ctx, method, path, body)
	})
}

// Calendar 每周放送日历
func (c *Client) Calendar(ctx context.Context) ([]CalendarDay, error) {
	data, err := c.cached(ctx, "calendar", ttlCalendar, http.MethodGet, "/calendar", nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Weekday struct {
			ID int `json:"id"`
		} `json:"weekday"`
		Items []rawCard `json:"items"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	days := make([]CalendarDay, 0, len(raw))
	for _, d := range raw {
		day := CalendarDay{Weekday: d.Weekday.ID, Items: make([]Card, 0, len(d.Items))}
		for _, it := range d.Items {
			day.Items = append(day.Items, it.card())
		}
		days = append(days, day)
	}
	return days, nil
}

// Subject 番剧详情
func (c *Client) Subject(ctx context.Context, id int) (*Subject, error) {
	data, err := c.cached(ctx, fmt.Sprintf("subject:%d", id), ttlSubject, http.MethodGet, fmt.Sprintf("/v0/subjects/%d", id), nil)
	if err != nil {
		return nil, err
	}
	var raw rawSubject
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	s := raw.subject()
	return &s, nil
}

// Episodes 本篇剧集列表（上游每页最多 100，循环取完，最多 5 页）
func (c *Client) Episodes(ctx context.Context, id int) ([]Episode, error) {
	var all []Episode
	for offset := 0; offset < 500; offset += 100 {
		path := fmt.Sprintf("/v0/episodes?subject_id=%d&type=0&limit=100&offset=%d", id, offset)
		data, err := c.cached(ctx, fmt.Sprintf("episodes:%d:%d", id, offset), ttlEpisodes, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Total int       `json:"total"`
			Data  []Episode `json:"data"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if len(all) >= page.Total || len(page.Data) == 0 {
			break
		}
	}
	return all, nil
}

func (c *Client) searchSubjects(ctx context.Context, key string, ttl time.Duration, body map[string]any, limit, offset int) (*SearchResult, error) {
	path := fmt.Sprintf("/v0/search/subjects?limit=%d&offset=%d", limit, offset)
	data, err := c.cached(ctx, key, ttl, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Total  int       `json:"total"`
		Offset int       `json:"offset"`
		Data   []rawCard `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	res := &SearchResult{Total: raw.Total, Offset: raw.Offset, Items: make([]Card, 0, len(raw.Data))}
	for _, it := range raw.Data {
		res.Items = append(res.Items, it.card())
	}
	return res, nil
}

// Search 按关键词搜动画（type=2），按匹配度排序
func (c *Client) Search(ctx context.Context, kw string, limit, offset int) (*SearchResult, error) {
	body := map[string]any{"keyword": kw, "sort": "match", "filter": map[string]any{"type": []int{2}}}
	key := fmt.Sprintf("search:%s:%d:%d", url.QueryEscape(kw), limit, offset)
	return c.searchSubjects(ctx, key, ttlSearch, body, limit, offset)
}

// SeasonWindow 热门榜的时间窗：上一个季度开头 ~ 当前季度结尾（含仍在播的上季番）
func SeasonWindow(now time.Time) (from, to string) {
	q := (int(now.Month()) - 1) / 3 // 0..3
	start := time.Date(now.Year(), time.Month(q*3+1), 1, 0, 0, 0, 0, time.UTC)
	return start.AddDate(0, -3, 0).Format("2006-01-02"), start.AddDate(0, 3, 0).Format("2006-01-02")
}

// Hot 近两季的热门番剧（按热度排序）
func (c *Client) Hot(ctx context.Context, limit, offset int) (*SearchResult, error) {
	from, to := SeasonWindow(c.now())
	body := map[string]any{
		"keyword": "",
		"sort":    "heat",
		"filter":  map[string]any{"type": []int{2}, "air_date": []string{">=" + from, "<" + to}},
	}
	key := fmt.Sprintf("hot:%s:%s:%d:%d", from, to, limit, offset)
	return c.searchSubjects(ctx, key, ttlHot, body, limit, offset)
}
