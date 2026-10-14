package kernel

// 合入前审查列出的 SS UDP 测试缺口（NAT 后两个用户、换用户记录、首包公网后私网、域名解析）；用户与口令均为虚构值。

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/time/rate"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
)

// ssGapSink 起一个只收不回的计数接收端。
func ssGapSink(t *testing.T) (*net.UDPAddr, func() int) {
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

// ssRawPacket 用用户 u 的主密钥造一个发往 target 的客户端 UDP 包（与回包同格式）。
func ssRawPacket(t *testing.T, a *shadowsocksAdapter, u *ssUser, target *net.UDPAddr, payload string) []byte {
	t.Helper()
	buf := make([]byte, ssUDPEncodeBufSize)
	pkt, err := a.encodeUDPPacket(buf, u.MasterKey, M.SocksaddrFromNet(target), []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), pkt...)
}

// ssDecodeReply 用用户 u 解开一个回包，返回负载与是否解开。
func ssDecodeReply(a *shadowsocksAdapter, u *ssUser, reply []byte) (string, bool) {
	sc := getSSScratch()
	defer putSSScratch(sc)
	saltLen := a.method.SaltLen
	if len(reply) < saltLen+16 {
		return "", false
	}
	plain := make([]byte, len(reply))
	_, payload, ok := a.openUDPWith(sc, u, reply[:saltLen], reply[saltLen:], plain)
	if !ok {
		return "", false
	}
	return string(payload), true
}

// ssUserByID 在 a.loadUsers().list 里按 ID 找用户记录。
func ssUserByID(a *shadowsocksAdapter, id int64) *ssUser {
	snap := a.loadUsers()
	for _, u := range snap.list {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func TestShadowsocksUDPTwoUsersBehindOneAddress(t *testing.T) {
	u1 := core.User{ID: 8871, UUID: "ss-udp-nat-1"}
	u2 := core.User{ID: 8872, UUID: "ss-udp-nat-2"}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, u1, u2)
	echo := hy2EchoUDP(t).(*net.UDPAddr)

	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	user1 := ssUserByID(a, 8871)
	user2 := ssUserByID(a, 8872)
	if user1 == nil || user2 == nil {
		t.Fatal("未找到初始用户记录")
	}

	exchange := func(u *ssUser, payload string) (string, *ssUser) {
		pkt := ssRawPacket(t, a, u, echo, payload)
		if _, err := raw.Write(pkt); err != nil {
			t.Fatalf("发送失败：%v", err)
		}
		reply := make([]byte, 2048)
		if err := raw.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("设置读截止失败：%v", err)
		}
		n, err := raw.Read(reply)
		if err != nil {
			t.Fatalf("读取回包超时或失败：%v", err)
		}
		if s, ok := ssDecodeReply(a, user1, reply[:n]); ok {
			return s, user1
		}
		if s, ok := ssDecodeReply(a, user2, reply[:n]); ok {
			return s, user2
		}
		return "", nil
	}

	// U1 发 "nat-u1-a"：回包只能用 U1 解开、负载一致。
	gotPayload, gotUser := exchange(user1, "nat-u1-a")
	if gotPayload != "nat-u1-a" || gotUser != user1 {
		t.Fatalf("U1 首包回显不符：payload=%q user=%v", gotPayload, gotUser)
	}

	// U2 发 "nat-u2-a"：回包只能用 U2 解开、负载一致（快路径先用 U1 的会话试解失败，慢路径归到 U2，没有串）。
	gotPayload, gotUser = exchange(user2, "nat-u2-a")
	if gotPayload != "nat-u2-a" || gotUser != user2 {
		t.Fatalf("U2 首包回显不符：payload=%q user=%v", gotPayload, gotUser)
	}

	// U1 再发 "nat-u1-b"：仍由 U1 解开。
	gotPayload, gotUser = exchange(user1, "nat-u1-b")
	if gotPayload != "nat-u1-b" || gotUser != user1 {
		t.Fatalf("U1 次包回显不符：payload=%q user=%v", gotPayload, gotUser)
	}

	if count := ssUDPSessionCount(a); count != 2 {
		t.Fatalf("会话数=%d，期望 2", count)
	}

	localAP := netip.MustParseAddrPort(raw.LocalAddr().String())
	localAP = netip.AddrPortFrom(localAP.Addr().Unmap(), localAP.Port())
	a.udpMu.Lock()
	sessions := a.udp[localAP]
	var ids []int64
	for _, s := range sessions {
		ids = append(ids, s.coreUser.ID)
	}
	a.udpMu.Unlock()

	if len(ids) != 2 || !((ids[0] == 8871 && ids[1] == 8872) || (ids[0] == 8872 && ids[1] == 8871)) {
		t.Fatalf("会话用户 ID 列表=%v，期望包含 8871 与 8872", ids)
	}

	wantU1Up := int64(len("nat-u1-a") + len("nat-u1-b"))
	wantU2Up := int64(len("nat-u2-a"))
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		up1 := a.sessions.peek(8871).Upload
		up2 := a.sessions.peek(8872).Upload
		return up1 == wantU1Up && up2 == wantU2Up,
			fmt.Sprintf("8871 up=%d（期望 %d） 8872 up=%d（期望 %d）", up1, wantU1Up, up2, wantU2Up)
	}); !ok {
		t.Fatalf("等待流量累加超时：%s", why)
	}
}

func TestShadowsocksUDPUpsertReplacesSession(t *testing.T) {
	user := core.User{ID: 8873, UUID: "ss-udp-upsert"}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)
	pc := lifecycleSSUDPClient(t, a, user.UUID)
	echo := hy2EchoUDP(t)

	if err := lifecycleSSUDPRoundTrip(pc, echo, "upsert-1", 2*time.Second); err != nil {
		t.Fatalf("首次往返失败：%v", err)
	}

	var old *ssUDPSession
	a.udpMu.Lock()
	for _, list := range a.udp {
		for _, s := range list {
			if s.ctx.Err() == nil {
				old = s
				break
			}
		}
		if old != nil {
			break
		}
	}
	a.udpMu.Unlock()

	if old == nil {
		t.Fatal("未找到初始会话")
	}
	if old.limiter != nil {
		t.Fatal("初始会话 limiter 应当为 nil")
	}

	if err := a.UpsertUsers([]core.User{{ID: 8873, UUID: "ss-udp-upsert", SpeedLimit: 8000}}); err != nil {
		t.Fatalf("UpsertUsers 失败：%v", err)
	}

	if err := lifecycleSSUDPRoundTrip(pc, echo, "upsert-2", 2*time.Second); err != nil {
		t.Fatalf("更新后往返失败：%v", err)
	}

	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return old.ctx.Err() != nil && ssUDPSessionCount(a) == 1,
			fmt.Sprintf("old.ctx.Err()=%v 会话数=%d", old.ctx.Err(), ssUDPSessionCount(a))
	}); !ok {
		t.Fatalf("等待旧会话取消或新会话就绪超时：%s", why)
	}

	var cur *ssUDPSession
	a.udpMu.Lock()
	for _, list := range a.udp {
		for _, s := range list {
			if s.ctx.Err() == nil {
				cur = s
				break
			}
		}
		if cur != nil {
			break
		}
	}
	a.udpMu.Unlock()

	if cur == nil {
		t.Fatal("未找到当前活跃会话")
	}
	if cur == old {
		t.Fatal("当前会话不应等于旧会话")
	}
	if cur.limiter == nil {
		t.Fatal("新会话 limiter 应当不为 nil")
	}
	wantLimit := rate.Limit(core.SpeedLimitBytesPerSecond(8000))
	if cur.limiter.Limit() != wantLimit {
		t.Fatalf("新会话限速=%v，期望 %v", cur.limiter.Limit(), wantLimit)
	}
}

func TestShadowsocksUDPPrivateTargetDroppedAfterPublicFirst(t *testing.T) {
	prev := outbound.BlockPrivateDestinations()
	outbound.SetBlockPrivateDestinations(true)
	t.Cleanup(func() { outbound.SetBlockPrivateDestinations(prev) })

	rt, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	plane := &routedDataPlane{current: rt}

	port := reserveTCPAndUDPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "ss-udp-private-gap", Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port,
		Raw: map[string]any{"method": "aes-128-gcm"},
	}}
	val, err := newShadowsocksAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	a := val.(*shadowsocksAdapter)
	if err := a.Start(context.Background(), spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	user := core.User{ID: 8874, UUID: "ss-udp-private"}
	if err := a.AddUsers([]core.User{user}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	origResolve := ssUDPResolve
	ssUDPResolve = func(ctx context.Context, destination M.Socksaddr) (*net.UDPAddr, error) {
		return nil, net.UnknownNetworkError("test public lookup drop")
	}
	t.Cleanup(func() { ssUDPResolve = origResolve })

	pc := lifecycleSSUDPClient(t, a, user.UUID)
	publicDst := M.ParseSocksaddrHostPort("public.example.test", 9)
	if _, err := pc.WriteTo([]byte("public-first"), publicDst); err != nil {
		t.Fatalf("首包公网发送失败：%v", err)
	}

	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return ssUDPSessionCount(a) == 1, "ssUDPSessionCount != 1"
	}); !ok {
		t.Fatalf("首包后未建立会话：%s", why)
	}

	sink, got := ssGapSink(t)
	for i := 0; i < 3; i++ {
		if _, err := pc.WriteTo([]byte("private-after"), sink); err != nil {
			t.Fatalf("发往私网目标失败：%v", err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if got() != 0 {
		t.Fatalf("私网目标收到 %d 包，期望 0", got())
	}
	if ssUDPSessionCount(a) != 1 {
		t.Fatalf("会话数=%d，期望仍为 1", ssUDPSessionCount(a))
	}
	// 会话按首包目标建了直连上游，之后发往私网的包由上游的私网拦截逐包丢掉。
}

func TestShadowsocksUDPDomainResolve(t *testing.T) {
	user := core.User{ID: 8875, UUID: "ss-udp-domain"}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)
	echo := hy2EchoUDP(t).(*net.UDPAddr)

	var calls atomic.Int32
	gate := make(chan struct{})
	var closeOnce sync.Once
	closeGate := func() { closeOnce.Do(func() { close(gate) }) }

	origResolve := ssUDPResolve
	origTTL := ssUDPResolveTTL

	ssUDPResolve = func(ctx context.Context, d M.Socksaddr) (*net.UDPAddr, error) {
		n := calls.Add(1)
		if n == 1 {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return echo, nil
	}
	ssUDPResolveTTL = 300 * time.Millisecond

	t.Cleanup(func() {
		closeGate()
		ssUDPResolve = origResolve
		ssUDPResolveTTL = origTTL
	})

	dst := M.ParseSocksaddrHostPort("echo.example.test", uint16(echo.Port))
	pc := lifecycleSSUDPClient(t, a, "ss-udp-domain")

	if _, err := pc.WriteTo([]byte("d-1"), dst); err != nil {
		t.Fatalf("发 d-1 失败：%v", err)
	}

	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return calls.Load() == 1, "calls != 1"
	}); !ok {
		t.Fatalf("等待首次解析未触发：%s", why)
	}

	if _, err := pc.WriteTo([]byte("d-2"), dst); err != nil {
		t.Fatalf("发 d-2 失败：%v", err)
	}
	if _, err := pc.WriteTo([]byte("d-3"), dst); err != nil {
		t.Fatalf("发 d-3 失败：%v", err)
	}
	// 先等节点处理完 d-2、d-3（计为解析期丢包）再放行解析：否则 -race 下读循环还没轮到它们，
	// 解析就已完成、缓存命中，它们会被正常转发。
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return a.udpDrops.resolve.Load() == 2, fmt.Sprintf("resolve_drops=%d", a.udpDrops.resolve.Load())
	}); !ok {
		t.Fatalf("解析期间的 d-2、d-3 应被丢：%s", why)
	}
	if c := calls.Load(); c != 1 {
		t.Fatalf("解析中解析调用次数=%d，期望 1", c)
	}

	closeGate()

	reply := make([]byte, 2048)
	if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err := pc.ReadFrom(reply)
	if err != nil {
		t.Fatalf("读 d-1 回包失败：%v", err)
	}
	if string(reply[:n]) != "d-1" {
		t.Fatalf("回包负载=%q，期望 d-1", string(reply[:n]))
	}

	// 之后 300ms 内读不到别的回包（d-2、d-3 丢了）
	if err := pc.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n2, _, err := pc.ReadFrom(reply); err == nil {
		t.Fatalf("意外收到多余回显：%q", string(reply[:n2]))
	}

	// 缓存期内发 d-4
	if _, err := pc.WriteTo([]byte("d-4"), dst); err != nil {
		t.Fatalf("发 d-4 失败：%v", err)
	}
	if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err = pc.ReadFrom(reply)
	if err != nil {
		t.Fatalf("读 d-4 回包失败：%v", err)
	}
	if string(reply[:n]) != "d-4" {
		t.Fatalf("d-4 回包负载=%q，期望 d-4", string(reply[:n]))
	}
	if c := calls.Load(); c != 1 {
		t.Fatalf("命中缓存时 calls=%d，期望 1", c)
	}

	time.Sleep(400 * time.Millisecond)

	// 过 TTL 发 d-5，触发重新解析
	if _, err := pc.WriteTo([]byte("d-5"), dst); err != nil {
		t.Fatalf("发 d-5 失败：%v", err)
	}
	if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err = pc.ReadFrom(reply)
	if err != nil {
		t.Fatalf("读 d-5 回包失败：%v", err)
	}
	if string(reply[:n]) != "d-5" {
		t.Fatalf("d-5 回包负载=%q，期望 d-5", string(reply[:n]))
	}
	if c := calls.Load(); c != 2 {
		t.Fatalf("重新解析后 calls=%d，期望 2", c)
	}
}
