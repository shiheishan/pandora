package certs

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)

// 两家国内提供方的 SDK 都把 endpoint 写死成 HTTPS 公网域名，lego 也不给注入 HTTP 客户端的口子：
//   - 腾讯云 SDK 认包级的 common.DefaultHttpClient：测试把它换成直接交给模拟 handler 的客户端；
//   - 阿里云 SDK（dara）每次请求自建 Transport，只认环境里的 HTTPS_PROXY 与系统根证书：测试起一个
//     CONNECT 中间人代理，用自签 CA 现签 *.aliyuncs.com 的证书，再用 SSL_CERT_FILE 让进程信任这个 CA。
//     SSL_CERT_FILE 只在 Linux 上生效（macOS 走系统钥匙串），所以阿里云的用例只在 Linux（CI）上跑。
// 两个环境变量都必须在任何 HTTPS 请求之前设好（net/http 与 crypto/x509 各自只读一次），所以放在 TestMain。

var (
	tencentHandler atomic.Pointer[http.Handler]
	aliHandler     atomic.Pointer[http.Handler]
	aliMITM        bool
)

func TestMain(m *testing.M) {
	common.DefaultHttpClient = inMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := tencentHandler.Load(); h != nil {
			(*h).ServeHTTP(w, r)
			return
		}
		http.Error(w, "no tencent mock", http.StatusBadGateway)
	}))
	cleanup := func() {}
	if runtime.GOOS == "linux" {
		var err error
		if cleanup, err = startAliMITM(); err != nil {
			fmt.Fprintln(os.Stderr, "阿里云模拟代理起不来:", err)
			os.Exit(1)
		}
		aliMITM = true
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func useTencent(t *testing.T, h http.Handler) {
	tencentHandler.Store(&h)
	t.Cleanup(func() { tencentHandler.Store(nil) })
}

func useAliDNS(t *testing.T, h http.Handler) {
	if !aliMITM {
		t.Skip("阿里云 SDK 的模拟要靠 SSL_CERT_FILE 信任自签 CA，只在 Linux 上生效（CI 会跑）")
	}
	aliHandler.Store(&h)
	t.Cleanup(func() { aliHandler.Store(nil) })
}

func startAliMITM() (func(), error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pandora certs test MITM CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caCert, _ := x509.ParseCertificate(caDER)
	dir, err := os.MkdirTemp("", "certs-mitm-")
	if err != nil {
		return nil, err
	}
	pemPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(pemPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		return nil, err
	}
	os.Setenv("SSL_CERT_FILE", pemPath)

	var mu sync.Mutex
	leafs := map[string]*tls.Certificate{}
	leafFor := func(host string) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		if c, ok := leafs[host]; ok {
			return c, nil
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host},
			DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			return nil, err
		}
		c := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
		leafs[host] = c
		return c, nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		host, _, _ := net.SplitHostPort(r.Host)
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go serveMITM(conn, host, leafFor)
	})}
	go func() { _ = srv.Serve(ln) }()
	os.Setenv("HTTPS_PROXY", "http://"+ln.Addr().String())
	return func() { _ = srv.Close(); _ = os.RemoveAll(dir) }, nil
}

// serveMITM 在劫持的连接上做 TLS 服务端，把里面的每个 HTTP 请求交给当前的阿里云模拟。
func serveMITM(conn net.Conn, host string, leafFor func(string) (*tls.Certificate, error)) {
	tlsConn := tls.Server(conn, &tls.Config{GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := hello.ServerName
		if name == "" {
			name = host
		}
		return leafFor(name)
	}})
	defer tlsConn.Close()
	br := bufio.NewReader(tlsConn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		rec := httptest.NewRecorder()
		if h := aliHandler.Load(); h != nil {
			(*h).ServeHTTP(rec, req)
		} else {
			http.Error(rec, "no alidns mock", http.StatusBadGateway)
		}
		resp := rec.Result()
		resp.ContentLength = int64(rec.Body.Len())
		if err := resp.Write(tlsConn); err != nil {
			return
		}
	}
}
