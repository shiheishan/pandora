//go:build !race

package kernel

// Juicity 官方客户端库（daeuniverse/outbound 及其 quic-go 分支）自带数据竞争：
// 拨号时 SetCongestionControl 与收发 goroutine 并发写拥塞控制器。与 interop 门里的
// 外部客户端同理不能进 race 套件；非 race 的全量测试照常跑它。

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	juicyp "github.com/daeuniverse/outbound/protocol"
	"github.com/daeuniverse/outbound/protocol/direct"
	juicityclient "github.com/daeuniverse/outbound/protocol/juicity"
)

func TestNativeJuicityOfficialClientTCPInterop(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	const userID = "6a4e1c5b-9152-4b1e-8c33-5fb2c2c9c4a0"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "juicity", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapterValue, err := newJuicityAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*juicityAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 92, UUID: userID}}); err != nil {
		t.Fatal(err)
	}
	client, err := juicityclient.NewDialer(direct.FullconeDirect, juicyp.Header{
		ProxyAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Feature1:     "cubic",
		TlsConfig:    &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, ServerName: "localhost"},
		User:         userID, Password: userID, IsClient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.DialContext(ctx, "tcp", "echo.test:80")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("official-juicity-client")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
		t.Fatalf("official client echo=%q err=%v", got, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 92 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}
}
