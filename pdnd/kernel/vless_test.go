package kernel

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	realitytls "github.com/aegispanel/nodeagent/internal/reality"
	realityhttp3 "github.com/aegispanel/nodeagent/internal/realityquic/http3"
	"github.com/aegispanel/nodeagent/route"
	"github.com/apernet/quic-go/http3"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
)

func TestReadVLESSRequestTCPDomain(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	id := uuid.New()
	user := core.User{ID: 9, UUID: id.String()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Write([]byte{vlessVersion})
		_, _ = client.Write(id[:])
		_, _ = client.Write([]byte{0, vlessTCP})
		var port [2]byte
		binary.BigEndian.PutUint16(port[:], 443)
		_, _ = client.Write(port[:])
		_, _ = client.Write([]byte{2, 11})
		_, _ = client.Write([]byte("example.com"))
	}()
	gotUser, gotDestination, err := readVLESSRequest(server, func(value string) (core.User, bool) { return user, value == id.String() })
	<-done
	if err != nil {
		t.Fatalf("readVLESSRequest: %v", err)
	}
	if gotUser.ID != user.ID || gotDestination.Domain != "example.com" || gotDestination.Port != 443 {
		t.Fatalf("decoded user=%+v destination=%+v", gotUser, gotDestination)
	}
}

func TestVLESSXHTTPH3Lifecycle(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	reserved, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.LocalAddr().(*net.UDPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"network": "xhttp-h3", "cert_path": certPath, "key_path": keyPath}}}
	aValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	a := aValue.(*vlessAdapter)
	if err := a.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := a.AddUsers([]core.User{{ID: 901, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	if a.packet == nil || a.h3Server == nil {
		t.Fatal("xhttp-h3 listener was not retained")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVLESSAdapterRealityXrayInterop(t *testing.T) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	targetRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetRaw.Close()
	targetTLS := testXHTTPServerTLSConfig(t)
	targetTLS.NextProtos = []string{"h2", "http/1.1"}
	target := tls.NewListener(targetRaw, targetTLS)
	defer target.Close()
	go func() {
		for {
			conn, acceptErr := target.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"security":     "reality",
		"dest":         targetRaw.Addr().String(),
		"server_names": []string{"example.com"},
		"private_key":  base64.RawURLEncoding.EncodeToString(key.Bytes()),
		"short_ids":    []string{"0102030405060708"},
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 903, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	raw, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := xrayreality.UClient(raw, &xrayreality.Config{Fingerprint: "firefox", ServerName: "example.com", PublicKey: key.PublicKey().Bytes(), ShortId: append([]byte(nil), shortID[:]...)}, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(443)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 0, 64)
	header = append(header, vlessVersion)
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	payload := []byte("native-vless-reality")
	if _, err := client.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, len(payload)+2)
	if _, err := io.ReadFull(client, body); err != nil {
		t.Fatal(err)
	}
	if body[0] != vlessVersion || body[1] != 0 || !bytes.Equal(body[2:], payload) {
		t.Fatalf("reality vless response=%q", body)
	}
	_ = client.Close()
	select {
	case <-upstreamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reality vless upstream did not finish")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		adapter.mu.RLock()
		observed := adapter.traffic[903]
		adapter.mu.RUnlock()
		if observed.Upload > 0 && observed.Download > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reality vless traffic was not fully accounted: %+v", observed)
		}
		time.Sleep(5 * time.Millisecond)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 903 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestVLESSAdapterXHTTPH3Bridge(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.LocalAddr().(*net.UDPAddr).Port
	_ = reserved.Close()

	certPath, keyPath := testXHTTPServerCertFiles(t)
	xhttpConfig, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp"})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := http.NewRequest(http.MethodPost, "https://127.0.0.1/xhttp/session-h3/1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID, seq, err := xhttpConfig.ExtractRequestMeta(preflight); err != nil || sessionID != "session-h3" || seq != "1" {
		t.Fatalf("xhttp metadata preflight: session=%q seq=%q err=%v", sessionID, seq, err)
	}
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network":   "xhttp-h3",
		"path":      "/xhttp",
		"cert_path": certPath,
		"key_path":  keyPath,
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 902, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	transport := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: transport}
	defer transport.Close()
	header := make([]byte, 0, 64)
	header = append(header, vlessVersion)
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	payload := []byte("native-vless-h3")
	resp, err := client.Post("https://"+net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port))+"/xhttp/session-h3/1/", "application/octet-stream", bytes.NewReader(append(header, payload...)))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.ProtoMajor != 3 || resp.StatusCode != http.StatusOK || len(body) != len(payload)+2 || body[0] != vlessVersion || body[1] != 0 || !bytes.Equal(body[2:], payload) {
		t.Fatalf("proto=%s code=%d body=%q", resp.Proto, resp.StatusCode, body)
	}
	select {
	case <-upstreamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream echo did not finish")
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 902 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestVLESSAdapterRealityXHTTPH3Bridge(t *testing.T) {
	targetListener, err := tls.Listen("tcp", "127.0.0.1:0", testXHTTPServerTLSConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	targetHandshake := make(chan error, 1)
	go func() {
		conn, acceptErr := targetListener.Accept()
		if acceptErr != nil {
			targetHandshake <- acceptErr
			return
		}
		defer conn.Close()
		if tlsConn, ok := conn.(*tls.Conn); ok {
			targetHandshake <- tlsConn.Handshake()
			return
		}
		targetHandshake <- nil
	}()

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := packet.LocalAddr().(*net.UDPAddr).Port
	_ = packet.Close()
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var shortID [8]byte
	copy(shortID[:], []byte("pandora1"))
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network":      "xhttp-h3",
		"security":     "reality",
		"path":         "/xhttp",
		"dest":         targetListener.Addr().String(),
		"server_names": []string{"localhost"},
		"private_key":  base64.RawURLEncoding.EncodeToString(serverKey.Bytes()),
		"short_ids":    []string{fmt.Sprintf("%x", shortID[:])},
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 907, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	transport := &realityhttp3.Transport{TLSClientConfig: &realitytls.Config{
		ServerName:         "localhost",
		PublicKey:          serverKey.PublicKey().Bytes(),
		ShortId:            shortID[:],
		InsecureSkipVerify: true,
	}}
	defer transport.Close()
	client := &http.Client{Transport: transport}
	header := make([]byte, 0, 64)
	header = append(header, vlessVersion)
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	payload := []byte("native-vless-reality-h3")
	resp, err := client.Post("https://"+packetAddr(adapter)+"/xhttp/reality-h3-session/1/", "application/octet-stream", bytes.NewReader(append(header, payload...)))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.ProtoMajor != 3 || resp.StatusCode != http.StatusOK || len(body) != len(payload)+2 || body[0] != vlessVersion || body[1] != 0 || !bytes.Equal(body[2:], payload) {
		t.Fatalf("proto=%s code=%d body=%q", resp.Proto, resp.StatusCode, body)
	}
	select {
	case <-targetHandshake:
		// The REALITY handoff intentionally consumes the target server flight
		// without completing the target-side TLS session; EOF here is expected.
	case <-time.After(2 * time.Second):
		t.Fatal("reality target handshake did not finish")
	}
	select {
	case <-upstreamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reality vless h3 upstream echo did not finish")
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 907 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func packetAddr(adapter *vlessAdapter) string {
	adapter.mu.RLock()
	defer adapter.mu.RUnlock()
	return adapter.packet.LocalAddr().String()
}

func TestVLESSAdapterXHTTPH3PacketReconnectBridge(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.LocalAddr().(*net.UDPAddr).Port
	_ = reserved.Close()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp-h3", "path": "/xhttp", "mode": "packet-up", "cert_path": certPath, "key_path": keyPath,
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 906, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	transport := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec -- test certificate is ephemeral.
	defer transport.Close()
	client := &http.Client{Transport: transport}
	payload := []byte("native-vless-xhttp-h3-packet")
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0, 0xbb, 1, 127, 0, 0, 1)
	postURL := fmt.Sprintf("https://127.0.0.1:%d/xhttp/h3-reconnect-session/0/", port)
	resp, err := client.Post(postURL, "application/octet-stream", bytes.NewReader(append(header, payload...)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("h3 packet upload status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	getURL := fmt.Sprintf("https://127.0.0.1:%d/xhttp/h3-reconnect-session/", port)
	resp, err = client.Get(getURL)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, len(payload)+2)
	if _, err := io.ReadFull(resp.Body, body); err != nil {
		_ = resp.Body.Close()
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || body[0] != vlessVersion || body[1] != 0 || !bytes.Equal(body[2:], payload) {
		t.Fatalf("h3 packet response status=%d body=%q", resp.StatusCode, body)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	var traffic []core.UserTraffic
	deadline := time.Now().Add(2 * time.Second)
	for {
		traffic, err = adapter.SnapshotTraffic()
		if err != nil {
			t.Fatal(err)
		}
		if len(traffic) == 1 && traffic[0].ID == 906 && traffic[0].Upload > 0 && traffic[0].Download > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("h3 packet traffic=%+v", traffic)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVLESSAdapterRejectsUnsupportedTransport(t *testing.T) {
	a := &vlessAdapter{}
	err := a.Validate(InboundSpec{Config: core.InboundConfig{Protocol: "vless", Port: 443, Raw: map[string]any{"network": "quic"}}})
	if err == nil {
		t.Fatal("expected unsupported transport error")
	}
}

func TestVLESSAdapterAcceptsRealityOverXHTTPH3(t *testing.T) {
	a := &vlessAdapter{}
	err := a.Validate(InboundSpec{Config: core.InboundConfig{Protocol: "vless", Port: 443, Raw: map[string]any{
		"network":      "xhttp-h3",
		"security":     "reality",
		"path":         "/xhttp",
		"dest":         "127.0.0.1:443",
		"server_names": []any{"example.com"},
		"private_key":  base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		"short_ids":    []any{"70616e646f726131"},
	}}})
	if err != nil {
		t.Fatalf("expected native xhttp-h3 + reality acceptance: %v", err)
	}
}

func TestNativeCoreDefaultRegistryIncludesVLESS(t *testing.T) {
	registry := NewDefaultAdapterRegistry()
	if _, err := registry.New(InboundSpec{Config: core.InboundConfig{Protocol: "vless", Port: 443}}); err != nil {
		t.Fatalf("registry.New(vless): %v", err)
	}
	types := registry.Types()
	seenVLESS, seenTrojan, seenShadowsocks := false, false, false
	for _, protocol := range types {
		seenVLESS = seenVLESS || protocol == "vless"
		seenTrojan = seenTrojan || protocol == "trojan"
		seenShadowsocks = seenShadowsocks || protocol == "shadowsocks"
	}
	if !seenVLESS || !seenTrojan || !seenShadowsocks {
		t.Fatalf("native registry types=%v", types)
	}
}

// vlessTestPlane 永远连到构造时写死的 target，但会记下调用方传进来的目标地址，
// 好让回环测试断言协议实现从请求头里解析出的目标是对的。见 testPlaneRecorder。
type vlessTestPlane struct {
	testPlaneRecorder
	target M.Socksaddr
}

func (p *vlessTestPlane) DialTCP(ctx context.Context, _ route.Meta, destination M.Socksaddr) (net.Conn, error) {
	p.recordDial(destination)
	return (&net.Dialer{}).DialContext(ctx, "tcp", p.target.String())
}
func (p *vlessTestPlane) ListenUDP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	p.recordListen(destination)
	return nil, io.ErrUnexpectedEOF
}

func TestVLESSAdapterLoopbackTCPAndTraffic(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()

	adapter := &vlessAdapter{users: make(map[string]core.User), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port}}
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := adapter.AddUsers([]core.User{{ID: 42, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	header := make([]byte, 0, 64)
	header = append(header, vlessVersion)
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatalf("response: %v", err)
	}
	if response[0] != vlessVersion || response[1] != 0 {
		t.Fatalf("response=%v", response)
	}
	if _, err := client.Write([]byte("native-vless")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("native-vless"))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("echo: %v", err)
	}
	if string(got) != "native-vless" {
		t.Fatalf("echo=%q", got)
	}
	// 请求头里写的是 127.0.0.1:443，解析出别的地址说明头部解析错位了。
	assertDialedTarget(t, plane, "127.0.0.1:443")
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 42 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestVLESSAdapterXHTTPH1Bridge(t *testing.T) {
	adapter := &vlessAdapter{users: make(map[string]core.User), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{})}
	id := uuid.New()
	if err := adapter.AddUsers([]core.User{{ID: 77, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	config, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp"})
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	adapter.plane = &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}
	server := XHTTPServer{Config: config, Handler: func(ctx context.Context, session XHTTPSession) error {
		conn := newXHTTPDuplexConn(ctx, session.Body, session.Writer)
		return adapter.handleConn(ctx, conn)
	}}
	header := make([]byte, 0, 64)
	header = append(header, vlessVersion)
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	req := httptest.NewRequest(http.MethodPost, "https://edge.example/xhttp/session/1/", bytes.NewReader(append(header, []byte("xhttp-vless")...)))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	body := recorder.Body.Bytes()
	if recorder.Code != http.StatusOK || len(body) < 2 || body[0] != vlessVersion || body[1] != 0 || string(body[2:]) != "xhttp-vless" {
		t.Fatalf("code=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestVLESSAdapterXHTTPPacketReconnectBridge(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp", "path": "/xhttp", "mode": "packet-up",
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 905, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	payload := []byte("native-vless-xhttp-packet")
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0, 0xbb, 1, 127, 0, 0, 1)
	postURL := fmt.Sprintf("http://127.0.0.1:%d/xhttp/reconnect-session/0/", port)
	resp, err := client.Post(postURL, "application/octet-stream", bytes.NewReader(append(header, payload...)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("packet upload status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	getURL := fmt.Sprintf("http://127.0.0.1:%d/xhttp/reconnect-session/", port)
	resp, err = client.Get(getURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, len(payload)+2)
	if _, err := io.ReadFull(resp.Body, body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body[0] != vlessVersion || body[1] != 0 || !bytes.Equal(body[2:], payload) {
		t.Fatalf("packet response status=%d body=%q", resp.StatusCode, body)
	}
	_ = adapter.Close()
}
