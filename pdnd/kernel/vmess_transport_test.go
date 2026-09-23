package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
)

func TestVMessNativeTLSLoopback(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	id := uuid.New()
	a := &vmessAdapter{users: make(map[string]vmessUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"security": "aes-128-gcm", "tls": true, "cert_path": certPath, "key_path": keyPath,
	}}}
	if err := a.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := a.AddUsers([]core.User{{ID: 997, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec -- ephemeral test certificate.
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	client, err := vmessref.NewClient(id.String(), "aes-128-gcm", 0)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.DialConn(raw, M.ParseSocksaddrHostPort("127.0.0.1", uint16(upstream.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	payload := []byte("native-vmess-tls")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q", got)
	}
	_ = a.Close()
	select {
	case <-upDone:
	case <-time.After(time.Second):
	}
}

func TestVMessNativeWebSocketLoopback(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	a := &vmessAdapter{users: make(map[string]vmessUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "ws", "path": "/pandora-vmess", "security": "aes-128-gcm",
	}}}
	if err := a.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := a.AddUsers([]core.User{{ID: 998, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	wsConn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d/pandora-vmess", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsConn.Close()
	raw := newWebSocketNetConn(wsConn)
	client, err := vmessref.NewClient(id.String(), "aes-128-gcm", 0)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.DialConn(raw, M.ParseSocksaddrHostPort("127.0.0.1", uint16(upstream.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	payload := []byte("native-vmess-websocket")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q", got)
	}
}
