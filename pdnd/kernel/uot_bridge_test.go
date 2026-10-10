package kernel

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

// UoT 每个目标占用户一个 UDP 会话名额（与 hysteria2 / TUIC 同口径）：名额用完时
// 新目标的包丢掉、不报写错误（否则 UoT 读循环会关整条流），已有目标照常收发。

func quotaHeld(q *udpSessionQuota, userID int64) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.byUser[userID]
}

func uotTarget(port int) net.Addr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port} }

func newTestUOTConn(t *testing.T, q *udpSessionQuota, userID int64, idle time.Duration, limited *atomic.Int32) *uotRoutedPacketConn {
	t.Helper()
	c := newUOTRoutedPacketConn(context.Background(), &hysteriaEchoPlane{}, route.Meta{Network: "udp", Protocol: "anytls"}, uotUDPLimits{
		quota: q, userID: userID, idle: idle,
		onDrop: func(err error) {
			if errors.Is(err, errUOTTargetLimit) {
				limited.Add(1)
			}
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// uotEcho 发一个包并读回显（hysteriaEchoPlane 原样回显）。
func uotEcho(t *testing.T, c *uotRoutedPacketConn, target net.Addr, label string) {
	t.Helper()
	if n, err := c.WriteTo([]byte(label), target); err != nil || n != len(label) {
		t.Fatalf("%s 写 n=%d err=%v", label, n, err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, _, err := c.ReadFrom(buf)
	if err != nil || string(buf[:n]) != label {
		t.Fatalf("%s 回显=%q err=%v", label, buf[:n], err)
	}
}

func TestUOTTargetsBoundedByUserUDPQuota(t *testing.T) {
	q := &udpSessionQuota{limit: 3}
	var limited atomic.Int32
	c := newTestUOTConn(t, q, 41, 0, &limited)
	for port := 1001; port <= 1003; port++ {
		uotEcho(t, c, uotTarget(port), "ok")
	}
	if held := quotaHeld(q, 41); held != 3 {
		t.Fatalf("3 个目标占名额 %d，应为 3", held)
	}
	// 第 4 个目标：丢包、不报错、不记上行字节，进观测链一次。
	if n, err := c.WriteTo([]byte("over"), uotTarget(1004)); n != 0 || err != nil {
		t.Fatalf("超额目标 WriteTo n=%d err=%v，应为 (0, nil)", n, err)
	}
	if limited.Load() != 1 {
		t.Fatalf("超额目标的观测回调 %d 次，应为 1", limited.Load())
	}
	c.mu.Lock()
	targets := len(c.upstreams)
	c.mu.Unlock()
	if targets != 3 || quotaHeld(q, 41) != 3 {
		t.Fatalf("超额后目标数=%d 名额=%d，都应仍为 3", targets, quotaHeld(q, 41))
	}
	// 已有目标不受影响。
	uotEcho(t, c, uotTarget(1002), "still")
	_ = c.Close()
	if held := quotaHeld(q, 41); held != 0 {
		t.Fatalf("关流后名额仍占 %d", held)
	}
}

// 同一用户的多条 UoT 流共用名额；一条流关掉后归还，另一条流的新目标随即可建。
func TestUOTQuotaSharedAcrossStreamsOfOneUser(t *testing.T) {
	q := &udpSessionQuota{limit: 2}
	var limited atomic.Int32
	a := newTestUOTConn(t, q, 42, 0, &limited)
	b := newTestUOTConn(t, q, 42, 0, &limited)
	other := newTestUOTConn(t, q, 43, 0, &limited)
	uotEcho(t, a, uotTarget(2001), "a1")
	uotEcho(t, b, uotTarget(2002), "b1")
	if n, err := b.WriteTo([]byte("b2"), uotTarget(2003)); n != 0 || err != nil || limited.Load() != 1 {
		t.Fatalf("同用户第 3 个目标应被拒：n=%d err=%v limited=%d", n, err, limited.Load())
	}
	// 别的用户有自己的名额。
	uotEcho(t, other, uotTarget(2003), "other")
	_ = a.Close()
	uotEcho(t, b, uotTarget(2003), "b2-after-release")
	if held := quotaHeld(q, 42); held != 2 {
		t.Fatalf("用户 42 名额=%d，应为 2", held)
	}
}

// 空闲超时的目标被回收、归还名额；之后再发往同一目标会新建，整条流不受影响。
func TestUOTIdleTargetsReclaimed(t *testing.T) {
	q := &udpSessionQuota{limit: 1}
	var limited atomic.Int32
	c := newTestUOTConn(t, q, 44, 100*time.Millisecond, &limited)
	uotEcho(t, c, uotTarget(3001), "first")
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		targets := len(c.upstreams)
		c.mu.Unlock()
		if targets == 0 && quotaHeld(q, 44) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("空闲 5 秒后目标数=%d 名额=%d，应都回收为 0", targets, quotaHeld(q, 44))
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 名额只有 1：回收之后另一个目标能建，说明名额确实还回来了。
	uotEcho(t, c, uotTarget(3002), "after-reclaim")
	if limited.Load() != 0 {
		t.Fatalf("回收后新目标被拒 %d 次", limited.Load())
	}
}

// 一直有收发的目标不会被回收。
func TestUOTActiveTargetNotReclaimed(t *testing.T) {
	q := &udpSessionQuota{limit: 4}
	var limited atomic.Int32
	c := newTestUOTConn(t, q, 45, 200*time.Millisecond, &limited)
	start := time.Now()
	for time.Since(start) < time.Second {
		uotEcho(t, c, uotTarget(4001), "busy")
		time.Sleep(20 * time.Millisecond)
	}
	if held := quotaHeld(q, 45); held != 1 {
		t.Fatalf("活跃目标名额=%d，应一直是 1（没被回收重建）", held)
	}
}

// rejectingEchoPlane 在 hysteriaEchoPlane 之上模拟两种坏目标：IP 10.0.0.1 建不了
// 上游（如路由拒绝），端口 7 的上游建得起来但写被拒（如私网拦截在写时生效）。
type rejectingEchoPlane struct{ hysteriaEchoPlane }

var errTestTargetRejected = errors.New("test target rejected")

func (p *rejectingEchoPlane) ListenUDP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	if destination.Addr.String() == "10.0.0.1" {
		return nil, errTestTargetRejected
	}
	conn, err := p.hysteriaEchoPlane.ListenUDP(ctx, meta, destination)
	if err != nil || destination.Port != 7 {
		return conn, err
	}
	return rejectWritesConn{PacketConn: conn}, nil
}

type rejectWritesConn struct{ net.PacketConn }

func (rejectWritesConn) WriteTo([]byte, net.Addr) (int, error) { return 0, errTestTargetRejected }

// 坏目标（建不了上游、写被拒）只丢那一个包、经 onDrop 记原因，不报写错误、不关
// 流；同一条流上的正常目标照常收发。修前写错误一路传到 UoT 读循环，整条流被关。
func TestUOTBadTargetDropsOnlyItsPacket(t *testing.T) {
	var drops []error
	var mu sync.Mutex
	c := newUOTRoutedPacketConn(context.Background(), &rejectingEchoPlane{}, route.Meta{Network: "udp", Protocol: "anytls"}, uotUDPLimits{
		onDrop: func(err error) { mu.Lock(); drops = append(drops, err); mu.Unlock() },
	})
	t.Cleanup(func() { _ = c.Close() })
	for _, bad := range []net.Addr{
		&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 53},
		uotTarget(7),
	} {
		if n, err := c.WriteTo([]byte("bad"), bad); n != 0 || err != nil {
			t.Fatalf("坏目标 %s WriteTo n=%d err=%v，应为 (0, nil)", bad, n, err)
		}
		uotEcho(t, c, uotTarget(5001), "good-after-"+bad.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(drops) != 2 || !errors.Is(drops[0], errTestTargetRejected) || !errors.Is(drops[1], errTestTargetRejected) {
		t.Fatalf("丢包原因=%v，应为两次 %v", drops, errTestTargetRejected)
	}
}
