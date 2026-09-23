package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
)

// TLSConfig 是中转出站的 TLS 参数。
//
// 从面板下发的 settings 里解析，键名与 sing-box 的 outbound tls 段保持一致，
// 这样面板上已有的中转配置不用改写。
type TLSConfig struct {
	Enabled    bool
	ServerName string
	ALPN       []string
	Insecure   bool
	// Fingerprint 是 uTLS 指纹名（chrome / firefox / safari / ios / random…）。
	// 空表示用 Go 自带的 TLS 栈。
	Fingerprint string
	// CA 是自签证书场景下的额外信任根。
	CA []byte
}

// ParseTLS 从 settings 里取出 TLS 段。
//
// 面板有两种写法：平铺（tls / sni / alpn …）和嵌套在 "tls" 对象里。
// 两种都吃掉——历史配置是平铺的，新的节点编辑器写嵌套。
func ParseTLS(m map[string]any) (TLSConfig, error) {
	var c TLSConfig
	src := m
	if nested, ok := m["tls"].(map[string]any); ok {
		src = nested
		c.Enabled = true
		if _, has := nested["enabled"]; has {
			c.Enabled = Bool(nested, "enabled")
		}
	} else {
		c.Enabled = Bool(m, "tls")
	}
	if !c.Enabled {
		return c, nil
	}

	c.ServerName = Str(src, "server_name")
	if c.ServerName == "" {
		c.ServerName = Str(src, "sni")
	}
	c.ALPN = Strings(src, "alpn")
	c.Insecure = Bool(src, "insecure") || Bool(src, "allow_insecure") ||
		Bool(src, "skip_cert_verify")
	c.Fingerprint = strings.ToLower(Str(src, "fingerprint"))
	if c.Fingerprint == "" {
		c.Fingerprint = strings.ToLower(Str(src, "utls"))
	}

	if raw := Str(src, "certificate"); raw != "" {
		pemBytes := []byte(raw)
		if !strings.Contains(raw, "BEGIN") {
			// 面板上也允许贴 base64 的 DER
			der, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				return c, fmt.Errorf("certificate 既不是 PEM 也不是 base64: %w", err)
			}
			pemBytes = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pemBytes) {
			return c, fmt.Errorf("certificate 解析不出任何证书")
		}
		c.CA = pemBytes
	}
	return c, nil
}

// StdConfig 生成标准库的 tls.Config。
//
// serverFallback 是没配 SNI 时的兜底——用上游服务器地址。上游是 IP 时
// 留空 ServerName：填 IP 会让证书校验按 IP SAN 走，绝大多数证书没有，
// 结果是「明明配对了却握手失败」。
func (c TLSConfig) StdConfig(serverFallback string) *tls.Config {
	cfg := &tls.Config{
		ServerName:         c.ServerName,
		NextProtos:         c.ALPN,
		InsecureSkipVerify: c.Insecure,
		MinVersion:         tls.VersionTLS12,
	}
	if cfg.ServerName == "" {
		cfg.ServerName = serverFallback
	}
	if len(c.CA) > 0 {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(c.CA)
		cfg.RootCAs = pool
	}
	return cfg
}

// Client 在已建立的连接上完成 TLS 握手。
//
// 带 uTLS 指纹时走 uTLS：中转到自建机器无所谓，但中转到第三方机场
// 或经过做 TLS 指纹识别的中间网络时，Go 自带的 ClientHello 是很显眼的
// 特征。指纹名认不出来时报错而不是悄悄退回 Go 栈——用户配了指纹
// 就是奔着这个来的，静默退回等于配置没生效而没人知道。
func (c TLSConfig) Client(ctx context.Context, conn net.Conn, serverFallback string) (net.Conn, error) {
	cfg := c.StdConfig(serverFallback)
	if c.Fingerprint != "" {
		return utlsClient(ctx, conn, cfg, c.Fingerprint)
	}
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("TLS 握手失败: %w", err)
	}
	return tlsConn, nil
}
