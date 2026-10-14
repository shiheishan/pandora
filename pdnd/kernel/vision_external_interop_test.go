//go:build interop

package kernel

// 完整 Xray 实例（socks 入站 → vless+REALITY+xtls-rprx-vision 出站）连原生 VLESS
// 入站，经代理对 TLS 1.3 站点做真握手并收发数据：Xray 读到 command=2 就改读裸 TCP，
// 服务端没真正直通时这里报 bad record MAC（10-08 真节点测试的原样症状）。

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
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

func TestExternalXrayVLESSRealityVisionTLS13Interop(t *testing.T) {
	targetTLS := testXHTTPServerTLSConfig(t)
	targetTLS.MinVersion = tls.VersionTLS13
	targetLn, err := tls.Listen("tcp", "127.0.0.1:0", targetTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go func() {
		for {
			conn, acceptErr := targetLn.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoyRaw.Close()
	decoyTLS := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
	go func() {
		for {
			conn, acceptErr := decoyTLS.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := "0102030405060708"
	serverPort := reserveTCPPort(t)
	clientPort := reserveTCPPort(t)
	u := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Tag: "external-xray-vision", Protocol: "vless", Listen: "127.0.0.1", Port: serverPort, Raw: map[string]any{
		"security": "reality", "flow": FlowVision,
		"dest": decoyRaw.Addr().String(), "server_names": []any{"example.com"},
		"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_ids": []any{shortID},
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.AddUsers([]core.User{{ID: 7502, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(targetLn.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	config := map[string]any{
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": clientPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": serverPort,
				"users": []any{map[string]any{"id": u.String(), "encryption": "none", "flow": FlowVision}},
			}}},
			"streamSettings": map[string]any{
				"network": "tcp", "security": "reality",
				"realitySettings": map[string]any{
					"serverName": "example.com", "fingerprint": "chrome",
					"publicKey": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "shortId": shortID,
				},
			},
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
	if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	destination := targetLn.Addr().(*net.TCPAddr)
	connectRequest := append([]byte{5, 1, 0, 1}, destination.IP.To4()...)
	connectRequest = append(connectRequest, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(connectRequest); err != nil {
		t.Fatal(err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil || connectReply[1] != 0 {
		t.Fatalf("socks connect reply=%v err=%v", connectReply, err)
	}
	inner := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec // 测试目标站自签证书
	if err := inner.Handshake(); err != nil {
		t.Fatalf("经 Xray Vision 的内层 TLS 1.3 握手: %v", err)
	}
	for i := 0; i < 5; i++ {
		ping := bytes.Repeat([]byte{byte('a' + i)}, 1000)
		if _, err := inner.Write(ping); err != nil {
			t.Fatal(err)
		}
		pong := make([]byte, len(ping))
		if _, err := io.ReadFull(inner, pong); err != nil || !bytes.Equal(pong, ping) {
			t.Fatalf("第 %d 轮内层 TLS 1.3 回显: %v", i, err)
		}
	}
	payload := make([]byte, 256<<10)
	_, _ = rand.Read(payload)
	go func() { _, _ = inner.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(inner, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("内层 TLS 1.3 大块回显: %v", err)
	}
}
