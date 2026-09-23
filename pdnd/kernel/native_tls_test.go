package kernel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	M "github.com/sagernet/sing/common/metadata"
)

func TestVLESSNativeTLSLoopback(t *testing.T) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "tcp", "tls": true, "cert_path": certPath, "key_path": keyPath,
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 991, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec -- test certificate is ephemeral.
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	payload := []byte("native-vless-tls")
	if _, err := client.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, len(payload)+2)
	if _, err := io.ReadFull(client, body); err != nil {
		t.Fatal(err)
	}
	if body[0] != vlessVersion || body[1] != 0 || !bytes.Equal(body[2:], payload) {
		t.Fatalf("TLS vless response=%q", body)
	}
}

func TestTrojanNativeTLSLoopback(t *testing.T) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"tls": true, "cert_path": certPath, "key_path": keyPath,
	}}}
	adapterValue, err := newTrojanAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*trojanAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 992, UUID: "native-trojan-tls"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec -- test certificate is ephemeral.
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	header := []byte(trojanPasswordProof("native-trojan-tls") + "\r\n")
	header = append(header, 1, 1, 127, 0, 0, 1, 0, 1, '\r', '\n')
	payload := []byte("native-trojan-tls-payload")
	if _, err := client.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("TLS trojan response=%q", got)
	}
}

func TestVLESSNativeWebSocketLoopback(t *testing.T) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "ws", "ws_path": "/pandora-vless",
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 993, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	wsConn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d/pandora-vless", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsConn.Close()
	_ = wsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	payload := []byte("native-vless-websocket")
	if err := wsConn.WriteMessage(websocket.BinaryMessage, append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	var response []byte
	for len(response) < len(payload)+2 {
		messageType, part, readErr := wsConn.ReadMessage()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if messageType != websocket.BinaryMessage {
			t.Fatalf("unexpected websocket message type %d", messageType)
		}
		response = append(response, part...)
	}
	if response[0] != vlessVersion || response[1] != 0 || !bytes.Equal(response[2:], payload) {
		t.Fatalf("websocket vless response=%q", response)
	}
}

func TestTrojanNativeWebSocketLoopback(t *testing.T) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "ws", "ws_path": "/pandora-trojan",
	}}}
	adapterValue, err := newTrojanAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*trojanAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	const password = "native-trojan-websocket"
	if err := adapter.AddUsers([]core.User{{ID: 994, UUID: password}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	wsConn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d/pandora-trojan", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsConn.Close()
	_ = wsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	header := []byte(trojanPasswordProof(password) + "\r\n")
	header = append(header, 1, 1, 127, 0, 0, 1, 0, 1, '\r', '\n')
	payload := []byte("native-trojan-websocket")
	if err := wsConn.WriteMessage(websocket.BinaryMessage, append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	messageType, response, err := wsConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.BinaryMessage || !bytes.Equal(response, payload) {
		t.Fatalf("websocket trojan response type=%d payload=%q", messageType, response)
	}
}

func readNativeHTTPUpgradeResponse(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil || !bytes.Contains([]byte(line), []byte("101")) {
		t.Fatalf("httpupgrade status=%q err=%v", line, err)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			return
		}
	}
}

func TestVLESSNativeHTTPUpgradeLoopback(t *testing.T) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "httpupgrade", "path": "/pandora-upgrade",
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 995, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(client)
	if _, err := io.WriteString(client, "GET /pandora-upgrade HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	readNativeHTTPUpgradeResponse(t, reader)
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	payload := []byte("native-vless-httpupgrade")
	if _, err := client.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload)+2)
	if _, err := io.ReadFull(reader, response); err != nil {
		t.Fatal(err)
	}
	if response[0] != vlessVersion || response[1] != 0 || !bytes.Equal(response[2:], payload) {
		t.Fatalf("httpupgrade vless response=%q", response)
	}
}

func TestTrojanNativeHTTPUpgradeLoopback(t *testing.T) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	const password = "native-trojan-httpupgrade"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "httpupgrade", "path": "/pandora-upgrade",
	}}}
	adapterValue, err := newTrojanAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*trojanAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 996, UUID: password}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(client)
	if _, err := io.WriteString(client, "GET /pandora-upgrade HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	readNativeHTTPUpgradeResponse(t, reader)
	header := []byte(trojanPasswordProof(password) + "\r\n")
	header = append(header, 1, 1, 127, 0, 0, 1, 0, 1, '\r', '\n')
	payload := []byte("native-trojan-httpupgrade")
	if _, err := client.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response, payload) {
		t.Fatalf("httpupgrade trojan response=%q", response)
	}
}

func socksAddr(addr net.Addr) (out M.Socksaddr) {
	return M.SocksaddrFromNet(addr).Unwrap()
}
