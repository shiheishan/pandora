package kernel

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// fakeCertSource 是测试用的证书源，实现 core.CertSource 接口。
type fakeCertSource struct {
	mu    sync.RWMutex
	certs map[string]*tls.Certificate
}

func newFakeCertSource() *fakeCertSource {
	return &fakeCertSource{
		certs: make(map[string]*tls.Certificate),
	}
}

func (s *fakeCertSource) Certificate(id string) (*tls.Certificate, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cert, ok := s.certs[id]
	return cert, ok
}

func (s *fakeCertSource) set(id string, cert *tls.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.certs[id] = cert
}

// testCertFor 用 crypto/ecdsa P-256 生成一张自签证书。
// SAN 里能解析成 IP 的放 IPAddresses，其余放 DNSNames；
// SerialNumber 用传入值，有效期 1 小时。
// 返回 Leaf 填好的 tls.Certificate，以及含它的 x509.CertPool。
func testCertFor(t *testing.T, serial int64, sans ...string) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject: pkix.Name{
			CommonName: "pandora-test",
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("创建自签证书失败: %v", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}

	cert := &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        leaf,
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return cert, pool
}

func TestCheckInboundCertificate(t *testing.T) {
	tests := []struct {
		name    string
		raw     map[string]any
		wantErr bool
	}{
		{
			name:    "file 模式两个路径都有",
			raw:     map[string]any{"cert_path": "/path/to/cert.pem", "key_path": "/path/to/key.pem"},
			wantErr: false,
		},
		{
			name:    "file 模式缺 key_path",
			raw:     map[string]any{"cert_path": "/path/to/cert.pem"},
			wantErr: true,
		},
		{
			name:    "file 模式缺 cert_path",
			raw:     map[string]any{"key_path": "/path/to/key.pem"},
			wantErr: true,
		},
		{
			name:    "file 模式两路径都为空",
			raw:     map[string]any{},
			wantErr: true,
		},
		{
			name:    "acme 模式有标识符",
			raw:     map[string]any{"cert_mode": "acme", "cert_identifier": "node1.example.com"},
			wantErr: false,
		},
		{
			name:    "acme 模式缺标识符",
			raw:     map[string]any{"cert_mode": "acme"},
			wantErr: true,
		},
		{
			name:    "acme 模式标识符为空白",
			raw:     map[string]any{"cert_mode": "acme", "cert_identifier": "   "},
			wantErr: true,
		},
		{
			name:    "acme 模式同时给 cert_path",
			raw:     map[string]any{"cert_mode": "acme", "cert_identifier": "node1.example.com", "cert_path": "/path/to/cert.pem"},
			wantErr: true,
		},
		{
			name:    "acme 模式同时给 key_path",
			raw:     map[string]any{"cert_mode": "acme", "cert_identifier": "node1.example.com", "key_path": "/path/to/key.pem"},
			wantErr: true,
		},
		{
			name:    "acme 模式同时给两个路径",
			raw:     map[string]any{"cert_mode": "acme", "cert_identifier": "node1.example.com", "cert_path": "/path/cert", "key_path": "/path/key"},
			wantErr: true,
		},
		{
			name:    "cert_mode 大写 ACME",
			raw:     map[string]any{"cert_mode": "ACME", "cert_identifier": "node1.example.com"},
			wantErr: false,
		},
		{
			name:    "cert_mode 混合大小写与空白",
			raw:     map[string]any{"cert_mode": "  AcMe  ", "cert_identifier": "node1.example.com"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkInboundCertificate(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkInboundCertificate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInboundHasCertificate(t *testing.T) {
	if !inboundHasCertificate(map[string]any{"cert_mode": "acme"}) {
		t.Error("acme 模式应返回 true")
	}
	if !inboundHasCertificate(map[string]any{"cert_mode": "ACME"}) {
		t.Error("ACME 大写模式应返回 true")
	}
	if !inboundHasCertificate(map[string]any{"cert_path": "/path/to/cert.pem"}) {
		t.Error("只给 cert_path 应返回 true")
	}
	if !inboundHasCertificate(map[string]any{"key_path": "/path/to/key.pem"}) {
		t.Error("只给 key_path 应返回 true")
	}
	if !inboundHasCertificate(map[string]any{"cert_path": "/path/cert", "key_path": "/path/key"}) {
		t.Error("两个路径都有应返回 true")
	}
	if inboundHasCertificate(map[string]any{}) {
		t.Error("空配置应返回 false")
	}
	if inboundHasCertificate(map[string]any{"port": 443}) {
		t.Error("无证书字段配置应返回 false")
	}
}

func TestInboundCertificateACMEPending(t *testing.T) {
	raw := map[string]any{
		"cert_mode":       "acme",
		"cert_identifier": "Node1.Example.com.",
	}

	// 1. src == nil
	_, err := inboundCertificate(raw, nil)
	if err == nil {
		t.Fatal("src == nil 时应返回错误")
	}
	var pendingNil *core.CertPendingError
	if !errors.As(err, &pendingNil) {
		t.Fatalf("src == nil 期望 *core.CertPendingError，实际收到: %v", err)
	}
	if pendingNil.Identifier != "node1.example.com" {
		t.Fatalf("Identifier 期望 node1.example.com，实际: %q", pendingNil.Identifier)
	}
	if pendingNil.RuntimeReason() != core.ReasonCertPending {
		t.Fatalf("RuntimeReason 期望 %s，实际: %s", core.ReasonCertPending, pendingNil.RuntimeReason())
	}

	// 2. 空的 fakeCertSource
	emptySrc := newFakeCertSource()
	_, err = inboundCertificate(raw, emptySrc)
	if err == nil {
		t.Fatal("空源时应返回错误")
	}
	var pendingEmpty *core.CertPendingError
	if !errors.As(err, &pendingEmpty) {
		t.Fatalf("空源期望 *core.CertPendingError，实际收到: %v", err)
	}
	if pendingEmpty.Identifier != "node1.example.com" {
		t.Fatalf("Identifier 期望 node1.example.com，实际: %q", pendingEmpty.Identifier)
	}
	if pendingEmpty.RuntimeReason() != core.ReasonCertPending {
		t.Fatalf("RuntimeReason 期望 %s，实际: %s", core.ReasonCertPending, pendingEmpty.RuntimeReason())
	}

	// 3. loadInboundTLSConfig 带 tls: true 与空源
	rawWithTLS := map[string]any{
		"tls":             true,
		"cert_mode":       "acme",
		"cert_identifier": "Node1.Example.com.",
	}
	_, _, err = loadInboundTLSConfig(rawWithTLS, emptySrc)
	if err == nil {
		t.Fatal("loadInboundTLSConfig 应返回错误")
	}
	var pendingLoad *core.CertPendingError
	if !errors.As(err, &pendingLoad) {
		t.Fatalf("loadInboundTLSConfig 期望 *core.CertPendingError，实际收到: %v", err)
	}
	if pendingLoad.Identifier != "node1.example.com" {
		t.Fatalf("Identifier 期望 node1.example.com，实际: %q", pendingLoad.Identifier)
	}

	// 4. validateInboundTLSConfig 对 acme 配置应直接返回 nil（不要求证书已到）
	if err := validateInboundTLSConfig(rawWithTLS); err != nil {
		t.Fatalf("validateInboundTLSConfig 对 acme 应返回 nil，实际: %v", err)
	}
}

func TestInboundCertificateFile(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	raw := map[string]any{
		"cert_path": certPath,
		"key_path":  keyPath,
	}
	getCert, err := inboundCertificate(raw, nil)
	if err != nil {
		t.Fatalf("inboundCertificate 返回错误: %v", err)
	}
	if getCert == nil {
		t.Fatal("inboundCertificate 返回函数为 nil")
	}
	cert, err := getCert(nil)
	if err != nil {
		t.Fatalf("getCert() 失败: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("getCert() 返回空证书")
	}

	// 路径指向不存在的文件
	badRaw := map[string]any{
		"cert_path": "/nonexistent/cert.pem",
		"key_path":  "/nonexistent/key.pem",
	}
	_, err = inboundCertificate(badRaw, nil)
	if err == nil {
		t.Fatal("不存在的文件路径应返回错误")
	}
	var pending *core.CertPendingError
	if errors.As(err, &pending) {
		t.Fatalf("文件不存在不应返回 *core.CertPendingError，实际为: %v", err)
	}
}

func TestACMECertificateSwapKeepsExistingConnections(t *testing.T) {
	src := newFakeCertSource()
	certA, _ := testCertFor(t, 1, "127.0.0.1")
	certB, _ := testCertFor(t, 2, "127.0.0.1")

	pool := x509.NewCertPool()
	pool.AddCert(certA.Leaf)
	pool.AddCert(certB.Leaf)

	src.set("127.0.0.1", certA)

	raw := map[string]any{
		"tls":             true,
		"cert_mode":       "acme",
		"cert_identifier": "127.0.0.1",
	}
	cfg, ok, err := loadInboundTLSConfig(raw, src)
	if err != nil || !ok || cfg == nil {
		t.Fatalf("loadInboundTLSConfig 失败: %v, ok=%v", err, ok)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("tls.Listen 失败: %v", err)
	}
	addr := ln.Addr().String()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	clientConfig := &tls.Config{
		RootCAs:    pool,
		ServerName: "127.0.0.1",
	}

	// 客户端 1 连接并握手
	conn1, err := tls.Dial("tcp", addr, clientConfig)
	if err != nil {
		t.Fatalf("客户端 1 连接失败: %v", err)
	}
	defer conn1.Close()

	if err := conn1.Handshake(); err != nil {
		t.Fatalf("客户端 1 握手失败: %v", err)
	}
	peerCerts1 := conn1.ConnectionState().PeerCertificates
	if len(peerCerts1) == 0 || peerCerts1[0].SerialNumber.Int64() != 1 {
		t.Fatalf("客户端 1 证书序列号期望 1，实际: %v", peerCerts1[0].SerialNumber)
	}

	// 客户端 1 写读一次确认回显
	msg1 := []byte("ping from client 1")
	if _, err := conn1.Write(msg1); err != nil {
		t.Fatalf("客户端 1 写入失败: %v", err)
	}
	buf1 := make([]byte, len(msg1))
	if _, err := io.ReadFull(conn1, buf1); err != nil || !bytes.Equal(buf1, msg1) {
		t.Fatalf("客户端 1 回显校验失败: %v, got: %q", err, string(buf1))
	}

	// 将源中证书替换为 serial=2 的证书 B
	src.set("127.0.0.1", certB)

	// 客户端 2 新建连接
	conn2, err := tls.Dial("tcp", addr, clientConfig)
	if err != nil {
		t.Fatalf("客户端 2 连接失败: %v", err)
	}
	defer conn2.Close()

	if err := conn2.Handshake(); err != nil {
		t.Fatalf("客户端 2 握手失败: %v", err)
	}
	peerCerts2 := conn2.ConnectionState().PeerCertificates
	if len(peerCerts2) == 0 || peerCerts2[0].SerialNumber.Int64() != 2 {
		t.Fatalf("客户端 2 证书序列号期望 2，实际: %v", peerCerts2[0].SerialNumber)
	}

	// 客户端 1 的老连接再次写读，确认换证书不影响已有连接
	msg1Again := []byte("ping from client 1 again")
	if _, err := conn1.Write(msg1Again); err != nil {
		t.Fatalf("客户端 1 老连接再次写入失败: %v", err)
	}
	buf1Again := make([]byte, len(msg1Again))
	if _, err := io.ReadFull(conn1, buf1Again); err != nil || !bytes.Equal(buf1Again, msg1Again) {
		t.Fatalf("客户端 1 老连接再次回显失败: %v, got: %q", err, string(buf1Again))
	}

	// 测试结束关闭监听与客户端连接，确保服务端 goroutine 全部退出
	_ = conn1.Close()
	_ = conn2.Close()
	_ = ln.Close()
	wg.Wait()
}

func TestACMEInboundsStartPendingWithoutCertificate(t *testing.T) {
	tests := []struct {
		protocol string
		factory  AdapterFactory
		raw      map[string]any
		plane    DataPlane
	}{
		{
			protocol: "trojan",
			factory:  newTrojanAdapter,
			raw: map[string]any{
				"tls":             true,
				"cert_mode":       "acme",
				"cert_identifier": "node1.example.com",
			},
			plane: &vlessTestPlane{},
		},
		{
			protocol: "hysteria2",
			factory:  newHysteria2Adapter,
			raw: map[string]any{
				"cert_mode":       "acme",
				"cert_identifier": "192.0.2.10",
			},
			plane: &hysteriaEchoPlane{},
		},
		{
			protocol: "tuic",
			factory:  newTUICAdapter,
			raw: map[string]any{
				"cert_mode":       "acme",
				"cert_identifier": "192.0.2.10",
				"network":         "udp",
			},
			plane: &hysteriaEchoPlane{},
		},
		{
			protocol: "juicity",
			factory:  newJuicityAdapter,
			raw: map[string]any{
				"cert_mode":       "acme",
				"cert_identifier": "192.0.2.10",
			},
			plane: &hysteriaEchoPlane{},
		},
		{
			protocol: "anytls",
			factory:  newAnyTLSAdapter,
			raw: map[string]any{
				"tls":             true,
				"cert_mode":       "acme",
				"cert_identifier": "node1.example.com",
			},
			plane: &vlessTestPlane{},
		},
		{
			protocol: "naive",
			factory:  newNaiveAdapter,
			raw: map[string]any{
				"tls":             true,
				"cert_mode":       "acme",
				"cert_identifier": "node1.example.com",
			},
			plane: &vlessTestPlane{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			port := reserveTCPAndUDPPort(t)
			spec := InboundSpec{
				Config: core.InboundConfig{
					Protocol: tt.protocol,
					Listen:   "127.0.0.1",
					Port:     port,
					Raw:      tt.raw,
				},
			}
			adapter, err := tt.factory(spec)
			if err != nil {
				t.Fatalf("创建 %s 适配器失败: %v", tt.protocol, err)
			}
			if err := adapter.Validate(spec); err != nil {
				t.Fatalf("%s Validate 应返回 nil，实际: %v", tt.protocol, err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = adapter.Start(ctx, spec, AdapterHooks{
				DataPlane: tt.plane,
				Certs:     nil,
			})
			if err == nil {
				t.Fatalf("%s Start 应因缺少证书返回错误", tt.protocol)
			}
			var pending *core.CertPendingError
			if !errors.As(err, &pending) {
				t.Fatalf("%s Start 返回错误期望能解包为 *core.CertPendingError，实际为: %v", tt.protocol, err)
			}
			_ = adapter.Close()
		})
	}
}

func TestACMEInboundServesWithCertificate(t *testing.T) {
	src := newFakeCertSource()
	cert, pool := testCertFor(t, 42, "127.0.0.1")
	src.set("127.0.0.1", cert)

	port := reserveTCPAndUDPPort(t)
	spec := InboundSpec{
		Config: core.InboundConfig{
			Protocol: "trojan",
			Listen:   "127.0.0.1",
			Port:     port,
			Raw: map[string]any{
				"tls":             true,
				"cert_mode":       "acme",
				"cert_identifier": "127.0.0.1",
			},
		},
	}
	adapter, err := newTrojanAdapter(spec)
	if err != nil {
		t.Fatalf("创建 trojan 适配器失败: %v", err)
	}
	if err := adapter.Validate(spec); err != nil {
		t.Fatalf("trojan Validate 失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := adapter.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{},
		Certs:     src,
	}); err != nil {
		t.Fatalf("trojan Start 失败: %v", err)
	}
	defer adapter.Close()

	client, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{
		RootCAs:    pool,
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("tls.Dial 失败: %v", err)
	}
	defer client.Close()

	if err := client.Handshake(); err != nil {
		t.Fatalf("客户端 TLS 握手失败: %v", err)
	}
	peerCerts := client.ConnectionState().PeerCertificates
	if len(peerCerts) == 0 || peerCerts[0].SerialNumber.Int64() != 42 {
		t.Fatalf("证书序列号期望 42，实际: %v", peerCerts[0].SerialNumber)
	}
}
