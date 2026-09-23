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
	"github.com/gofrs/uuid/v5"
	tuic "github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
)

func TestTUICValidateRejectsAmbiguousTransportAndAuth(t *testing.T) {
	a := &tuicAdapter{}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "tuic", Port: 443, Raw: map[string]any{
		"cert_path": "cert", "key_path": "key", "network": "tcp",
	}}}
	if err := a.Validate(spec); err == nil {
		t.Fatal("TUIC TCP transport accepted")
	}
	spec.Config.Raw["network"] = "udp"
	spec.Config.Raw["congestion_control"] = "reno"
	if err := a.Validate(spec); err == nil {
		t.Fatal("unknown TUIC congestion control accepted")
	}
	spec.Config.Raw["congestion_control"] = "cubic"
	spec.Config.Raw["zero_rtt"] = "yes"
	if err := a.Validate(spec); err == nil {
		t.Fatal("non-boolean TUIC zero_rtt accepted")
	}
}

func TestTUICNativeClientTCPUDPAndAuth(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()

	const userID = "5d6f0a52-6ae1-4e8e-91b3-1f269c8f5c7a"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "tuic", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapter, err := newTUICAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 77, UUID: userID}}); err != nil {
		t.Fatal(err)
	}
	parsed, err := uuid.FromString(userID)
	if err != nil {
		t.Fatal(err)
	}
	direct := outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil)
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context: ctx, Dialer: direct, ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
		TLSConfig: &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{"h3"}}},
		UUID:      [16]byte(parsed), Password: userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)

	tcp, err := client.DialConn(ctx, M.ParseSocksaddr("echo.test:80"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tcp.Write([]byte("tuic-tcp")); err != nil {
		t.Fatal(err)
	}
	_ = tcp.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len("tuic-tcp"))
	if _, err := io.ReadFull(tcp, got); err != nil || string(got) != "tuic-tcp" {
		t.Fatalf("TCP echo=%q err=%v", got, err)
	}
	_ = tcp.Close()

	udp, err := client.ListenPacket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if _, err := udp.WriteTo([]byte("tuic-udp"), M.ParseSocksaddr("127.0.0.1:53")); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, _, err := udp.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "tuic-udp" {
		t.Fatalf("UDP echo=%q err=%v", buf[:n], err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) == 0 || traffic[0].ID != 77 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}

	wrong, err := tuic.NewClient(tuic.ClientOptions{
		Context: ctx, Dialer: direct, ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
		TLSConfig: &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{"h3"}}},
		UUID:      [16]byte(parsed), Password: "wrong-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongCtx, wrongCancel := context.WithTimeout(ctx, 2*time.Second)
	wrongConn, wrongErr := wrong.DialConn(wrongCtx, M.ParseSocksaddr("echo.test:80"))
	if wrongErr == nil {
		_ = wrongConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = wrongConn.Write([]byte("force-auth"))
		probe := make([]byte, 1)
		_, wrongErr = wrongConn.Read(probe)
		_ = wrongConn.Close()
	}
	wrongCancel()
	_ = wrong.CloseWithError(nil)
	if wrongErr == nil {
		t.Fatal("wrong TUIC password unexpectedly authenticated")
	}
}
