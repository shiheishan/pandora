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
	quic "github.com/apernet/quic-go"
	juicyp "github.com/daeuniverse/outbound/protocol"
	"github.com/daeuniverse/outbound/protocol/direct"
	juicityclient "github.com/daeuniverse/outbound/protocol/juicity"
	"github.com/google/uuid"
)

func TestJuicityValidateRequiresTLS(t *testing.T) {
	a := &juicityAdapter{}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "juicity", Port: 443, Raw: map[string]any{}}}
	if err := a.Validate(spec); err == nil {
		t.Fatal("missing Juicity certificate unexpectedly accepted")
	}
	spec.Config.Raw = map[string]any{"cert_path": "cert", "key_path": "key", "congestion_control": "reno"}
	if err := a.Validate(spec); err == nil {
		t.Fatal("unsupported Juicity congestion control unexpectedly accepted")
	}
}

func TestNativeJuicityOfficialClientTCPInterop(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	const userID = "6a4e1c5b-9152-4b1e-8c33-5fb2c2c9c4a0"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "juicity", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapterValue, err := newJuicityAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*juicityAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 92, UUID: userID}}); err != nil {
		t.Fatal(err)
	}
	client, err := juicityclient.NewDialer(direct.FullconeDirect, juicyp.Header{
		ProxyAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Feature1:     "cubic",
		TlsConfig:    &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, ServerName: "localhost"},
		User:         userID, Password: userID, IsClient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.DialContext(ctx, "tcp", "echo.test:80")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("official-juicity-client")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
		t.Fatalf("official client echo=%q err=%v", got, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 92 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}
}

func TestNativeJuicityQUICStreamAndPacketLoopback(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	const userID = "f1d2d2f9-8f3f-4b5d-ae8c-7a4d09be11f4"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "juicity", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapterValue, err := newJuicityAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*juicityAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 91, UUID: userID}}); err != nil {
		t.Fatal(err)
	}

	client, err := quic.DialAddr(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{
		InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, ServerName: "localhost",
	}, &quic.Config{HandshakeIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(0, "test complete")
	id, err := uuid.Parse(userID)
	if err != nil {
		t.Fatal(err)
	}
	state := client.ConnectionState().TLS
	token, err := state.ExportKeyingMaterial(string(id[:]), []byte(userID), 32)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := client.OpenUniStream()
	if err != nil {
		t.Fatal(err)
	}
	authBytes := append([]byte{juicityVersion, juicityAuthenticate}, id[:]...)
	authBytes = append(authBytes, token...)
	if _, err := auth.Write(authBytes); err != nil {
		t.Fatal(err)
	}
	if err := auth.Close(); err != nil {
		t.Fatal(err)
	}

	tcp, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	tcpHeader := append([]byte{juicityNetworkTCP}, marshalJuicityAddress(juicityAddress{typ: 3, Host: "echo.test", Port: 80})...)
	if _, err := tcp.Write(append(tcpHeader, []byte("juicity-tcp")...)); err != nil {
		t.Fatal(err)
	}
	_ = tcp.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len("juicity-tcp"))
	if _, err := io.ReadFull(tcp, got); err != nil || string(got) != "juicity-tcp" {
		t.Fatalf("TCP echo=%q err=%v", got, err)
	}
	_ = tcp.Close()

	udp, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	initial := marshalJuicityAddress(juicityAddress{typ: 1, Host: "127.0.0.1", Port: 53})
	if _, err := udp.Write(append([]byte{juicityNetworkUDP}, initial...)); err != nil {
		t.Fatal(err)
	}
	packet := marshalJuicityPacket(juicityAddress{typ: 1, Host: "127.0.0.1", Port: 53}, []byte("juicity-udp"))
	if _, err := udp.Write(packet); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	responseTarget, response, err := readJuicityPacket(udp)
	if err != nil || string(response) != "juicity-udp" || responseTarget.Port == 0 {
		t.Fatalf("UDP echo target=%+v payload=%q err=%v", responseTarget, response, err)
	}
	_ = udp.Close()

	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) == 0 || traffic[0].ID != 91 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}
}
