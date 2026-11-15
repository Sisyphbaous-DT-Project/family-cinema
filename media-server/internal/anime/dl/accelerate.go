package dl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Channel 一条下载通道：盒子直连，或某台手机代取。Fetch 不能改全局目标地址。
type Channel struct {
	ID    string
	Phone bool
	Fetch Fetcher
}

// Traffic 某条通道新收到的数据，用于区分普通速度和加速贡献。
type Traffic struct {
	Channel string
	Phone   bool
	Accel   bool
	Bytes   int64
}

// Acceleration 控制一集下载的并发。Enabled 可在下载过程中改变，下一拍就会增减工人。
type Acceleration struct {
	Enabled        func() bool
	Channels       func() []Channel
	NormalParallel int
	BoxParallel    int
	PhoneParallel  int
	MaxParallel    int
	Slots          chan struct{} // 可空；同一任务的两集共享，避免每集各自吃满 16 路
	OnTraffic      func(Traffic)
	OnStats        func(connections int)
	OnSupport      func(bool)
}

func (a *Acceleration) enabled() bool {
	return a != nil && a.Enabled != nil && a.Enabled()
}

func (a *Acceleration) traffic(c Channel, accel bool, n int64) {
	if a != nil && a.OnTraffic != nil && n > 0 {
		a.OnTraffic(Traffic{Channel: c.ID, Phone: c.Phone, Accel: accel, Bytes: n})
	}
}

func (a *Acceleration) support(ok bool) {
	if a != nil && a.OnSupport != nil {
		a.OnSupport(ok)
	}
}

const (
	maxBoxParallel   = 8
	maxPhoneParallel = 4
	maxAllParallel   = 16
)

type channelLimit struct {
	ch    Channel
	limit int
}

func (a *Acceleration) channels() []Channel {
	base := Channel{ID: "box"}
	if a != nil && a.Channels != nil {
		if cs := a.Channels(); len(cs) > 0 {
			base = cs[0]
		}
	}
	box, phone, total := 1, 0, 1
	if a == nil {
		box, total = 3, 3
	} else if a.NormalParallel > 0 {
		box, total = a.NormalParallel, a.NormalParallel
	}
	if a.enabled() {
		box, phone, total = maxBoxParallel, maxPhoneParallel, maxAllParallel
		if a.BoxParallel > 0 {
			box = a.BoxParallel
		}
		if a.PhoneParallel > 0 {
			phone = a.PhoneParallel
		}
		if a.MaxParallel > 0 {
			total = a.MaxParallel
		}
	}
	list := []channelLimit{{ch: base, limit: box}}
	if a.enabled() && a.Channels != nil {
		seen := map[string]bool{base.ID: true}
		for _, c := range a.Channels()[1:] {
			if c.ID == "" || seen[c.ID] || !c.Phone || c.Fetch == nil {
				continue
			}
			seen[c.ID] = true
			list = append(list, channelLimit{ch: c, limit: phone})
		}
	}
	var out []Channel
	// 各通道轮流领工人，避免盒子的 8 个位置先把小块全领完、手机一直闲着。
	for round := 0; len(out) < total; round++ {
		added := false
		for _, c := range list {
			if round >= c.limit {
				continue
			}
			out = append(out, c.ch)
			added = true
			if len(out) >= total {
				break
			}
		}
		if !added {
			break
		}
	}
	return out
}

type countBody struct {
	rc    io.ReadCloser
	ch    Channel
	accel bool
	a     *Acceleration
}

func (b *countBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.a.traffic(b.ch, b.accel, int64(n))
	return n, err
}
func (b *countBody) Close() error { return b.rc.Close() }

func countedFetch(a *Acceleration, ch Channel, accel bool, fetch Fetcher) Fetcher {
	if a == nil || a.OnTraffic == nil {
		return fetch
	}
	return func(ctx context.Context, rawURL, rng string) (*http.Response, error) {
		resp, err := fetch(ctx, rawURL, rng)
		if err != nil {
			return nil, err
		}
		if resp == nil || resp.Body == nil {
			return nil, fmt.Errorf("下载通道返回了空响应")
		}
		resp.Body = &countBody{rc: resp.Body, ch: ch, accel: accel, a: a}
		return resp, nil
	}
}

type dynamicResult struct {
	index int
	ch    Channel
	err   error
}

// runDynamic 让各条通道动态领任务。手机通道失败只把这块重新排队，不连累整集；
// 每块允许换通道重派一次，第二次仍失败才按整集失败处理。
func runDynamic(ctx context.Context, a *Acceleration, items []int, base Fetcher, work func(context.Context, int, Fetcher) error) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan dynamicResult, maxAllParallel)
	var wg sync.WaitGroup
	var firstErr error
	next, active := 0, 0
	var pending []int
	reassigned := map[int]bool{}
	badPhone := map[string]bool{}
	start := func(ch Channel, idx int, accel bool) bool {
		if a != nil && a.Slots != nil {
			select {
			case a.Slots <- struct{}{}:
			case <-cctx.Done():
				return false
			}
		}
		active++
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a != nil && a.Slots != nil {
				defer func() { <-a.Slots }()
			}
			fetch := ch.Fetch
			if fetch == nil {
				fetch = base
			}
			if fetch == nil {
				fetch = func(context.Context, string, string) (*http.Response, error) {
					return nil, fmt.Errorf("下载通道不可用")
				}
			}
			results <- dynamicResult{index: idx, ch: ch, err: work(cctx, idx, countedFetch(a, ch, accel, fetch))}
		}()
		return true
	}

	for active > 0 || next < len(items) || len(pending) > 0 {
		if firstErr != nil || cctx.Err() != nil {
			break
		}
		var channels []Channel
		for _, ch := range a.channels() {
			if !badPhone[ch.ID] {
				channels = append(channels, ch)
			}
		}
		for _, ch := range channels {
			// channels 是目标工人数，不是每收到一个结果就再新增一批工人。
			// 必须限制总在飞数，否则完成结果可能塞满队列、工人持着共享额度等发送，形成死锁。
			if active >= len(channels) {
				break
			}
			if next >= len(items) && len(pending) == 0 {
				break
			}
			var idx int
			if len(pending) > 0 {
				idx, pending = pending[0], pending[1:]
			} else {
				idx, next = items[next], next+1
			}
			// 盒子的第一条通道是普通下载，其余盒子和手机工人都计入加速贡献。
			accel := a.enabled() && (ch.Phone || ch.ID != "box" || active > 0)
			if !start(ch, idx, accel) {
				break
			}
			if a != nil && a.OnStats != nil {
				a.OnStats(active)
			}
		}
		if active == 0 {
			break
		}
		res := <-results
		active--
		if a != nil && a.OnStats != nil {
			a.OnStats(active)
		}
		if res.err == nil {
			continue
		}
		if res.ch.Phone {
			badPhone[res.ch.ID] = true
		}
		canRetryElsewhere := res.ch.Phone
		for _, ch := range channels {
			if ch.ID != res.ch.ID && !badPhone[ch.ID] {
				canRetryElsewhere = true
				break
			}
		}
		if !reassigned[res.index] && canRetryElsewhere {
			reassigned[res.index] = true
			pending = append([]int{res.index}, pending...)
			continue
		}
		firstErr = res.err
		cancel()
	}
	cancel()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return firstErr
}
