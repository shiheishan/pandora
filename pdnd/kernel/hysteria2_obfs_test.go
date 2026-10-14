package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	M "github.com/sagernet/sing/common/metadata"
)

// 开 Salamander 混淆的入站经真实客户端（sing-quic，同样开混淆）走大块 TCP 与
// 各种长度的 UDP：Linux 上服务端混淆层走 recvmmsg + GSO（批量 Salamander），
// 大块下行会触发多段 GSO，字节必须逐一对上。
func TestHysteria2SalamanderBulkTCPAndUDP(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "hysteria2", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
		"obfs": map[string]any{"type": "salamander", "password": "obfs-secret"},
	}}}
	adapter, err := newHysteria2Adapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 7, UUID: "obfs-user"}}); err != nil {
		t.Fatal(err)
	}
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)), Password: "obfs-user",
		SalamanderPassword: "obfs-secret",
		TLSConfig:          &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)

	tcp, err := client.DialConn(ctx, M.ParseSocksaddr("echo.test:80"))
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	payload := make([]byte, 4<<20)
	_, _ = rand.Read(payload)
	go func() { _, _ = tcp.Write(payload) }()
	_ = tcp.SetReadDeadline(time.Now().Add(20 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(tcp, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("混淆下 TCP 大块回显不一致 err=%v", err)
	}

	udp, err := client.ListenPacket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	reply := make([]byte, 64<<10)
	for i, size := range []int{1, 64, 1200, 1350, 3000, 512} {
		data := bytes.Repeat([]byte{byte(i + 1)}, size)
		if _, err := udp.WriteTo(data, M.ParseSocksaddr("127.0.0.1:53")); err != nil {
			t.Fatal(err)
		}
		_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := udp.ReadFrom(reply)
		if err != nil || !bytes.Equal(reply[:n], data) {
			t.Fatalf("混淆下第 %d 个 UDP 包（%d 字节）回显 n=%d err=%v", i, size, n, err)
		}
	}
}
