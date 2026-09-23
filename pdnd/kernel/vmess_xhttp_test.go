package kernel

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/apernet/quic-go/http3"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/http2"
)

type vmessXHTTPClientConn struct {
	reader io.ReadCloser
	writer *io.PipeWriter
	mu     sync.Mutex
}

func (c *vmessXHTTPClientConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *vmessXHTTPClientConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *vmessXHTTPClientConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	writerErr := c.writer.Close()
	readerErr := c.reader.Close()
	if writerErr != nil {
		return writerErr
	}
	return readerErr
}
func (c *vmessXHTTPClientConn) LocalAddr() net.Addr              { return vmessGRPCAddr("xhttp-client") }
func (c *vmessXHTTPClientConn) RemoteAddr() net.Addr             { return vmessGRPCAddr("xhttp-server") }
func (c *vmessXHTTPClientConn) SetDeadline(time.Time) error      { return nil }
func (c *vmessXHTTPClientConn) SetReadDeadline(time.Time) error  { return nil }
func (c *vmessXHTTPClientConn) SetWriteDeadline(time.Time) error { return nil }

func TestVMessNativeXHTTPStreamLoopback(t *testing.T) {
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
	id := "c10c7c31-69ef-42ac-b06e-a81d0047f0a9"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp", "path": "/xhttp", "security": "none",
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
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err == nil {
		t.Fatal("second xhttp start unexpectedly succeeded")
	}
	if err := adapter.AddUsers([]core.User{{ID: 96, UUID: id}}); err != nil {
		t.Fatal(err)
	}

	requestReader, requestWriter := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+itoa(port)+"/xhttp/vmess-session/1/", requestReader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()
	responseCh := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, roundTripErr := transport.RoundTrip(request)
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
		t.Fatalf("xhttp status=%d", response.StatusCode)
	}
	conn := &vmessXHTTPClientConn{reader: response.Body, writer: requestWriter}
	client, err := vmessref.NewClient(id, "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn, err := client.DialConn(conn, M.ParseSocksaddrHostPort("echo.test", 80))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-vmess-xhttp")
	if _, err := proxyConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = proxyConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(proxyConn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("xhttp echo=%q", got)
	}
	_ = proxyConn.Close()
	_ = conn.Close()
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	time.Sleep(50 * time.Millisecond)
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 96 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("xhttp traffic=%+v err=%v", traffic, err)
	}
}

func TestVMessNativeXHTTPH3StreamLoopback(t *testing.T) {
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
	id := "bd428581-803c-4e4f-a1de-2e098d64f3f5"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp-h3", "path": "/xhttp", "security": "none", "cert_path": certPath, "key_path": keyPath,
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
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err == nil {
		t.Fatal("second xhttp-h3 start unexpectedly succeeded")
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 97, UUID: id}}); err != nil {
		t.Fatal(err)
	}

	requestReader, requestWriter := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://127.0.0.1:"+itoa(port)+"/xhttp/vmess-h3/1/", requestReader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	transport := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec -- test certificate is ephemeral.
	defer transport.Close()
	responseCh := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, roundTripErr := (&http.Client{Transport: transport}).Do(request)
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
		t.Fatalf("xhttp h3 status=%d", response.StatusCode)
	}
	conn := &vmessXHTTPClientConn{reader: response.Body, writer: requestWriter}
	client, err := vmessref.NewClient(id, "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn, err := client.DialConn(conn, M.ParseSocksaddrHostPort("echo.test", 80))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-vmess-xhttp-h3")
	if _, err := proxyConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = proxyConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(proxyConn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("xhttp h3 echo=%q", got)
	}
	_ = proxyConn.Close()
	_ = conn.Close()
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	time.Sleep(50 * time.Millisecond)
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 97 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("xhttp h3 traffic=%+v err=%v", traffic, err)
	}
}

func TestVMessNativeXHTTPPacketReconnectLoopback(t *testing.T) {
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
	id := "0f590b12-90b6-44b8-aae9-6e0b3b0e6d24"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp", "path": "/xhttp", "mode": "packet-up", "security": "none",
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
	if err := adapter.AddUsers([]core.User{{ID: 98, UUID: id}}); err != nil {
		t.Fatal(err)
	}

	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()
	requestReader, requestWriter := io.Pipe()
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+itoa(port)+"/xhttp/vmess-packet/0/", requestReader)
	if err != nil {
		t.Fatal(err)
	}
	postReq.Header.Set("Content-Type", "application/octet-stream")
	postResponseCh := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, roundTripErr := transport.RoundTrip(postReq)
		postResponseCh <- struct {
			resp *http.Response
			err  error
		}{resp: resp, err: roundTripErr}
	}()

	readPipeR, readPipeW := io.Pipe()
	conn := &vmessXHTTPClientConn{reader: readPipeR, writer: requestWriter}
	client, err := vmessref.NewClient(id, "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn, err := client.DialConn(conn, M.ParseSocksaddrHostPort("echo.test", 80))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-vmess-xhttp-packet")
	if _, err := proxyConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := requestWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-postResponseCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.resp.StatusCode != http.StatusOK {
			_ = result.resp.Body.Close()
			t.Fatalf("xhttp packet upload status=%d", result.resp.StatusCode)
		}
		_ = result.resp.Body.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+itoa(port)+"/xhttp/vmess-packet/", nil)
	if err != nil {
		t.Fatal(err)
	}
	getReq.Header.Set("Content-Type", "application/octet-stream")
	getResp, err := transport.RoundTrip(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("xhttp packet download status=%d", getResp.StatusCode)
	}
	go func() {
		_, _ = io.Copy(readPipeW, getResp.Body)
		_ = readPipeW.Close()
	}()
	_ = proxyConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(proxyConn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("xhttp packet echo=%q", got)
	}
	_ = proxyConn.Close()
	_ = conn.Close()
	_ = readPipeW.Close()
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 98 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("xhttp packet traffic=%+v err=%v", traffic, err)
	}
}

func TestVMessNativeXHTTPH3PacketReconnectLoopback(t *testing.T) {
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
	id := "45dfa3e1-36fa-4fc3-8a4e-a70769c272b0"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp-h3", "path": "/xhttp", "mode": "packet-up", "security": "none", "cert_path": certPath, "key_path": keyPath,
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
	if err := adapter.AddUsers([]core.User{{ID: 99, UUID: id}}); err != nil {
		t.Fatal(err)
	}

	transport := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec -- test certificate is ephemeral.
	defer transport.Close()
	requestReader, requestWriter := io.Pipe()
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://127.0.0.1:"+itoa(port)+"/xhttp/vmess-h3-packet/0/", requestReader)
	if err != nil {
		t.Fatal(err)
	}
	postReq.Header.Set("Content-Type", "application/octet-stream")
	postResponseCh := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, roundTripErr := (&http.Client{Transport: transport}).Do(postReq)
		postResponseCh <- struct {
			resp *http.Response
			err  error
		}{resp: resp, err: roundTripErr}
	}()
	readPipeR, readPipeW := io.Pipe()
	conn := &vmessXHTTPClientConn{reader: readPipeR, writer: requestWriter}
	client, err := vmessref.NewClient(id, "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn, err := client.DialConn(conn, M.ParseSocksaddrHostPort("echo.test", 80))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-vmess-xhttp-h3-packet")
	if _, err := proxyConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := requestWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-postResponseCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.resp.StatusCode != http.StatusOK {
			_ = result.resp.Body.Close()
			t.Fatalf("xhttp h3 packet upload status=%d", result.resp.StatusCode)
		}
		_ = result.resp.Body.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:"+itoa(port)+"/xhttp/vmess-h3-packet/", nil)
	if err != nil {
		t.Fatal(err)
	}
	getReq.Header.Set("Content-Type", "application/octet-stream")
	getResp, err := (&http.Client{Transport: transport}).Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("xhttp h3 packet download status=%d", getResp.StatusCode)
	}
	go func() {
		_, _ = io.Copy(readPipeW, getResp.Body)
		_ = readPipeW.Close()
	}()
	_ = proxyConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(proxyConn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("xhttp h3 packet echo=%q", got)
	}
	_ = proxyConn.Close()
	_ = conn.Close()
	_ = readPipeW.Close()
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 99 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("xhttp h3 packet traffic=%+v err=%v", traffic, err)
	}
}
