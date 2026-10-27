package main

import (
	"log"
	"net"
	"strings"
)

const (
	// discoverPort App 与盒子约定的局域网发现端口
	discoverPort = 45222
	// discoverMagic App 广播的探测串
	discoverMagic = "FAMILY_CINEMA_DISCOVER"
	// discoverReplyPrefix 应答前缀，后跟 HTTP 端口和 web 版本
	discoverReplyPrefix = "FAMILY_CINEMA"
)

// startDiscovery 启动局域网自动发现应答器。
// App 在内网 UDP 广播 discoverMagic，这里收到后回复 "FAMILY_CINEMA|<http端口>|<web版本>"，
// 让 App 在盒子 IP 变化（路由器重拨 / DHCP 重新分配）时也能自动找到服务，不用手填地址。
// 启动失败不致命：只记日志，不影响主 HTTP 服务。
func startDiscovery(httpPort string) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: discoverPort})
	if err != nil {
		log.Printf("UDP 发现服务启动失败（不影响主服务）: %v", err)
		return
	}
	go func() {
		defer conn.Close()
		buf := make([]byte, 256)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if strings.TrimSpace(string(buf[:n])) != discoverMagic {
				continue
			}
			reply := discoverReplyPrefix + "|" + httpPort + "|" + buildWebManifest().Version
			if _, err := conn.WriteToUDP([]byte(reply), remote); err != nil {
				log.Printf("UDP 发现应答失败: %v", err)
			}
		}
	}()
	log.Printf("局域网自动发现已启动（UDP :%d）", discoverPort)
}
