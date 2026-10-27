package main

import (
	"context"
	"errors"
	"net"
	"time"
)

// Android 机顶盒上没有 /etc/resolv.conf，Go 的默认 resolver 无法解析域名。
// 这里用自定义 resolver 直连公共 DNS（UDP），保证服务在盒子上也能解析域名。
var customDNS = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 4 * time.Second}
		// 依次尝试公共 DNS，哪个可达用哪个
		for _, server := range []string{"223.5.5.5:53", "119.29.29.29:53", "114.114.114.114:53"} {
			conn, err := d.DialContext(ctx, "udp", server)
			if err == nil {
				return conn, nil
			}
		}
		return d.DialContext(ctx, "udp", address)
	},
}

// DHT 引导节点（BT 网络的入口）。Android 上 Go 解析不了域名，
// 必须自己用公共 DNS 解析成 IP 再交给 DHT，否则引导必然失败。
var dhtBootstrapHosts = []string{
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"router.utorrent.com:6881",
	"dht.libtorrent.org:25401",
	"dht.aelitis.com:6881",
}

// ResolveDHTBootstrap 把引导节点域名解析成 UDP 地址
func ResolveDHTBootstrap(network string) ([]*net.UDPAddr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var out []*net.UDPAddr
	for _, hp := range dhtBootstrapHosts {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		p := 6881
		if n, err := net.LookupPort("udp", port); err == nil {
			p = n
		}
		ips, err := customDNS.LookupIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			continue
		}
		for _, ip := range ips {
			out = append(out, &net.UDPAddr{IP: ip, Port: p})
			break // 每个域名取一个地址即可
		}
	}
	if len(out) == 0 {
		return nil, errNoBootstrap
	}
	return out, nil
}

var errNoBootstrap = errors.New("DHT 引导节点全部解析失败")

// dialWithDNS 自定义拨号：用公共 DNS 解析域名后连接
func dialWithDNS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(host) != nil {
		// 已是 IP，直接连
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	ips, err := customDNS.LookupIP(ctx, "ip4", host)
	if err != nil {
		// 兜底走系统解析（宿主机有 resolv.conf 时）
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	if len(ips) == 0 {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	// 并发尝试前 3 个 IP
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, len(ips))
	for _, ip := range ips[:min(3, len(ips))] {
		go func(ip net.IP) {
			var d net.Dialer
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			ch <- result{c, err}
		}(ip)
	}
	var lastErr error
	for range ips[:min(3, len(ips))] {
		r := <-ch
		if r.err == nil {
			return r.conn, nil
		}
		lastErr = r.err
	}
	return nil, lastErr
}
