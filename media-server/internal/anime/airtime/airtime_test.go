package airtime

import (
	"strings"
	"testing"
	"time"
)

const sample = `{"siteMeta":{"bangumi":{"title":"番组计划"}},"items":[
{"title":"周更番","begin":"2026-10-07T15:00:00.000Z","broadcast":"R/2026-10-07T15:00:00.000Z/P7D","end":"","sites":[
  {"site":"bangumi","id":"412008"},
  {"site":"gamer","id":"1","begin":"2026-09-30T15:30:00.000Z","broadcast":"R/2026-09-30T15:30:00.000Z/P7D"},
  {"site":"bilibili","id":"2","begin":"2026-10-07T16:30:00.000Z","broadcast":"R/2026-10-07T16:30:00.000Z/P7D"}]},
{"title":"早完结了","begin":"2020-01-01T15:00:00.000Z","broadcast":"R/2020-01-01T15:00:00.000Z/P7D","end":"2020-03-25T15:00:00.000Z","sites":[{"site":"bangumi","id":"1"}]},
{"title":"没有 Bangumi 编号","begin":"2026-10-01T15:00:00.000Z","end":"","sites":[]},
{"title":"剧场版","begin":"2026-10-20T03:00:00.000Z","broadcast":"","end":"2026-10-20T03:00:00.000Z","sites":[{"site":"bangumi","id":"9"}]}
]}`

func TestParseAndView(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) // 北京时间 10-27（周二）20:00
	m, err := Parse(strings.NewReader(sample), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("应只留 2 部，实际 %d: %+v", len(m), m)
	}
	v := m[412008].View(now)
	// 日本 周四 00:00 = 北京 周三 23:00；下一次是 10-28
	if v.Weekday != 3 || v.Time != "23:00" || v.Every != 7 || v.Next == nil || !v.Next.Equal(time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("周更番: %+v", v)
	}
	if v.CN == nil || v.CN.Site != "B站" || v.CN.Weekday != 4 || v.CN.Time != "00:30" {
		t.Fatalf("B站: %+v", v.CN)
	}
	mv := m[9].View(now)
	if !mv.Ended || mv.Every != 0 || mv.Next != nil || mv.Time != "11:00" {
		t.Fatalf("剧场版: %+v", mv)
	}
}

func TestSlot(t *testing.T) {
	first := time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)
	s := Slot{First: first, Period: 7 * 24 * time.Hour}
	at := first.Add(14 * 24 * time.Hour)
	if n := s.Next(at, time.Time{}); !n.Equal(at) {
		t.Fatalf("正好播出那一刻: %v", n)
	}
	if l := s.Last(at.Add(time.Minute)); !l.Equal(at) {
		t.Fatalf("Last: %v", l)
	}
	if l := s.Last(first.Add(-time.Minute)); !l.IsZero() {
		t.Fatalf("还没开播: %v", l)
	}
	if n := s.Next(at.Add(time.Minute), at); !n.IsZero() {
		t.Fatalf("播完了还有下一次: %v", n)
	}
	if period("P1D") != 24*time.Hour || period("P0D") != 0 || period("P1M") != 0 || period("P2W") != 14*24*time.Hour {
		t.Fatal("period 解析不对")
	}
}
