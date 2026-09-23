//go:build interop_external

package kernel

import (
	"context"
	"encoding/json"
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

// Juicity 和 Naive 的互操作，用各自的官方客户端二进制。
//
// 这两个是全部入站里最后没有第三方验证的：Mihomo 两个都不实现出站；
// sing-box 有 naive 出站，但它走 cronet-go（Chromium 网络栈的预编译库），
// 本环境里那个包没有可用的 Go 文件，编不进来。所以只能落到官方二进制。
//
// 跑法（二进制自己下、自己核对哈希，永远不会成为生产依赖）：
//
//	JUICITY_CLIENT_BIN=/path/juicity-client NAIVE_BIN=/path/naive \
//	  go test -tags interop_external ./kernel/ -run TestExternal
//
// 为什么非要外部客户端：Trojan 的请求头解析曾按 SOCKS5 的 VER|CMD|RSV|ATYP
// 读，六处单元测试按同一套错误格式编码，自己和自己对得上，一路绿到拿真实
// 客户端一连才露馅。没有外部对端的协议，等于没验证。

// externalEchoUpstream 起一个回声服务当「目标网站」。
func externalEchoUpstream(t *testing.T) net.Listener {
	t.Helper()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
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
	return upstream
}

// externalSOCKSRoundTrip 通过客户端的 SOCKS 端口往回声服务发一段数据并校验。
func externalSOCKSRoundTrip(t *testing.T, socksPort int, destination *net.TCPAddr, label string, diag func() string) {
	t.Helper()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp",
			net.JoinHostPort("127.0.0.1", itoa(socksPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("连不上客户端的 SOCKS 端口：%v\n%s", err, diag())
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatalf("SOCKS 协商失败：%v\n%s", err, diag())
	}
	request := []byte{5, 1, 0, 1}
	request = append(request, destination.IP.To4()...)
	request = append(request, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("SOCKS 连接失败：%v\n%s", err, diag())
	}

	payload := []byte(strings.Repeat("pandora-"+label+"-interop-", 200))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		// 客户端断开的那一刻它自己的日志还没落盘，立刻读只会拿到出错前
		// 的部分——最关键的那条恰好缺席。
		time.Sleep(500 * time.Millisecond)
		t.Fatalf("读回声失败：%v\n%s", err, diag())
	}
	if string(got) != string(payload) {
		t.Fatalf("回声不一致：收到 %d 字节，期望 %d", len(got), len(payload))
	}
}

func TestExternalSingBoxVLESSTLSVisionInterop(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("SING_BOX_BIN"))
	if bin == "" {
		t.Skip("set SING_BOX_BIN to an independently downloaded and hash-verified official sing-box executable")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("SING_BOX_BIN: %v", err)
	}
	requireExternalBinarySHA256(t, bin, "SING_BOX_SHA256")

	upstream := externalEchoUpstream(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	serverPort := reserveTCPPort(t)
	socksPort := reserveTCPPort(t)
	userID := uuid.New()

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-sing-box-tls-vision", Protocol: "vless",
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
	if err := adapterValue.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapterValue.AddUsers([]core.User{{ID: 7600, UUID: userID.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var serverErrs []string
	var serverErrMu sync.Mutex
	if err := adapterValue.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()},
		OnConnError: func(ce ConnError) {
			serverErrMu.Lock()
			serverErrs = append(serverErrs, fmt.Sprintf("[%s] %v", ce.Stage, ce.Err))
			serverErrMu.Unlock()
		},
	}); err != nil {
		t.Fatal(err)
	}
	defer adapterValue.Close()

	clientConfig, err := json.Marshal(map[string]any{
		"log": map[string]any{"level": "debug", "timestamp": true},
		"inbounds": []any{map[string]any{
			"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": socksPort,
		}},
		"outbounds": []any{map[string]any{
			"type": "vless", "tag": "pandora", "server": "127.0.0.1", "server_port": serverPort,
			"uuid": userID.String(), "flow": FlowVision,
			"tls": map[string]any{"enabled": true, "server_name": "localhost", "insecure": true,
				"utls": map[string]any{"enabled": true, "fingerprint": "chrome"}},
		}},
		"route": map[string]any{"final": "pandora"},
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "sing-box.json")
	if err := os.WriteFile(configPath, clientConfig, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "sing-box.log")
	diag := externalStartClient(t, bin, []string{"run", "-c", configPath}, nil, logPath, func() string {
		serverErrMu.Lock()
		defer serverErrMu.Unlock()
		if len(serverErrs) == 0 {
			return "(server reported no connection error)"
		}
		return strings.Join(serverErrs, "\n  ")
	})
	externalSOCKSRoundTrip(t, socksPort, upstream.Addr().(*net.TCPAddr), "sing-box-vless-tls-vision", diag)
}

func TestExternalJuicityClientInterop(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("JUICITY_CLIENT_BIN"))
	if bin == "" {
		t.Skip("设置 JUICITY_CLIENT_BIN 指向自行下载并核对过哈希的 juicity-client")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("JUICITY_CLIENT_BIN: %v", err)
	}
	requireExternalBinarySHA256(t, bin, "JUICITY_CLIENT_SHA256")

	upstream := externalEchoUpstream(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	serverPort := reserveTCPPort(t)
	socksPort := reserveTCPPort(t)
	// juicity 的认证把 UUID 同时当身份和导出密钥材料的上下文，
	// 所以客户端的 uuid 和 password 填的是同一个值。
	secret := uuid.New().String()

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-juicity", Protocol: "juicity",
		Listen: "127.0.0.1", Port: serverPort,
		Raw: map[string]any{"cert_path": certPath, "key_path": keyPath},
	}}
	adapterValue, err := newJuicityAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterValue.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapterValue.AddUsers([]core.User{{ID: 7601, UUID: secret}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var serverErrs []string
	var serverErrMu sync.Mutex
	if err := adapterValue.Start(ctx, spec, AdapterHooks{
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
	defer adapterValue.Close()

	clientConfig, err := json.Marshal(map[string]any{
		"listen":             "127.0.0.1:" + itoa(socksPort),
		"server":             "127.0.0.1:" + itoa(serverPort),
		"uuid":               secret,
		"password":           secret,
		"sni":                "localhost",
		"allow_insecure":     true,
		"congestion_control": "bbr",
		"log_level":          "debug",
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "juicity-client.json")
	if err := os.WriteFile(configPath, clientConfig, 0600); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "juicity.log")
	diag := externalStartClient(t, bin, []string{"run", "-c", configPath}, nil, logPath, func() string {
		serverErrMu.Lock()
		defer serverErrMu.Unlock()
		if len(serverErrs) == 0 {
			return "(服务端没报错)"
		}
		return strings.Join(serverErrs, "\n  ")
	})

	externalSOCKSRoundTrip(t, socksPort, upstream.Addr().(*net.TCPAddr), "juicity", diag)
}

func TestExternalNaiveClientInterop(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("NAIVE_BIN"))
	if bin == "" {
		t.Skip("设置 NAIVE_BIN 指向自行下载并核对过哈希的 naive 客户端")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("NAIVE_BIN: %v", err)
	}
	requireExternalBinarySHA256(t, bin, "NAIVE_SHA256")

	upstream := externalEchoUpstream(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	serverPort := reserveTCPPort(t)
	socksPort := reserveTCPPort(t)
	// Naive 的用户名和密码服务端取的是同一个 core.User.UUID。
	secret := uuid.New().String()

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-naive", Protocol: "naive",
		Listen: "127.0.0.1", Port: serverPort,
		Raw: map[string]any{"tls": true, "cert_path": certPath, "key_path": keyPath},
	}}
	adapterValue, err := newNaiveAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterValue.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapterValue.AddUsers([]core.User{{ID: 7602, UUID: secret}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var serverErrs []string
	var serverErrMu sync.Mutex
	if err := adapterValue.Start(ctx, spec, AdapterHooks{
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
	defer adapterValue.Close()

	// naive 是 Chromium 出身，没有 skip-verify 这类开关，这一版连
	// --ignore-certificate-errors-spki-list 都没有（见 --help）。剩下的
	// 干净办法只有把测试证书当作受信根喂给它——绝不去动这台机器的系统
	// 信任库，那会影响机器上的其它东西。
	logPath := filepath.Join(t.TempDir(), "naive.log")
	diag := externalStartClient(t, bin, []string{
		"--listen=socks://127.0.0.1:" + itoa(socksPort),
		"--proxy=https://" + secret + ":" + secret + "@localhost:" + itoa(serverPort),
		"--host-resolver-rules=MAP localhost 127.0.0.1",
		"--log",
	}, []string{"SSL_CERT_FILE=" + certPath}, logPath, func() string {
		serverErrMu.Lock()
		defer serverErrMu.Unlock()
		if len(serverErrs) == 0 {
			return "(服务端没报错)"
		}
		return strings.Join(serverErrs, "\n  ")
	})

	externalSOCKSRoundTrip(t, socksPort, upstream.Addr().(*net.TCPAddr), "naive", diag)
}

// externalStartClient 起一个外部客户端进程，返回一个把它的日志和服务端
// 错误一起吐出来的诊断函数。互操作失败时两头都不说话是常态，没有这个
// 就只能靠猜。
func externalStartClient(t *testing.T, bin string, args []string, env []string, logPath string, serverDiag func() string) func() string {
	t.Helper()
	clientCtx, clientCancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(clientCtx, bin, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		clientCancel()
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		clientCancel()
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clientCancel()
		_ = cmd.Wait()
		_ = logFile.Close()
	})
	return func() string {
		contents, _ := os.ReadFile(logPath)
		return "服务端错误:\n  " + serverDiag() + "\n客户端日志:\n" + string(contents)
	}
}
