package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"
)

// loadInboundTLSConfig is the shared native TCP TLS boundary. It deliberately
// keeps TLS outside protocol framing: the protocol adapter receives a normal
// net.Conn after the handshake, so no wrapper bytes or non-standard markers
// are introduced into VLESS/Trojan traffic.
func loadInboundTLSConfig(raw map[string]any) (*tls.Config, bool, error) {
	enabled, ok := raw["tls"].(bool)
	if !ok || !enabled {
		return nil, false, nil
	}
	certPath := strings.TrimSpace(rawString(raw, "cert_path"))
	keyPath := strings.TrimSpace(rawString(raw, "key_path"))
	if certPath == "" || keyPath == "" {
		return nil, false, fmt.Errorf("native TLS requires cert_path and key_path")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, false, fmt.Errorf("native TLS certificate: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, true, nil
}

// ============================================================
//  限时握手
// ============================================================

// inboundHandshakeTimeout 是入站握手（TLS、ShadowTLS 伪装握手、HTTP 承载的
// 请求头）的上限，与各协议读请求头的 10 秒同一口径。正常客户端一两个 RTT
// 就完成；不设上限的话，只建 TCP 不说话的扫描器会一直占着 goroutine 和 fd，
// 攒够了就是 EMFILE。
const inboundHandshakeTimeout = 10 * time.Second

// withHandshakeDeadline 给 conn 挂上 timeout 的读写截止时间再跑 handshake，
// 成功后清掉截止时间；失败时原样返回错误，由调用方关连接、上报。
func withHandshakeDeadline(conn net.Conn, timeout time.Duration, handshake func() error) error {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := handshake(); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	return nil
}

// inboundWebALPN 是 TCP 直连类 TLS 入站（Trojan、VLESS、VMess 的 tcp+tls 与
// AnyTLS）对外宣告的 ALPN，与常见 Web 服务器一致。以前一个都不设，客户端给了
// h2 / http/1.1 服务端也不选，这本身就是「不是网站」的特征。
var inboundWebALPN = []string{"h2", "http/1.1"}

// withInboundWebALPN 给 TCP 直连类 TLS 配置挂上 inboundWebALPN（已显式设了
// NextProtos 的不动）。客户端宣告的 ALPN 与之完全不重叠时（例如只给 h3 或
// 自定义串），退回不选 ALPN 照常握手：Go 默认会以 no_application_protocol
// 拒绝，那会让以前能连上的客户端连不上。返回的是新配置，不改 config。
func withInboundWebALPN(config *tls.Config) *tls.Config {
	out := config.Clone()
	if len(out.NextProtos) != 0 || out.GetConfigForClient != nil {
		return out
	}
	noALPN := out.Clone()
	out.NextProtos = append([]string(nil), inboundWebALPN...)
	out.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if len(hello.SupportedProtos) == 0 || alpnOverlaps(hello.SupportedProtos, inboundWebALPN) {
			return nil, nil
		}
		return noALPN, nil
	}
	return out
}

func alpnOverlaps(client, server []string) bool {
	for _, c := range client {
		for _, s := range server {
			if c == s {
				return true
			}
		}
	}
	return false
}

// serverTLSHandshake 在调用方（连接自己的 goroutine）里做服务端 TLS 握手：
// 限时 timeout，ctx 取消（适配器 Close）也会打断它。config 每次 Clone，
// 握手不会改到共享配置。只给 TCP 直连承载用，ALPN 按 withInboundWebALPN。
func serverTLSHandshake(ctx context.Context, conn net.Conn, config *tls.Config, timeout time.Duration) (*tls.Conn, error) {
	tlsConn := tls.Server(conn, withInboundWebALPN(config))
	if err := withHandshakeDeadline(conn, timeout, func() error { return tlsConn.HandshakeContext(ctx) }); err != nil {
		return nil, err
	}
	return tlsConn, nil
}
