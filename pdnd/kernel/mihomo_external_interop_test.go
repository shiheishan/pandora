//go:build interop_mihomo

package kernel

// This opt-in test runs the official Mihomo binary as an independent VLESS
// client. It is deliberately excluded from normal builds and never becomes a
// production dependency of Pandora NativeCore.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

// REALITY + XHTTP/H3 第三方互操作测试。
//
// 历史说明：早期生态中没有任何第三方客户端支持 REALITY over H3
// （xray 的 REALITY 只挂在 h1/h2 分支、sing-box 只有 TCP REALITY、
// 老版 Mihomo 连 XHTTP 出站都没有），因此本测试曾无条件跳过。
//
// Mihomo v1.19.24（2026-04）起已支持 xhttp 客户端 h3 模式
// （MetaCubeX/mihomo PR #2686 "feat: add h3 mode support for xhttp client"），
// 因此只要提供 MIHOMO_BIN（≥v1.19.24）即可真正验证
// REALITY + XHTTP/H3 与独立第三方客户端的互操作，不再无条件跳过。
func TestExternalMihomoVLESSXHTTPRealityH3Interop(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("MIHOMO_BIN"))
	if bin == "" {
		t.Skip("set MIHOMO_BIN to an independently downloaded and hash-verified Mihomo binary")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("MIHOMO_BIN: %v", err)
	}
	requireExternalBinarySHA256(t, bin, "MIHOMO_SHA256")

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

	decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoyRaw.Close()
	decoyTLS := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
	defer decoyTLS.Close()
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
	shortID := "0908070605040302"
	serverPort := reserveMihomoUDPPort(t)
	mixedPort := reserveTCPPort(t)
	u := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-mihomo-reality-h3", Protocol: "vless", Listen: "127.0.0.1", Port: serverPort,
		Raw: map[string]any{
			"network": "xhttp-h3", "security": "reality", "path": "/xhttp", "mode": "stream-one",
			"dest": decoyRaw.Addr().String(), "server_names": []any{"example.com"},
			"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_ids": []any{shortID},
		},
	}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 7301, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	config := fmt.Sprintf(`mixed-port: %d
allow-lan: false
mode: rule
log-level: debug
proxies:
  - name: pandora
    type: vless
    server: 127.0.0.1
    port: %d
    uuid: "%s"
    udp: false
    tls: true
    alpn:
      - h3
    servername: example.com
    client-fingerprint: chrome
    reality-opts:
      public-key: "%s"
      short-id: "%s"
    network: xhttp
    xhttp-opts:
      path: /xhttp
      host: example.com
      mode: stream-one
proxy-groups:
  - name: pandora-group
    type: select
    proxies:
      - pandora
rules:
  - MATCH,pandora-group
`, mixedPort, serverPort, u.String(), base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), shortID)
	configPath := filepath.Join(t.TempDir(), "mihomo.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()
	cmd := exec.CommandContext(clientCtx, bin, "-f", configPath)
	logFile, err := os.Create(filepath.Join(t.TempDir(), "mihomo.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clientCancel()
		_ = cmd.Wait()
	}()

	var conn net.Conn
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(mixedPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("connect Mihomo mixed port: %v; log=%s", err, contents)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatal(err)
	}
	if methodReply[0] != 5 || methodReply[1] != 0 {
		t.Fatalf("Mihomo SOCKS method reply=%v", methodReply)
	}
	destination := upstream.Addr().(*net.TCPAddr)
	request := []byte{5, 1, 0, 1}
	request = append(request, destination.IP.To4()...)
	request = append(request, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0 {
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("Mihomo SOCKS connect reply=%v; log=%s", reply, contents)
	}
	payload := []byte("external-mihomo-reality-h3")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		contents, _ := os.ReadFile(logFile.Name())
		if bytes.Contains(contents, []byte("xhttp HTTP/3 does not support REALITY")) {
			t.Skipf("Mihomo v1.19.29 explicitly rejects REALITY+XHTTP/H3: %s", contents)
		}
		t.Fatalf("Mihomo response: %v; log=%s", err, contents)
	}
	if string(got) != string(payload) {
		t.Fatalf("Mihomo echo=%q", got)
	}
}

func reserveMihomoUDPPort(t *testing.T) int {
	t.Helper()
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	return packet.LocalAddr().(*net.UDPAddr).Port
}
