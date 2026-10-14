package kernel

// 经典 Shadowsocks UDP 会话生命周期：每用户名额上限（拒新不踢旧）、双向空闲回收、
// 删用户踢会话、入站 Close 关上游 socket 且不留会话 goroutine。用户与口令均为虚构值。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	SS "github.com/sagernet/sing-shadowsocks2"

	"github.com/aegispanel/nodeagent/core"
)

// startSSUDPAdapter 直接起一个经典 SS 适配器（aes-128-gcm，监听 127.0.0.1，同端口 TCP+UDP），数据面是 plane。
func startSSUDPAdapter(t *testing.T, plane DataPlane, users ...core.User) *shadowsocksAdapter {
	t.Helper()
	allowLoopbackTargets(t)
	port := reserveTCPAndUDPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "ss-udp-lifecycle", Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port,
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
	if err := a.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// lifecycleSSUDPClient 建一个经 SS 到适配器 UDP 端口的客户端。
func lifecycleSSUDPClient(t *testing.T, a *shadowsocksAdapter, password string) net.PacketConn {
	t.Helper()
	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	method, err := SS.CreateMethod(context.Background(), "aes-128-gcm", SS.MethodOptions{Password: password})
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	pc := method.DialPacketConn(raw)
	t.Cleanup(func() { _ = raw.Close() })
	return pc
}

// lifecycleSSUDPRoundTrip 发一包到 target 并在 timeout 内等回显；没回包返回错误。
func lifecycleSSUDPRoundTrip(pc net.PacketConn, target net.Addr, payload string, timeout time.Duration) error {
	defer func() { _ = pc.SetReadDeadline(time.Time{}) }()
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := pc.WriteTo([]byte(payload), target); err != nil {
			return err
		}
		_ = pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := pc.ReadFrom(buf)
		if err == nil {
			if string(buf[:n]) != payload {
				return fmt.Errorf("echo=%q", buf[:n])
			}
			return nil
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			return err
		}
		lastErr = err
	}
	if lastErr != nil {
		return fmt.Errorf("%v 内无回显：%v", timeout, lastErr)
	}
	return fmt.Errorf("%v 内无回显", timeout)
}

// setSSUDPIdle 在本测试期间把 ssUDPIdleTimeout 改成 d（必须在起适配器之前调用）。
func setSSUDPIdle(t *testing.T, d time.Duration) {
	t.Helper()
	old := ssUDPIdleTimeout
	ssUDPIdleTimeout = d
	t.Cleanup(func() { ssUDPIdleTimeout = old })
}

// ssUDPQuotaInUse 读该用户占着的会话名额。
func ssUDPQuotaInUse(a *shadowsocksAdapter, userID int64) int {
	a.udpQuota.mu.Lock()
	n := a.udpQuota.byUser[userID]
	a.udpQuota.mu.Unlock()
	return n
}

// ssUDPStackWorkersGone 查 a 的读循环、空闲回收与会话 goroutine 是否都已退出（栈里带
// 接收者地址，不数同包其他用例的适配器）；没退完时返回残留的那几段栈。
func ssUDPStackWorkersGone(a *shadowsocksAdapter) (bool, string) {
	buf := make([]byte, 8<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var marks []string
	for _, method := range []string{"packetLoop", "udpJanitor", "runUDPSession", "acceptLoop", "udpWorker"} {
		marks = append(marks, fmt.Sprintf("(*shadowsocksAdapter).%s(%p", method, a))
	}
	var parts []string
	for _, block := range strings.Split(string(buf), "\n\n") {
		for _, mark := range marks {
			if strings.Contains(block, mark) {
				parts = append(parts, block)
				break
			}
		}
	}
	return len(parts) == 0, strings.Join(parts, "\n\n")
}

func TestShadowsocksUDPSessionQuota(t *testing.T) {
	// 空闲时限要明显长于客户端 B 的等待（500ms），否则 race 下 A 的会话可能在核对会话数之前就被回收
	setSSUDPIdle(t, 2*time.Second)
	user := core.User{ID: 8811, UUID: "ss-udp-quota-password"}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)
	a.udpQuota.mu.Lock()
	a.udpQuota.limit = 1
	a.udpQuota.mu.Unlock()
	echo := hy2EchoUDP(t)

	clientA := lifecycleSSUDPClient(t, a, user.UUID)
	clientB := lifecycleSSUDPClient(t, a, user.UUID)
	payload := "ss-udp-quota-payload"

	if err := lifecycleSSUDPRoundTrip(clientA, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("客户端 A 首次往返：%v", err)
	}
	if err := lifecycleSSUDPRoundTrip(clientB, echo, payload, 500*time.Millisecond); err == nil {
		t.Fatal("客户端 B 在名额满时应往返失败")
	}
	if ssUDPSessionCount(a) != 1 {
		t.Fatalf("会话数=%d，期望 1", ssUDPSessionCount(a))
	}
	if err := lifecycleSSUDPRoundTrip(clientA, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("客户端 A 再次往返：%v", err)
	}

	if ok, why := waitFor(8*time.Second, func() (bool, string) {
		return ssUDPSessionCount(a) == 0 && ssUDPQuotaInUse(a, user.ID) == 0,
			fmt.Sprintf("会话=%d 名额=%d", ssUDPSessionCount(a), ssUDPQuotaInUse(a, user.ID))
	}); !ok {
		t.Fatalf("空闲后未回收：%s", why)
	}
	if err := lifecycleSSUDPRoundTrip(clientB, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("名额归还后客户端 B 往返：%v", err)
	}
}

func TestShadowsocksUDPIdleReclaim(t *testing.T) {
	setSSUDPIdle(t, 300*time.Millisecond)
	plane := &udpRelayTestPlane{}
	user := core.User{ID: 8812, UUID: "ss-udp-idle-password"}
	a := startSSUDPAdapter(t, plane, user)
	client := lifecycleSSUDPClient(t, a, user.UUID)
	echo := hy2EchoUDP(t)
	payload := "ss-udp-idle-payload"

	if err := lifecycleSSUDPRoundTrip(client, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("首次往返：%v", err)
	}
	if ssUDPSessionCount(a) != 1 {
		t.Fatalf("会话数=%d，期望 1", ssUDPSessionCount(a))
	}
	if got := a.OnlineIPs()[user.ID]; len(got) != 1 {
		t.Fatalf("在线设备=%v，期望 1 个", got)
	}
	if liveSessionsOf(a) != 1 {
		t.Fatalf("在途登记=%d，期望 1", liveSessionsOf(a))
	}

	if ok, why := waitFor(3*time.Second, func() (bool, string) {
		return ssUDPSessionCount(a) == 0 && ssUDPQuotaInUse(a, user.ID) == 0 &&
				len(a.OnlineIPs()[user.ID]) == 0 && liveSessionsOf(a) == 0,
			fmt.Sprintf("会话=%d 名额=%d 在线=%d 登记=%d",
				ssUDPSessionCount(a), ssUDPQuotaInUse(a, user.ID), len(a.OnlineIPs()[user.ID]), liveSessionsOf(a))
	}); !ok {
		t.Fatalf("空闲回收未完成：%s", why)
	}
	plane.waitAllClosed(t, 2*time.Second)

	if err := lifecycleSSUDPRoundTrip(client, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("回收后重建往返：%v", err)
	}
	if ssUDPSessionCount(a) != 1 {
		t.Fatalf("重建后会话数=%d，期望 1", ssUDPSessionCount(a))
	}

	var first *ssUDPSession
	a.udpMu.Lock()
	for _, list := range a.udp {
		if len(list) > 0 {
			first = list[0]
			break
		}
	}
	a.udpMu.Unlock()
	if first == nil {
		t.Fatal("有流量前未找到会话")
	}
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if err := lifecycleSSUDPRoundTrip(client, echo, payload, 500*time.Millisecond); err != nil {
			t.Fatalf("保活往返：%v", err)
		}
		if ssUDPSessionCount(a) != 1 {
			t.Fatalf("保活期间会话数=%d，期望 1", ssUDPSessionCount(a))
		}
		a.udpMu.Lock()
		var cur *ssUDPSession
		for _, list := range a.udp {
			if len(list) > 0 {
				cur = list[0]
				break
			}
		}
		a.udpMu.Unlock()
		if cur != first {
			t.Fatal("保活期间会话被回收重建")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestShadowsocksUDPDelUserClosesSessions(t *testing.T) {
	plane := &udpRelayTestPlane{}
	u1 := core.User{ID: 8813, UUID: "ss-udp-del-a"}
	u2 := core.User{ID: 8814, UUID: "ss-udp-del-b"}
	a := startSSUDPAdapter(t, plane, u1, u2)
	echo := hy2EchoUDP(t)
	payload := "ss-udp-del-payload"

	pc1 := lifecycleSSUDPClient(t, a, u1.UUID)
	pc2 := lifecycleSSUDPClient(t, a, u2.UUID)
	if err := lifecycleSSUDPRoundTrip(pc1, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("u1 往返：%v", err)
	}
	if err := lifecycleSSUDPRoundTrip(pc2, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("u2 往返：%v", err)
	}
	if ssUDPSessionCount(a) != 2 {
		t.Fatalf("会话数=%d，期望 2", ssUDPSessionCount(a))
	}

	if err := a.DelUsers([]string{u1.UUID}); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return ssUDPSessionCount(a) == 1 && ssUDPQuotaInUse(a, u1.ID) == 0 && len(a.OnlineIPs()[u1.ID]) == 0,
			fmt.Sprintf("会话=%d u1名额=%d u1在线=%d", ssUDPSessionCount(a), ssUDPQuotaInUse(a, u1.ID), len(a.OnlineIPs()[u1.ID]))
	}); !ok {
		t.Fatalf("删 u1 后状态不对：%s", why)
	}
	if err := lifecycleSSUDPRoundTrip(pc1, echo, payload, 500*time.Millisecond); err == nil {
		t.Fatal("删用户后 u1 客户端仍应往返失败")
	}
	if err := lifecycleSSUDPRoundTrip(pc2, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("u2 仍应往返成功：%v", err)
	}
}

func TestShadowsocksUDPCloseLeavesNoGoroutines(t *testing.T) {
	plane := &udpRelayTestPlane{}
	user := core.User{ID: 8815, UUID: "ss-udp-close-password"}
	a := startSSUDPAdapter(t, plane, user)
	echo := hy2EchoUDP(t)
	payload := "ss-udp-close-payload"

	for i := 0; i < 5; i++ {
		pc := lifecycleSSUDPClient(t, a, user.UUID)
		if err := lifecycleSSUDPRoundTrip(pc, echo, payload, 2*time.Second); err != nil {
			t.Fatalf("客户端 %d 往返：%v", i, err)
		}
	}
	if ssUDPSessionCount(a) != 5 {
		t.Fatalf("会话数=%d，期望 5", ssUDPSessionCount(a))
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close：%v", err)
	}
	plane.waitAllClosed(t, 2*time.Second)
	if ssUDPSessionCount(a) != 0 {
		t.Fatalf("Close 后会话数=%d，期望 0", ssUDPSessionCount(a))
	}
	if ssUDPQuotaInUse(a, user.ID) != 0 {
		t.Fatalf("Close 后名额=%d，期望 0", ssUDPQuotaInUse(a, user.ID))
	}
	if len(a.OnlineIPs()[user.ID]) != 0 {
		t.Fatalf("Close 后仍在线：%v", a.OnlineIPs()[user.ID])
	}
	if liveSessionsOf(a) != 0 {
		t.Fatalf("Close 后在途登记=%d，期望 0", liveSessionsOf(a))
	}

	if ok, why := waitFor(2*time.Second, func() (bool, string) { return ssUDPStackWorkersGone(a) }); !ok {
		t.Fatalf("仍残留 SS UDP goroutine：\n%s", why)
	}

	pc := lifecycleSSUDPClient(t, a, user.UUID)
	if err := lifecycleSSUDPRoundTrip(pc, echo, payload, 500*time.Millisecond); err == nil {
		t.Fatal("入站已关闭，新客户端往返应失败")
	}
}
