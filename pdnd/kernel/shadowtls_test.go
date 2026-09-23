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

func TestNativeShadowTLSV3Loopback(t *testing.T) {
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
