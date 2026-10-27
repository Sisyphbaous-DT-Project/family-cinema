package main

import "testing"

func TestEpisodeNumber(t *testing.T) {
	cases := []struct {
		name  string
		index int
		want  int
	}{
		{"第01集", 0, 1},
		{"第12话", 3, 12},
		{"EP05", 0, 5},
		{"ep 7", 0, 7},
		{"05", 9, 5},
		{"1", 4, 1},
		{"前篇", 0, 1},
		{"后篇", 1, 2},
		{"1080P", 2, 3}, // 画质不是集数，按位置
		{"0", 5, 6},
		{"第1集 HD", 6, 7},
	}
	for _, c := range cases {
		if got := episodeNumber(c.name, c.index); got != c.want {
			t.Errorf("episodeNumber(%q,%d)=%d 期望 %d", c.name, c.index, got, c.want)
		}
	}
}

func TestClearFinishedAnimeTasks(t *testing.T) {
	animeDL.mu.Lock()
	old := animeDL.tasks
	animeDL.tasks = []*animeDLTask{
		{info: TorrentTask{ID: "running", Status: "running"}},
		{info: TorrentTask{ID: "done", Status: "done"}},
		{info: TorrentTask{ID: "error", Status: "error"}},
		{info: TorrentTask{ID: "stopped", Status: "stopped"}},
	}
	animeDL.mu.Unlock()
	defer func() {
		animeDL.mu.Lock()
		animeDL.tasks = old
		animeDL.mu.Unlock()
	}()

	if got := clearFinishedAnimeTasks(); got != 3 {
		t.Fatalf("清理了 %d 条，期望 3 条", got)
	}
	left := animeTaskList()
	if len(left) != 1 || left[0].ID != "running" {
		t.Fatalf("清理后保留下来的任务不对：%+v", left)
	}
}

// 拼接不占下载带宽；全部都在拼接时速度要归零，不能把最后一次速度一直留着。
func TestAnimeSpeedStopsWhenOnlyMerging(t *testing.T) {
	sl := &animeDLSlot{ep: animeDLEpisode{Label: "第01集", File: "第01集.mp4"}, state: "running", frac: 0.4, bytes: 4096}
	other := &animeDLSlot{ep: animeDLEpisode{Label: "第02集", File: "第02集.mp4"}, state: "merging", frac: 1, bytes: 1024}
	r := &animeDLRun{t: &animeDLTask{eps: make([]animeDLEpisode, 3)}, active: []*animeDLSlot{sl, other}}
	var info TorrentTask
	r.sync(&info)
	if info.SpeedKBs == 0 {
		t.Fatal("还有一集在下载时应有实时速度")
	}
	sl.state = "merging"
	r.sync(&info)
	if info.SpeedKBs != 0 {
		t.Fatalf("全部在拼接时速度应归零: %v", info.SpeedKBs)
	}
	if r.lastBytes != 5120 || r.lastAt.IsZero() {
		t.Fatalf("拼接期间应重算测速基线: bytes=%d at=%v", r.lastBytes, r.lastAt)
	}
}
