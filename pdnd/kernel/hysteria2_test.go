package kernel

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/route"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	M "github.com/sagernet/sing/common/metadata"
)

func TestHysteria2ValidateRequiresNativeSecurityInputs(t *testing.T) {
	a := &hysteria2Adapter{}
	base := InboundSpec{Config: core.InboundConfig{Protocol: "hysteria2", Port: 443, Raw: map[string]any{}}}
	if err := a.Validate(base); err == nil {
		t.Fatal("missing certificate accepted")
	}
	base.Config.Raw = map[string]any{"cert_path": "cert", "key_path": "key", "network": "tcp"}
	if err := a.Validate(base); err == nil {
		t.Fatal("TCP transport accepted")
	}
	base.Config.Raw["network"] = "udp"
	base.Config.Raw["obfs"] = map[string]any{"type": "http", "password": "secret"}
	if err := a.Validate(base); err == nil {
		t.Fatal("unsupported obfs accepted")
	}
	base.Config.Raw["obfs"] = map[string]any{"type": "salamander"}
	if err := a.Validate(base); err == nil {
		t.Fatal("empty salamander password accepted")
	}
}

type hysteriaEchoPlane struct{}

func (hysteriaEchoPlane) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		data := make([]byte, 32<<10)
		for {
			n, err := server.Read(data)
			if err != nil {
				return
			}
			if _, err := server.Write(data[:n]); err != nil {
				return
			}
		}
	}()
	return client, nil
}

func (p *hysteriaEchoPlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	data, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	go func() {
		payload := make([]byte, 64<<10)
		for {
			n, addr, err := server.ReadFrom(payload)
			if err != nil {
				return
			}
			_, _ = server.WriteTo(payload[:n], addr)
		}
	}()
	return hysteriaEchoPacketConn{PacketConn: data, target: server.LocalAddr(), server: server}, nil
}

type hysteriaEchoPacketConn struct {
	net.PacketConn
	target net.Addr
	server net.PacketConn
}

func (c hysteriaEchoPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.PacketConn.WriteTo(p, c.target)
}

func (c hysteriaEchoPacketConn) Close() error {
	_ = c.server.Close()
	return c.PacketConn.Close()
}

func TestHysteria2NativeClientTCPUDPAndAuth(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()

	spec := InboundSpec{Config: core.InboundConfig{Protocol: "hysteria2", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapter, err := newHysteria2Adapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	plane := &hysteriaEchoPlane{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 42, UUID: "native-secret"}}); err != nil {
		t.Fatal(err)
	}

	direct := outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil)
	clientTLS := &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost"}}
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: direct, ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
		Password: "native-secret", TLSConfig: clientTLS,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)

	tcp, err := client.DialConn(ctx, M.ParseSocksaddr("echo.test:80"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tcp.Write([]byte("hysteria2-tcp")); err != nil {
		t.Fatal(err)
	}
	_ = tcp.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len("hysteria2-tcp"))
	if _, err := io.ReadFull(tcp, got); err != nil || string(got) != "hysteria2-tcp" {
		t.Fatalf("TCP echo=%q err=%v", got, err)
	}
	_ = tcp.Close()

	udp, err := client.ListenPacket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	destination := M.ParseSocksaddr("127.0.0.1:53")
	if _, err := udp.WriteTo([]byte("hysteria2-udp"), destination); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	got = make([]byte, 64)
	n, _, err := udp.ReadFrom(got)
	if err != nil || string(got[:n]) != "hysteria2-udp" {
		t.Fatalf("UDP echo=%q err=%v", got[:n], err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) == 0 || traffic[0].ID != 42 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}

	wrong, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: direct, ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
		Password: "wrong-secret", TLSConfig: &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongCtx, wrongCancel := context.WithTimeout(ctx, 2*time.Second)
	_, wrongErr := wrong.DialConn(wrongCtx, M.ParseSocksaddr("echo.test:80"))
	wrongCancel()
	_ = wrong.CloseWithError(nil)
	if wrongErr == nil {
		t.Fatal("wrong Hysteria2 password unexpectedly authenticated")
	}
}

func TestHysteria2AddUsersRejectsBatchAtomically(t *testing.T) {
	a := &hysteria2Adapter{
		users: make(map[string]int),
		traffic: make(map[int64]core.UserTraffic),
		online: make(map[int64]map[string]struct{}),
		active: make(map[net.Conn]struct{}),
	}
	if err := a.AddUsers([]core.User{{ID: 1, UUID: "ok"}, {ID: 2}}); err == nil {
		t.Fatal("invalid batch unexpectedly accepted")
	}
	if len(a.users) != 0 || len(a.slots) != 0 {
		t.Fatalf("invalid batch partially published: users=%v slots=%d", a.users, len(a.slots))
	}
	if err := a.AddUsers([]core.User{{ID: 1, UUID: "ok"}}); err != nil {
		t.Fatalf("valid batch after rejection failed: %v", err)
	}
	if len(a.users) != 1 || len(a.slots) != 1 {
		t.Fatalf("valid batch not published: users=%v slots=%d", a.users, len(a.slots))
	}
}
