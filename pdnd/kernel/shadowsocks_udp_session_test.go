package kernel

// Shadowsocks 经典 AEAD 的 UDP 会话守卫测试：
//
// 1. 同端口 TCP 与 UDP 互通：同一端口同时接 TCP 与 UDP 客户端，互不干扰；
//    UDP 客户端向多个不同回显目标发送数据报时走同一个会话（按客户端地址+用户复用），
//    且回包按上游真实来源地址编码返回（多目标来回）；连接关闭或有交互后流量计入。
// 2. 下行突发（DownlinkBurstWithoutUplink）：客户端只发首包打开关联，服务端连发
//    1000 包下行，验证上下行解耦，下行不被限速、不丢包，且流量计量准确。
// 3. 空闲会话存活堆（IdleUDPSessionHeap）：守的是「空闲会话不常驻收包缓冲」——
//    改前每包 64KB 读缓冲，若下行改回持有 64KB 阻塞读，这里会到每会话 64KB 以上。
//    复用 hy2DownlinkUDP 后，1000 个空闲会话的平均存活堆每会话 ≤16KB。
//
// 全部测试只使用虚构用户与回环地址。

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	SS "github.com/sagernet/sing-shadowsocks2"

	"github.com/aegispanel/nodeagent/core"
)

// ssUDPTestClient 建一个经 SS（aes-128-gcm）到 127.0.0.1:port 的 UDP 客户端；raw 是底层 socket（调读缓冲、关闭用）。
func ssUDPTestClient(t testing.TB, port int, password string) (net.PacketConn, *net.UDPConn) {
	t.Helper()
	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	method, err := SS.CreateMethod(context.Background(), "aes-128-gcm", SS.MethodOptions{Password: password})
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return method.DialPacketConn(raw), raw
}

// startSSUDPCore 起一个经典 SS 入站（真 NativeCore、真出站、放开回环），返回端口与适配器。
func startSSUDPCore(t *testing.T, user core.User) (int, *shadowsocksAdapter) {
	t.Helper()
	c, port, tag := startLifecycleCore(t, lifecycleProto{name: "shadowsocks", raw: map[string]any{"method": "aes-128-gcm"}}, []core.User{user})
	adapter := deviceWiringAdapter(t, c, tag)
	ss, ok := adapter.(*shadowsocksAdapter)
	if !ok {
		t.Fatalf("adapter is %T, want *shadowsocksAdapter", adapter)
	}
	return port, ss
}

func TestShadowsocksSamePortTCPAndUDPInterop(t *testing.T) {
	user := core.User{ID: 8801, UUID: "ss-udp-interop-password"}
	port, ss := startSSUDPCore(t, user)

	// TCP 互通：通过 SS TCP 连向本机 TCP 回显服务
	tcpEcho := startLifecycleEcho(t, false)
	conn, err := dialLifecycleSS(port, user, tcpEcho.addr())
	if err != nil {
		t.Fatalf("dialLifecycleSS failed: %v", err)
	}
	tcpPayload := []byte("tcp-over-ss")
	if _, err := conn.Write(tcpPayload); err != nil {
		_ = conn.Close()
		t.Fatalf("tcp write failed: %v", err)
	}
	tcpBuf := make([]byte, len(tcpPayload))
	if _, err := io.ReadFull(conn, tcpBuf); err != nil {
		_ = conn.Close()
		t.Fatalf("tcp read failed: %v", err)
	}
	if string(tcpBuf) != string(tcpPayload) {
		_ = conn.Close()
		t.Fatalf("tcp echo got %q, want %q", tcpBuf, tcpPayload)
	}
	_ = conn.Close()

	// UDP 互通：两个回显目标，单客户端交替收发，验证多目标来回走同一个会话且来源地址正确
	a := hy2EchoUDP(t)
	b := hy2EchoUDP(t)
	pc, _ := ssUDPTestClient(t, port, user.UUID)

	buf := make([]byte, 2048)
	for i := 0; i < 20; i++ {
		target := a
		if i%2 == 1 {
			target = b
		}
		payload := fmt.Sprintf("udp-%d", i)
		if _, err := pc.WriteTo([]byte(payload), target); err != nil {
			t.Fatalf("step %d: WriteTo %v failed: %v", i, target, err)
		}
		if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("step %d: SetReadDeadline failed: %v", i, err)
		}
		n, src, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("step %d: ReadFrom failed: %v", i, err)
		}
		if string(buf[:n]) != payload {
			t.Fatalf("step %d: got payload %q, want %q", i, string(buf[:n]), payload)
		}
		if src.String() != target.String() {
			t.Fatalf("step %d: got source addr %s, want %s (回包未按真实来源编码)", i, src.String(), target.String())
		}
	}

	// 20 来回都走同一个客户端，会话总数应为 1
	if got := ssUDPSessionCount(ss); got != 1 {
		t.Fatalf("ssUDPSessionCount=%d, want 1", got)
	}

	// 流量：等待用户的上传与下载流量均上报计入
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		tr := ss.sessions.peek(user.ID)
		return tr.Upload > 0 && tr.Download > 0, fmt.Sprintf("upload=%d download=%d", tr.Upload, tr.Download)
	}); !ok {
		t.Fatalf("等待流量计入超时: %s", why)
	}
}

func TestShadowsocksUDPDownlinkBurstWithoutUplink(t *testing.T) {
	user := core.User{ID: 8802, UUID: "ss-udp-burst-password"}
	port, ss := startSSUDPCore(t, user)
	target := startUDPBurstServer(t, udpBurstCount)

	pc, raw := ssUDPTestClient(t, port, user.UUID)
	_ = raw.SetReadBuffer(1 << 20)

	// 发一包 "open" 到 target
	if _, err := pc.WriteTo([]byte("open"), target); err != nil {
		t.Fatalf("WriteTo open failed: %v", err)
	}

	_ = pc.SetReadDeadline(time.Now().Add(udpBurstWindow))
	buf := make([]byte, 2048)
	expectUDPBurst(t, udpBurstCount, func() ([]byte, error) {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), buf[:n]...), nil
	})

	assertUDPRelayTraffic(t, ss.SnapshotTraffic, user.ID, int64(len("open")), udpBurstCount*udpBurstPayloadSize)
}

func TestShadowsocksIdleUDPSessionHeap(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器自带的分配会抬高堆，不量")
	}

	const sessions = 1000
	user := core.User{ID: 8803, UUID: "ss-udp-mem-password"}
	port, ss := startSSUDPCore(t, user)

	// 将每用户上限调到 2000，避免默认 1024 限制挡住
	ss.udpQuota.mu.Lock()
	ss.udpQuota.limit = 2000
	ss.udpQuota.mu.Unlock()

	echo := hy2EchoUDP(t)

	// 先把 1000 个客户端全部建好，再量初始存活堆
	clients := make([]net.PacketConn, sessions)
	for i := range clients {
		pc, _ := ssUDPTestClient(t, port, user.UUID)
		clients[i] = pc
	}

	before := heapAfterGC()

	// 逐个一来一回（不一次灌 1000 包，免得入站 socket 的收包缓冲溢出丢包）；共用一个 2KB 缓冲。
	buf := make([]byte, 2048)
	for i, pc := range clients {
		if _, err := pc.WriteTo([]byte("ping"), echo); err != nil {
			t.Fatalf("client %d write ping failed: %v", i, err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "ping" {
			t.Fatalf("client %d read echo failed: n=%d err=%v payload=%q", i, n, err, buf[:n])
		}
	}

	// 等全部 1000 个会话登记到位
	if ok, why := waitFor(5*time.Second, func() (bool, string) {
		count := ssUDPSessionCount(ss)
		return count == sessions, fmt.Sprintf("count=%d want %d", count, sessions)
	}); !ok {
		t.Fatalf("等待 1000 个会话建齐超时: %s", why)
	}

	after := heapAfterGC()
	perSession := (int64(after) - int64(before)) / sessions
	t.Logf("%d 个空闲会话：存活堆 %d → %d 字节，平均每会话 %d 字节", sessions, before, after, perSession)

	if os.Getenv("PDND_SS_MEM_REPORT") != "" {
		return
	}

	// 守的是「空闲会话不常驻收包缓冲」——改前每包 64KB 读缓冲，
	// 若下行改回持有 64KB 阻塞读，这里会到每会话 64KB 以上。
	if perSession > 16<<10 {
		t.Fatalf("每个空闲会话常驻 %d 字节，超过 16KB 上限", perSession)
	}
}
