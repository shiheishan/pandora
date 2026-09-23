package outbound

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

type testDialer struct {
	dial func(context.Context, string, M.Socksaddr) (net.Conn, error)
}

func (d testDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return d.dial(ctx, network, destination)
}

func (d testDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, ErrUDPNotSupported
}

type trackedConn struct {
	net.Conn
	mu     sync.Mutex
	closed bool
}

func (c *trackedConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *trackedConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func TestHTTPConnectPreservesBufferedTunnelBytesAndAuth(t *testing.T) {
	client, server := net.Pipe()
	requestCh := make(chan *http.Request, 1)
	go func() {
		defer server.Close()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			requestCh <- nil
			return
		}
		requestCh <- req
		_, _ = io.WriteString(server, "HTTP/1.1 200 Connection Established\r\nContent-Length: 0\r\n\r\nREADY")
	}()

	o := &httpOut{
		tag:      "relay",
		server:   M.ParseSocksaddrHostPort("proxy.example", 8080),
		username: "alice",
		password: "secret",
		dialer: testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
			return client, nil
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := o.DialTCP(ctx, M.ParseSocksaddrHostPort("target.example", 443))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	req := <-requestCh
	if req == nil {
		t.Fatal("upstream did not receive a valid CONNECT request")
	}
	if req.Method != http.MethodConnect || req.Host != "target.example:443" {
		t.Fatalf("unexpected CONNECT request: method=%s host=%s", req.Method, req.Host)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	if got := req.Header.Get("Proxy-Authorization"); got != wantAuth {
		t.Fatalf("proxy auth = %q, want %q", got, wantAuth)
	}

	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "READY" {
		t.Fatalf("buffered tunnel bytes = %q", buf)
	}
}

func TestHTTPConnectTLSFailureClosesOwnedTransport(t *testing.T) {
	client, server := net.Pipe()
	tracked := &trackedConn{Conn: client}
	go func() {
		defer server.Close()
		buf := make([]byte, 4096)
		_, _ = server.Read(buf)
		_, _ = server.Write([]byte("not tls"))
	}()

	o := &httpOut{
		tag:    "relay",
		server: M.ParseSocksaddrHostPort("proxy.example", 443),
		tls:    TLSConfig{Enabled: true, ServerName: "proxy.example"},
		dialer: testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
			return tracked, nil
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := o.DialTCP(ctx, M.ParseSocksaddrHostPort("target.example", 443)); err == nil {
		t.Fatal("invalid TLS upstream unexpectedly succeeded")
	}
	if !tracked.isClosed() {
		t.Fatal("transport leaked after TLS handshake failure")
	}
}

func TestHTTPConnectRejectionClosesTransport(t *testing.T) {
	client, server := net.Pipe()
	tracked := &trackedConn{Conn: client}
	go func() {
		defer server.Close()
		_, _ = http.ReadRequest(bufio.NewReader(server))
		_, _ = io.WriteString(server, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
	}()
	o := &httpOut{
		tag:    "relay",
		server: M.ParseSocksaddrHostPort("proxy.example", 8080),
		dialer: testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
			return tracked, nil
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := o.DialTCP(ctx, M.ParseSocksaddrHostPort("target.example", 443)); err == nil {
		t.Fatal("rejected CONNECT unexpectedly succeeded")
	}
	if !tracked.isClosed() {
		t.Fatal("transport leaked after CONNECT rejection")
	}
}

func TestHTTPConnectRejectionDoesNotDrainUntrustedBody(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	dialer := testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return client, nil
	}}
	o, err := New(Options{Tag: "http", Type: "http", Settings: map[string]any{
		"server": "127.0.0.1", "server_port": 8080,
	}, Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		reader := bufio.NewReader(server)
		_, _ = http.ReadRequest(reader)
		_, _ = io.WriteString(server, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 999999999\r\n\r\n")
	}()
	result := make(chan error, 1)
	go func() {
		_, err := o.DialTCP(context.Background(), M.ParseSocksaddrHostPort("example.com", 443))
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("407 response unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP CONNECT rejection tried to drain an untrusted body")
	}
}

func TestHTTPConnectCancellationInterruptsSilentPeer(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	started := make(chan struct{})
	dialer := testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		close(started)
		return client, nil
	}}
	o, err := New(Options{Tag: "http", Type: "http", Settings: map[string]any{
		"server": "127.0.0.1", "server_port": 8080,
	}, Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := o.DialTCP(ctx, M.ParseSocksaddrHostPort("example.com", 443))
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled handshake unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not interrupt HTTP handshake")
	}
}

func TestSOCKS5ConnectHandshake(t *testing.T) {
	client, server := net.Pipe()
	commandCh := make(chan byte, 1)
	go serveSOCKS5(t, server, 0, commandCh, true)

	o := &socksOut{
		tag:    "relay",
		server: M.ParseSocksaddrHostPort("127.0.0.1", 1080),
		dialer: testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
			return client, nil
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := o.DialTCP(ctx, M.ParseSocksaddrHostPort("target.example", 443))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if command := <-commandCh; command != 1 {
		t.Fatalf("SOCKS command = %d, want CONNECT", command)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "READY" {
		t.Fatalf("post-handshake bytes = %q", buf)
	}
}

func TestSOCKS5HandshakeFailureClosesTransport(t *testing.T) {
	client, server := net.Pipe()
	tracked := &trackedConn{Conn: client}
	go func() {
		defer server.Close()
		greeting := make([]byte, 3)
		_, _ = io.ReadFull(server, greeting)
		_, _ = server.Write([]byte{5, 0xff})
	}()
	o := &socksOut{
		tag:    "relay",
		server: M.ParseSocksaddrHostPort("127.0.0.1", 1080),
		dialer: testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
			return tracked, nil
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := o.DialTCP(ctx, M.ParseSocksaddrHostPort("target.example", 443)); err == nil {
		t.Fatal("rejected SOCKS5 handshake unexpectedly succeeded")
	}
	if !tracked.isClosed() {
		t.Fatal("transport leaked after SOCKS5 handshake failure")
	}
}

func TestSOCKS5CancellationInterruptsSilentPeer(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	started := make(chan struct{})
	dialer := testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		close(started)
		return client, nil
	}}
	o, err := New(Options{Tag: "socks", Type: "socks5", Settings: map[string]any{
		"server": "127.0.0.1", "server_port": 1080,
	}, Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := o.DialTCP(ctx, M.ParseSocksaddrHostPort("example.com", 443))
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled SOCKS5 handshake unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not interrupt SOCKS5 handshake")
	}
}

func TestSOCKS5UDPAssociateUsesConnectedRelayAndOwnsBothSockets(t *testing.T) {
	controlClient, controlServer := net.Pipe()
	udpClient, udpServer := net.Pipe()
	defer udpServer.Close()
	trackedControl := &trackedConn{Conn: controlClient}
	trackedUDP := &trackedConn{Conn: udpClient}
	commandCh := make(chan byte, 1)
	go serveSOCKS5(t, controlServer, 5300, commandCh, false)

	var calls int
	dialer := testDialer{dial: func(_ context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
		calls++
		switch calls {
		case 1:
			if network != "tcp" || destination.String() != "127.0.0.1:1080" {
				t.Fatalf("control dial = %s %s", network, destination)
			}
			return trackedControl, nil
		case 2:
			if network != "udp" || destination.String() != "127.0.0.1:5300" {
				t.Fatalf("relay dial = %s %s, want udp 127.0.0.1:5300", network, destination)
			}
			return trackedUDP, nil
		default:
			t.Fatalf("unexpected extra dial %d", calls)
			return nil, nil
		}
	}}
	o := &socksOut{
		tag:    "relay",
		server: M.ParseSocksaddrHostPort("127.0.0.1", 1080),
		dialer: dialer,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pc, err := o.ListenUDP(ctx, M.ParseSocksaddrHostPort("dns.example", 53))
	if err != nil {
		t.Fatal(err)
	}
	if command := <-commandCh; command != 3 {
		t.Fatalf("SOCKS command = %d, want UDP ASSOCIATE", command)
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	if !trackedControl.isClosed() || !trackedUDP.isClosed() {
		t.Fatalf("association close ownership: control=%v udp=%v", trackedControl.isClosed(), trackedUDP.isClosed())
	}
}

func serveSOCKS5(t *testing.T, conn net.Conn, bindPort uint16, commandCh chan<- byte, sendReady bool) {
	t.Helper()
	defer conn.Close()
	greeting := make([]byte, 3)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	commandCh <- header[1]
	switch header[3] {
	case 1:
		_, _ = io.CopyN(io.Discard, conn, 4+2)
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(conn, length); err != nil {
			return
		}
		_, _ = io.CopyN(io.Discard, conn, int64(length[0])+2)
	case 4:
		_, _ = io.CopyN(io.Discard, conn, 16+2)
	default:
		return
	}
	response := []byte{5, 0, 0, 1, 0, 0, 0, 0, byte(bindPort >> 8), byte(bindPort)}
	if _, err := conn.Write(response); err != nil {
		return
	}
	if sendReady {
		_, _ = conn.Write([]byte("READY"))
		return
	}
	_, _ = io.Copy(io.Discard, conn)
}
