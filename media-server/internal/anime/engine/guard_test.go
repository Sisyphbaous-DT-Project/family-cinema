package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"media-server/internal/anime/rules"
)

// 规则会从第三方镜像自动更新：抓页面时不能被拿去访问本机/家里的设备，跳转过去的也不行
func TestFetchBlocksPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jump" {
			http.Redirect(w, r, "http://127.0.0.1:9/admin", http.StatusFound)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	e := &Engine{BlockPrivate: true, LookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
		switch host {
		case "router.test":
			return []net.IP{net.ParseIP("192.168.1.1")}, nil
		default:
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
	}}
	// 不管请求的是哪个"公网"域名，都连到本地模拟站点
	e.HTTP = &http.Client{CheckRedirect: e.CheckRedirect, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	r := &rules.Rule{Name: "t"}
	ctx := context.Background()

	if _, err := e.Fetch(ctx, r, http.MethodGet, srv.URL+"/", nil, ""); !errors.Is(err, errPrivate) {
		t.Errorf("直接写本机地址应被挡: %v", err)
	}
	if _, err := e.Fetch(ctx, r, http.MethodGet, "http://router.test/", nil, ""); !errors.Is(err, errPrivate) {
		t.Errorf("域名解析到内网应被挡: %v", err)
	}
	if _, err := e.Fetch(ctx, r, http.MethodGet, "http://site.test/jump", nil, ""); !errors.Is(err, errPrivate) {
		t.Errorf("跳转到内网应被挡: %v", err)
	}
	if pg, err := e.Fetch(ctx, r, http.MethodGet, "http://site.test/", nil, ""); err != nil || string(pg.Body) != "ok" {
		t.Errorf("正常公网站点不该被挡: %v", err)
	}

	// 测试里的本地模拟站点（BlockPrivate=false）照常能抓
	open := &Engine{HTTP: srv.Client()}
	if _, err := open.Fetch(ctx, r, http.MethodGet, srv.URL+"/", nil, ""); err != nil {
		t.Errorf("没开拦截时不该挡: %v", err)
	}
}
