package kernel

// Shadowsocks 经典 UDP 按目标复查路由：目标变化时 Select 与会话出站 tag 不一致则丢包。

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// ssRoutePlane 按目标端口报出站：端口在 blocked 里报 BlockTag，其余报 DirectTag；ListenUDP 照 udpRelayTestPlane。
type ssRoutePlane struct {
	udpRelayTestPlane
	mu      sync.Mutex
	blocked map[uint16]bool
}

func (p *ssRoutePlane) SelectRoute(meta route.Meta) (string, bool) {
	p.mu.Lock()
	blocked := p.blocked[meta.Port]
	p.mu.Unlock()
	if blocked {
		return BlockTag, true
	}
	return DirectTag, true
}

// startSSRouteSink 起一个只收不回的 UDP 端点，返回地址与当前收到的包数。
func startSSRouteSink(t *testing.T) (*net.UDPAddr, func() int) {
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
			_, _, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			count.Add(1)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr), func() int { return int(count.Load()) }
}

func TestShadowsocksUDPRouteCheckedPerDestination(t *testing.T) {
	user := core.User{ID: 8851, UUID: "ss-udp-route"}
	echoA := hy2EchoUDP(t)
	echoB := hy2EchoUDP(t)
	blockedSink, blockedCount := startSSRouteSink(t)
	plane := &ssRoutePlane{blocked: map[uint16]bool{uint16(blockedSink.Port): true}}
	a := startSSUDPAdapter(t, plane, user)
	pc := lifecycleSSUDPClient(t, a, user.UUID)

	if err := lifecycleSSUDPRoundTrip(pc, echoA, "route-a", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if ssUDPSessionCount(a) != 1 {
		t.Fatalf("会话数=%d，期望 1", ssUDPSessionCount(a))
	}

	for i := 0; i < 5; i++ {
		if _, err := pc.WriteTo([]byte("blocked"), blockedSink); err != nil {
			t.Fatal(err)
		}
	}
	ok, why := waitFor(2*time.Second, func() (bool, string) {
		got := a.udpDrops.route.Load()
		return got >= 5, fmt.Sprintf("route 丢包=%d，期望 >= 5", got)
	})
	if !ok {
		t.Fatalf("未等到 route 丢包：%s", why)
	}
	if got := a.udpDrops.route.Load(); got < 5 {
		t.Fatalf("route 丢包=%d，期望 >= 5", got)
	}
	time.Sleep(200 * time.Millisecond)
	if blockedCount() != 0 {
		t.Fatalf("blockedSink 收到 %d 包，期望 0", blockedCount())
	}

	if err := lifecycleSSUDPRoundTrip(pc, echoA, "route-a-again", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := lifecycleSSUDPRoundTrip(pc, echoB, "route-b", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if ssUDPSessionCount(a) != 1 {
		t.Fatalf("会话数=%d，期望仍为 1", ssUDPSessionCount(a))
	}
}

func TestShadowsocksUDPRouteFirstDestinationBlocked(t *testing.T) {
	user := core.User{ID: 8852, UUID: "ss-udp-route-first-blocked"}
	blockedSink, _ := startSSRouteSink(t)
	plane := &ssRoutePlane{blocked: map[uint16]bool{uint16(blockedSink.Port): true}}
	a := startSSUDPAdapter(t, plane, user)
	pc := lifecycleSSUDPClient(t, a, user.UUID)

	if _, err := pc.WriteTo([]byte("first"), blockedSink); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if a.udpDrops.route.Load() != 0 {
		t.Fatalf("首包目标 route 丢包=%d，期望 0", a.udpDrops.route.Load())
	}
}

func TestSelectUDPRoute(t *testing.T) {
	rt, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	tag, ok := selectUDPRoute(&routedDataPlane{current: rt}, route.Meta{Network: "udp", Port: 53})
	if !ok || tag != DirectTag {
		t.Fatalf("routedDataPlane+runtime: ok=%v tag=%q，期望 ok=true tag=%q", ok, tag, DirectTag)
	}

	_, ok = selectUDPRoute(&routedDataPlane{}, route.Meta{})
	if ok {
		t.Fatal("空 routedDataPlane 应 ok=false")
	}

	_, ok = selectUDPRoute(&udpRelayTestPlane{}, route.Meta{})
	if ok {
		t.Fatal("udpRelayTestPlane 应 ok=false")
	}

	tag, ok = selectUDPRoute(&ssRoutePlane{blocked: map[uint16]bool{9: true}}, route.Meta{Port: 9})
	if !ok || tag != BlockTag {
		t.Fatalf("ssRoutePlane 端口 9: ok=%v tag=%q，期望 ok=true tag=%q", ok, tag, BlockTag)
	}
}
