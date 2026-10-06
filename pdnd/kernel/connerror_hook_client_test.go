package kernel

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	quic "github.com/apernet/quic-go"
	mieruclient "github.com/enfein/mieru/v3/apis/client"
	mierupb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/gofrs/uuid/v5"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	tuic "github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
	"google.golang.org/protobuf/proto"
)

// 需要真实客户端才走得到失败路径的协议：QUIC 系（Hysteria2、TUIC、Juicity）
// 的认证在 QUIC 握手之后，mieru 的认证在 mux 内部且静默，只能测已认证之后
// 的失败。夹具见 connerror_hook_test.go。

func reserveHookUDPPort(t *testing.T) int {
	t.Helper()
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	return port
}

func quicTestTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{"h3"}} //nolint:gosec -- ephemeral test certificate.
}

// Hysteria2 的口令拒绝由伪装处理器上报：协议本身对错误口令只回 404。
func TestConnErrorHookHysteria2WrongPassword(t *testing.T) {
	port := reserveHookUDPPort(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	_, rec := startHookedAdapter(t, "hysteria2", port, map[string]any{"cert_path": certPath, "key_path": keyPath, "network": "udp"}, refusePlane{}, core.User{ID: 502, UUID: "fictional-hy2-secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
		Password:      "wrong-secret", TLSConfig: &hysteria2TLSConfig{std: quicTestTLS()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)
	if _, err := client.DialConn(ctx, M.ParseSocksaddr("target.test:80")); err == nil {
		t.Fatal("错误口令居然通过了认证")
	}
	ev := rec.wait(t, "hysteria2", StageSession, connErrAuth)
	if maskRemoteAddr(ev.Remote) != "127.0.0.0/24" {
		t.Fatalf("伪装处理器没带上对端地址: %v", ev.Remote)
	}
}

// TUIC 的 token 校验失败只在上游库内部 logger.Error 一次，经日志桥上报。
func TestConnErrorHookTUICWrongPassword(t *testing.T) {
	port := reserveHookUDPPort(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	_, rec := startHookedAdapter(t, "tuic", port, map[string]any{"cert_path": certPath, "key_path": keyPath, "network": "udp"}, refusePlane{}, hookUser)
	parsed, err := uuid.FromString(hookUserUUID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
		TLSConfig:     &hysteria2TLSConfig{std: quicTestTLS()},
		UUID:          [16]byte(parsed), Password: "wrong-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)
	if conn, err := client.DialConn(ctx, M.ParseSocksaddr("target.test:80")); err == nil {
		_, _ = conn.Write([]byte("force-auth"))
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Read(make([]byte, 1))
		_ = conn.Close()
	}
	rec.wait(t, "tuic", StageSession, connErrAuth)
}

// TUIC 鉴权通过之后拨目标失败，在 NewConnectionEx 回调里上报。
func TestConnErrorHookTUICUpstreamRefusal(t *testing.T) {
	port := reserveHookUDPPort(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	_, rec := startHookedAdapter(t, "tuic", port, map[string]any{"cert_path": certPath, "key_path": keyPath, "network": "udp"}, refusePlane{}, hookUser)
	parsed, err := uuid.FromString(hookUserUUID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
		TLSConfig:     &hysteria2TLSConfig{std: quicTestTLS()},
		UUID:          [16]byte(parsed), Password: hookUserUUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)
	conn, err := client.DialConn(ctx, M.ParseSocksaddr("target.test:80"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("hello"))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Read(make([]byte, 1))
	_ = conn.Close()
	if ev := rec.wait(t, "tuic", StageSession, ""); !errors.Is(ev.Err, errFictionalRefusal) {
		t.Fatalf("拨号失败没有原样上报: %v", ev.Err)
	}
}

func TestConnErrorHookJuicityWrongToken(t *testing.T) {
	port := reserveHookUDPPort(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	_, rec := startHookedAdapter(t, "juicity", port, map[string]any{"cert_path": certPath, "key_path": keyPath, "network": "udp"}, refusePlane{}, hookUser)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := quic.DialAddr(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), quicTestTLS(), &quic.Config{HandshakeIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(0, "test complete")
	parsed, err := uuid.FromString(hookUserUUID)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := client.OpenUniStream()
	if err != nil {
		t.Fatal(err)
	}
	// UUID 对、token 是 32 个零字节：必然对不上导出的密钥材料。
	payload := append([]byte{juicityVersion, juicityAuthenticate}, parsed[:]...)
	payload = append(payload, make([]byte, 32)...)
	if _, err := auth.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = auth.Close()
	rec.wait(t, "juicity", StageSession, connErrAuth)
}

// mieru 认证失败在 mux 内部静默丢弃；能测的是认证之后拨目标失败。
func TestConnErrorHookMieruUpstreamRefusal(t *testing.T) {
	port := reserveTCPPort(t)
	const user = "fictional-mieru-user"
	_, rec := startHookedAdapter(t, "mieru", port, map[string]any{"transport": "tcp"}, refusePlane{}, core.User{ID: 503, UUID: user})
	client := mieruclient.NewClient()
	profile := &mierupb.ClientProfile{ProfileName: proto.String("hook-test"), User: &mierupb.User{Name: proto.String(user), Password: proto.String(user)}, Servers: []*mierupb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*mierupb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: mierupb.TransportProtocol_TCP.Enum()}}}}}
	if err := client.Store(&mieruclient.ClientConfig{Profile: profile}); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if conn, err := client.DialContext(ctx, &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 80}); err == nil {
		_ = conn.Close()
	}
	if ev := rec.wait(t, "mieru", StageSession, ""); !errors.Is(ev.Err, errFictionalRefusal) {
		t.Fatalf("拨号失败没有原样上报: %v", ev.Err)
	}
}
