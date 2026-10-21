package kernel

// 入站证书的两种来源（配置文档 cert_mode，见 core/cert.go）：
//   - file（缺省）：节点机上的 cert_path / key_path，入站启动时加载一次，换证书要重建入站；
//   - acme：节点端自己申请（pdnd/certs），按 cert_identifier 从 core.CertSource 取。每次握手都取当前那张，
//     续期换新后新握手立刻用上，已建立的连接不受影响，入站不用重建。
// 两种来源都经 tls.Config.GetCertificate 交给握手，各协议的 TLS 配置只认 inboundCertificate 给的取证书函数。

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/aegispanel/nodeagent/core"
)

// checkInboundCertificate 只核证书配置的形状，不读文件、不取证书（各协议 Validate 用）：
// file 要两个路径都有；acme 要有 cert_identifier，且不得同时给路径。
func checkInboundCertificate(raw map[string]any) error {
	certPath := strings.TrimSpace(rawString(raw, "cert_path"))
	keyPath := strings.TrimSpace(rawString(raw, "key_path"))
	if core.InboundCertMode(raw) == core.CertModeACME {
		if core.InboundCertIdentifier(raw) == "" {
			return fmt.Errorf("cert_mode acme requires cert_identifier")
		}
		if certPath != "" || keyPath != "" {
			return fmt.Errorf("cert_mode acme must not set cert_path or key_path")
		}
		return nil
	}
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("requires cert_path and key_path")
	}
	return nil
}

// inboundHasCertificate 报告入站是否配了证书：acme，或 file 给了任一路径。
// TLS 可选的协议（AnyTLS）用它决定要不要起 TLS；一边有一边没有的路径由 checkInboundCertificate 拦下。
func inboundHasCertificate(raw map[string]any) bool {
	if core.InboundCertMode(raw) == core.CertModeACME {
		return true
	}
	return strings.TrimSpace(rawString(raw, "cert_path")) != "" || strings.TrimSpace(rawString(raw, "key_path")) != ""
}

// inboundCertificate 返回给 tls.Config.GetCertificate 用的取证书函数（各协议 Start 用）。
//   - file：现在就加载密钥对，读不出来返回错误；之后每次握手给同一张。
//   - acme：src 里还没有这张证书时返回 *core.CertPendingError，入站不起（编排层报 cert_pending，旧配置在就继续服务）；
//     有的话之后每次握手都向 src 取当前那张。
func inboundCertificate(raw map[string]any, src core.CertSource) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
	if err := checkInboundCertificate(raw); err != nil {
		return nil, err
	}
	if core.InboundCertMode(raw) == core.CertModeACME {
		id := core.InboundCertIdentifier(raw)
		if src == nil {
			return nil, &core.CertPendingError{Identifier: id}
		}
		if _, ok := src.Certificate(id); !ok {
			return nil, &core.CertPendingError{Identifier: id}
		}
		return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if cert, ok := src.Certificate(id); ok {
				return cert, nil
			}
			// 入站在用的证书不会被回收（证书管理按 Want 保留），走到这里说明本机证书目录被人删了
			return nil, fmt.Errorf("no certificate for %s", id)
		}, nil
	}
	cert, err := tls.LoadX509KeyPair(strings.TrimSpace(rawString(raw, "cert_path")), strings.TrimSpace(rawString(raw, "key_path")))
	if err != nil {
		return nil, fmt.Errorf("TLS certificate: %w", err)
	}
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }, nil
}
