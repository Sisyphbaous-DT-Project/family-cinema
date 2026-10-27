package sniff

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNoWorker(t *testing.T) {
	h := NewHub()
	if _, err := h.Sniff(context.Background(), Request{Key: "k"}); err != ErrNoWorker {
		t.Fatalf("没有助手应返回 ErrNoWorker: %v", err)
	}
	// 助手超过存活窗口没来 poll → 视为离线
	h.AliveWindow = 10 * time.Millisecond
	h.touch("phone")
	time.Sleep(30 * time.Millisecond)
	if h.Online() != 0 {
		t.Error("超过存活窗口应视为离线")
	}
}

// 起一个模拟手机：循环 poll，拿到任务后回报
func fakeWorker(h *Hub, stop <-chan struct{}, handle func(*Job) Result) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		j := h.Poll(context.Background(), "phone", 50*time.Millisecond)
		if j == nil {
			continue
		}
		r := handle(j)
		r.JobID = j.ID
		h.Report(r)
	}
}

func TestSniffRoundTripAndMerge(t *testing.T) {
	h := NewHub()
	h.touch("phone") // 先登记在线，避免第一次 Sniff 时助手还没来得及 poll
	stop := make(chan struct{})
	defer close(stop)
	var handled int32
	go fakeWorker(h, stop, func(j *Job) Result {
		atomic.AddInt32(&handled, 1)
		time.Sleep(100 * time.Millisecond) // 让并发请求有机会合并
		return Result{OK: true, URL: "https://cdn.test/" + j.PageURL + ".m3u8", Kind: "hls",
			Headers: map[string]string{"Referer": j.PageURL}}
	})

	var wg sync.WaitGroup
	results := make([]Result, 3)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := h.Sniff(context.Background(), Request{Key: "same", PageURL: "ep1"})
			if err != nil {
				t.Errorf("Sniff 失败: %v", err)
			}
			results[i] = r
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if !r.OK || r.URL != "https://cdn.test/ep1.m3u8" || r.Headers["Referer"] != "ep1" {
			t.Errorf("结果不对: %+v", r)
		}
	}
	if n := atomic.LoadInt32(&handled); n != 1 {
		t.Errorf("相同 key 的并发请求应合并成 1 个任务，实际 %d", n)
	}
}

func TestSniffTimeoutAndStaleJobDropped(t *testing.T) {
	h := NewHub()
	h.WaitTimeout = 50 * time.Millisecond
	h.touch("phone")
	if _, err := h.Sniff(context.Background(), Request{Key: "slow", PageURL: "ep"}); err != ErrTimeout {
		t.Fatalf("没人处理应超时: %v", err)
	}
	// 超时的任务还躺在队列里，助手后来 poll 时应当把它丢掉而不是派出去
	if j := h.Poll(context.Background(), "phone", 30*time.Millisecond); j != nil {
		t.Errorf("陈旧任务不应再派给助手: %+v", j)
	}
	// 对已过期任务回报返回 false
	if h.Report(Result{JobID: "gone"}) {
		t.Error("过期任务的回报应被忽略")
	}
}

func TestSniffContextCancel(t *testing.T) {
	h := NewHub()
	h.touch("phone")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := h.Sniff(ctx, Request{Key: "c", PageURL: "ep"}); err != context.DeadlineExceeded {
		t.Fatalf("调用方取消应立刻返回: %v", err)
	}
}
