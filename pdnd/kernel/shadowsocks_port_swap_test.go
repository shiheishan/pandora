package kernel

// 同一 tag 从 SS（tcp+udp）换成只听 TCP 的协议后，UDP 键随替换释放（port_claims_test.go 只测了冲突与删除）。

import (
	"testing"
)

func TestPortClaimShadowsocksSwapToTCPOnlyReleasesUDP(t *testing.T) {
	c := newTestNativeCore(t)
	port := reserveTCPAndUDPPort(t)

	if err := c.ApplyInbound(ssInbound("swap", port, nil), nil); err != nil {
		t.Fatalf("启动 Shadowsocks 入站失败：%v", err)
	}
	if owner := c.PortOwner(port, "tcp"); owner != "swap" {
		t.Fatalf("TCP 归属=%q，期望 swap", owner)
	}
	if owner := c.PortOwner(port, "udp"); owner != "swap" {
		t.Fatalf("UDP 归属=%q，期望 swap", owner)
	}

	// 同一个 tag 换成只听 TCP 的协议
	if err := c.ApplyInbound(vlessInbound("swap", port, "tcp"), nil); err != nil {
		t.Fatalf("换成 TCP-only vless 失败：%v", err)
	}
	if owner := c.PortOwner(port, "tcp"); owner != "swap" {
		t.Fatalf("换协议后 TCP 归属=%q，期望 swap", owner)
	}
	if owner := c.PortOwner(port, "udp"); owner != "" {
		t.Fatalf("换协议后 UDP 归属=%q，期望空（已释放）", owner)
	}

	// 别的入站在同端口听 UDP
	if err := c.ApplyInbound(vlessInbound("other-kcp", port, "mkcp"), nil); err != nil {
		t.Fatalf("同端口听 UDP 失败：%v", err)
	}
	if owner := c.PortOwner(port, "udp"); owner != "other-kcp" {
		t.Fatalf("新 UDP 入站归属=%q，期望 other-kcp", owner)
	}
}
