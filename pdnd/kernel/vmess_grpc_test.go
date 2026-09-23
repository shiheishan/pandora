package kernel

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/http2"
)

type vmessGRPCClientConn struct {
	reader  io.ReadCloser
	writer  *io.PipeWriter
	readMu  sync.Mutex
	pending []byte
}

func (c *vmessGRPCClientConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.pending) == 0 {
		var header [5]byte
		if _, err := io.ReadFull(c.reader, header[:]); err != nil {
			return 0, err
		}
		if header[0] != 0 {
			return 0, fmt.Errorf("grpc response compression is not supported")
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length > 16<<20 {
			return 0, fmt.Errorf("grpc response message exceeds limit")
		}
		c.pending = make([]byte, length)
		if _, err := io.ReadFull(c.reader, c.pending); err != nil {
			c.pending = nil
			return 0, err
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *vmessGRPCClientConn) Write(p []byte) (int, error) {
	frame := make([]byte, 5+len(p))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(p)))
	copy(frame[5:], p)
	if _, err := c.writer.Write(frame); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *vmessGRPCClientConn) Close() error {
	writerErr := c.writer.Close()
	readerErr := c.reader.Close()
	if writerErr != nil {
		return writerErr
	}
	return readerErr
}

func (c *vmessGRPCClientConn) LocalAddr() net.Addr              { return vmessGRPCAddr("client") }
func (c *vmessGRPCClientConn) RemoteAddr() net.Addr             { return vmessGRPCAddr("server") }
func (c *vmessGRPCClientConn) SetDeadline(time.Time) error      { return nil }
func (c *vmessGRPCClientConn) SetReadDeadline(time.Time) error  { return nil }
func (c *vmessGRPCClientConn) SetWriteDeadline(time.Time) error { return nil }

type vmessGRPCAddr string

func (a vmessGRPCAddr) Network() string { return "grpc" }
func (a vmessGRPCAddr) String() string  { return string(a) }

func TestVMessNativeGRPCH2CLoopback(t *testing.T) {
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
	id := "8e2f1bf0-61f7-4ed4-b1e2-df54dc43b3ab"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "grpc", "grpc_service_name": "VMessService", "security": "none",
	}}}
	adapterValue, err := newVMessAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vmessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 94, UUID: id}}); err != nil {
		t.Fatal(err)
	}

	requestReader, requestWriter := io.Pipe()
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	// The adapter's h2c endpoint exposes one long-lived gRPC POST. VMess bytes
	// are framed as gRPC messages by vmessGRPCClientConn below.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+itoa(port)+"/VMessService/Tun", requestReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	responseCh := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, roundTripErr := transport.RoundTrip(req)
		responseCh <- struct {
			resp *http.Response
			err  error
		}{resp: resp, err: roundTripErr}
	}()
	var response *http.Response
	select {
	case result := <-responseCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		response = result.resp
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("grpc status=%d", response.StatusCode)
	}
	grpcConn := &vmessGRPCClientConn{reader: response.Body, writer: requestWriter}
	client, err := vmessref.NewClient(id, "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn, err := client.DialConn(grpcConn, M.ParseSocksaddrHostPort("echo.test", 80))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-vmess-grpc")
	if _, err := proxyConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = proxyConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(proxyConn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("grpc echo=%q", got)
	}
	_ = proxyConn.Close()
	transport.CloseIdleConnections()
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	time.Sleep(50 * time.Millisecond)
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 94 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}
}

func TestVMessNativeGRPCTLSLoopback(t *testing.T) {
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
	certPath, keyPath := testXHTTPServerCertFiles(t)
	id := "f3841f5a-a327-4e2e-bfe3-c2f0cf90070b"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "grpc", "grpc_service_name": "VMessTLSService", "security": "none", "tls": true,
		"cert_path": certPath, "key_path": keyPath,
	}}}
	adapterValue, err := newVMessAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vmessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 95, UUID: id}}); err != nil {
		t.Fatal(err)
	}

	requestReader, requestWriter := io.Pipe()
	transport := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return (&tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}}}).DialContext(ctx, network, address) //nolint:gosec -- test certificate is ephemeral.
		},
	}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://127.0.0.1:"+itoa(port)+"/VMessTLSService/Tun", requestReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	responseCh := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, roundTripErr := transport.RoundTrip(req)
		responseCh <- struct {
			resp *http.Response
			err  error
		}{resp: resp, err: roundTripErr}
	}()
	var response *http.Response
	select {
	case result := <-responseCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		response = result.resp
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("grpc tls status=%d", response.StatusCode)
	}
	grpcConn := &vmessGRPCClientConn{reader: response.Body, writer: requestWriter}
	client, err := vmessref.NewClient(id, "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn, err := client.DialConn(grpcConn, M.ParseSocksaddrHostPort("echo.test", 80))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-vmess-grpc-tls")
	if _, err := proxyConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = proxyConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(proxyConn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("grpc tls echo=%q", got)
	}
	_ = proxyConn.Close()
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	time.Sleep(50 * time.Millisecond)
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 95 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("tls traffic=%+v err=%v", traffic, err)
	}
}
