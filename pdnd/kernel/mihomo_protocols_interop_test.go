//go:build interop_mihomo

package kernel

import (
	"context"
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
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

// 用官方 Mihomo 二进制逐个验证其余协议的线格式。
//
// 单元测试只能证明「我编的和我解的一致」；协议实现的价值在于别人按上游
// 规范编的东西我们能解。这两件事的差别在握手阶段看不出来，要到数据面才
// 暴露，正是最难查的那类问题——VLESS+Vision 那次前后猜错五次就是例子。
//
// 表驱动而不是一个协议一个文件：这些用例只在「inbound 配置」和「Mihomo
// proxies 条目」两处不同，其余流程（回声上游、启客户端、SOCKS 往返、
// 校验回声）完全一样。共用一套 harness，加一个协议就是加一行。
//
// 和同目录其它互操作测试一样：opt-in 构建标签 + MIHOMO_BIN 环境变量，
// 二进制自己下、自己核对哈希，永远不会变成 Pandora NativeCore 的生产依赖。

type mihomoProtoCase struct {
	name     string
	protocol string
	factory  AdapterFactory
	// raw 是入站配置。needCert 为真时，harness 会生成自签证书并把
	// cert_path / key_path 补进去。
	raw      map[string]any
	needCert bool
	// prepare 在入站配置定型前介入，返回客户端该用的凭证。
	// 多数协议的凭证就是 core.User.UUID，用不上它；
	// Shadowsocks 2022 例外——密钥是入站级的 base64 PSK，
	// 不从用户身份派生，得先生成再两头对齐。
	prepare func(t *testing.T, raw map[string]any, defaultSecret string) string
	// proxy 返回 Mihomo 配置里那一条 proxies 条目，缩进要对齐模板。
	proxy func(port int, secret string) string
}

func mihomoProtoCases() []mihomoProtoCase {
	return []mihomoProtoCase{
		{
			name:     "vmess-tcp",
			protocol: "vmess",
			factory:  newVMessAdapter,
			raw:      map[string]any{},
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: vmess
    server: 127.0.0.1
    port: %d
    uuid: "%s"
    alterId: 0
    cipher: auto
    udp: false`, port, secret)
			},
		},
		{
			name:     "trojan-tls",
			protocol: "trojan",
			factory:  newTrojanAdapter,
			raw:      map[string]any{"tls": true},
			needCert: true,
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: trojan
    server: 127.0.0.1
    port: %d
    password: "%s"
    sni: localhost
    skip-cert-verify: true
    udp: false`, port, secret)
			},
		},
		{
			name:     "hysteria2",
			protocol: "hysteria2",
			factory:  newHysteria2Adapter,
			raw:      map[string]any{},
			needCert: true,
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: hysteria2
    server: 127.0.0.1
    port: %d
    password: "%s"
    sni: localhost
    skip-cert-verify: true`, port, secret)
			},
		},
		{
			name:     "tuic",
			protocol: "tuic",
			factory:  newTUICAdapter,
			raw:      map[string]any{},
			needCert: true,
			proxy: func(port int, secret string) string {
				// TUIC 的 uuid 和 password 服务端取的是同一个
				// core.User.UUID，客户端两处都填它。
				return fmt.Sprintf(`  - name: pandora
    type: tuic
    server: 127.0.0.1
    port: %d
    uuid: "%s"
    password: "%s"
    sni: localhost
    skip-cert-verify: true
    udp-relay-mode: native
    congestion-controller: bbr`, port, secret, secret)
			},
		},
		{
			name:     "anytls",
			protocol: "anytls",
			factory:  newAnyTLSAdapter,
			raw:      map[string]any{"tls": true},
			needCert: true,
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: anytls
    server: 127.0.0.1
    port: %d
    password: "%s"
    sni: localhost
    skip-cert-verify: true`, port, secret)
			},
		},
		{
			name:     "socks5",
			protocol: "socks",
			factory:  newSOCKSAdapter,
			raw:      map[string]any{},
			proxy: func(port int, secret string) string {
				// 用户名和密码服务端取的是同一个 core.User.UUID。
				return fmt.Sprintf(`  - name: pandora
    type: socks5
    server: 127.0.0.1
    port: %d
    username: "%s"
    password: "%s"
    udp: false`, port, secret, secret)
			},
		},
		{
			name:     "http",
			protocol: "http",
			factory:  newHTTPProxyAdapter,
			raw:      map[string]any{},
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: http
    server: 127.0.0.1
    port: %d
    username: "%s"
    password: "%s"`, port, secret, secret)
			},
		},
		{
			name:     "mieru",
			protocol: "mieru",
			factory:  newMieruAdapter,
			raw:      map[string]any{"transport": "TCP"},
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: mieru
    server: 127.0.0.1
    port: %d
    transport: TCP
    username: "%s"
    password: "%s"`, port, secret, secret)
			},
		},
		{
			name:     "shadowtls-v3",
			protocol: "shadowtls",
			factory:  newShadowTLSAdapter,
			raw:      map[string]any{"method": "aes-128-gcm", "version": 3},
			prepare: func(t *testing.T, raw map[string]any, defaultSecret string) string {
				// ShadowTLS 要借一个真实站点的 TLS 握手来伪装，服务端会把
				// 客户端的握手原样转给它。这里起一个本地 TLS 服务当那个站点。
				decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				decoy := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
				t.Cleanup(func() { _ = decoy.Close() })
				go func() {
					for {
						conn, acceptErr := decoy.Accept()
						if acceptErr != nil {
							return
						}
						go func() {
							defer conn.Close()
							_, _ = io.Copy(io.Discard, conn)
						}()
					}
				}()
				raw["server"] = decoyRaw.Addr().String()
				raw["password"] = defaultSecret + "-outer"
				return defaultSecret
			},
			proxy: func(port int, secret string) string {
				// 外层是 ShadowTLS 的伪装密码，内层是 Shadowsocks 的密码
				//（服务端拿 core.User.UUID 派生），两者是不同的东西。
				return fmt.Sprintf(`  - name: pandora
    type: ss
    server: 127.0.0.1
    port: %d
    cipher: aes-128-gcm
    password: "%s"
    udp: false
    plugin: shadow-tls
    plugin-opts:
      host: "localhost"
      password: "%s-outer"
      version: 3
      skip-cert-verify: true`, port, secret, secret)
			},
		},
		{
			name:     "shadowsocks-2022-blake3-aes-128-gcm",
			protocol: "shadowsocks",
			factory:  newShadowsocksAdapter,
			raw:      map[string]any{"method": "2022-blake3-aes-128-gcm"},
			prepare: func(t *testing.T, raw map[string]any, _ string) string {
				key := make([]byte, 16)
				if _, err := rand.Read(key); err != nil {
					t.Fatal(err)
				}
				psk := base64.StdEncoding.EncodeToString(key)
				raw["password"] = psk
				return psk
			},
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: ss
    server: 127.0.0.1
    port: %d
    cipher: 2022-blake3-aes-128-gcm
    password: "%s"
    udp: false`, port, secret)
			},
		},
		{
			name:     "shadowsocks-aes-128-gcm",
			protocol: "shadowsocks",
			factory:  newShadowsocksAdapter,
			raw:      map[string]any{"method": "aes-128-gcm"},
			proxy: func(port int, secret string) string {
				return fmt.Sprintf(`  - name: pandora
    type: ss
    server: 127.0.0.1
    port: %d
    cipher: aes-128-gcm
    password: "%s"
    udp: false`, port, secret)
			},
		},
	}
}

// interopDialPlane 按协议实际解析出来的目标拨号，而不是把目标写死。
//
// 换掉 vlessTestPlane 有两个理由。一是 ShadowTLS 必须这样：它要把客户端
// 的 TLS 握手原样转给被借用的那个站点，目标写死的话握手会被送进回声服务，
// 客户端收到自己的 ClientHello 就报
// "unexpected handshake message of type *tls.clientHelloMsg"。
// 二是顺带让这套互操作真正校验地址解析——目标写死的假数据面看不见协议
// 把地址解析错了没有，Trojan 曾经把 ADDR 和 PORT 读反、解析出
// 1.187.127.0:1 这种垃圾地址，测试照样全绿。
type interopDialPlane struct{}

func (interopDialPlane) DialTCP(ctx context.Context, _ route.Meta, destination M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", destination.String())
}

func (interopDialPlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp", "127.0.0.1:0")
}

func TestExternalMihomoProtocolsInterop(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("MIHOMO_BIN"))
	if bin == "" {
		t.Skip("设置 MIHOMO_BIN 指向自行下载并核对过哈希的 Mihomo 二进制")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("MIHOMO_BIN: %v", err)
	}
	requireExternalBinarySHA256(t, bin, "MIHOMO_SHA256")
	for _, tc := range mihomoProtoCases() {
		t.Run(tc.name, func(t *testing.T) {
			runMihomoProtoCase(t, bin, tc)
		})
	}
}

func runMihomoProtoCase(t *testing.T, bin string, tc mihomoProtoCase) {
	t.Helper()

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
	mixedPort := reserveTCPPort(t)
	// 多数协议的用户凭证就是 core.User.UUID：VMess 直接当 id，Trojan、
	// Hysteria2、AnyTLS 当密码，TUIC 的 uuid 和 password 都取它，
	// Shadowsocks 拿它派生主密钥。所以一个值就够，例外由 prepare 覆盖。
	secret := uuid.New().String()

	raw := map[string]any{}
	for k, v := range tc.raw {
		raw[k] = v
	}
	if tc.needCert {
		certPath, keyPath := testXHTTPServerCertFiles(t)
		raw["cert_path"], raw["key_path"] = certPath, keyPath
	}
	if tc.prepare != nil {
		secret = tc.prepare(t, raw, secret)
	}

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-mihomo-" + tc.name, Protocol: tc.protocol,
		Listen: "127.0.0.1", Port: serverPort, Raw: raw,
	}}
	adapterValue, err := tc.factory(spec)
	if err != nil {
		t.Fatalf("创建 %s 适配器：%v", tc.protocol, err)
	}
	if err := adapterValue.Validate(spec); err != nil {
		t.Fatalf("校验 %s 配置：%v", tc.protocol, err)
	}
	if err := adapterValue.AddUsers([]core.User{{ID: 7401, UUID: secret}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var serverErrs []string
	var serverErrMu sync.Mutex
	if err := adapterValue.Start(ctx, spec, AdapterHooks{
		DataPlane: interopDialPlane{},
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
	defer adapterValue.Close()

	config := fmt.Sprintf(`mixed-port: %d
allow-lan: false
mode: rule
log-level: debug
proxies:
%s
proxy-groups:
  - name: pandora-group
    type: select
    proxies:
      - pandora
rules:
  - MATCH,pandora-group
`, mixedPort, tc.proxy(serverPort, secret))

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

	// Mihomo 刚起来时代理组可能还没就绪，第一次拨号偶尔会被拒。
	// 失败就重来，别把启动竞态记成协议不通。
	destination := upstream.Addr().(*net.TCPAddr)
	socksConnect := func(c net.Conn) error {
		if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			return err
		}
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(c, greeting); err != nil {
			return err
		}
		req := []byte{0x05, 0x01, 0x00, 0x01}
		req = append(req, destination.IP.To4()...)
		req = append(req, byte(destination.Port>>8), byte(destination.Port))
		if _, err := c.Write(req); err != nil {
			return err
		}
		reply := make([]byte, 10)
		_, err := io.ReadFull(c, reply)
		return err
	}
	if err := socksConnect(conn); err != nil {
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("SOCKS 握手失败：%v\n服务端错误:\n  %s\n客户端日志=%s",
			err, dumpServerErrs(), contents)
	}

	payload := []byte(strings.Repeat("pandora-"+tc.name+"-interop-", 200))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		// 客户端把连接掐掉的那一刻它自己的日志还没落盘，立刻读只会
		// 拿到出错前的部分——最关键的那条恰好缺席。
		time.Sleep(500 * time.Millisecond)
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("读回声失败：%v\n服务端错误:\n  %s\n客户端日志=%s",
			err, dumpServerErrs(), contents)
	}
	if string(got) != string(payload) {
		t.Fatalf("回声不一致：收到 %d 字节，期望 %d", len(got), len(payload))
	}
}
