package sniff

import (
	"context"
	"testing"
	"time"
)

func TestRelayNodesRegisterRefreshAndExpire(t *testing.T) {
	h := NewHub()
	h.AliveWindow = 30 * time.Millisecond
	h.RegisterNode("a", "192.168.1.20", 18001, "ta")
	h.RegisterNode("b", "192.168.1.21", 18002, "tb")
	if got := len(h.RelayNodes()); got != 2 {
		t.Fatalf("应登记两台手机，实际 %d", got)
	}
	h.Poll(context.Background(), "a", time.Millisecond)
	nodes := h.RelayNodes()
	if len(nodes) != 2 {
		t.Fatal("普通轮询不应清掉已经登记的节点")
	}
	for _, n := range nodes {
		if n.ID == "a" && (n.Addr != "192.168.1.20" || n.Port != 18001 || n.Token != "ta") {
			t.Fatalf("节点信息被改动: %+v", n)
		}
	}
	h.RegisterNode("b", "192.168.1.21", 0, "")
	if got := h.RelayNodes(); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("停止代取的手机不应再参与: %+v", got)
	}
	time.Sleep(40 * time.Millisecond)
	if got := h.RelayNodes(); len(got) != 0 {
		t.Fatalf("离线节点应自动剔除: %+v", got)
	}
}
