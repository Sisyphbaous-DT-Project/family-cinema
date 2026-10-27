// Package airtime 番剧的播出时间（几点更新）。
//
// Bangumi 只有"星期几"，没有几点；精确时刻来自社区维护的开源数据 bangumi-data
// （每部番的首播时刻、各平台上线时刻，带 Bangumi 编号）。盒子连不上 GitHub，
// 改从 npm 国内镜像（npmmirror）下它的发布包（约 1.5MB），解开后只留还在播的番，存一份到磁盘。
package airtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	registryURL  = "https://registry.npmmirror.com/bangumi-data/latest"
	refreshEvery = 24 * time.Hour
	retryEvery   = 30 * time.Minute
	keepEnded    = 180 * 24 * time.Hour // 完结超过半年的番不留
)

// Beijing 北京时间（盒子上没有时区数据库，用固定偏移）
var Beijing = time.FixedZone("北京时间", 8*3600)

// Slot 一种播出规律：从 First 开始每隔 Period 播一次
type Slot struct {
	First  time.Time     `json:"first"`
	Period time.Duration `json:"period"` // 0 = 只播一次
}

// Next t 时刻及以后的下一次播出；播完了（过了 end）返回零值
func (s Slot) Next(t, end time.Time) time.Time {
	n := s.First
	if t.After(n) {
		if s.Period <= 0 {
			return time.Time{}
		}
		k := (t.Sub(n) + s.Period - 1) / s.Period
		n = n.Add(k * s.Period)
	}
	if !end.IsZero() && n.After(end.Add(24*time.Hour)) {
		return time.Time{}
	}
	return n
}

// Last t 时刻及以前最近一次播出；还没开播返回零值
func (s Slot) Last(t time.Time) time.Time {
	if t.Before(s.First) {
		return time.Time{}
	}
	if s.Period <= 0 {
		return s.First
	}
	return s.First.Add(t.Sub(s.First) / s.Period * s.Period)
}

// Entry 一部番的播出信息
type Entry struct {
	JP     Slot      `json:"jp"`               // 首播（日本电视台；国产动画就是国内首播）
	End    time.Time `json:"end,omitzero"`     // 完结日期（还没完结为零值）
	CNSite string    `json:"cnSite,omitempty"` // 国内正版平台名
	CN     *Slot     `json:"cn,omitempty"`     // 国内正版平台的上线规律
}

// View 给前端看的样子（时间都换成北京时间）
type View struct {
	Weekday int        `json:"weekday"`        // 1=周一 … 7=周日
	Time    string     `json:"time"`           // "23:00"
	Every   int        `json:"every"`          // 每几天更新一次；0 = 只播一次
	Begin   time.Time  `json:"begin"`          // 首播
	Next    *time.Time `json:"next,omitempty"` // 下一次播出
	Ended   bool       `json:"ended,omitempty"`
	CN      *CNView    `json:"cn,omitempty"`
}

// CNView 国内平台什么时候上线
type CNView struct {
	Site    string `json:"site"`
	Weekday int    `json:"weekday"`
	Time    string `json:"time"`
}

// View 按 now 算出下一次播出等信息
func (e Entry) View(now time.Time) View {
	v := View{Begin: e.JP.First, Every: int(e.JP.Period / (24 * time.Hour))}
	ref := e.JP.First
	if n := e.JP.Next(now, e.End); !n.IsZero() {
		v.Next, ref = &n, n
	} else {
		v.Ended = true
	}
	v.Weekday, v.Time = clock(ref)
	if e.CN != nil {
		cref := e.CN.First
		if n := e.CN.Next(now, time.Time{}); !n.IsZero() {
			cref = n
		}
		wd, tm := clock(cref)
		v.CN = &CNView{Site: e.CNSite, Weekday: wd, Time: tm}
	}
	return v
}

func clock(t time.Time) (int, string) {
	b := t.In(Beijing)
	wd := int(b.Weekday())
	if wd == 0 {
		wd = 7
	}
	return wd, b.Format("15:04")
}

// DB 播出时间库
type DB struct {
	HTTP *http.Client
	File string // 磁盘缓存（盒子重启后不用重新下）

	mu      sync.RWMutex
	m       map[int]Entry
	version string
	fetched time.Time
}

type diskData struct {
	Version string        `json:"version"`
	Fetched time.Time     `json:"fetched"`
	Entries map[int]Entry `json:"entries"`
}

// Get 按 Bangumi 编号查
func (d *DB) Get(id int) (Entry, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	e, ok := d.m[id]
	return e, ok
}

// Run 先读磁盘缓存，过期了就联网更新；之后每小时看一眼要不要更新（一直跑到 ctx 结束）
func (d *DB) Run(ctx context.Context) {
	d.load()
	for {
		wait := time.Hour
		d.mu.RLock()
		stale := time.Since(d.fetched) > refreshEvery
		d.mu.RUnlock()
		if stale {
			if err := d.refresh(ctx); err != nil {
				log.Printf("番剧播出时间更新失败（%v 后重试）: %v", retryEvery, err)
				wait = retryEvery
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (d *DB) load() {
	b, err := os.ReadFile(d.File)
	if err != nil {
		return
	}
	var dd diskData
	if json.Unmarshal(b, &dd) != nil {
		return
	}
	d.mu.Lock()
	d.m, d.version, d.fetched = dd.Entries, dd.Version, dd.Fetched
	d.mu.Unlock()
}

func (d *DB) refresh(ctx context.Context) error {
	var meta struct {
		Version string `json:"version"`
		Dist    struct {
			Tarball string `json:"tarball"`
		} `json:"dist"`
	}
	if err := d.getJSON(ctx, registryURL, &meta); err != nil {
		return err
	}
	if meta.Dist.Tarball == "" {
		return errors.New("镜像没给下载地址")
	}
	d.mu.RLock()
	same := meta.Version == d.version && len(d.m) > 0
	d.mu.RUnlock()
	m := map[int]Entry(nil)
	if !same {
		var err error
		if m, err = d.download(ctx, meta.Dist.Tarball); err != nil {
			return err
		}
	}
	d.mu.Lock()
	if m != nil {
		d.m, d.version = m, meta.Version
	}
	d.fetched = time.Now()
	dd := diskData{Version: d.version, Fetched: d.fetched, Entries: d.m}
	d.mu.Unlock()
	if !same {
		log.Printf("番剧播出时间已更新: bangumi-data %s，在播 %d 部", meta.Version, len(m))
	}
	b, _ := json.Marshal(dd)
	tmp := d.File + ".tmp"
	if err := os.MkdirAll(filepath.Dir(d.File), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, d.File)
}

func (d *DB) getJSON(ctx context.Context, u string, v any) error {
	resp, err := d.get(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

func (d *DB) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// download 下发布包，从里面找 dist/data.json 解析
func (d *DB) download(ctx context.Context, tarball string) (map[int]Entry, error) {
	resp, err := d.get(ctx, tarball)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	gz, err := gzip.NewReader(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("发布包里没找到 data.json：%w", err)
		}
		if strings.HasSuffix(h.Name, "dist/data.json") {
			return Parse(tr, time.Now())
		}
	}
}

type rawSite struct {
	Site      string `json:"site"`
	ID        string `json:"id"`
	Begin     string `json:"begin"`
	Broadcast string `json:"broadcast"`
}

type rawItem struct {
	Begin     string    `json:"begin"`
	End       string    `json:"end"`
	Broadcast string    `json:"broadcast"`
	Sites     []rawSite `json:"sites"`
}

// 国内正版平台，按常见程度排
var cnSites = []struct{ key, name string }{
	{"bilibili", "B站"}, {"qq", "腾讯视频"}, {"iqiyi", "爱奇艺"}, {"youku", "优酷"}, {"mgtv", "芒果TV"}, {"acfun", "AcFun"},
}

// Parse 逐条读 bangumi-data 的 data.json（整个文件约 8MB，不一次性全读进内存），只留还在播或刚完结不久的番
func Parse(r io.Reader, now time.Time) (map[int]Entry, error) {
	dec := json.NewDecoder(r)
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	out := map[int]Entry{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if key, _ := tok.(string); key != "items" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		for dec.More() {
			var it rawItem
			if err := dec.Decode(&it); err != nil {
				return nil, err
			}
			if id, e, ok := entryOf(it, now); ok {
				out[id] = e
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, errors.New("data.json 里没有在播的番")
	}
	return out, nil
}

func entryOf(it rawItem, now time.Time) (int, Entry, bool) {
	var id int
	for _, s := range it.Sites {
		if s.Site == "bangumi" {
			id, _ = strconv.Atoi(s.ID)
		}
	}
	if id <= 0 {
		return 0, Entry{}, false
	}
	e := Entry{}
	if it.End != "" {
		end, err := time.Parse(time.RFC3339, it.End)
		if err != nil || now.Sub(end) > keepEnded {
			return 0, Entry{}, false
		}
		e.End = end
	}
	jp, ok := slotOf(it.Begin, it.Broadcast)
	if !ok {
		return 0, Entry{}, false
	}
	e.JP = jp
	for _, c := range cnSites {
		for _, s := range it.Sites {
			if s.Site != c.key || e.CN != nil {
				continue
			}
			if sl, ok := slotOf(s.Begin, s.Broadcast); ok {
				e.CN, e.CNSite = &sl, c.name
			}
		}
	}
	return id, e, true
}

// slotOf 解析 begin + broadcast（ISO 8601 重复区间，如 "R/2026-10-07T15:00:00.000Z/P7D"）
func slotOf(begin, broadcast string) (Slot, bool) {
	if p := strings.Split(broadcast, "/"); len(p) == 3 && p[0] == "R" {
		first, err := time.Parse(time.RFC3339, p[1])
		if err == nil {
			return Slot{First: first, Period: period(p[2])}, true
		}
	}
	first, err := time.Parse(time.RFC3339, begin)
	if err != nil {
		return Slot{}, false
	}
	return Slot{First: first}, true
}

// period 只认按天/周的写法（P7D、P1D、P1W），别的（P0D、按月）当作不重复
func period(s string) time.Duration {
	s = strings.TrimPrefix(s, "P")
	unit := 24 * time.Hour
	switch {
	case strings.HasSuffix(s, "D"):
		s = strings.TrimSuffix(s, "D")
	case strings.HasSuffix(s, "W"):
		s, unit = strings.TrimSuffix(s, "W"), 7*24*time.Hour
	default:
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * unit
}
