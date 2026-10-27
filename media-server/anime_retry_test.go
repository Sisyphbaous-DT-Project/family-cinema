package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// 运行中的任务点"重试"：失败的集应该接回这个任务的队列，失败记录清掉
func TestAnimeRetryAppendsToRunningTask(t *testing.T) {
	task := newAnimeDLTask("aafun", "https://example.com/anime/1.html", "测试番", "series", t.TempDir(), []animeDLEpisode{
		{Label: "第01集", File: "第01集.mp4", URL: "https://example.com/1-1.html"},
		{Label: "第02集", File: "第02集.mp4", URL: "https://example.com/1-2.html"},
	})
	defer task.cancel()
	task.queued = false
	task.info.AnimeFailedFiles = []string{"第02集.mp4"}
	task.info.AnimeFailures = []AnimeFailure{{File: "第02集.mp4", Message: "取不到视频（HTTP 404）"}}

	animeDL.mu.Lock()
	animeDL.tasks = append([]*animeDLTask{task}, animeDL.tasks...)
	animeDL.mu.Unlock()
	defer func() {
		animeDL.mu.Lock()
		animeDL.tasks = nil
		animeDL.mu.Unlock()
	}()

	req := httptest.NewRequest("POST", "/api/anime/download/"+task.info.ID+"/retry", strings.NewReader(`{"files":["第02集.mp4"]}`))
	req.SetPathValue("id", task.info.ID)
	w := httptest.NewRecorder()
	handleAnimeRetry(w, req)

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if resp["ok"] != true || resp["queued"] != float64(1) {
		t.Fatalf("重试应该排上 1 集，实际 %v", resp)
	}

	animeDL.mu.Lock()
	eps, failed, failures := len(task.eps), len(task.info.AnimeFailedFiles), len(task.info.AnimeFailures)
	animeDL.mu.Unlock()
	if eps != 3 {
		t.Errorf("失败的集应该追加回队列（3 集），实际 %d", eps)
	}
	if failed != 0 || failures != 0 {
		t.Errorf("失败记录应该清掉，实际 failed=%d failures=%d", failed, failures)
	}
}

// 已经结束的任务点"重试"：走新任务分支（这里只验证能识别并拒绝不存在的任务）
func TestAnimeRetryUnknownTask(t *testing.T) {
	animeDL.mu.Lock()
	animeDL.tasks = nil
	animeDL.mu.Unlock()

	req := httptest.NewRequest("POST", "/api/anime/download/nope/retry", strings.NewReader(`{}`))
	req.SetPathValue("id", "nope")
	w := httptest.NewRecorder()
	handleAnimeRetry(w, req)

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if resp["ok"] != false || resp["error"] == "" {
		t.Fatalf("不存在的任务应该报错，实际 %v", resp)
	}
}
