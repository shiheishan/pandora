package kernel

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 探测方能看到的 HTTP/2 行为（10-21 迁移到 net/http 自带实现时逐入口采过帧）：
//   - TLS 上协商出 h2 的入站握完就发 SETTINGS，不等客户端前言（真 Go 服务端与 nginx 都这样）；
//   - 探测回落的 h2 也一样，只收到半截前言就发 SETTINGS：这是回落仍用 x/net ServeConn 的理由
//     （net/http 对非 *tls.Conn 只能走明文 h2，要等齐 24 字节前言才发 SETTINGS）；
//   - 明文 gRPC / XHTTP 只认 prior knowledge，不认 Upgrade: h2c（回 HTTP/1）。

// readFirstH2Frame 读对端发来的第一帧的类型（帧头第 4 字节）。
func readFirstH2Frame(t *testing.T, conn net.Conn, within time.Duration) byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	var head [9]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		t.Fatalf("等第一帧：%v", err)
	}
	return head[3]
}

const h2FrameSettings = 0x4

func dialH2TLS(t *testing.T, addr string) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}}) //nolint:gosec -- 测试证书。
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := requireH2(conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestInboundH2TLSSendsSettingsBeforePreface(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cert := probeTestCert(t, "localhost")
	srv, err := serveNativeGRPC(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}), "/svc/Tun", "", 16<<20,
		func(_ context.Context, c net.Conn) { _ = c.Close() })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	naiveAddr, _ := startProbeInbound(t, "naive", nil, nil)
	for name, addr := range map[string]string{"grpc": ln.Addr().String(), "naive": naiveAddr} {
		conn := dialH2TLS(t, addr)
		if got := readFirstH2Frame(t, conn, 2*time.Second); got != h2FrameSettings {
			t.Fatalf("%s：握完不说话时第一帧类型=%#x，要 SETTINGS", name, got)
		}
	}
}

func TestProbeFallbackH2SettingsBeforeFullPreface(t *testing.T) {
	for _, protocol := range []string{"trojan", "anytls"} {
		addr, _ := startProbeInbound(t, protocol, nil, nil)
		conn := dialH2TLS(t, addr)
		// 只发前言的第一行（16 字节，已足够判定认证失败、进回落），不发其余 8 字节。
		if _, err := conn.Write([]byte("PRI * HTTP/2.0\r\n")); err != nil {
			t.Fatal(err)
		}
		if got := readFirstH2Frame(t, conn, 2*time.Second); got != h2FrameSettings {
			t.Fatalf("%s 回落：半截前言时第一帧类型=%#x，要 SETTINGS", protocol, got)
		}
	}
}

func TestInboundH2PlainIgnoresH2CUpgrade(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := serveNativeGRPC(ln, "/svc/Tun", "", 16<<20, func(_ context.Context, c net.Conn) { _ = c.Close() })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	req := "GET /fp HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABkAAQAoAAAAAIAAAAA\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols || strings.EqualFold(resp.Header.Get("Upgrade"), "h2c") {
		t.Fatalf("明文 gRPC 接受了 h2c 升级：%s %v", resp.Status, resp.Header)
	}
	if resp.ProtoMajor != 1 || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("h2c 升级请求应按 HTTP/1 回 404：%s", resp.Status)
	}
}
