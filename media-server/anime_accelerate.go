package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"media-server/internal/anime/dl"
	"media-server/internal/anime/hls"
)

// accelTraffic 是一集的实时通道统计；任务页汇总所有正在下载的集。
type accelTraffic struct {
	mu            sync.Mutex
	total         int64
	accel         int64
	phone         int64
	lastAt        time.Time
	lastAll       int64
	lastAcc       int64
	lastPh        int64
	lastAccelRate float64
	lastPhoneRate float64
	phoneAt       map[string]time.Time
	conn          atomic.Int64
}

func newAccelTraffic() *accelTraffic {
	return &accelTraffic{lastAt: time.Now(), phoneAt: map[string]time.Time{}}
}

func (s *accelTraffic) add(tr dl.Traffic) {
	s.mu.Lock()
	s.total += tr.Bytes
	if tr.Accel {
		s.accel += tr.Bytes
	}
	if tr.Phone {
		s.phone += tr.Bytes
		s.phoneAt[tr.Channel] = time.Now()
	}
	s.mu.Unlock()
}

func (s *accelTraffic) rates(now time.Time, active bool) (accel, phone float64, phones, connections int) {
	connections = int(s.conn.Load())
	s.mu.Lock()
	defer s.mu.Unlock()
	if !active {
		s.lastAll, s.lastAcc, s.lastPh, s.lastAt = s.total, s.accel, s.phone, now
		s.lastAccelRate, s.lastPhoneRate = 0, 0
		return 0, 0, 0, 0
	}
	accel, phone = s.lastAccelRate, s.lastPhoneRate
	if el := now.Sub(s.lastAt); el >= time.Second {
		if s.total >= s.lastAll && s.accel >= s.lastAcc && s.phone >= s.lastPh {
			accel = float64(s.accel-s.lastAcc) / 1024 / el.Seconds()
			phone = float64(s.phone-s.lastPh) / 1024 / el.Seconds()
		}
		s.lastAll, s.lastAcc, s.lastPh, s.lastAt = s.total, s.accel, s.phone, now
		s.lastAccelRate, s.lastPhoneRate = accel, phone
	}
	for _, at := range s.phoneAt {
		if now.Sub(at) <= 3*time.Second {
			phones++
		}
	}
	return
}

func (s *accelTraffic) recentPhones(now time.Time) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for id, at := range s.phoneAt {
		if now.Sub(at) <= 3*time.Second {
			out[id] = true
		}
	}
	return out
}

// newEpisodeAcceleration 把一集接到任务级开关与全部在线手机上。
// 所有节点都只连接家里的 WiFi；没有蜂窝网络或系统代理切换。
func newEpisodeAcceleration(t *animeDLTask, sl *animeDLSlot, target hls.Target) *dl.Acceleration {
	stats := newAccelTraffic()
	sl.traffic = stats
	base := target
	return &dl.Acceleration{
		Enabled: t.accel.Load,
		Slots:   t.accelSlots,
		Channels: func() []dl.Channel {
			channels := []dl.Channel{{
				ID: "box",
				Fetch: func(ctx context.Context, rawURL, rng string) (*http.Response, error) {
					return animeDL.api.Fetch(ctx, base, rawURL, rng)
				},
			}}
			for _, node := range animeDL.api.RelayNodes() {
				relayTarget := target
				relayTarget.Relay = "http://" + net.JoinHostPort(node.Addr, strconv.Itoa(node.Port)) + "/relay"
				relayTarget.RelayToken = node.Token
				channels = append(channels, dl.Channel{
					ID: node.ID, Phone: true,
					Fetch: func(ctx context.Context, rawURL, rng string) (*http.Response, error) {
						return animeDL.api.Fetch(ctx, relayTarget, rawURL, rng)
					},
				})
			}
			return channels
		},
		OnTraffic: stats.add,
		OnStats:   func(n int) { stats.conn.Store(int64(n)) },
		OnSupport: func(ok bool) {
			t.update(func(info *TorrentTask) {
				info.AccelSupported = ok
				if ok {
					info.AccelReason = ""
				} else {
					info.AccelReason = "这个视频不支持分段，已按原方式下载"
				}
			})
		},
	}
}
