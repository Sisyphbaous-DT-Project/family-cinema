// Package sniff 是"手机解析助手"的任务中枢：盒子静态解析不出视频地址时，
// 把页面交给在线的手机 App，由它的隐藏浏览器嗅探真实地址再回报。
package sniff

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

var (
	ErrNoWorker = errors.New("没有在线的解析助手")
	ErrTimeout  = errors.New("解析助手超时")
	ErrBusy     = errors.New("解析队列已满")
)

// Request 一次嗅探请求。Key 相同的并发请求会合并成一个任务。
type Request struct {
	Key       string
	PageURL   string
	Referer   string
	UA        string
	TimeoutMs int
}

// Job 派给手机的任务
type Job struct {
	ID        string `json:"id"`
	PageURL   string `json:"pageUrl"`
	Referer   string `json:"referer"`
	UA        string `json:"ua"`
	TimeoutMs int    `json:"timeoutMs"`
}

// Result 手机回报的结果。Headers 是嗅探到的那个请求带的头（Referer/User-Agent/Cookie）。
type Result struct {
	JobID   string            `json:"jobId"`
	OK      bool              `json:"ok"`
	URL     string            `json:"url"`
	Kind    string            `json:"kind"`
	Headers map[string]string `json:"headers"`
	Error   string            `json:"error"`
	// Alts 手机嗅探到的其他候选（按可信度从高到低）。主地址盒子读不到时（广告、连不上的 CDN）依次试这些。
	Alts []Candidate `json:"alts,omitempty"`
	// 手机代取：令牌短期有效，地址只由盒子根据回报的 RemoteAddr 填写，不信任手机自报地址
	RelayPort  int    `json:"relayPort,omitempty"`
	RelayToken string `json:"relayToken,omitempty"`
	RelayAddr  string `json:"-"`
}

// Candidate 一个备选的视频地址
type Candidate struct {
	URL     string            `json:"url"`
	Kind    string            `json:"kind"`
	Headers map[string]string `json:"headers"`
}

// Hub 嗅探任务中枢
type Hub struct {
	// AliveWindow 多久没来 poll 就算助手离线；poll 间隔 25s + 单个任务最长约 20s，所以默认 60s
	AliveWindow time.Duration
	// WaitTimeout 盒子最多等一个嗅探任务多久
	WaitTimeout time.Duration

	mu      sync.Mutex
	workers map[string]time.Time
	pending map[string]chan Result
	jobs    chan *Job
	seq     uint64
	sf      singleflight.Group
}

func NewHub() *Hub {
	return &Hub{
		AliveWindow: 60 * time.Second,
		WaitTimeout: 28 * time.Second,
		workers:     map[string]time.Time{},
		pending:     map[string]chan Result{},
		jobs:        make(chan *Job, 64),
	}
}

func (h *Hub) touch(id string) {
	h.mu.Lock()
	h.workers[id] = time.Now()
	h.mu.Unlock()
}

// Online 在线的助手数量（顺便清掉离线的）
func (h *Hub) Online() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for id, t := range h.workers {
		if time.Since(t) <= h.AliveWindow {
			n++
		} else {
			delete(h.workers, id)
		}
	}
	return n
}

func (h *Hub) isPending(jobID string) bool {
	h.mu.Lock()
	_, ok := h.pending[jobID]
	h.mu.Unlock()
	return ok
}

// Poll 手机长轮询取任务；wait 内没有任务返回 nil
func (h *Hub) Poll(ctx context.Context, deviceID string, wait time.Duration) *Job {
	h.touch(deviceID)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case j := <-h.jobs:
			// 已经没人等的陈旧任务（等待方超时走了）直接丢掉，别派给手机白跑
			if h.isPending(j.ID) {
				h.touch(deviceID)
				return j
			}
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// Report 手机回报结果；任务已过期（没人等了）返回 false
func (h *Hub) Report(res Result) bool {
	h.mu.Lock()
	ch, ok := h.pending[res.JobID]
	h.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- res:
	default:
	}
	return true
}

// Sniff 派任务并等结果
func (h *Hub) Sniff(ctx context.Context, req Request) (Result, error) {
	if h.Online() == 0 {
		return Result{}, ErrNoWorker
	}
	dc := h.sf.DoChan(req.Key, func() (any, error) {
		h.mu.Lock()
		h.seq++
		job := &Job{ID: fmt.Sprintf("j%d-%d", time.Now().UnixNano(), h.seq), PageURL: req.PageURL,
			Referer: req.Referer, UA: req.UA, TimeoutMs: req.TimeoutMs}
		ch := make(chan Result, 1)
		h.pending[job.ID] = ch
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			delete(h.pending, job.ID)
			h.mu.Unlock()
		}()

		select {
		case h.jobs <- job:
		default:
			return nil, ErrBusy
		}
		select {
		case r := <-ch:
			return r, nil
		case <-time.After(h.WaitTimeout):
			return nil, ErrTimeout
		}
	})
	select {
	case r := <-dc:
		if r.Err != nil {
			return Result{}, r.Err
		}
		return r.Val.(Result), nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}
