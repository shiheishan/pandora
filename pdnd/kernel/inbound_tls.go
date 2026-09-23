package kernel

import (
	"crypto/tls"
	"fmt"
	"strings"
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
