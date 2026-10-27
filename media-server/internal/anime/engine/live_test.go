//go:build live

package engine

import (
	"context"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"testing"
	"time"

	"media-server/internal/anime/rules"
)

// 联网实测：对规则目录里每条可用规则真搜一遍并取选集。
// 默认不跑，需要时：go test -tags live ./internal/anime/engine -run Live -v
// 也能编成 ARM 测试程序放到盒子上跑（看盒子真实网络下各站的情况）：
//
//	GOOS=linux GOARCH=arm GOARM=7 go test -c -tags live -o live.arm ./internal/anime/engine
//	盒子上：SSL_CERT_FILE=/data/server/pansou/cacert.pem LIVE_RULES=<规则目录> ./live.arm -test.run Live -test.v
func TestLiveRules(t *testing.T) {
	dir := os.Getenv("LIVE_RULES")
	if dir == "" {
		dir = "../../../rules"
	}
	set := rules.Load(dir, nil)
	if len(set.All()) == 0 {
		t.Fatal("没加载到规则")
	}
	// 盒子上没有 /etc/resolv.conf，和 media-server 一样改用公共 DNS
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "udp", "223.5.5.5:53")
		}}
	}
	// 直连（不走开发机的 7890 代理），模拟盒子的网络环境；cookie 罐和正式环境一致
	jar, _ := cookiejar.New(nil)
	e := &Engine{HTTP: &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		Jar:       jar,
	}}
	kw := os.Getenv("LIVE_KW")
	if kw == "" {
		kw = "孤独摇滚"
	}
	for _, r := range set.All() {
		r := r
		t.Run(r.Name, func(t *testing.T) {
			if reason := r.Unsupported(); reason != "" {
				t.Skip("不支持:", reason)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			items, err := e.Search(ctx, r, kw)
			if err != nil {
				t.Logf("搜索失败: %v", err)
				return
			}
			t.Logf("搜索结果 %d 条", len(items))
			if len(items) == 0 {
				return
			}
			t.Logf("首条: %s -> %s", items[0].Name, items[0].Src)
			roads, err := e.Chapters(ctx, r, items[0].Src)
			if err != nil {
				t.Logf("选集失败: %v", err)
				return
			}
			n := 0
			for _, rd := range roads {
				n += len(rd.Episodes)
			}
			t.Logf("线路 %d 条，共 %d 集", len(roads), n)
			if len(roads) > 0 && len(roads[0].Episodes) > 0 {
				ep := roads[0].Episodes[0]
				t.Logf("第一集: %s -> %s", ep.Name, ep.URL)
			}
		})
	}
}
