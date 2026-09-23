package kernel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

func TestNativeSOCKS5ConnectLoopback(t *testing.T) {
	upstream, target := startProxyEcho(t)
	defer upstream.Close()
	adapter, port := startProxyAdapter(t, "socks", target)
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 7, UUID: "proxy-user"}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	if got, err := reader.ReadByte(); err != nil || got != 5 {
		t.Fatalf("socks version=%d err=%v", got, err)
	}
	if got, err := reader.ReadByte(); err != nil || got != 2 {
		t.Fatalf("socks method=%d err=%v", got, err)
	}
	if _, err := conn.Write([]byte{1, 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("proxy-user")); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{10}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("proxy-user")); err != nil {
		t.Fatal(err)
	}
	var auth [2]byte
	if _, err := io.ReadFull(reader, auth[:]); err != nil || !bytes.Equal(auth[:], []byte{1, 0}) {
		t.Fatalf("socks auth=%v err=%v", auth, err)
	}
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0x01, 0xbb}
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 10)
	if _, err := io.ReadFull(reader, response); err != nil || response[1] != 0 {
		t.Fatalf("socks response=%v err=%v", response, err)
	}
	payload := []byte("pandora-socks-native")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo=%q err=%v", got, err)
	}
}

func TestNativeSOCKS4ConnectLoopback(t *testing.T) {
	upstream, target := startProxyEcho(t)
	defer upstream.Close()
	adapter, port := startProxyAdapter(t, "socks", target)
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 70, UUID: "socks4-user"}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	targetAddr := target.(*net.TCPAddr)
	request := []byte{4, 1, byte(targetAddr.Port >> 8), byte(targetAddr.Port), targetAddr.IP[0], targetAddr.IP[1], targetAddr.IP[2], targetAddr.IP[3]}
	request = append(request, []byte("socks4-user")...)
	request = append(request, 0)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 90 {
		t.Fatalf("socks4 reply=%v err=%v", reply, err)
	}
	payload := []byte("pandora-socks4-native")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo=%q err=%v", got, err)
	}
}

func TestNativeSOCKS4aConnectLoopback(t *testing.T) {
	upstream, target := startProxyEcho(t)
	defer upstream.Close()
	adapter, port := startProxyAdapter(t, "socks", target)
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 71, UUID: "socks4a-user"}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	targetAddr := target.(*net.TCPAddr)
	request := []byte{4, 1, byte(targetAddr.Port >> 8), byte(targetAddr.Port), 0, 0, 0, 1}
	request = append(request, []byte("socks4a-user")...)
	request = append(request, 0)
	request = append(request, []byte("example.invalid")...)
	request = append(request, 0)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 90 {
		t.Fatalf("socks4a reply=%v err=%v", reply, err)
	}
	payload := []byte("pandora-socks4a-native")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo=%q err=%v", got, err)
	}
}

func TestNativeHTTPConnectLoopback(t *testing.T) {
	upstream, target := startProxyEcho(t)
	defer upstream.Close()
	adapter, port := startProxyAdapter(t, "http", target)
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 8, UUID: "http-user"}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	auth := base64.StdEncoding.EncodeToString([]byte("http-user:http-user"))
	request := fmt.Sprintf("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%s", response.Status)
	}
	payload := []byte("pandora-http-native")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo=%q err=%v", got, err)
	}
}

func TestNativeHTTPForwardGETLoopback(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/native-get" {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "pandora-http-get")
	}))
	defer origin.Close()
	originURL, err := url.ParseRequestURI(origin.URL + "/native-get")
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.ResolveTCPAddr("tcp", originURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	// The test plane dials the target listener, so use a dedicated HTTP origin
	// plane that points at the httptest server below.
	adapter, port := startProxyAdapter(t, "http", target)
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 10, UUID: "http-get-user"}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("http-get-user:http-get-user"))
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\nConnection: close\r\n\r\n", origin.URL+"/native-get", originURL.Host, auth)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "pandora-http-get" {
		t.Fatalf("status=%d body=%q err=%v", response.StatusCode, body, err)
	}
}

type socksUDPTestPlane struct{}

func (socksUDPTestPlane) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	return nil, io.ErrUnexpectedEOF
}

func (socksUDPTestPlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp4", "127.0.0.1:0")
}

func TestNativeSOCKS5UDPAssociateLoopback(t *testing.T) {
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, readErr := echo.ReadFrom(buf)
			if readErr != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()
	port := reserveTCPPort(t)
	adapterValue, err := newProxyAdapter("socks", InboundSpec{Config: core.InboundConfig{Protocol: "socks", Listen: "127.0.0.1", Port: port}})
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*proxyAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, InboundSpec{Config: core.InboundConfig{Protocol: "socks", Listen: "127.0.0.1", Port: port}}, AdapterHooks{DataPlane: socksUDPTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 9, UUID: "socks-udp-user"}}); err != nil {
		t.Fatal(err)
	}
	control, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(control)
	_, _ = control.Write([]byte{5, 1, 2})
	method := make([]byte, 2)
	if _, err := io.ReadFull(reader, method); err != nil || !bytes.Equal(method, []byte{5, 2}) {
		t.Fatalf("method=%v err=%v", method, err)
	}
	_, _ = control.Write([]byte{1, 14})
	_, _ = control.Write([]byte("socks-udp-user"))
	_, _ = control.Write([]byte{14})
	_, _ = control.Write([]byte("socks-udp-user"))
	auth := make([]byte, 2)
	if _, err := io.ReadFull(reader, auth); err != nil || !bytes.Equal(auth, []byte{1, 0}) {
		t.Fatalf("auth=%v err=%v", auth, err)
	}
	_, _ = control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	reply := make([]byte, 10)
	if _, err := io.ReadFull(reader, reply); err != nil || reply[1] != 0 {
		t.Fatalf("associate reply=%v err=%v", reply, err)
	}
	assocPort := int(reply[8])<<8 | int(reply[9])
	udpClient, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	echoAddr := echo.LocalAddr().(*net.UDPAddr)
	echoIP := echoAddr.IP.To4()
	packet := []byte{0, 0, 0, 1, echoIP[0], echoIP[1], echoIP[2], echoIP[3], byte(echoAddr.Port >> 8), byte(echoAddr.Port), 'u', 'd', 'p'}
	if _, err := udpClient.WriteTo(packet, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: assocPort}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	_ = udpClient.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := udpClient.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 || !bytes.HasSuffix(buf[:n], []byte("udp")) {
		t.Fatalf("udp response=%x", buf[:n])
	}
}

func TestNativeProxyRejectsUnsupportedTransport(t *testing.T) {
	for _, protocol := range []string{"http"} {
		adapter, err := newProxyAdapter(protocol, InboundSpec{Config: core.InboundConfig{Protocol: protocol, Port: 1080, Raw: map[string]any{"network": "udp"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Validate(InboundSpec{Config: core.InboundConfig{Protocol: protocol, Port: 1080, Raw: map[string]any{"network": "udp"}}}); err == nil {
			t.Fatalf("%s accepted udp transport", protocol)
		}
	}
	adapter, err := newProxyAdapter("socks", InboundSpec{Config: core.InboundConfig{Protocol: "socks", Port: 1080, Raw: map[string]any{"network": "quic"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(InboundSpec{Config: core.InboundConfig{Protocol: "socks", Port: 1080, Raw: map[string]any{"network": "quic"}}}); err == nil {
		t.Fatal("socks accepted quic transport")
	}
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func startProxyEcho(t *testing.T) (net.Listener, net.Addr) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	return listener, listener.Addr()
}

func startProxyAdapter(t *testing.T, protocol string, target net.Addr) (*proxyAdapter, int) {
	t.Helper()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	adapter, err := newProxyAdapter(protocol, InboundSpec{Config: core.InboundConfig{Protocol: protocol, Listen: "127.0.0.1", Port: port}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, InboundSpec{Config: core.InboundConfig{Protocol: protocol, Listen: "127.0.0.1", Port: port}}, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(target)}}); err != nil {
		t.Fatal(err)
	}
	return adapter.(*proxyAdapter), port
}
