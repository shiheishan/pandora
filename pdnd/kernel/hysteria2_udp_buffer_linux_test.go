//go:build linux

package kernel

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// 私网放开、出站交出裸 socket 时，读回内核实际给的缓冲：等于
// min(期望, rmem_max / wmem_max) 的两倍（Linux 读回值是设置值的两倍）。
func TestHy2RawUpstreamGetsQUICSizedBuffers(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	u := newHy2UDPUpstream(pc)
	if u.raw != pc {
		t.Fatal("裸 socket 没被识别")
	}
	raw, err := pc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var rcv, snd int
	_ = raw.Control(func(fd uintptr) {
		rcv, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		snd, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	})
	if want := 2 * min(quicSocketBufferWant, sysctlInt(t, "net/core/rmem_max")); rcv != want {
		t.Fatalf("接收缓冲=%d，期望 %d", rcv, want)
	}
	if want := 2 * min(quicSocketBufferWant, sysctlInt(t, "net/core/wmem_max")); snd != want {
		t.Fatalf("发送缓冲=%d，期望 %d", snd, want)
	}
}

func sysctlInt(t *testing.T, name string) int {
	t.Helper()
	data, err := os.ReadFile("/proc/sys/" + name)
	if err != nil {
		t.Skipf("读不到 %s：%v", name, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
