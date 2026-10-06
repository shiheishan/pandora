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
	nativeShadowTLS "github.com/aegispanel/nodeagent/internal/nativewire/shadowtls"
	"github.com/aegispanel/nodeagent/route"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 正常握手要通，且通过认证之后静置超过握手超时再发数据也不能被掐断：
// 客户端握手完成到第一次写之间本来就可能空闲，v3 等首个 HMAC 帧这段不限时。
func TestNativeShadowTLSV3Loopback(t *testing.T) {
	t.Run("immediate", func(t *testing.T) { runShadowTLSV3Loopback(t, 0) })
	t.Run("idle past handshake timeout", func(t *testing.T) { runShadowTLSV3Loopback(t, 4*testShadowTLSHandshakeTimeout) })
}

const testShadowTLSHandshakeTimeout = 150 * time.Millisecond

func runShadowTLSV3Loopback(t *testing.T, idle time.Duration) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	decoy, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer decoy.Close()
	go func() {
		conn, acceptErr := decoy.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		if tlsConn, ok := conn.(*tls.Conn); ok {
			_ = tlsConn.Handshake()
		}
		time.Sleep(2 * time.Second)
	}()

	upstream, target := startProxyEcho(t)
	defer upstream.Close()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	decoyAddr := decoy.Addr().String()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowtls", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"password": "outer-password", "server": decoyAddr, "strict": false}}}
	adapterValue, err := newShadowTLSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*shadowTLSAdapter)
	adapter.handshakeTimeout = testShadowTLSHandshakeTimeout
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 19, UUID: "inner-password"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: shadowTestPlane{decoy: M.ParseSocksaddr(decoyAddr), target: socksAddr(target)}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	client, err := nativeShadowTLS.NewClient(nativeShadowTLS.ClientConfig{Version: 3, Password: "outer-password", Server: M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", port)), Dialer: N.SystemDialer, StrictMode: false, TLSHandshake: nativeShadowTLS.DefaultTLSHandshakeFunc("outer-password", &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}), Logger: logger.NOP()}) //nolint:gosec -- ephemeral loopback certificate.
	if err != nil {
		t.Fatal(err)
	}
	outer, err := client.DialContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	innerMethod, err := shadowaead.New("aes-256-gcm", nil, "inner-password")
	if err != nil {
		t.Fatal(err)
	}
	inner := innerMethod.DialEarlyConn(outer, M.ParseSocksaddr(target.String()))
	defer inner.Close()
	time.Sleep(idle)
	payload := []byte("pandora-shadowtls-native")
	if _, err := inner.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(inner, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q", got)
	}
}

// 生产口径：适配器交给 nativewire 的握手超时就是 inboundHandshakeTimeout。
func TestShadowTLSAdapterUsesInboundHandshakeTimeout(t *testing.T) {
	adapter, err := newShadowTLSAdapter(InboundSpec{Config: core.InboundConfig{Protocol: "shadowtls"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := adapter.(*shadowTLSAdapter).handshakeTimeout; got != inboundHandshakeTimeout {
		t.Fatalf("handshakeTimeout = %v，期望 inboundHandshakeTimeout %v", got, inboundHandshakeTimeout)
	}
}

// 只建 TCP 不发 ClientHello 的连接按握手超时关掉，并按 tls-handshake /
// timeout 上报，而不是永远占着 goroutine 和 fd。
func TestShadowTLSSilentConnClosedAndReported(t *testing.T) {
	port := reserveTCPPort(t)
	decoy := "192.0.2.1:443" // 文档地址：静默连接走不到拨诱饵那一步。
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowtls", Tag: "stls-test", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"password": "outer-password", "server": decoy}}}
	adapterValue, err := newShadowTLSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*shadowTLSAdapter)
	adapter.handshakeTimeout = testShadowTLSHandshakeTimeout
	reported := make(chan ConnError, 4)
	hooks := AdapterHooks{
		DataPlane:   shadowTestPlane{decoy: M.ParseSocksaddr(decoy)},
		OnConnError: func(e ConnError) { reported <- e },
	}
	if err := adapter.Start(context.Background(), spec, hooks); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	silent, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	_ = silent.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := silent.Read(make([]byte, 1)); err == nil {
		t.Fatal("静默连接读到了数据")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("静默连接 3 秒后仍未被关掉：ShadowTLS 握手没有限时")
	}
	select {
	case e := <-reported:
		if e.Stage != StageTLSHandshake || e.Tag != "stls-test" || e.Protocol != "shadowtls" {
			t.Fatalf("上报 = %+v，期望 shadowtls / tls-handshake", e)
		}
		if category := classifyConnError(e.Err); category != connErrTimeout {
			t.Fatalf("分类 = %q（%v），期望 %q", category, e.Err, connErrTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("握手超时没有上报")
	}
}

type shadowTestPlane struct{ decoy, target M.Socksaddr }

func (p shadowTestPlane) DialTCP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.Conn, error) {
	if meta.Protocol == "shadowtls-handshake" {
		return N.SystemDialer.DialContext(ctx, "tcp", p.decoy)
	}
	return N.SystemDialer.DialContext(ctx, "tcp", p.target)
}
func (shadowTestPlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("not used")
}
