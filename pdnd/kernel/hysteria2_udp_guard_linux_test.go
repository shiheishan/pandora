//go:build linux

package kernel

import (
	"testing"

	"github.com/aegispanel/nodeagent/outbound"
	"golang.org/x/net/ipv4"
)

// Linux 上不强制任何开关：生产默认配置（拦私网）下，hy2 / TUIC 的 UDP 转发选中
// 带检查的批量通道（sendmmsg / recvmmsg），而不是退回逐包，也不拿裸 socket。
func TestHy2DefaultConfigBatchesOnLinux(t *testing.T) {
	if !hy2UDPBatchSupported {
		t.Fatal("Linux 上应默认开启批量收发")
	}
	u := newHy2UDPUpstream(guardedUpstream(t))
	if u.raw != nil {
		t.Fatal("默认拦私网时不能拿到裸 socket")
	}
	if u.batch == nil {
		t.Fatal("默认配置退回了逐包收发")
	}
	// 必须是出站给的带检查接口，而不是直接包在裸 socket 上的 ipv4.PacketConn。
	if _, ok := u.batch.(outbound.UDPBatchConn); !ok {
		t.Fatalf("批量通道不是带检查的出站接口：%T", u.batch)
	}
	if _, ok := u.batch.(*ipv4.PacketConn); ok {
		t.Fatal("批量通道直接包在裸 socket 上")
	}
}
