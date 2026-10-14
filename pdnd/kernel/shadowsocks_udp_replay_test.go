package kernel

// 守的是 UDP 每包查 salt；重放的包不转发、不建会话。

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
)

// startSSReplaySink 起一个只收不回的 UDP 接收端，返回地址与「已收到包数」读取函数。
func startSSReplaySink(t *testing.T) (*net.UDPAddr, func() int) {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int64
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n > 0 {
				count.Add(1)
			}
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr), func() int { return int(count.Load()) }
}

func TestShadowsocksUDPReplayedPacketDropped(t *testing.T) {
	user := core.User{ID: 8861, UUID: "ss-udp-replay"}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)
	u := a.loadUsers().list[0]

	sink, recvCount := startSSReplaySink(t)

	pkt, err := a.encodeUDPPacket(make([]byte, ssUDPEncodeBufSize), u.MasterKey, M.SocksaddrFromNet(sink), []byte("replay-me"))
	if err != nil {
		t.Fatal(err)
	}
	pkt = append([]byte(nil), pkt...)

	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	if _, err := raw.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return recvCount() == 1, fmt.Sprintf("recv=%d", recvCount())
	}); !ok {
		t.Fatalf("首包未到 sink：%s", why)
	}

	if _, err := raw.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return a.udpDrops.replay.Load() == 2, fmt.Sprintf("replay=%d", a.udpDrops.replay.Load())
	}); !ok {
		t.Fatalf("重放计数：%s", why)
	}
	time.Sleep(200 * time.Millisecond)
	if recvCount() != 1 {
		t.Fatalf("重放后 sink 计数=%d，期望 1", recvCount())
	}

	pkt2, err := a.encodeUDPPacket(make([]byte, ssUDPEncodeBufSize), u.MasterKey, M.SocksaddrFromNet(sink), []byte("replay-me"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Write(pkt2); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return recvCount() == 2, fmt.Sprintf("recv=%d", recvCount())
	}); !ok {
		t.Fatalf("新 salt 包未到 sink：%s", why)
	}

	raw2, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw2.Close() })
	if _, err := raw2.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return a.udpDrops.replay.Load() == 3, fmt.Sprintf("replay=%d", a.udpDrops.replay.Load())
	}); !ok {
		t.Fatalf("新来源重放 pkt：%s", why)
	}
	if recvCount() != 2 {
		t.Fatalf("新来源重放后 sink 计数=%d，期望 2", recvCount())
	}
	if ssUDPSessionCount(a) != 1 {
		t.Fatalf("重放未建会话：ssUDPSessionCount=%d，期望 1", ssUDPSessionCount(a))
	}
}
