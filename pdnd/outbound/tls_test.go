package outbound

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseTLSNestedAndFlatCompatibility(t *testing.T) {
	nested, err := ParseTLS(map[string]any{
		"tls": map[string]any{
			"server_name": "relay.example",
			"alpn":        []any{"h2", "http/1.1"},
			"fingerprint": "Chrome",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !nested.Enabled || nested.ServerName != "relay.example" || nested.Fingerprint != "chrome" {
		t.Fatalf("nested TLS parsed incorrectly: %+v", nested)
	}
	if !reflect.DeepEqual(nested.ALPN, []string{"h2", "http/1.1"}) {
		t.Fatalf("ALPN = %v", nested.ALPN)
	}

	flat, err := ParseTLS(map[string]any{
		"tls":              true,
		"sni":              "legacy.example",
		"skip_cert_verify": true,
		"utls":             "Firefox",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !flat.Enabled || flat.ServerName != "legacy.example" || !flat.Insecure || flat.Fingerprint != "firefox" {
		t.Fatalf("flat TLS parsed incorrectly: %+v", flat)
	}
}

func TestParseTLSRejectsInvalidCertificate(t *testing.T) {
	_, err := ParseTLS(map[string]any{
		"tls":         true,
		"certificate": "not-pem-or-base64!",
	})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("invalid certificate error = %v", err)
	}
}

func TestStdConfigUsesHostnameOrIPFallbackForVerification(t *testing.T) {
	if got := (TLSConfig{Enabled: true}).StdConfig("relay.example").ServerName; got != "relay.example" {
		t.Fatalf("hostname fallback = %q", got)
	}
	if got := (TLSConfig{Enabled: true}).StdConfig("192.0.2.1").ServerName; got != "192.0.2.1" {
		t.Fatalf("IP verification fallback = %q", got)
	}
}

func TestTLSClientVerifiesIPSANFallback(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	serverCertificate, err := tls.X509KeyPair(certificate, pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: mustMarshalPKCS8(t, privateKey),
	}))
	if err != nil {
		t.Fatal(err)
	}
	clientSide, serverSide := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		server := tls.Server(serverSide, &tls.Config{Certificates: []tls.Certificate{serverCertificate}})
		serverErr <- server.Handshake()
		_ = server.Close()
	}()
	conn, err := (TLSConfig{Enabled: true, CA: certificate}).Client(context.Background(), clientSide, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func mustMarshalPKCS8(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestTLSClientRejectsUnknownFingerprintWithoutNetworkHandshake(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_, err := (TLSConfig{
		Enabled:     true,
		ServerName:  "relay.example",
		Fingerprint: "unknown-browser",
	}).Client(context.Background(), client, "relay.example")
	if err == nil || !strings.Contains(err.Error(), "unknown-browser") {
		t.Fatalf("unknown fingerprint error = %v", err)
	}
}
