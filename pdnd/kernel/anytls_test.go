package kernel

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/util"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/uot"
)

func TestAnyTLSValidateRequiresConsistentTLSAndPadding(t *testing.T) {
	a := &anyTLSAdapter{}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "anytls", Port: 443, Raw: map[string]any{"tls": true}}}
	if err := a.Validate(spec); err == nil {
		t.Fatal("AnyTLS TLS without certificate accepted")
	}
	spec.Config.Raw = map[string]any{"cert_path": "cert", "tls": false}
	if err := a.Validate(spec); err == nil {
		t.Fatal("one-sided AnyTLS certificate accepted")
	}
	spec.Config.Raw = map[string]any{"padding_scheme": []any{""}}
	if err := a.Validate(spec); err == nil {
		t.Fatal("empty AnyTLS padding line accepted")
	}
}

func TestAnyTLSNativeClientTCPAndUOTUDP(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "anytls", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	adapter, err := newAnyTLSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 88, UUID: "anytls-secret"}}); err != nil {
		t.Fatal(err)
	}
	dialOut := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	client, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: "anytls-secret", DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tcp, err := client.CreateProxy(ctx, M.ParseSocksaddr("echo.test:80"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tcp.Write([]byte("anytls-tcp")); err != nil {
		t.Fatal(err)
	}
	_ = tcp.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len("anytls-tcp"))
	if _, err := io.ReadFull(tcp, got); err != nil || string(got) != "anytls-tcp" {
		t.Fatalf("TCP echo=%q err=%v", got, err)
	}
	_ = tcp.Close()

	proxy, err := client.CreateProxy(ctx, M.Socksaddr{Fqdn: uot.MagicAddress})
	if err != nil {
		t.Fatal(err)
	}
	uotConn, err := (&uot.Client{Version: uot.Version}).DialConn(proxy, false, M.ParseSocksaddr("127.0.0.1:53"))
	if err != nil {
		t.Fatal(err)
	}
	defer uotConn.Close()
	if _, err := uotConn.WriteTo([]byte("anytls-uot"), M.ParseSocksaddr("127.0.0.1:53")); err != nil {
		t.Fatal(err)
	}
	_ = uotConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got = make([]byte, 64)
	n, _, err := uotConn.ReadFrom(got)
	if err != nil || string(got[:n]) != "anytls-uot" {
		t.Fatalf("UoT UDP echo=%q err=%v", got[:n], err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) == 0 || traffic[0].ID != 88 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}

	wrong, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: "wrong-secret", DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	wrongConn, wrongErr := wrong.CreateProxy(ctx, M.ParseSocksaddr("echo.test:80"))
	if wrongErr == nil {
		_ = wrongConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = wrongConn.Write([]byte("force-auth"))
		probe := make([]byte, 1)
		_, wrongErr = wrongConn.Read(probe)
		_ = wrongConn.Close()
	}
	_ = wrong.Close()
	if wrongErr == nil {
		t.Fatal("wrong AnyTLS password unexpectedly authenticated")
	}
}
