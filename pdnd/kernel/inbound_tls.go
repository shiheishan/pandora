// [INPUT]: 依赖 crypto/tls 的证书加载与服务端握手，依赖 rawString 读 raw 配置
// [OUTPUT]: 包内提供 loadInboundTLSConfig（vless / vmess / trojan / socks·http / naive 共用的证书加载）、inboundHandshakeTimeout、withHandshakeDeadline、serverTLSHandshake
// [POS]: kernel 的普通 TLS 入站边界：证书加载，以及「在连接自己的 goroutine 里、限时」的服务端握手；accept_loop.go 只管接连接，握手由各适配器为每条连接起的 goroutine 调这里完成
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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

// serverTLSHandshake 在调用方（连接自己的 goroutine）里做服务端 TLS 握手：
// 限时 timeout，ctx 取消（适配器 Close）也会打断它。config 每次 Clone，
// 握手不会改到共享配置。
func serverTLSHandshake(ctx context.Context, conn net.Conn, config *tls.Config, timeout time.Duration) (*tls.Conn, error) {
	tlsConn := tls.Server(conn, config.Clone())
	if err := withHandshakeDeadline(conn, timeout, func() error { return tlsConn.HandshakeContext(ctx) }); err != nil {
		return nil, err
	}
	return tlsConn, nil
}
