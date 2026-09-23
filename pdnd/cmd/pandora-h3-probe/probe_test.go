package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/kernel"
)

func TestVLESSTCPHeader(t *testing.T) {
	var id [16]byte
	for i := range id {
		id[i] = byte(i)
	}
	header, err := vlessTCPHeader(id, "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) != 1+16+1+1+2+1+1+len("example.com") || header[0] != vlessVersion || header[18] != vlessTCP || header[21] != addrDomain {
		t.Fatalf("unexpected VLESS header: %x", header)
	}
}

// TestProcessProbeRoundTrip starts the native server in this test process and
// executes the probe as a separate OS process. It is intentionally distinct
// from the in-process HTTP/3 tests and is the acceptance seam for replacing
// the probe with a desktop/mobile client later.
func TestProcessProbeRoundTrip(t *testing.T) {
	goBinary := os.Getenv("PANDORA_GO")
	if goBinary == "" {
		var err error
		goBinary, err = exec.LookPath("go")
		if err != nil {
			t.Skip("go executable not available for process probe")
		}
	}
	target, err := tls.Listen("tcp", "127.0.0.1:0", processProbeTLSConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, acceptErr := target.Accept()
		if acceptErr == nil {
			if tlsConn, ok := conn.(*tls.Conn); ok {
				_ = tlsConn.Handshake()
			}
			_ = conn.Close()
		}
	}()

	packet, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var shortID [8]byte
	copy(shortID[:], []byte("probe001"))
	xhttpConfig, err := kernel.ParseXHTTPConfig(map[string]any{"path": "/xhttp", "mode": "stream-one"})
	if err != nil {
		t.Fatal(err)
	}
	xhttp := kernel.XHTTPServer{Config: xhttpConfig, Handler: func(_ context.Context, session kernel.XHTTPSession) error {
		payload, readErr := io.ReadAll(session.Body)
		if readErr != nil {
			return readErr
		}
		_, writeErr := session.Writer.Write(payload)
		return writeErr
	}}
	h3Server, err := xhttp.ServeH3Reality(packet, kernel.RealityServerConfig{
		Dest:        target.Addr().String(),
		ServerNames: map[string]bool{"localhost": true},
		PrivateKey:  key.Bytes(),
		ShortIDs:    map[[8]byte]bool{shortID: true},
		MaxTimeDiff: time.Minute,
	}, (&net.Dialer{}).DialContext)
	if err != nil {
		t.Fatal(err)
	}
	defer h3Server.Close()

	_, file, _, _ := runtime.Caller(0)
	commandDir := filepath.Dir(file)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBinary, "run", ".", "-addr", packet.LocalAddr().String(), "-server-name", "localhost", "-public-key", base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "-short-id", hex.EncodeToString(shortID[:]), "-path", "/xhttp/process-probe/1/", "-payload", "process-probe")
	cmd.Dir = commandDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("process probe failed: %v output=%s", err, output)
	}
	if !strings.Contains(string(output), "proto=HTTP/3.0 status=200") || !strings.Contains(string(output), `body="process-probe"`) {
		t.Fatalf("unexpected process probe output: %s", output)
	}
}

func processProbeTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	return &tls.Config{Certificates: []tls.Certificate{testCertificate(t)}, MinVersion: tls.VersionTLS13}
}

func testCertificate(t *testing.T) tls.Certificate {
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
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
