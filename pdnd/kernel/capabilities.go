package kernel

import (
	"sort"
	"strings"
)

// Capability describes a protocol surface that is owned by Pandora
// NativeCore. It is deliberately a data type rather than a UI-specific
// response so the panel, node diagnostics and future clients can consume the
// same source of truth.
type Capability struct {
	Protocol   string   `json:"protocol"`
	Status     string   `json:"status"`
	Networks   []string `json:"networks,omitempty"`
	Security   []string `json:"security,omitempty"`
	Features   []string `json:"features,omitempty"`
	Boundaries []string `json:"boundaries,omitempty"`
}

// NativeCapabilities returns a deterministic copy of the verified NativeCore
// matrix. Callers may mutate the returned slices without changing the global
// registry. A boundary is listed explicitly when a similar-looking transport
// is intentionally rejected rather than silently delegated to a third-party
// core.
func NativeCapabilities() []Capability {
	capabilities := []Capability{
		{Protocol: "vless", Status: "stable-with-experimental-features", Networks: []string{"tcp", "ws", "httpupgrade", "grpc", "xhttp", "xhttp-h3", "mkcp"}, Security: []string{"none", "tls", "reality"}, Features: []string{"xhttp-stream", "xhttp-packet", "xudp-mux", "http2", "http3", "grpc-gzip", "reality-h3-experimental", "udp-forward", "xtls-rprx-vision", "xtls-rprx-vision-xudp"}, Boundaries: []string{"external-reality-xhttp-h3-unverified", "reality-xhttp-h3-requires-explicit-opt-in"}},
		{Protocol: "vmess", Networks: []string{"tcp", "ws", "httpupgrade", "grpc", "xhttp", "xhttp-h3", "mkcp"}, Security: []string{"none", "zero", "aes-128-gcm", "chacha20-poly1305", "auto"}, Features: []string{"mux", "xhttp-stream", "xhttp-packet", "http2", "http3", "grpc-gzip"}},
		{Protocol: "trojan", Networks: []string{"tcp", "ws", "httpupgrade", "grpc", "mkcp"}, Security: []string{"tls", "reality"}, Features: []string{"http2", "grpc-gzip", "reality-tcp", "udp-forward"}},
		{Protocol: "shadowsocks", Networks: []string{"tcp", "udp"}, Features: []string{"classic-aead", "2022-aead"}},
		{Protocol: "hysteria2", Networks: []string{"udp"}, Features: []string{"quic", "obfs", "udp-forward"}},
		{Protocol: "tuic", Networks: []string{"udp"}, Features: []string{"quic", "congestion-control", "udp-forward"}},
		{Protocol: "anytls", Networks: []string{"tcp"}, Features: []string{"tls", "multiplex", "udp-over-tcp"}},
		{Protocol: "socks", Networks: []string{"tcp", "udp"}, Features: []string{"socks4", "socks4a", "socks5", "udp-associate"}},
		{Protocol: "http", Networks: []string{"tcp"}, Features: []string{"http-connect", "http-forward"}},
		{Protocol: "naive", Networks: []string{"tcp"}, Features: []string{"https-connect"}},
		{Protocol: "shadowtls", Networks: []string{"tcp"}, Features: []string{"shadowtls-v3"}},
		{Protocol: "mieru", Networks: []string{"tcp", "udp"}, Features: []string{"native-listener"}},
		{Protocol: "juicity", Networks: []string{"udp"}, Features: []string{"quic", "tcp-forward", "udp-forward"}},
	}
	for i := range capabilities {
		if capabilities[i].Status == "" {
			capabilities[i].Status = "stable"
		}
		capabilities[i].Networks = append([]string(nil), capabilities[i].Networks...)
		capabilities[i].Security = append([]string(nil), capabilities[i].Security...)
		capabilities[i].Features = append([]string(nil), capabilities[i].Features...)
		capabilities[i].Boundaries = append([]string(nil), capabilities[i].Boundaries...)
	}
	return capabilities
}

// NativeProtocolNames returns the canonical protocol names in stable order.
func NativeProtocolNames() []string {
	capabilities := NativeCapabilities()
	names := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		names = append(names, capability.Protocol)
	}
	sort.Strings(names)
	return names
}

// NativeCapabilityFor returns the capability for a protocol, accepting the
// panel alias "ss" for Shadowsocks. The returned value is a copy.
func NativeCapabilityFor(protocol string) (Capability, bool) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "ss" {
		protocol = "shadowsocks"
	}
	for _, capability := range NativeCapabilities() {
		if capability.Protocol == protocol {
			return capability, true
		}
	}
	return Capability{}, false
}

// NativeCapabilityReport is the stable envelope used by diagnostics and the
// future panel capability endpoint.
type NativeCapabilityReport struct {
	Kernel       string       `json:"kernel"`
	NativeOnly   bool         `json:"native_only"`
	Capabilities []Capability `json:"capabilities"`
}

// NativeCapabilityReportFor creates a report without exposing internal
// adapter or compatibility-core state.
func NativeCapabilityReportFor(nativeOnly bool) NativeCapabilityReport {
	return NativeCapabilityReport{Kernel: "pandora-native", NativeOnly: nativeOnly, Capabilities: NativeCapabilities()}
}
