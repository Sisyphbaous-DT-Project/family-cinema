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

// 连点"重试"或失败列表里有重复项时，同一集不能被排第二遍
func TestAnimeRetryDoesNotDuplicateEpisode(t *testing.T) {
	task := newAnimeDLTask("aafun", "https://example.com/anime/1.html", "测试番", "series", t.TempDir(), []animeDLEpisode{
		{Label: "第01集", File: "第01集.mp4", URL: "https://example.com/1-1.html"},
		{Label: "第02集", File: "第02集.mp4", URL: "https://example.com/1-2.html"},
	})
	defer task.cancel()
	task.queued = false
	// 老版本重试留下的重复项：队列里第02集本来就有两份
	task.eps = append(task.eps, task.eps[1])
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

	call := func() map[string]any {
		req := httptest.NewRequest("POST", "/api/anime/download/"+task.info.ID+"/retry", strings.NewReader(`{"files":["第02集.mp4"]}`))
		req.SetPathValue("id", task.info.ID)
		w := httptest.NewRecorder()
		handleAnimeRetry(w, req)
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
		return resp
	}

	if resp := call(); resp["ok"] != true || resp["queued"] != float64(1) {
		t.Fatalf("第一次重试应该只排 1 集，实际 %v", resp)
	}
	animeDL.mu.Lock()
	eps := len(task.eps)
	animeDL.mu.Unlock()
	if eps != 4 {
		t.Fatalf("一次重试只该追加一份（2→3 再加历史重复项 = 4），实际 %d", eps)
	}

	// 还没轮到下载时又失败了一条（或前端重复提交）：不能再排一份
	animeDL.mu.Lock()
	task.info.AnimeFailedFiles = []string{"第02集.mp4"}
	task.info.AnimeFailures = []AnimeFailure{{File: "第02集.mp4", Message: "又失败了"}}
	animeDL.mu.Unlock()
	if resp := call(); resp["ok"] != false {
		t.Fatalf("已经在队列里的集不该重复排，实际 %v", resp)
	}
	animeDL.mu.Lock()
	eps, failures := len(task.eps), len(task.info.AnimeFailures)
	animeDL.mu.Unlock()
	if eps != 4 || failures != 0 {
		t.Errorf("队列不该变长（4）、失败记录该清掉，实际 eps=%d failures=%d", eps, failures)
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
