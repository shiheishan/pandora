package kernel

// 经典 Shadowsocks UDP 上行限速（ReserveN）与仅按上行判断空闲、在线设备让出。

import (
	"bytes"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// setSSUDPDeviceIdle 在本测试期间把 ssUDPDeviceIdleTimeout 改成 d（必须在起适配器之前调用）。
func setSSUDPDeviceIdle(t *testing.T, d time.Duration) {
	t.Helper()
	old := ssUDPDeviceIdleTimeout
	ssUDPDeviceIdleTimeout = d
	t.Cleanup(func() { ssUDPDeviceIdleTimeout = old })
}

// startSSTrickleServer 起一个回环 UDP 服务：收到第一个包后，每 every 向发包方发一个 size 字节的包，
// 持续 dur（或用例结束）；返回它的地址与「已发包数」读取函数。
func startSSTrickleServer(t *testing.T, every time.Duration, size int, dur time.Duration) (*net.UDPAddr, func() int) {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int64
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 64<<10)
		deadline := time.Now().Add(dur)
		for time.Now().Before(deadline) {
			_ = pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			_, addr, err := pc.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			// 第一个包触发 trickle：之后只往它的来源发
			payload := bytes.Repeat([]byte{'x'}, size)
			for time.Now().Before(deadline) {
				time.Sleep(every)
				if _, err := pc.WriteToUDP(payload, addr); err != nil {
					return
				}
				sent.Add(1)
			}
			return
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr), func() int { return int(sent.Load()) }
}

// startSSPrefixSink 起一个只收不回的 UDP 接收端，返回地址与「收到的、以 prefix 开头的包数」读取函数。
func startSSPrefixSink(t *testing.T, prefix string) (*net.UDPAddr, func() int) {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	var got atomic.Int64
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, _, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if len(buf[:n]) >= len(prefix) && string(buf[:len(prefix)]) == prefix {
				got.Add(1)
			}
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr), func() int { return int(got.Load()) }
}

// TestShadowsocksUDPUplinkPassesWhileDownlinkSaturated：改前上行用 AllowN，下行 WaitN 跑满时 sink 几乎收不到。
func TestShadowsocksUDPUplinkPassesWhileDownlinkSaturated(t *testing.T) {
	user := core.User{ID: 8841, UUID: "ss-udp-rate", SpeedLimit: 800}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)
	flood, _ := startSSTrickleServer(t, time.Millisecond, 1200, 5*time.Second)
	sink, got := startSSPrefixSink(t, "up-")
	client := lifecycleSSUDPClient(t, a, user.UUID)
	if _, err := client.WriteTo([]byte("open"), flood); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	payload := append([]byte("up-"), bytes.Repeat([]byte{'p'}, 97)...)
	for i := 0; i < 20; i++ {
		_, _ = client.WriteTo(payload, sink)
		time.Sleep(50 * time.Millisecond)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return got() >= 10, fmt.Sprintf("got=%d", got())
	}); !ok {
		t.Fatalf("上行应能在下行饱和时通过：%s rate_drops=%d", why, a.udpDrops.rate.Load())
	}
}

func TestShadowsocksUDPDownlinkOnlyReleasesDevice(t *testing.T) {
	setSSUDPIdle(t, 1500*time.Millisecond)
	setSSUDPDeviceIdle(t, 300*time.Millisecond)
	user := core.User{ID: 8842, UUID: "ss-udp-devidle", DeviceLimit: 1}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)
	trickle, _ := startSSTrickleServer(t, 20*time.Millisecond, 64, 4*time.Second)
	echo := hy2EchoUDP(t)
	client := lifecycleSSUDPClient(t, a, user.UUID)

	if _, err := client.WriteTo([]byte("open"), trickle); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := client.ReadFrom(buf); err != nil {
		t.Fatalf("应收到下行：%v", err)
	}
	_ = client.SetReadDeadline(time.Time{})
	ips := a.OnlineIPs()[user.ID]
	if len(ips) != 1 || ips[0] != "127.0.0.1" {
		t.Fatalf("在线 IP=%v", ips)
	}

	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return len(a.OnlineIPs()[user.ID]) == 0 && ssUDPSessionCount(a) == 1,
			fmt.Sprintf("online=%v sessions=%d", a.OnlineIPs()[user.ID], ssUDPSessionCount(a))
	}); !ok {
		t.Fatalf("仅下行应让出设备且会话保留：%s", why)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := client.ReadFrom(buf); err != nil {
		t.Fatalf("让出设备后下行仍应通：%v", err)
	}
	_ = client.SetReadDeadline(time.Time{})

	if !a.online.enter(user, deviceWiringOtherIP) {
		t.Fatal("名额应已让出，别的设备应能 enter")
	}

	if _, err := client.WriteTo([]byte("again"), trickle); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return ssUDPSessionCount(a) == 0, fmt.Sprintf("sessions=%d", ssUDPSessionCount(a))
	}); !ok {
		t.Fatalf("重新 enter 被拒应关会话：%s", why)
	}
	ips = a.OnlineIPs()[user.ID]
	if len(ips) != 1 || ips[0] != deviceWiringOtherIP {
		t.Fatalf("在线应只剩别的设备：%v", ips)
	}

	a.online.leave(user, deviceWiringOtherIP)
	if err := lifecycleSSUDPRoundTrip(client, echo, "devidle-back", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	ips = a.OnlineIPs()[user.ID]
	if len(ips) != 1 || ips[0] != "127.0.0.1" {
		t.Fatalf("回显后在线 IP=%v", ips)
	}
}

// TestShadowsocksUDPDownlinkOnlySessionReclaimed：下行不续期，上游一直推包也按客户端上行空闲回收。
func TestShadowsocksUDPDownlinkOnlySessionReclaimed(t *testing.T) {
	setSSUDPIdle(t, 800*time.Millisecond)
	setSSUDPDeviceIdle(t, 300*time.Millisecond)
	user := core.User{ID: 8843, UUID: "ss-udp-downidle"}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)
	trickle, sent := startSSTrickleServer(t, 20*time.Millisecond, 64, 5*time.Second)
	client := lifecycleSSUDPClient(t, a, user.UUID)
	if _, err := client.WriteTo([]byte("open"), trickle); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(3*time.Second, func() (bool, string) {
		return ssUDPSessionCount(a) == 0 && ssUDPQuotaInUse(a, user.ID) == 0,
			fmt.Sprintf("sessions=%d quota=%d", ssUDPSessionCount(a), ssUDPQuotaInUse(a, user.ID))
	}); !ok {
		t.Fatalf("上行空闲应回收会话：%s", why)
	}
	a0 := sent()
	time.Sleep(100 * time.Millisecond)
	if sent() <= a0 {
		t.Fatalf("回收时 trickle 应仍在发：before=%d after=%d", a0, sent())
	}
}
