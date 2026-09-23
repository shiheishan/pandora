//go:build interop_mihomo

package kernel

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

// VLESS + Vision 走普通 TLS（不带 REALITY）的 Mihomo 互操作测试。
//
// 这个用例存在的唯一理由是二分：REALITY 单独可用、Vision 单独可用
// （见 TestVLESSVisionLoopback），只有两者叠加时 Mihomo 会静默断开。
// 把 REALITY 换成普通 TLS、其余保持不变，就能判定问题出在哪一层——
// 通过说明 Vision 本身没错，锅在 REALITY 提供的 TLS 状态；不通过则
// 反过来。没有这一刀，只能继续从字节往回猜。
func TestExternalMihomoVLESSTLSVisionInterop(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("MIHOMO_BIN"))
	if bin == "" {
		t.Skip("设置 MIHOMO_BIN 指向自行下载并核对过哈希的 Mihomo 二进制")
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

	certPath, keyPath := testXHTTPServerCertFiles(t)
	serverPort := reserveTCPPort(t)
	mixedPort := reserveTCPPort(t)
	u := uuid.New()

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-mihomo-tls-vision", Protocol: "vless",
		Listen: "127.0.0.1", Port: serverPort,
		Raw: map[string]any{
			"tls": true, "cert_path": certPath, "key_path": keyPath,
			"flow": FlowVision,
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
	if err := adapter.AddUsers([]core.User{{ID: 7303, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var serverErrs []string
	var serverErrMu sync.Mutex
	if err := adapter.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{
			target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap(),
		},
		OnConnError: func(ce ConnError) {
			serverErrMu.Lock()
			serverErrs = append(serverErrs, fmt.Sprintf("[%s] %v", ce.Stage, ce.Err))
			serverErrMu.Unlock()
		},
	}); err != nil {
		t.Fatal(err)
	}
	dumpServerErrs := func() string {
		serverErrMu.Lock()
		defer serverErrMu.Unlock()
		if len(serverErrs) == 0 {
			return "(服务端没报错)"
		}
		return strings.Join(serverErrs, "\n  ")
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
    flow: xtls-rprx-vision
    servername: localhost
    skip-cert-verify: true
    client-fingerprint: chrome
    network: tcp
proxy-groups:
  - name: pandora-group
    type: select
    proxies:
      - pandora
rules:
  - MATCH,pandora-group
`, mixedPort, serverPort, u.String())

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
		conn, err = net.DialTimeout("tcp",
			net.JoinHostPort("127.0.0.1", itoa(mixedPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("连不上 Mihomo 的 mixed 端口：%v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	destination := upstream.Addr().(*net.TCPAddr)
	request := []byte{0x05, 0x01, 0x00}
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatal(err)
	}
	request = []byte{0x05, 0x01, 0x00, 0x01}
	request = append(request, destination.IP.To4()...)
	request = append(request, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("读 SOCKS 回包失败：%v\n服务端错误:\n  %s\n客户端日志=%s",
			err, dumpServerErrs(), contents)
	}

	payload := []byte(strings.Repeat("pandora-vision-tls-interop-", 400))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		time.Sleep(500 * time.Millisecond)
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("读回声失败：%v\n服务端错误:\n  %s\n客户端日志=%s",
			err, dumpServerErrs(), contents)
	}
	if string(got) != string(payload) {
		t.Fatalf("回声不一致：收到 %d 字节，期望 %d", len(got), len(payload))
	}
}
