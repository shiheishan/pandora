//go:build interop

package kernel

// 用完整的 Xray 实例（socks 入站 → vmess 出站）连原生 VMess 入站，覆盖 v2rayN /
// v2rayNG 等 Xray 内核客户端的真实行为。与其它外部客户端门一样不进 race 套件。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	xraycore "github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

func TestExternalXrayVMessInterop(t *testing.T) {
	for _, security := range []string{"auto", "aes-128-gcm", "chacha20-poly1305", "none", "zero"} {
		t.Run(security, func(t *testing.T) { testExternalXrayVMess(t, security) })
	}
}

func testExternalXrayVMess(t *testing.T, security string) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	serverPort := reserveTCPPort(t)
	clientPort := reserveTCPPort(t)
	u := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Tag: "external-xray-vmess", Protocol: "vmess", Listen: "127.0.0.1", Port: serverPort, Raw: map[string]any{}}}
	adapterValue, err := newVMessAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vmessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 7403, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	config := map[string]any{
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": clientPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vmess",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": serverPort,
				"users": []any{map[string]any{"id": u.String(), "security": security}},
			}}},
		}},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	xray, err := xraycore.StartInstance("json", configBytes)
	if err != nil {
		t.Fatalf("start external xray client: %v", err)
	}
	defer xray.Close()

	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(clientPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect external xray socks: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatal(err)
	}
	destination := upstream.Addr().(*net.TCPAddr)
	connectRequest := append([]byte{5, 1, 0, 1}, destination.IP.To4()...)
	connectRequest = append(connectRequest, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(connectRequest); err != nil {
		t.Fatal(err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil {
		t.Fatal(err)
	}
	if connectReply[1] != 0 {
		t.Fatalf("socks connect reply=%v", connectReply)
	}
	payload := bytes.Repeat([]byte("external-xray-vmess-"+security+"|"), 2048)
	go func() { _, _ = conn.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		traffic, _ := adapter.SnapshotTraffic()
		t.Fatalf("external xray vmess %s response: %v; native traffic=%+v", security, err, traffic)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("external xray vmess %s echo mismatch", security)
	}
}
