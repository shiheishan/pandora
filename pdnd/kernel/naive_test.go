package kernel

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	singcore "github.com/aegispanel/nodeagent/core/sing"
	"golang.org/x/net/http2"
)

func TestNativeNaiveH2Loopback(t *testing.T) {
	upstream, target := startProxyEcho(t)
	defer upstream.Close()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "naive", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"tls": true, "cert_path": certPath, "key_path": keyPath}}}
	adapterValue, err := newNaiveAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*naiveAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 18, UUID: "naive-user"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(target)}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	raw, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http2.NextProtoTLS}}) //nolint:gosec -- ephemeral test certificate.
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if got := raw.ConnectionState().NegotiatedProtocol; got != http2.NextProtoTLS {
		t.Fatalf("ALPN=%q", got)
	}
	clientConn, err := (&http2.Transport{}).NewClientConn(raw)
	if err != nil {
		t.Fatal(err)
	}
	pipeReader, pipeWriter := io.Pipe()
	req, err := http.NewRequest(http.MethodConnect, "https://127.0.0.1/", pipeReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = target.String()
	req.Header.Set("Padding", "~native-test-padding~")
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("naive-user:naive-user")))
	resp, err := clientConn.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Padding") == "" {
		t.Fatalf("status=%s padding=%q", resp.Status, resp.Header.Get("Padding"))
	}
	naiveConn := singcore.NewNaiveClientConn(resp.Body, pipeWriter, naiveTestFlusher{}, raw.RemoteAddr())
	defer naiveConn.Close()
	_ = naiveConn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("GET /native-naive HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n")
	if _, err := naiveConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(naiveConn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q", got)
	}
}

func TestNativeNaiveRequiresTLS(t *testing.T) {
	adapter, err := newNaiveAdapter(InboundSpec{Config: core.InboundConfig{Protocol: "naive", Port: 28445}})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(InboundSpec{Config: core.InboundConfig{Protocol: "naive", Port: 28445}}); err == nil {
		t.Fatal("naive accepted a cleartext listener")
	}
}

type naiveTestFlusher struct{}

func (naiveTestFlusher) Flush() {}
