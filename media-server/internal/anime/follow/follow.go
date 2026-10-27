// Package follow 追番：记住追了哪些番，盒子按播出时间去片源站看有没有出新的一集，
// 有就记一条"更新消息"；手机 App 定时来取消息、在通知栏提醒。
//
// 盒子连不上 Bangumi，所以"出没出"看的是片源站上的集数（多了就是更新了），
// 这也正好是"能看了"的意思（片源站一般比电视首播晚一两个小时）。
package follow

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"media-server/internal/anime/airtime"
	"media-server/internal/anime/engine"
)

// Follow 一部追着的番
type Follow struct {
	ID      int       `json:"id"` // Bangumi 编号
	Title   string    `json:"title"`
	Name    string    `json:"name,omitempty"` // 原名（搜片源时第二个关键词）
	Cover   string    `json:"cover,omitempty"`
	AddedAt time.Time `json:"addedAt"`

	// 拿来查更新的片源：用户最近在哪个站看的就查哪个；没看过就盒子自己挑一个搜得到的
	Rule     string `json:"rule,omitempty"`
	Src      string `json:"src,omitempty"`
	SrcName  string `json:"srcName,omitempty"`
	PickedBy string `json:"pickedBy,omitempty"` // user | auto

	Count     int       `json:"count"`            // 片源站现在有几集（各线路里最多的）
	Latest    string    `json:"latest,omitempty"` // 最新一集叫什么（"第5集"）
	Baseline  bool      `json:"baseline"`         // 已经记下起点（之后集数变多才算更新）
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"` // 最近一次发现更新
	CheckErr  string    `json:"checkErr,omitempty"`
	NextCheck time.Time `json:"nextCheck,omitzero"`
	Fails     int       `json:"-"`
}

// Event 一条更新消息
type Event struct {
	ID        int64     `json:"id"`
	SubjectID int       `json:"subjectId"`
	Title     string    `json:"title"`
	Ep        string    `json:"ep"`   // 最新一集（"第5集"）
	Site      string    `json:"site"` // 在哪个片源站能看
	At        time.Time `json:"at"`
}

const keepEvents = 200

// Deps 查更新要用的能力（由番剧接口层注入，测试时换成假的）
type Deps struct {
	File     string          // 追番清单和消息存哪
	Rules    func() []string // 能用的片源规则名（按优先顺序）
	Search   func(ctx context.Context, rule, q string) ([]engine.Item, error)
	Chapters func(ctx context.Context, rule, src string) ([]engine.Road, error)
	Air      func(id int) (airtime.Entry, bool) // 播出时间
	Now      func() time.Time
}

// Service 追番服务
type Service struct {
	d    Deps
	wake chan struct{}

	mu        sync.Mutex
	follows   []*Follow
	events    []Event
	nextEvent int64
	busy      map[int]bool
}

type diskData struct {
	Follows   []*Follow `json:"follows"`
	Events    []Event   `json:"events"`
	NextEvent int64     `json:"nextEvent"`
}

// New 读出磁盘上的追番清单
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &Service{d: d, wake: make(chan struct{}, 1), busy: map[int]bool{}, nextEvent: 1}
	if b, err := os.ReadFile(d.File); err == nil {
		var dd diskData
		if err := json.Unmarshal(b, &dd); err == nil {
			s.follows, s.events = dd.Follows, dd.Events
			if dd.NextEvent > 0 {
				s.nextEvent = dd.NextEvent
			}
		} else {
			log.Printf("追番清单读取失败（当作空的）: %v", err)
		}
	}
	return s
}

// save 调用方持有 s.mu
func (s *Service) save() {
	b, err := json.MarshalIndent(diskData{Follows: s.follows, Events: s.events, NextEvent: s.nextEvent}, "", " ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.d.File), 0o755)
	tmp := s.d.File + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("追番清单保存失败: %v", err)
		return
	}
	_ = os.Rename(tmp, s.d.File)
}

func (s *Service) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) find(id int) *Follow {
	for _, f := range s.follows {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// List 全部追番（新追的在前）
func (s *Service) List() []Follow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Follow, 0, len(s.follows))
	for i := len(s.follows) - 1; i >= 0; i-- {
		out = append(out, *s.follows[i])
	}
	return out
}

// Get 查一部
func (s *Service) Get(id int) (Follow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.find(id); f != nil {
		return *f, true
	}
	return Follow{}, false
}

// Add 追番（已经追了就只补上片源）；马上去片源站记下起点
func (s *Service) Add(f Follow) Follow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.find(f.ID); old != nil {
		if f.Rule != "" && f.Src != "" {
			s.setSource(old, f.Rule, f.Src, f.SrcName)
		}
		return *old
	}
	n := &Follow{ID: f.ID, Title: f.Title, Name: f.Name, Cover: f.Cover, AddedAt: s.d.Now()}
	if f.Rule != "" && f.Src != "" {
		n.Rule, n.Src, n.SrcName, n.PickedBy = f.Rule, f.Src, f.SrcName, "user"
	}
	s.follows = append(s.follows, n)
	s.save()
	s.kick()
	return *n
}

// Remove 取消追番
func (s *Service) Remove(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.follows, func(f *Follow) bool { return f.ID == id })
	if i < 0 {
		return false
	}
	s.follows = slices.Delete(s.follows, i, i+1)
	s.save()
	return true
}

// SetSource 用户在某个片源站看这部番 → 以后就查这个站（没追的番不记）
func (s *Service) SetSource(id int, rule, src, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.find(id)
	if f == nil || rule == "" || src == "" {
		return false
	}
	if f.Rule == rule && f.Src == src {
		if f.PickedBy != "user" {
			f.PickedBy = "user"
			s.save()
		}
		return true
	}
	s.setSource(f, rule, src, name)
	return true
}

// setSource 换了片源：集数起点要按新站重新记，不能拿两个站的集数比
func (s *Service) setSource(f *Follow, rule, src, name string) {
	if f.Rule == rule && f.Src == src {
		return
	}
	f.Rule, f.Src, f.SrcName, f.PickedBy = rule, src, name, "user"
	f.Baseline, f.Count, f.CheckErr, f.Fails = false, 0, "", 0
	f.NextCheck = time.Time{}
	s.save()
	s.kick()
}

// Events 取 after 之后的更新消息；after < 0 表示只要最新编号（手机第一次来，不补发旧消息）
func (s *Service) Events(after int64) (latest int64, evs []Event, follows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, follows = s.nextEvent-1, len(s.follows)
	if after < 0 {
		return latest, []Event{}, follows
	}
	evs = []Event{}
	for _, e := range s.events {
		if e.ID > after {
			evs = append(evs, e)
		}
	}
	return latest, evs, follows
}

// Run 一直在后台查更新（到 ctx 结束）：每分钟看一眼哪些番到点该查了，一部一部查，不同时压片源站
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		for {
			f, ok := s.nextDue()
			if !ok {
				break
			}
			cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			res := s.check(cctx, f)
			cancel()
			s.apply(f.ID, res)
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// nextDue 取一部到点该查的番（拿副本出去查，查的时候不占锁）
func (s *Service) nextDue() (Follow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.Now()
	for _, f := range s.follows {
		if !s.busy[f.ID] && !now.Before(f.NextCheck) {
			s.busy[f.ID] = true
			return *f, true
		}
	}
	return Follow{}, false
}

// apply 把查到的结果写回去（查的时候可能被取消追番或换了片源，那就作废）
func (s *Service) apply(id int, r checkResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, id)
	f := s.find(id)
	if f == nil {
		return
	}
	now := s.d.Now()
	if r.discovered {
		if f.Rule != "" { // 查的时候用户自己选了片源，以用户的为准
			s.save()
			return
		}
		f.Rule, f.Src, f.SrcName, f.PickedBy = r.rule, r.src, r.srcName, "auto"
		f.Baseline = false
	} else if f.Rule != r.rule || f.Src != r.src {
		return
	}
	f.CheckedAt = now
	if r.err != "" {
		f.CheckErr = r.err
		f.Fails++
		// 盒子自己挑的站连着查不动，下次换个站
		if f.PickedBy == "auto" && f.Fails >= 3 {
			f.Rule, f.Src, f.SrcName, f.PickedBy, f.Baseline = "", "", "", "", false
		}
		f.NextCheck = now.Add(min(time.Duration(f.Fails)*30*time.Minute, 3*time.Hour))
		s.save()
		return
	}
	f.CheckErr, f.Fails = "", 0
	switch {
	case !f.Baseline:
		f.Count, f.Latest, f.Baseline = r.count, r.latest, true
	case r.count > f.Count:
		f.Count, f.Latest, f.UpdatedAt = r.count, r.latest, now
		s.events = append(s.events, Event{ID: s.nextEvent, SubjectID: f.ID, Title: f.Title, Ep: r.latest, Site: f.SrcName, At: now})
		s.nextEvent++
		if len(s.events) > keepEvents {
			s.events = s.events[len(s.events)-keepEvents:]
		}
		log.Printf("追番更新: %s %s（%s）", f.Title, r.latest, f.SrcName)
	case r.count < f.Count: // 片源站删了几集/换了线路：跟着改，不算更新
		f.Count, f.Latest = r.count, r.latest
	}
	f.NextCheck = s.schedule(*f, now)
	s.save()
}
