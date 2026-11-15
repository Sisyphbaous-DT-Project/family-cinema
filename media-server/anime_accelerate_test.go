package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	animeapi "media-server/internal/anime/api"
	"media-server/internal/anime/dl"
	"media-server/internal/anime/rules"
)

func TestAnimeAccelerateToggle(t *testing.T) {
	oldTasks, oldAPI := animeDL.tasks, animeDL.api
	defer func() { animeDL.tasks, animeDL.api = oldTasks, oldAPI }()
	client := &http.Client{}
	animeDL.api = animeapi.New(animeapi.Deps{HTTP: client, StreamHTTP: client, Rules: rules.NewSet(nil)})
	rule := &rules.Rule{Name: "test", Version: "1"}
	task := newAnimeDLTask(rule, "1", "测试", "series", t.TempDir(), []animeDLEpisode{{Label: "第01集", File: "第01集.mp4", URL: "https://example.com/1"}})
	task.info.AccelSupported = true
	animeDL.tasks = []*animeDLTask{task}

	for _, enabled := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/api/anime/download/x/accelerate", strings.NewReader(`{"enabled":`+map[bool]string{true: "true", false: "false"}[enabled]+`}`))
		req.SetPathValue("id", task.info.ID)
		rec := httptest.NewRecorder()
		handleAnimeAccelerate(rec, req)
		var res struct {
			OK      bool `json:"ok"`
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil || !res.OK || res.Enabled != enabled {
			t.Fatalf("开关接口结果不对: %+v, %v", res, err)
		}
		if task.accel.Load() != enabled || task.info.AccelEnabled != enabled {
			t.Fatalf("原子开关和任务展示不一致: %v", enabled)
		}
	}
}

func TestAccelTextDefaultsAreSafe(t *testing.T) {
	rule := &rules.Rule{Name: "test", Version: "1"}
	task := newAnimeDLTask(rule, "1", "测试", "series", t.TempDir(), nil)
	if task.accel.Load() || task.info.AccelEnabled {
		t.Fatal("下载加速必须默认关闭")
	}
}

func TestAccelRatesHoldBetweenProgressSamples(t *testing.T) {
	s := newAccelTraffic()
	now := time.Now()
	s.lastAt = now.Add(-2 * time.Second)
	s.add(dl.Traffic{Channel: "phone-a", Phone: true, Accel: true, Bytes: 4 << 20})
	a1, p1, phones1, _ := s.rates(now, true)
	a2, p2, phones2, _ := s.rates(now.Add(500*time.Millisecond), true)
	if a1 <= 0 || p1 <= 0 || a2 != a1 || p2 != p1 || phones1 != 1 || phones2 != 1 {
		t.Fatalf("半秒进度刷新不应清零速度或节点: first=(%v,%v,%d) second=(%v,%v,%d)", a1, p1, phones1, a2, p2, phones2)
	}
}
