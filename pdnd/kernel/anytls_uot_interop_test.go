//go:build interop

package kernel

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/util"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/uot"

	"github.com/aegispanel/nodeagent/core"
)

// 这组用例挂在 TestAnyTLSNativeClientTCPAndUOTUDP 下随 CI 的 interop 门跑。
func runAnyTLSUOTGroup(t *testing.T) {
	t.Run("close-releases", func(t *testing.T) { t.Parallel(); runAnyTLSUOTCloseReleases(t) })
	t.Run("traffic", func(t *testing.T) { t.Parallel(); runAnyTLSUOTTraffic(t) })
}

// startAnyTLSUOTEcho 起一个以 hysteriaEchoPlane 为数据面的 AnyTLS 入站（UDP 原样
// 回显），返回适配器与已经开好的 UoT 连接（v2，非 connect 模式）。
func startAnyTLSUOTEcho(t *testing.T, userID int64) (*anyTLSAdapter, net.PacketConn, M.Socksaddr) {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "anytls", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	adapter, err := newAnyTLSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	secret := "anytls-uot-secret-" + strconv.FormatInt(userID, 10)
	if err := adapter.AddUsers([]core.User{{ID: userID, UUID: secret}}); err != nil {
		t.Fatal(err)
	}
	dialOut := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	client, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: secret, DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	proxy, err := client.CreateProxy(ctx, M.Socksaddr{Fqdn: uot.MagicAddress})
	if err != nil {
		t.Fatal(err)
	}
	target := M.ParseSocksaddr("127.0.0.1:53")
	pc, err := (&uot.Client{Version: uot.Version}).DialConn(proxy, false, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return adapter.(*anyTLSAdapter), pc, target
}

// 客户端关掉 UoT 流后，服务端立刻收尾：在线设备随之归零（不必等下一个回包或
// 入站关闭）。修前这条流在服务端一直挂着，设备名额、goroutine 与上游 socket 不释放。
func runAnyTLSUOTCloseReleases(t *testing.T) {
	adapter, pc, target := startAnyTLSUOTEcho(t, 6306)
	if _, err := pc.WriteTo([]byte("uot"), target); err != nil {
		t.Fatal(err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(anyTLSOpTimeout))
	if _, _, err := pc.ReadFrom(make([]byte, 64)); err != nil {
		t.Fatalf("UoT 回显：%v", err)
	}
	if online := adapter.OnlineIPs(); len(online[6306]) != 1 {
		t.Fatalf("UoT 进行中在线设备=%v，应为 1 个", online)
	}
	_ = pc.Close()
	deadline := time.Now().Add(anyTLSOpTimeout)
	for len(adapter.OnlineIPs()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("客户端关掉 UoT 流 5 秒后服务端仍记着在线设备 %v：这条流没有收尾", adapter.OnlineIPs())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 只走 UoT（不开任何 TCP 流）也要计流量：口径与 hysteria2 / TUIC 的 UDP 一样按
// UDP 负载字节记，上行是发往上游的负载、下行是上游回来的负载，UoT 的帧头
// （地址、长度）不算。回显 100 个 1000 字节的包，上下行都应恰好 100000。
// 修前 handleUOT 一个字节都不记，SnapshotTraffic 返回空。
func runAnyTLSUOTTraffic(t *testing.T) {
	const (
		userID  = 6307
		packets = 100
		size    = 1000
	)
	adapter, pc, target := startAnyTLSUOTEcho(t, userID)
	payload := make([]byte, size)
	got := make([]byte, 2048)
	for i := 0; i < packets; i++ {
		payload[0] = byte(i)
		if _, err := pc.WriteTo(payload, target); err != nil {
			t.Fatal(err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(anyTLSOpTimeout))
		n, _, err := pc.ReadFrom(got)
		if err != nil || n != size || got[0] != byte(i) {
			t.Fatalf("第 %d 个包回显 n=%d err=%v", i, n, err)
		}
	}
	// 客户端读到最后一个回包时，服务端可能还没把这次计数写完：等计数到位再取快照。
	const want = packets * size
	deadline := time.Now().Add(anyTLSOpTimeout)
	for {
		tr := adapter.sessions.peek(userID)
		if tr.Upload == want && tr.Download == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("只走 UoT 的流量 上行=%d 下行=%d，应都为 %d", tr.Upload, tr.Download, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != userID || traffic[0].Upload != want || traffic[0].Download != want {
		t.Fatalf("SnapshotTraffic=%+v err=%v，应为用户 %d 上下行各 %d", traffic, err, userID, want)
	}
}
