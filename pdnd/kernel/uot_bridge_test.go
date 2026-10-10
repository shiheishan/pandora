package kernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
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
//
// 带名额跑：建不了上游（ListenUDP 失败）那一次先占的名额必须归还，否则坏目标每发
// 一次就漏一个，最后该用户的 UDP 全被拒。
func TestUOTBadTargetDropsOnlyItsPacket(t *testing.T) {
	var drops []error
	var mu sync.Mutex
	q := &udpSessionQuota{limit: 4}
	c := newUOTRoutedPacketConn(context.Background(), &rejectingEchoPlane{}, route.Meta{Network: "udp", Protocol: "anytls"}, uotUDPLimits{
		quota: q, userID: 48,
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
		c.mu.Lock()
		targets := len(c.upstreams)
		c.mu.Unlock()
		if held := quotaHeld(q, 48); held != targets {
			t.Fatalf("坏目标 %s 之后名额=%d，应等于存活目标数 %d", bad, held, targets)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(drops) != 2 || !errors.Is(drops[0], errTestTargetRejected) || !errors.Is(drops[1], errTestTargetRejected) {
		t.Fatalf("丢包原因=%v，应为两次 %v", drops, errTestTargetRejected)
	}
}

// readUpstreamGoroutines 数属于 c 的 readUpstream goroutine（栈里带接收者地址，
// 不数同包其他用例的流）。
func readUpstreamGoroutines(c *uotRoutedPacketConn) int {
	buf := make([]byte, 8<<20)
	buf = buf[:runtime.Stack(buf, true)]
	needle := fmt.Sprintf("(*uotRoutedPacketConn).readUpstream(%p", c)
	return strings.Count(string(buf), needle)
}

// waitUOTReclaimed 等到流上没有存活目标、名额全还。
func waitUOTReclaimed(t *testing.T, c *uotRoutedPacketConn, q *udpSessionQuota, userID int64, round int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		targets := len(c.upstreams)
		c.mu.Unlock()
		if targets == 0 && quotaHeld(q, userID) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("第 %d 轮：5 秒后目标数=%d 名额=%d，应都回收为 0", round, targets, quotaHeld(q, userID))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 下行消费方停住（客户端不读）时，被回收的目标的读 goroutine 和 64KB 缓冲要和名额
// 同时释放。修前 readUpstream 阻塞在 results 上只等整条流关闭，名额却已还：名额
// 16、30 轮共 480 个目标后留下 416 个 goroutine（results 只吸收 64 个包），随时间
// 无上限增长。
func TestUOTReclaimedTargetsReleaseReaderWhenConsumerStalls(t *testing.T) {
	const limit, rounds = 16, 30
	q := &udpSessionQuota{limit: limit}
	var limited atomic.Int32
	c := newTestUOTConn(t, q, 46, 40*time.Millisecond, &limited)
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	port := 6000
	for round := 1; round <= rounds; round++ {
		for i := 0; i < limit; i++ {
			port++
			if n, err := c.WriteTo([]byte("x"), uotTarget(port)); n != 1 || err != nil {
				t.Fatalf("第 %d 轮目标 %d 写 n=%d err=%v（被拒 %d 次）", round, port, n, err, limited.Load())
			}
		}
		waitUOTReclaimed(t, c, q, 46, round)
	}
	// 回收在 drop 里同步发出，读 goroutine 随后退出，给调度留一点时间。
	deadline := time.Now().Add(5 * time.Second)
	readers := readUpstreamGoroutines(c)
	for readers > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		readers = readUpstreamGoroutines(c)
	}
	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("%d 轮 %d 个目标后：readUpstream goroutines=%d results 积压=%d 堆增量=%dKB",
		rounds, rounds*limit, readers, len(c.results), (int64(after.HeapInuse)-int64(before.HeapInuse))>>10)
	c.mu.Lock()
	targets := len(c.upstreams)
	c.mu.Unlock()
	if readers > limit || readers > targets {
		t.Fatalf("readUpstream goroutines=%d，存活目标=%d 名额=%d：被回收的目标留下了读 goroutine", readers, targets, limit)
	}
	if held := quotaHeld(q, 46); held != targets {
		t.Fatalf("名额=%d，应等于存活目标数 %d", held, targets)
	}
}

// 回收在锁内复查 lastActive：快照之后刚有收发的目标不回收。ensureUpstream 命中已有
// 目标时在锁内 touch，所以「写方拿到目标」和「回收删掉目标」不会交错成写到已关
// socket 上丢包。
func TestUOTReapSkipsTargetTouchedAfterSnapshot(t *testing.T) {
	q := &udpSessionQuota{limit: 2}
	var limited atomic.Int32
	c := newTestUOTConn(t, q, 47, time.Hour, &limited)
	uotEcho(t, c, uotTarget(7001), "first")
	c.mu.Lock()
	up := c.upstreams[uotTarget(7001).String()]
	c.mu.Unlock()
	// 模拟回收方拿到快照时它还是空闲的：快照时刻的 cutoff 早于之后的一次写。
	cutoff := time.Now().UnixNano()
	up.lastActive.Store(cutoff - int64(time.Second))
	uotEcho(t, c, uotTarget(7001), "touched")
	if c.dropIdle(uotTarget(7001).String(), up, cutoff) {
		t.Fatal("快照后刚写过的目标被回收了")
	}
	c.mu.Lock()
	still := c.upstreams[uotTarget(7001).String()] == up
	c.mu.Unlock()
	if !still || quotaHeld(q, 47) != 1 {
		t.Fatalf("目标仍在=%v 名额=%d，应为 true、1", still, quotaHeld(q, 47))
	}
	// 真空闲的照常回收。
	up.lastActive.Store(cutoff - int64(time.Second))
	if !c.dropIdle(uotTarget(7001).String(), up, cutoff) || quotaHeld(q, 47) != 0 {
		t.Fatalf("空闲目标没被回收，名额=%d", quotaHeld(q, 47))
	}
}
