package shadowtls

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
	"io"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// ============================================================
//  伪装握手的判定前限时
// ============================================================
//
// 测试把 HandshakeTimeout 压到 150ms，生产口径是 kernel 传进来的 10 秒。
// 「判定前」各阶段必须按它超时；判定之后的诱饵中继、判定本身还没发生
// 但对外与诱饵中继无法区分的阶段（v2 全程、v3 等首个 HMAC 帧）都不能被它掐断。

const (
	testHandshakeTimeout = 150 * time.Millisecond
	testPassword         = "outer-password"
	// testWatchdog 是测试自己的兜底：超过它还没返回，就是没有限时。
	testWatchdog = 3 * time.Second
)

// 客户端只建 TCP 不说话：三个版本的 NewConnection 都必须在超时后带着
// os.ErrDeadlineExceeded 返回，而不是永远占着 goroutine 和 fd。
func TestServiceSilentClientTimesOut(t *testing.T) {
	decoy := startDecoy(t, func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	for _, version := range []int{1, 2, 3} {
		t.Run(versionName(version), func(t *testing.T) {
			service := newTestService(t, version, decoy, nopHandler{})
			client, done := serveOne(t, service)
			defer client.Close()
			start := time.Now()
			err := waitDone(t, done)
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("NewConnection 返回 %v，期望读超时", err)
			}
			if elapsed := time.Since(start); elapsed > 10*testHandshakeTimeout {
				t.Fatalf("静默连接 %v 后才放掉，超时设的是 %v", elapsed, testHandshakeTimeout)
			}
		})
	}
}

// v3 通过 ClientHello 校验后要把它转给诱饵、等诱饵回 ServerHello。诱饵
// 不回话也不能无限挂着：这一步只有带合法（或重放的）ClientHello 才进得来，
// 仍属判定前。
func TestServiceV3SilentDecoyTimesOut(t *testing.T) {
	decoy := startDecoy(t, func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	service := newTestService(t, 3, decoy, nopHandler{})
	clientConn, done := serveOne(t, service)
	defer clientConn.Close()
	client, err := NewClient(ClientConfig{
		Version:      3,
		Password:     testPassword,
		Server:       decoy,
		TLSHandshake: DefaultTLSHandshakeFunc(testPassword, &tls.Config{ServerName: "decoy.test", InsecureSkipVerify: true}), //nolint:gosec -- 诱饵不回话，证书不会走到校验。
		Logger:       logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = client.DialContextConn(context.Background(), clientConn) }()
	err = waitDone(t, done)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("NewConnection 返回 %v，期望等 ServerHello 超时", err)
	}
}

// 未通过认证的流量回落到诱饵站点后必须和真站点一样不设时限：发完
// ClientHello 静置超过握手超时，双向仍能继续转发。v2 没有 ClientHello
// 认证，ClientHello 之后的握手中继对外就是诱饵中继，同样不能被掐断。
func TestServiceDecoyRelayOutlivesHandshakeTimeout(t *testing.T) {
	ping := tlsRecord(handshake, []byte("ping"))
	pong := tlsRecord(applicationData, []byte("pong"))
	decoy := startDecoy(t, func(conn net.Conn) {
		if _, err := extractFrame(conn); err != nil {
			return
		}
		frame, err := extractFrame(conn)
		if err != nil || !bytes.Equal(frame.Bytes(), ping) {
			return
		}
		_, _ = conn.Write(pong)
		_, _ = io.Copy(io.Discard, conn)
	})
	clientHello := captureClientHello(t)
	for _, version := range []int{2, 3} {
		t.Run(versionName(version), func(t *testing.T) {
			service := newTestService(t, version, decoy, nopHandler{})
			client, done := serveOne(t, service)
			defer client.Close()
			if _, err := client.Write(clientHello); err != nil {
				t.Fatal(err)
			}
			time.Sleep(4 * testHandshakeTimeout)
			if _, err := client.Write(ping); err != nil {
				t.Fatal(err)
			}
			_ = client.SetReadDeadline(time.Now().Add(testWatchdog))
			got := make([]byte, len(pong))
			if _, err := io.ReadFull(client, got); err != nil {
				select {
				case serviceErr := <-done:
					t.Fatalf("静置后读诱饵回包失败：%v（服务端已返回：%v）", err, serviceErr)
				default:
					t.Fatalf("静置后读诱饵回包失败：%v", err)
				}
			}
			if !bytes.Equal(got, pong) {
				t.Fatalf("诱饵回包 = %x", got)
			}
		})
	}
}

// v1 整段握手中继都在判定前，握手完成后连接交给 handler 时截止时间必须已清，
// 否则内层会话会在握手超时后被莫名掐断。v1 的帧状态机只认 TLS 1.2。
func TestServiceV1HandshakeClearsDeadline(t *testing.T) {
	cert := testCertificate(t)
	decoy := startDecoy(t, func(conn net.Conn) {
		_ = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MaxVersion: tls.VersionTLS12}).Handshake()
		_, _ = io.Copy(io.Discard, conn)
	})
	got := make(chan []byte, 1)
	service := newTestService(t, 1, decoy, readHandler{got: got})
	client, done := serveOne(t, service)
	defer client.Close()
	if err := tls.Client(client, &tls.Config{ServerName: "decoy.test", InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}).Handshake(); err != nil { //nolint:gosec -- 测试证书是临时生成的。
		t.Fatal(err)
	}
	time.Sleep(4 * testHandshakeTimeout)
	if _, err := client.Write([]byte("inner")); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-got:
		if string(payload) != "inner" {
			t.Fatalf("handler 读到 %q", payload)
		}
	case err := <-done:
		t.Fatalf("握手后静置被掐断：%v", err)
	case <-time.After(testWatchdog):
		t.Fatal("handler 没读到握手后的数据")
	}
}

// ------------------------------------------------------------
//  夹具
// ------------------------------------------------------------

func versionName(version int) string { return "v" + string(rune('0'+version)) }

func newTestService(t *testing.T, version int, decoy M.Socksaddr, handler N.TCPConnectionHandlerEx) *Service {
	t.Helper()
	service, err := NewService(ServiceConfig{
		Version:          version,
		Password:         testPassword,
		Users:            []User{{Name: "fixture", Password: testPassword}},
		Handshake:        HandshakeConfig{Server: decoy, Dialer: N.SystemDialer},
		HandshakeTimeout: testHandshakeTimeout,
		Handler:          handler,
		Logger:           logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// serveOne 起一个只接一条连接的监听，服务端一侧交给 service.NewConnection，
// 返回客户端一侧与 NewConnection 的结果；服务端连接在它返回后关掉，和
// kernel 的 acceptLoop 一致。
func serveOne(t *testing.T, service *Service) (net.Conn, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		err := service.NewConnection(context.Background(), conn, M.SocksaddrFromNet(conn.RemoteAddr()), M.Socksaddr{}, nil)
		_ = conn.Close()
		done <- err
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return client, done
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(testWatchdog):
		t.Fatalf("NewConnection %v 后仍未返回：握手阶段没有限时", testWatchdog)
		return nil
	}
}

// startDecoy 起一个诱饵 TCP 服务，每条连接交给 serve。
func startDecoy(t *testing.T, serve func(net.Conn)) M.Socksaddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				serve(conn)
			}()
		}
	}()
	return M.SocksaddrFromNet(ln.Addr())
}

// captureClientHello 让标准库 TLS 客户端说出一个真实的 ClientHello 记录。
// 它的 session ID 是随机的，过 v3 的 HMAC 校验的概率是 2^-32。
func captureClientHello(t *testing.T) []byte {
	t.Helper()
	server, client := net.Pipe()
	defer server.Close()
	go func() {
		defer client.Close()
		_ = tls.Client(client, &tls.Config{ServerName: "decoy.test", InsecureSkipVerify: true}).Handshake() //nolint:gosec -- 只取 ClientHello。
	}()
	frame, err := extractFrame(server)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), frame.Bytes()...)
}

func tlsRecord(recordType byte, payload []byte) []byte {
	record := []byte{recordType, 0x03, 0x03, byte(len(payload) >> 8), byte(len(payload))}
	return append(record, payload...)
}

func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "decoy.test"},
		DNSNames:     []string{"decoy.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type nopHandler struct{}

func (nopHandler) NewConnectionEx(context.Context, net.Conn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc) {
}

// readHandler 读出内层连接上的第一段数据。
type readHandler struct{ got chan<- []byte }

func (h readHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	payload := make([]byte, 5)
	if _, err := io.ReadFull(conn, payload); err == nil {
		h.got <- payload
	}
}
