package protocol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestGenerateECHKey_echExample(t *testing.T) {
	k, err := GenerateECHKey("ech.example.com")
	if err != nil {
		t.Fatalf("GenerateECHKey: %v", err)
	}
	if len(k.ConfigList) < 2 {
		t.Fatal("ConfigList 过短")
	}
	wantTotal := len(k.ConfigList) - 2
	if int(binary.BigEndian.Uint16(k.ConfigList[0:2])) != wantTotal {
		t.Fatalf("ConfigList 前 2 字节应为 %d，得到 %d", wantTotal, binary.BigEndian.Uint16(k.ConfigList[0:2]))
	}
	fromList, err := ECHConfigFromList(k.ConfigList)
	if err != nil {
		t.Fatalf("ECHConfigFromList: %v", err)
	}
	if len(fromList) != len(k.Config) {
		t.Fatalf("ECHConfig 长度不一致: %d vs %d", len(fromList), len(k.Config))
	}
	for i := range fromList {
		if fromList[i] != k.Config[i] {
			t.Fatalf("ECHConfig 字节与 Config 不一致，偏移 %d", i)
		}
	}
	name, err := ECHPublicNameFromList(k.ConfigList)
	if err != nil {
		t.Fatalf("ECHPublicNameFromList: %v", err)
	}
	if name != "ech.example.com" {
		t.Fatalf("public_name 应为 ech.example.com，得到 %q", name)
	}
	if len(k.PrivateKey) != 32 {
		t.Fatalf("PrivateKey 应为 32 字节，得到 %d", len(k.PrivateKey))
	}

	k2, err := GenerateECHKey("ech.example.com")
	if err != nil {
		t.Fatalf("第二次 GenerateECHKey: %v", err)
	}
	if len(k.PrivateKey) == len(k2.PrivateKey) && string(k.PrivateKey) == string(k2.PrivateKey) {
		t.Fatal("两次生成的私钥不应相同")
	}
}

func TestGenerateECHKey_非法公开名(t *testing.T) {
	longName := strings.Repeat("a", 254) + ".example.com"
	cases := []string{"", "192.0.2.1", "*.example.com", longName}
	for _, name := range cases {
		_, err := GenerateECHKey(name)
		if err == nil {
			t.Fatalf("期望 %q 报错", name)
		}
	}
}

func TestECHHandshake(t *testing.T) {
	k, err := GenerateECHKey("ech-cover.example.com")
	if err != nil {
		t.Fatalf("GenerateECHKey: %v", err)
	}

	cert, pool := echTestServerCert("node.example.com")

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		EncryptedClientHelloKeys: []tls.EncryptedClientHelloKey{
			{
				Config:      k.Config,
				PrivateKey:  k.PrivateKey,
				SendAsRetry: true,
			},
		},
	}
	clientCfg := &tls.Config{
		ServerName:                     "node.example.com",
		RootCAs:                        pool,
		MinVersion:                     tls.VersionTLS13,
		EncryptedClientHelloConfigList: k.ConfigList,
	}

	clientConn, serverConn := net.Pipe()
	srvErrc := make(chan error, 1)
	go func() {
		srv := tls.Server(serverConn, serverCfg)
		srvErrc <- srv.Handshake()
	}()

	cli := tls.Client(clientConn, clientCfg)
	if err := cli.Handshake(); err != nil {
		t.Fatalf("客户端握手失败: %v", err)
	}
	if !cli.ConnectionState().ECHAccepted {
		t.Fatal("期望 ECHAccepted 为 true")
	}
	if srvErr := <-srvErrc; srvErr != nil {
		t.Fatalf("服务端握手失败: %v", srvErr)
	}
}

func echTestServerCert(serverName string) (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: serverName},
		DNSNames:     []string{serverName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return cert, pool
}

func TestECHConfigFromList_非法输入(t *testing.T) {
	k, err := GenerateECHKey("ech.example.com")
	if err != nil {
		t.Fatal(err)
	}
	list := k.ConfigList

	cases := []struct {
		name string
		data []byte
	}{
		{"过短", []byte{0x00}},
		{"总长不符", append([]byte{0xff, 0xff}, list[2:]...)},
		{"版本不对", patchECHConfigVersion(list, 0x1234)},
		{"内容长不符", patchECHConfigContentLen(list, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ECHConfigFromList(tc.data)
			if err == nil {
				t.Fatal("期望报错")
			}
		})
	}
}

func patchECHConfigVersion(list []byte, ver uint16) []byte {
	out := append([]byte(nil), list...)
	if len(out) >= 6 {
		binary.BigEndian.PutUint16(out[4:6], ver)
	}
	return out
}

func patchECHConfigContentLen(list []byte, contentLen uint16) []byte {
	out := append([]byte(nil), list...)
	// ECHConfigList：2 字节总长，ECHConfig：2 字节版本、2 字节内容长
	if len(out) >= 6 {
		binary.BigEndian.PutUint16(out[4:6], contentLen)
	}
	return out
}

func TestECHConfigPEM(t *testing.T) {
	k, err := GenerateECHKey("ech.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pem := ECHConfigPEM(k.ConfigList)
	lines := strings.Split(pem, "\n")
	if lines[0] != "-----BEGIN ECH CONFIGS-----" {
		t.Fatalf("首行不对: %q", lines[0])
	}
	if lines[len(lines)-1] != "-----END ECH CONFIGS-----" {
		t.Fatalf("末行不对: %q", lines[len(lines)-1])
	}
	var b64 strings.Builder
	for i := 1; i < len(lines)-1; i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		if len(line) > 64 {
			t.Fatalf("第 %d 行超过 64 字符: len=%d", i, len(line))
		}
		b64.WriteString(line)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatalf("base64 解码: %v", err)
	}
	if string(decoded) != string(k.ConfigList) {
		t.Fatal("解码结果与 ConfigList 不一致")
	}
}

func TestECHCapable(t *testing.T) {
	cases := []struct {
		name     string
		nodeType string
		kernel   map[string]any
		want     bool
	}{
		{"trojan tls none", "trojan", map[string]any{"tls": true, "security": "none"}, true},
		{"trojan tls false reality", "trojan", map[string]any{"tls": false, "security": "reality"}, false},
		{"vless xhttp-h3", "vless", map[string]any{"tls": true, "network": "xhttp-h3"}, false},
		{"anytls cert", "anytls", map[string]any{"cert_path": "/path/to/cert.pem"}, true},
		{"anytls no cert", "anytls", map[string]any{"cert_path": ""}, false},
		{"naive tls", "naive", map[string]any{"tls": true}, true},
		{"hysteria2", "hysteria2", map[string]any{"tls": true}, false},
		{"shadowsocks", "shadowsocks", map[string]any{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ECHCapable(tc.nodeType, tc.kernel)
			if got != tc.want {
				t.Fatalf("ECHCapable=%v，期望 %v；原因: %q", got, tc.want, ECHUnsupportedReason(tc.nodeType, tc.kernel))
			}
			if tc.want && ECHUnsupportedReason(tc.nodeType, tc.kernel) != "" {
				t.Fatalf("能开 ECH 时原因应为空，得到 %q", ECHUnsupportedReason(tc.nodeType, tc.kernel))
			}
			if !tc.want && ECHUnsupportedReason(tc.nodeType, tc.kernel) == "" {
				t.Fatal("不能开 ECH 时原因不应为空")
			}
		})
	}
}
