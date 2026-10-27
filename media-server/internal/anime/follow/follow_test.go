package follow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"media-server/internal/anime/airtime"
	"media-server/internal/anime/engine"
)

type fake struct {
	now      time.Time
	chapters map[string]int // rule|src → 集数
	items    map[string][]engine.Item
	air      map[int]airtime.Entry
	calls    int
}

func roadsOf(n int) []engine.Road {
	r := engine.Road{Name: "线路1"}
	for i := 1; i <= n; i++ {
		r.Episodes = append(r.Episodes, engine.Episode{Name: fmt.Sprintf("%02d", i)})
	}
	return []engine.Road{r, {Name: "线路2", Episodes: r.Episodes[:min(1, n)]}}
}

func newSvc(t *testing.T, fk *fake) *Service {
	return New(Deps{
		File:  filepath.Join(t.TempDir(), "follows.json"),
		Rules: func() []string { return []string{"A", "B", "C"} },
		Search: func(ctx context.Context, rule, q string) ([]engine.Item, error) {
			return fk.items[rule], nil
		},
		Chapters: func(ctx context.Context, rule, src string) ([]engine.Road, error) {
			fk.calls++
			n, ok := fk.chapters[rule+"|"+src]
			if !ok {
				return nil, errors.New("站点挂了")
			}
			return roadsOf(n), nil
		},
		Air: func(id int) (airtime.Entry, bool) { e, ok := fk.air[id]; return e, ok },
		Now: func() time.Time { return fk.now },
	})
}

// runOnce 把到点该查的都查一遍（和 Run 里一样的流程）
func runOnce(s *Service) {
	for {
		f, ok := s.nextDue()
		if !ok {
			return
		}
		s.apply(f.ID, s.check(context.Background(), f))
	}
}

func TestFollowDetectsNewEpisode(t *testing.T) {
	fk := &fake{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), chapters: map[string]int{"A|/a": 3}}
	s := newSvc(t, fk)
	s.Add(Follow{ID: 7, Title: "转生成为魔剑 第二季", Rule: "A", Src: "/a", SrcName: "A"})
	runOnce(s)
	f, _ := s.Get(7)
	if !f.Baseline || f.Count != 3 || f.Latest != "第3集" {
		t.Fatalf("起点没记对: %+v", f)
	}
	if latest, evs, n := s.Events(0); latest != 0 || len(evs) != 0 || n != 1 {
		t.Fatalf("记起点不该发消息: %d %v", latest, evs)
	}

	fk.chapters["A|/a"] = 4
	fk.now = f.NextCheck
	runOnce(s)
	latest, evs, _ := s.Events(0)
	if latest != 1 || len(evs) != 1 || evs[0].Ep != "第4集" || evs[0].Site != "A" || evs[0].SubjectID != 7 {
		t.Fatalf("更新消息不对: %d %+v", latest, evs)
	}
	if l, evs, _ := s.Events(-1); l != 1 || len(evs) != 0 {
		t.Fatal("手机第一次来只要编号")
	}
	if _, evs, _ := s.Events(1); len(evs) != 0 {
		t.Fatal("取过的不该再给")
	}

	// 换片源：起点按新站重记，不拿两个站的集数比
	fk.chapters["B|/b"] = 9
	s.SetSource(7, "B", "/b", "B")
	runOnce(s)
	if f, _ := s.Get(7); f.Count != 9 || f.Rule != "B" {
		t.Fatalf("换片源后: %+v", f)
	}
	if l, _, _ := s.Events(0); l != 1 {
		t.Fatal("换片源不该算更新")
	}

	// 重新读磁盘：清单和消息都还在
	s2 := New(Deps{File: s.d.File})
	if f, ok := s2.Get(7); !ok || f.Count != 9 {
		t.Fatalf("没存住: %+v", f)
	}
	if l, _, _ := s2.Events(0); l != 1 {
		t.Fatal("消息编号没存住")
	}
}

func TestDiscoverPicksBestMatch(t *testing.T) {
	fk := &fake{
		now: time.Now(),
		items: map[string][]engine.Item{
			"A": {{Name: "别的番", Src: "/x"}},
			"B": {{Name: "无职转生 第二季", Src: "/b"}}, // 只是包含关系，集数更多也不选
			"C": {{Name: "无职转生", Src: "/c"}},
		},
		chapters: map[string]int{"B|/b": 24, "C|/c": 5, "A|/x": 99},
	}
	s := newSvc(t, fk)
	s.Add(Follow{ID: 1, Title: "无职转生"})
	runOnce(s)
	f, _ := s.Get(1)
	if f.Rule != "C" || f.Src != "/c" || f.PickedBy != "auto" || f.Count != 5 || !f.Baseline {
		t.Fatalf("自己挑的片源不对: %+v", f)
	}
	// 用户后来在别的站看了：以用户选择的片源为准
	s.SetSource(1, "B", "/b", "B")
	if f, _ := s.Get(1); f.PickedBy != "user" || f.Baseline {
		t.Fatalf("%+v", f)
	}
}

func TestScheduleFollowsAirTime(t *testing.T) {
	air := time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC) // 每周三 23:00（北京时间）
	fk := &fake{air: map[int]airtime.Entry{1: {JP: airtime.Slot{First: air, Period: 7 * 24 * time.Hour}}}}
	s := newSvc(t, fk)
	f := Follow{ID: 1}
	at := func(d time.Duration) time.Time { return air.Add(d) }
	if n := s.schedule(f, at(time.Hour)); !n.Equal(at(time.Hour + 20*time.Minute)) {
		t.Fatalf("刚播完应 20 分钟后再看: %v", n)
	}
	if n := s.schedule(f, at(10*time.Hour)); !n.Equal(at(11 * time.Hour)) {
		t.Fatalf("播完半天应每小时看: %v", n)
	}
	if n := s.schedule(f, at(48*time.Hour)); !n.Equal(at(54 * time.Hour)) {
		t.Fatalf("一直没等到应每 6 小时看: %v", n)
	}
	f.UpdatedAt = at(2 * time.Hour)
	if n := s.schedule(f, at(3*time.Hour)); !n.Equal(at(27 * time.Hour)) {
		t.Fatalf("等到了就最多歇一天: %v", n)
	}
	if n := s.schedule(f, at(6*24*time.Hour)); !n.Equal(at(7 * 24 * time.Hour)) {
		t.Fatalf("应该在下次播出时再看: %v", n)
	}
	if n := s.schedule(Follow{ID: 2}, at(0)); !n.Equal(at(3 * time.Hour)) {
		t.Fatalf("不知道播出时间应每 3 小时看: %v", n)
	}
}

func TestFailuresBackOffAndRediscover(t *testing.T) {
	fk := &fake{now: time.Now(), items: map[string][]engine.Item{"A": {{Name: "某番", Src: "/a"}}}, chapters: map[string]int{"A|/a": 2}}
	s := newSvc(t, fk)
	s.Add(Follow{ID: 1, Title: "某番"})
	runOnce(s)
	delete(fk.chapters, "A|/a") // 站点挂了
	for i := 0; i < 3; i++ {
		f, _ := s.Get(1)
		fk.now = f.NextCheck
		runOnce(s)
	}
	if f, _ := s.Get(1); f.Rule != "" || f.CheckErr == "" {
		t.Fatalf("自己挑的站连着失败 3 次应重新找: %+v", f)
	}
}

func TestEpLabel(t *testing.T) {
	for in, want := range map[string]string{"05": "第5集", "第12话": "第12集", "EP7": "第7集", "特别篇": "特别篇", "": "第3集"} {
		if got := epLabel(in, 3); got != want {
			t.Errorf("epLabel(%q)=%q 期望 %q", in, got, want)
		}
	}
}

func TestSeasonMatch(t *testing.T) {
	for in, want := range map[string]int{
		"转生成为魔剑 第二季": 2, "转生成为魔剑": 1, "転生したら剣でしたⅡ": 2, "转生成为魔剑2": 2, "86 -不存在的战区-": 1,
		"Re:Zero 3rd Season": 3, "关于我转生变成史莱姆这档事 第3季": 3, "某番 第十二季": 12, "间谍过家家 Season 2": 2,
		"2024 年度番": 1, "Lv2从零开始": 1, "进击的巨人 S4": 4, "Dr.STONE 新石纪": 1, "転生したら剣でしたII": 2, "Overlord III": 3, "WII 体操": 1,
	} {
		if got := seasonOf(in); got != want {
			t.Errorf("seasonOf(%q)=%d 期望 %d", in, got, want)
		}
	}
	items := []engine.Item{{Name: "转生成为魔剑", Src: "/s1"}, {Name: "转生成为魔剑 第二季", Src: "/s2"}}
	it, sc := bestItem(items, searchTitles("转生成为魔剑 第二季", "転生したら剣でしたⅡ"))
	if it.Src != "/s2" || sc != 2 {
		t.Fatalf("第二季选成了 %+v %d", it, sc)
	}
	if it, _ := bestItem(items, searchTitles("转生成为魔剑", "転生したら剣でした")); it.Src != "/s1" {
		t.Fatalf("第一季选成了 %+v", it)
	}
	if _, sc := bestItem(items[:1], searchTitles("转生成为魔剑 第二季", "")); sc != 0 {
		t.Fatal("只有第一季时不该选")
	}
}
