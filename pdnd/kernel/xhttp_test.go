package kernel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apernet/quic-go/http3"
	"golang.org/x/net/http2"
)

func TestParseXHTTPConfigAndRequestMeta(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{
		"path": "/xhttp", "mode": "stream-one", "host": "edge.example",
		"headers":           map[string]any{"X-Pandora": "native"},
		"session_placement": "query", "seq_placement": "path", "session_key": "sid",
		"sc_max_each_post_bytes": map[string]any{"from": float64(1024), "to": float64(2048)},
	})
	if err != nil {
		t.Fatalf("ParseXHTTPConfig: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://edge.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyRequestMeta(req, "abc", "7"); err != nil {
		t.Fatalf("ApplyRequestMeta: %v", err)
	}
	if req.URL.Path != "/xhttp/7/" || req.URL.Query().Get("sid") != "abc" || req.Host != "edge.example" {
		t.Fatalf("request metadata = %s?%s host=%s", req.URL.Path, req.URL.RawQuery, req.Host)
	}
	if gotSid, gotSeq, err := c.ExtractRequestMeta(req); err != nil || gotSid != "abc" || gotSeq != "7" {
		t.Fatalf("ExtractRequestMeta = %q,%q,%v", gotSid, gotSeq, err)
	}
}

func TestParseXHTTPRejectsUnsafeConfig(t *testing.T) {
	for _, raw := range []map[string]any{
		{"mode": "unknown"},
		{"session_placement": "body"},
		{"headers": map[string]any{"X-Bad": "a\nb"}},
		{"sc_max_each_post_bytes": map[string]any{"from": float64(5), "to": float64(4)}},
	} {
		if _, err := ParseXHTTPConfig(raw); err == nil {
			t.Fatalf("unsafe config accepted: %#v", raw)
		}
	}
}

func TestXHTTPPacketDownlinkAllowsMissingSequenceWithQuerySession(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{
		"path": "/xhttp", "mode": "stream-down", "session_placement": "query", "session_key": "sid",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://edge.example/xhttp/?sid=abc", nil)
	sessionID, seq, err := c.extractRequestMeta(req, true)
	if err != nil || sessionID != "abc" || seq != "" {
		t.Fatalf("packet downlink metadata=%q,%q err=%v", sessionID, seq, err)
	}
}

func TestXHTTPPacketQueueOrdersRetriesAndResume(t *testing.T) {
	queue, err := NewXHTTPPacketQueue(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Push(XHTTPPacket{Seq: 1, Payload: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Push(XHTTPPacket{Seq: 0, Payload: []byte("zero")}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Push(XHTTPPacket{Seq: 1, Payload: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	packet, err := queue.Read(context.Background())
	if err != nil || packet.Seq != 0 || string(packet.Payload) != "zero" {
		t.Fatalf("first packet=%+v err=%v", packet, err)
	}
	if err := queue.ResumeFrom(1); err != nil {
		t.Fatal(err)
	}
	packet, err = queue.Read(context.Background())
	if err != nil || packet.Seq != 1 || string(packet.Payload) != "one" {
		t.Fatalf("resumed packet=%+v err=%v", packet, err)
	}
	if got := queue.Expected(); got != 2 {
		t.Fatalf("expected sequence=%d", got)
	}
}

func TestXHTTPPacketBrokerReconnectAndGC(t *testing.T) {
	broker, err := NewXHTTPPacketBroker(4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, err := broker.Open("session-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Push(XHTTPPacket{Seq: 0, Payload: []byte("persisted")}); err != nil {
		t.Fatal(err)
	}
	reconnected, err := broker.Open("session-a")
	if err != nil || reconnected != first {
		t.Fatalf("reconnect queue=%p first=%p err=%v", reconnected, first, err)
	}
	packet, err := reconnected.Read(context.Background())
	if err != nil || string(packet.Payload) != "persisted" {
		t.Fatalf("reconnect packet=%+v err=%v", packet, err)
	}
	if removed := broker.GC(time.Now().Add(2 * time.Minute)); removed != 1 {
		t.Fatalf("expired sessions=%d", removed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := reconnected.Read(ctx); err == nil {
		t.Fatal("expired queue unexpectedly remained readable")
	}
}

func TestXHTTPServerH1Session(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{
		"path": "/xhttp", "session_placement": "query", "seq_placement": "path", "session_key": "sid",
		"headers": map[string]any{"X-Pandora": "native"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := XHTTPServer{Config: c, Handler: func(_ context.Context, session XHTTPSession) error {
		payload, err := io.ReadAll(session.Body)
		if err != nil {
			return err
		}
		_, err = session.Writer.Write(payload)
		return err
	}}
	req := httptest.NewRequest(http.MethodPost, "https://edge.example/xhttp/7/?sid=abc", strings.NewReader("hello-xhttp"))
	req.Host = "edge.example"
	req.Header.Set("X-Pandora", "native")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "hello-xhttp" {
		t.Fatalf("code=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestXHTTPServerStreamOneBasePath(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp", "mode": "stream-one"})
	if err != nil {
		t.Fatal(err)
	}
	server := XHTTPServer{Config: c, Handler: func(_ context.Context, session XHTTPSession) error {
		if session.ID != "" || session.Seq != "" {
			t.Fatalf("stream-one metadata=%q,%q", session.ID, session.Seq)
		}
		payload, err := io.ReadAll(session.Body)
		if err != nil {
			return err
		}
		_, err = session.Writer.Write(payload)
		return err
	}}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "https://edge.example/xhttp/", strings.NewReader("hello-stream-one")))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "hello-stream-one" {
		t.Fatalf("code=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestXHTTPServerRejectsOversizedBody(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp", "sc_max_each_post_bytes": map[string]any{"from": float64(1), "to": float64(4)}})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	server := XHTTPServer{Config: c, Handler: func(context.Context, XHTTPSession) error {
		called = true
		return nil
	}}
	req := httptest.NewRequest(http.MethodPost, "https://edge.example/xhttp/session/1/", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("code=%d handler_called=%v", recorder.Code, called)
	}
}

func TestXHTTPServerH2CSession(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := XHTTPServer{Config: c, Handler: func(_ context.Context, session XHTTPSession) error {
		payload, err := io.ReadAll(session.Body)
		if err != nil {
			return err
		}
		_, err = session.Writer.Write(payload)
		return err
	}}
	httpServer, err := server.Serve(listener)
	if err != nil {
		t.Fatal(err)
	}
	defer httpServer.Close()
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()
	url := "http://" + listener.Addr().String() + "/xhttp/session-h2/1/"
	resp, err := client.Post(url, "application/octet-stream", strings.NewReader("hello-h2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK || string(body) != "hello-h2" {
		t.Fatalf("proto=%s code=%d body=%q", resp.Proto, resp.StatusCode, body)
	}
}

func TestXHTTPServerH3Session(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp"})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	server := XHTTPServer{Config: c, Handler: func(_ context.Context, session XHTTPSession) error {
		payload, err := io.ReadAll(session.Body)
		if err != nil {
			return err
		}
		_, err = session.Writer.Write(payload)
		return err
	}}
	h3Server, err := server.ServeH3(packet, testXHTTPServerTLSConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer h3Server.Close()
	transport := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: transport}
	defer transport.Close()
	url := "https://" + packet.LocalAddr().String() + "/xhttp/session-h3/1/"
	resp, err := client.Post(url, "application/octet-stream", strings.NewReader("hello-h3"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ProtoMajor != 3 || resp.StatusCode != http.StatusOK || string(body) != "hello-h3" {
		t.Fatalf("proto=%s code=%d body=%q", resp.Proto, resp.StatusCode, body)
	}
}

func testXHTTPServerTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS13}
}

func testXHTTPServerCertFiles(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
