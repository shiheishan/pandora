package kernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/time/rate"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// ssGatedPlane 在目标端口等于 gatedPort 时让 ListenUDP 卡住（直到 release 关闭或 ctx 取消），
// fail 为真时改成直接返回错误；其余目标照 udpRelayTestPlane 建回环 socket。
type ssGatedPlane struct {
	udpRelayTestPlane
	gatedPort   int
	fail        bool
	failed      atomic.Int32  // fail 为真时 ListenUDP 返回错误的次数
	entered     chan struct{} // 每进入一次卡住的 ListenUDP 送一个（缓冲 16，满了不送）
	release     chan struct{}
	releaseOnce sync.Once
}

func newSSGatedPlane(gatedPort int, fail bool) *ssGatedPlane {
	return &ssGatedPlane{
		gatedPort: gatedPort,
		fail:      fail,
		entered:   make(chan struct{}, 16),
		release:   make(chan struct{}),
	}
}

func (p *ssGatedPlane) ListenUDP(ctx context.Context, meta route.Meta, dest M.Socksaddr) (net.PacketConn, error) {
	if int(dest.Port) == p.gatedPort {
		if p.fail {
			p.failed.Add(1)
			return nil, errors.New("gated plane: upstream unavailable")
		}
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.udpRelayTestPlane.ListenUDP(ctx, meta, dest)
}

func (p *ssGatedPlane) releaseGated() {
	p.releaseOnce.Do(func() { close(p.release) })
}

// startSSCountingSink 起一个只收不回的 UDP 接收端，返回地址与「已收到包数」读取函数。
func startSSCountingSink(t testing.TB) (*net.UDPAddr, func() int) {
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

func installSSGatedPlaneReleaseCleanup(t *testing.T, plane *ssGatedPlane) {
	t.Helper()
	t.Cleanup(func() { plane.releaseGated() })
}

func TestShadowsocksUDPSlowUpstreamDoesNotBlockOthers(t *testing.T) {
	gated := hy2EchoUDP(t)
	other := hy2EchoUDP(t)
	plane := newSSGatedPlane(gated.(*net.UDPAddr).Port, false)
	installSSGatedPlaneReleaseCleanup(t, plane)

	userA := core.User{ID: 8821, UUID: "ss-udp-gated-a"}
	userB := core.User{ID: 8822, UUID: "ss-udp-gated-b"}
	a := startSSUDPAdapter(t, plane, userA, userB)

	pcB := lifecycleSSUDPClient(t, a, userB.UUID)
	if err := lifecycleSSUDPRoundTrip(pcB, other, "b-warmup", 2*time.Second); err != nil {
		t.Fatalf("客户端 B 预热往返：%v", err)
	}

	pcA := lifecycleSSUDPClient(t, a, userA.UUID)
	if _, err := pcA.WriteTo([]byte("a-first"), gated); err != nil {
		t.Fatal(err)
	}
	select {
	case <-plane.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内未进入卡住的 ListenUDP")
	}

	for i := 0; i < 10; i++ {
		if err := lifecycleSSUDPRoundTrip(pcB, other, fmt.Sprintf("b-during-%d", i), time.Second); err != nil {
			t.Fatalf("A 卡住期间 B 往返 %d：%v", i, err)
		}
	}
	pcB2 := lifecycleSSUDPClient(t, a, userB.UUID)
	if err := lifecycleSSUDPRoundTrip(pcB2, other, "b-new-port", time.Second); err != nil {
		t.Fatalf("A 卡住期间 B 新客户端往返：%v", err)
	}

	if ssUDPSessionCount(a) != 3 {
		t.Fatalf("会话数=%d，期望 3", ssUDPSessionCount(a))
	}

	plane.releaseGated()
	deadline := time.Now().Add(2 * time.Second)
	buf := make([]byte, 2048)
	var gotEcho bool
	for time.Now().Before(deadline) {
		_ = pcA.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := pcA.ReadFrom(buf)
		if err == nil && string(buf[:n]) == "a-first" {
			gotEcho = true
			break
		}
	}
	if !gotEcho {
		t.Fatal("放行上游后 2 秒内未读到 a-first 回显")
	}
}

func TestShadowsocksUDPPendingQueueBounded(t *testing.T) {
	sinkAddr, received := startSSCountingSink(t)
	plane := newSSGatedPlane(sinkAddr.Port, false)
	installSSGatedPlaneReleaseCleanup(t, plane)
	user := core.User{ID: 8823, UUID: "ss-udp-pending"}
	a := startSSUDPAdapter(t, plane, user)
	pc := lifecycleSSUDPClient(t, a, user.UUID)

	if _, err := pc.WriteTo([]byte("p-0"), sinkAddr); err != nil {
		t.Fatal(err)
	}
	select {
	case <-plane.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内未进入卡住的 ListenUDP")
	}
	for i := 1; i <= 19; i++ {
		if _, err := pc.WriteTo([]byte(fmt.Sprintf("p-%d", i)), sinkAddr); err != nil {
			t.Fatal(err)
		}
	}

	wantDrops := uint64(20 - ssUDPPendingMax)
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return a.udpDrops.pending.Load() == wantDrops,
			fmt.Sprintf("pending 丢包=%d，期望 %d", a.udpDrops.pending.Load(), wantDrops)
	}); !ok {
		t.Fatalf("等待 pending 丢包计数：%s", why)
	}

	plane.releaseGated()
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return received() == ssUDPPendingMax,
			fmt.Sprintf("sink 收到=%d，期望 %d", received(), ssUDPPendingMax)
	}); !ok {
		t.Fatalf("等待 sink 收包：%s", why)
	}
	time.Sleep(200 * time.Millisecond)
	if received() != ssUDPPendingMax {
		t.Fatalf("多等 200ms 后 sink 收到=%d，仍应为 %d", received(), ssUDPPendingMax)
	}
}

func TestShadowsocksUDPEstablishFailureReleases(t *testing.T) {
	gated := hy2EchoUDP(t)
	other := hy2EchoUDP(t)
	plane := newSSGatedPlane(gated.(*net.UDPAddr).Port, true)
	user := core.User{ID: 8824, UUID: "ss-udp-fail", DeviceLimit: 1}
	a := startSSUDPAdapter(t, plane, user)
	pc := lifecycleSSUDPClient(t, a, user.UUID)

	for i := 0; i < 3; i++ {
		if _, err := pc.WriteTo([]byte(fmt.Sprintf("fail-%d", i)), gated); err != nil {
			t.Fatal(err)
		}
		// 先等这一包真的走到建上游失败：包还没到服务端时「会话 0、名额 0」本来就成立，只看释放会提前放行；
		// 第三包还在路上就发 after-fail 的话，两包都没会话、由慢路径 worker 并发处理，after-fail 先建好会话时
		// fail-2 会进同一会话、经它的上游发到 gated 回显，往返读到的就是 fail-2（10-20 查明的偶发）
		want := int32(i + 1)
		if ok, why := waitFor(2*time.Second, func() (bool, string) {
			return plane.failed.Load() == want && ssUDPSessionCount(a) == 0 && ssUDPQuotaInUse(a, user.ID) == 0 &&
					len(a.OnlineIPs()[user.ID]) == 0 && liveSessionsOf(a) == 0,
				fmt.Sprintf("建上游失败=%d（期望 %d） 会话=%d 名额=%d 在线=%d 登记=%d", plane.failed.Load(), want,
					ssUDPSessionCount(a), ssUDPQuotaInUse(a, user.ID), len(a.OnlineIPs()[user.ID]), liveSessionsOf(a))
		}); !ok {
			t.Fatalf("第 %d 次失败后未释放资源：%s", i+1, why)
		}
		if i < 2 {
			time.Sleep(300 * time.Millisecond)
		}
	}

	if err := lifecycleSSUDPRoundTrip(pc, other, "after-fail", 2*time.Second); err != nil {
		t.Fatalf("失败路径后仍应能与 other 往返：%v", err)
	}
}

func TestShadowsocksUDPEncodeDomainSource(t *testing.T) {
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, core.User{ID: 8825, UUID: "ss-udp-atyp3"})
	user := a.loadUsers().list[0]

	dst := make([]byte, ssUDPEncodeBufSize)
	out, err := a.encodeUDPPacket(dst, user.MasterKey, M.ParseSocksaddrHostPort("relay.example.test", 5353), []byte("domain-src"))
	if err != nil {
		t.Fatalf("编码域名来源：%v", err)
	}

	sc := getSSScratch()
	defer putSSScratch(sc)
	saltLen := a.method.SaltLen
	plain := make([]byte, len(out))
	dest, payload, ok := a.openUDPWith(sc, user, out[:saltLen], out[saltLen:], plain)
	if !ok {
		t.Fatal("openUDPWith 应成功")
	}
	if dest.Domain != "relay.example.test" || dest.Port != 5353 || string(payload) != "domain-src" {
		t.Fatalf("解包结果 dest=%+v payload=%q", dest, payload)
	}
	sc.setSalt(out[:saltLen])
	aead, err := a.method.NewAEAD(sc.subkey(user.MasterKey, a.method.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := aead.Open(plain[:0], sc.zeroNonce(), out[saltLen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened[0] != 3 {
		t.Fatalf("明文 ATYP=%d，期望 3", opened[0])
	}

	_, err = a.encodeUDPPacket(dst, user.MasterKey, M.ParseSocksaddrHostPort("127.0.0.1", 0), []byte("x"))
	if !errors.Is(err, errSSUDPSourceInvalid) {
		t.Fatalf("端口 0 应返回 errSSUDPSourceInvalid，得到 %v", err)
	}
	_, err = a.encodeUDPPacket(dst, user.MasterKey, M.ParseSocksaddrHostPort(strings.Repeat("a", 256), 5353), []byte("x"))
	if !errors.Is(err, errSSUDPSourceInvalid) {
		t.Fatalf("过长域名应返回 errSSUDPSourceInvalid，得到 %v", err)
	}
}

func TestShadowsocksUDPWritePacketDropsNonFatal(t *testing.T) {
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, core.User{ID: 8826, UUID: "ss-udp-write-drop"})
	user := a.loadUsers().list[0]
	ctx, cancel := context.WithCancel(context.Background())
	client := netip.MustParseAddrPort("127.0.0.1:9")
	s := &ssUDPSession{
		hub: &a.ssUDPHub, user: user,
		ctx: ctx, cancel: cancel,
	}
	s.client.Store(&client)
	w := ssUDPClientWriter{s: s}
	pkt := func(p string) *buf.Buffer {
		b := buf.NewSize(len(p))
		_, _ = b.WriteString(p)
		return b
	}

	if err := w.WritePacket(pkt("x"), M.Socksaddr{Addr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("来源端口 0 应返回 nil，得到 %v", err)
	}
	if a.udpDrops.downlink.Load() != 1 {
		t.Fatalf("downlink=%d，期望 1", a.udpDrops.downlink.Load())
	}
	if ctx.Err() != nil {
		t.Fatalf("ctx 不应已取消：%v", ctx.Err())
	}

	s.limiter = rate.NewLimiter(rate.Limit(1000), 100)
	if err := w.WritePacket(pkt(strings.Repeat("b", 200)), M.ParseSocksaddrHostPort("127.0.0.1", 5353)); err != nil {
		t.Fatalf("限速丢包应返回 nil，得到 %v", err)
	}
	if a.udpDrops.downlink.Load() != 2 {
		t.Fatalf("downlink=%d，期望 2", a.udpDrops.downlink.Load())
	}

	cancel()
	if err := w.WritePacket(pkt(strings.Repeat("c", 10)), M.ParseSocksaddrHostPort("127.0.0.1", 5353)); err == nil {
		t.Fatal("ctx 取消后 WritePacket 应返回非 nil")
	}
}

func TestShadowsocksUDPLookupSkipsCanceled(t *testing.T) {
	live, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	dead, deadCancel := context.WithCancel(context.Background())
	deadCancel()

	client := netip.MustParseAddrPort("127.0.0.1:40000")
	a := &shadowsocksAdapter{ssUDPHub: ssUDPHub{udp: map[ssUDPKey][]*ssUDPSession{
		{addr: client}: {{ctx: dead}, {ctx: live}},
	}}}
	got := a.lookupUDPSessions(ssUDPKey{addr: client}, nil)
	if len(got) != 1 {
		t.Fatalf("lookup 长度=%d，期望 1", len(got))
	}
	if got[0].ctx != live {
		t.Fatal("应只返回未取消的会话")
	}
}

func TestShadowsocksBindFailureNamesL4(t *testing.T) {
	t.Run("UDP被占", func(t *testing.T) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = pc.Close() }()
		port := pc.LocalAddr().(*net.UDPAddr).Port
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Skip("TCP 同号碰巧被占，跳过")
		}
		_ = ln.Close()

		c := newTestNativeCore(t)
		err = c.ApplyInbound(ssInbound("ss-udp-busy", port, nil), nil)
		assertPortInUse(t, err, port, "udp", "", false)
		p := strconv.Itoa(port)
		assertConflictText(t, err, "启动原生协议 shadowsocks: 端口 "+p+"/UDP 已被本机其他服务占用",
			"port_in_use:"+p+"/udp:other")
		if c.PortOwner(port, "tcp") != "" || c.PortOwner(port, "udp") != "" {
			t.Fatalf("登记表 tcp=%q udp=%q 应为空", c.PortOwner(port, "tcp"), c.PortOwner(port, "udp"))
		}
	})

	t.Run("TCP被占", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		port := ln.Addr().(*net.TCPAddr).Port
		pc, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Skip("UDP 同号碰巧被占，跳过")
		}
		_ = pc.Close()

		c := newTestNativeCore(t)
		err = c.ApplyInbound(ssInbound("ss-tcp-busy", port, nil), nil)
		assertPortInUse(t, err, port, "tcp", "", false)
		p := strconv.Itoa(port)
		assertConflictText(t, err, "启动原生协议 shadowsocks: 端口 "+p+"/TCP 已被本机其他服务占用",
			"port_in_use:"+p+"/tcp:other")
		if c.PortOwner(port, "tcp") != "" || c.PortOwner(port, "udp") != "" {
			t.Fatalf("登记表 tcp=%q udp=%q 应为空", c.PortOwner(port, "tcp"), c.PortOwner(port, "udp"))
		}
	})
}
