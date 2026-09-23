//go:build interop

package kernel

// This opt-in test starts a real Xray client instance as a separate protocol
// implementation. It is intentionally excluded from the normal NativeCore
// test suite: the production binary must not gain an Xray dependency merely
// because an external interoperability gate exists.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	xraycore "github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

func TestExternalXrayVLESSXHTTPH3Interop(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
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

	certPath, keyPath := testXHTTPServerCertFiles(t)
	serverPort := reserveUDPPort(t)
	clientPort := reserveTCPPort(t)
	u := uuid.New()
	tag := "external-xray-xhttp-h3"

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: tag, Protocol: "vless", Listen: "127.0.0.1", Port: serverPort,
		Raw: map[string]any{"network": "xhttp-h3", "security": "none", "path": "/xhttp", "mode": "stream-one", "cert_path": certPath, "key_path": keyPath},
	}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.AddUsers([]core.User{{ID: 7001, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("decode test certificate")
	}
	certHash := sha256.Sum256(certBlock.Bytes)

	logDir := t.TempDir()
	accessLog := filepath.Join(logDir, "xray-access.log")
	errorLog := filepath.Join(logDir, "xray-error.log")
	config := map[string]any{
		"log": map[string]any{"loglevel": "debug", "access": accessLog, "error": errorLog},
		"inbounds": []any{map[string]any{
			"listen":   "127.0.0.1",
			"port":     clientPort,
			"protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{
				"vnext": []any{map[string]any{
					"address": "127.0.0.1",
					"port":    serverPort,
					"users": []any{map[string]any{
						"id":         u.String(),
						"encryption": "none",
					}},
				}},
			},
			"streamSettings": map[string]any{
				"network":  "xhttp",
				"security": "tls",
				"tlsSettings": map[string]any{
					"serverName":           "localhost",
					"pinnedPeerCertSha256": hex.EncodeToString(certHash[:]),
					"alpn":                 []string{"h3"},
				},
				"xhttpSettings": map[string]any{
					"path": "/xhttp",
					"mode": "stream-one",
					"host": "localhost",
				},
			},
		}},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	xray, err := xraycore.StartInstance("json", configBytes)
	if err != nil {
		t.Fatalf("start external xray client: %v", err)
	}
	defer xray.Close()

	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(clientPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect external xray socks: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(methodReply, []byte{5, 0}) {
		t.Fatalf("socks method reply=%v", methodReply)
	}
	destination := upstream.Addr().(*net.TCPAddr)
	connectRequest := []byte{5, 1, 0, 1}
	connectRequest = append(connectRequest, destination.IP.To4()...)
	connectRequest = append(connectRequest, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(connectRequest); err != nil {
		t.Fatal(err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil {
		t.Fatal(err)
	}
	if connectReply[1] != 0 {
		t.Fatalf("socks connect reply=%v", connectReply)
	}
	payload := []byte("external-xray-xhttp-h3")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		traffic, _ := adapter.SnapshotTraffic()
		access, _ := os.ReadFile(accessLog)
		xrayErrors, _ := os.ReadFile(errorLog)
		t.Fatalf("external xray response: %v; native traffic=%+v; xray access=%s; xray error=%s", err, traffic, access, xrayErrors)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("external xray echo=%q", got)
	}
	_ = conn.Close()
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	trafficDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(trafficDeadline) {
		adapter.mu.RLock()
		current := adapter.traffic[7001]
		adapter.mu.RUnlock()
		if current.Upload > 0 && current.Download > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 7001 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("native traffic=%+v err=%v", traffic, err)
	}
}

func TestExternalXrayXHTTPH3Transport(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("decode test certificate")
	}
	certHash := sha256.Sum256(certBlock.Bytes)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	c, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp", "mode": "stream-one"})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan []byte, 1)
	server := XHTTPServer{Config: c, Handler: func(_ context.Context, session XHTTPSession) error {
		buf := make([]byte, 512)
		n, readErr := session.Body.Read(buf)
		if n > 0 {
			seen <- append([]byte(nil), buf[:n]...)
		}
		_, _ = session.Writer.Write([]byte("transport-ok"))
		return readErr
	}}
	h3Server, err := server.ServeH3(packet, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	defer h3Server.Close()
	serverPort := packet.LocalAddr().(*net.UDPAddr).Port
	clientPort := reserveTCPPort(t)
	u := uuid.New()
	config := map[string]any{
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": clientPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": serverPort,
				"users": []any{map[string]any{"id": u.String(), "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "tls",
				"tlsSettings":   map[string]any{"serverName": "localhost", "pinnedPeerCertSha256": hex.EncodeToString(certHash[:]), "alpn": []string{"h3"}},
				"xhttpSettings": map[string]any{"path": "/xhttp", "mode": "stream-one", "host": "localhost"},
			},
		}},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	xray, err := xraycore.StartInstance("json", configBytes)
	if err != nil {
		t.Fatalf("start external xray client: %v", err)
	}
	defer xray.Close()
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(clientPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect external xray socks: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatal(err)
	}
	connectRequest := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}
	if _, err := conn.Write(connectRequest); err != nil {
		t.Fatal(err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("transport-probe")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if len(got) == 0 {
			t.Fatal("external xray sent an empty HTTP/3 body")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external xray request did not reach NativeCore HTTP/3 transport")
	}
}

func TestExternalXrayVLESSXHTTPRealityH2Interop(t *testing.T) {
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
	decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoyRaw.Close()
	decoyTLS := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
	defer decoyTLS.Close()
	go func() {
		for {
			conn, acceptErr := decoyTLS.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := "0908070605040302"
	serverPort := reserveTCPPort(t)
	u := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-xray-reality-h2", Protocol: "vless", Listen: "127.0.0.1", Port: serverPort,
		Raw: map[string]any{
			"network": "xhttp", "security": "reality", "path": "/xhttp", "mode": "stream-one",
			"dest": decoyRaw.Addr().String(), "server_names": []any{"example.com"},
			"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_ids": []any{shortID},
		},
	}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 7002, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	clientPort := reserveTCPPort(t)
	config := map[string]any{
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": clientPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": serverPort,
				"users": []any{map[string]any{"id": u.String(), "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "reality",
				"realitySettings": map[string]any{
					"serverName": "example.com", "fingerprint": "firefox",
					"publicKey": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "shortId": shortID,
				},
				"xhttpSettings": map[string]any{"path": "/xhttp", "mode": "stream-one", "host": "example.com"},
			},
		}},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	xray, err := xraycore.StartInstance("json", configBytes)
	if err != nil {
		t.Fatalf("start external xray REALITY client: %v", err)
	}
	defer xray.Close()
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(clientPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect external xray REALITY socks: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatal(err)
	}
	destination := upstream.Addr().(*net.TCPAddr)
	connectRequest := []byte{5, 1, 0, 1}
	connectRequest = append(connectRequest, destination.IP.To4()...)
	connectRequest = append(connectRequest, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(connectRequest); err != nil {
		t.Fatal(err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil {
		t.Fatal(err)
	}
	if connectReply[1] != 0 {
		t.Fatalf("REALITY SOCKS connect reply=%v", connectReply)
	}
	payload := []byte("external-xray-reality-h2")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("external Xray REALITY H2 response: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("external Xray REALITY H2 echo=%q", got)
	}
	_ = conn.Close()
	trafficDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(trafficDeadline) {
		adapter.mu.RLock()
		current := adapter.traffic[7002]
		adapter.mu.RUnlock()
		if current.Upload > 0 && current.Download > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 7002 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("external REALITY H2 traffic=%+v err=%v", traffic, err)
	}
}

func reserveUDPPort(t *testing.T) int {
	t.Helper()
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	return packet.LocalAddr().(*net.UDPAddr).Port
}
