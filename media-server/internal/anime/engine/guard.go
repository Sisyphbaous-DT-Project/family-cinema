package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errPrivate 规则让盒子去访问本机或家里的设备：一律不去（与播放代理 hls.Proxy 的判断一致）
var errPrivate = errors.New("不能访问内网地址")

func privateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// guard BlockPrivate 时拒绝指向本机/内网的地址；域名解析失败就放行，让真正的请求去失败
func (e *Engine) guard(ctx context.Context, u *url.URL) error {
	if !e.BlockPrivate {
		return nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("只能访问 http/https 地址")
	}
	host := u.Hostname()
	if v, ok := e.okHosts.Load(host); ok && time.Now().Before(v.(time.Time)) {
		return nil
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else if strings.EqualFold(host, "localhost") {
		return errPrivate
	} else if e.LookupIP != nil {
		ips, _ = e.LookupIP(ctx, host)
	}
	for _, ip := range ips {
		if privateIP(ip) {
			return errPrivate
		}
	}
	e.okHosts.Store(host, time.Now().Add(5*time.Minute))
	return nil
}

// CheckRedirect 给抓页面的 http.Client 用：跳转到的地址同样要过 guard
func (e *Engine) CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("跳转次数太多")
	}
	return e.guard(req.Context(), req.URL)
}
