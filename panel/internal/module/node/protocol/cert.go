package protocol

import (
	"encoding/json"
	"net"
	"net/netip"
	"strings"
)

// 证书来源（协议配置的 cert_mode）：acme 由节点端向 Let's Encrypt 自动申请，file 读节点机上的 cert_path / key_path。
// 缺省按 file（旧数据）。配置文档里面板另写 cert_identifier（node/service/config_docs.go），节点端按它申请。
const (
	CertModeACME = "acme"
	CertModeFile = "file"
)

var certIdentifierCGNAT = netip.MustParsePrefix("100.64.0.0/10")

// UsesCertificate 报告这份内核形状的配置在节点上会不会用到我们的证书：
//
//	hysteria2、tuic、juicity、naive 总是用；
//	anytls 总是用（requireAnyTLSCertificate 已要求必须有证书）；
//	socks、http 在 tls 为 true 时用；
//	trojan 在 tls 为 true 且 security 不是 reality 时用；
//	其余协议（含 vless、vmess）一律 false。
func UsesCertificate(nodeType string, kernel map[string]any) bool {
	canonical := CanonicalType(nodeType)
	switch canonical {
	case "hysteria2", "tuic", "juicity", "naive", "anytls":
		return true
	case "socks", "http":
		return kernelBoolTrue(kernel["tls"])
	case "trojan":
		sec, _ := kernel["security"].(string)
		if strings.EqualFold(strings.TrimSpace(sec), "reality") {
			return false
		}
		return kernelBoolTrue(kernel["tls"])
	default:
		return false
	}
}

// CertMode 返回节点实际的证书来源：UsesCertificate 为假返回 ""；
// cert_mode 是字符串 "acme" 返回 CertModeACME；其余（含缺省）返回 CertModeFile。
func CertMode(nodeType string, kernel map[string]any) string {
	if !UsesCertificate(nodeType, kernel) {
		return ""
	}
	mode, _ := kernel["cert_mode"].(string)
	if strings.EqualFold(strings.TrimSpace(mode), CertModeACME) {
		return CertModeACME
	}
	return CertModeFile
}

// CertIdentifier 是 acme 证书的名字：kernel["server_name"] 是非空字符串时用它，否则用 host；
// 结果经 NormalizeCertIdentifier。
func CertIdentifier(kernel map[string]any, host string) string {
	if name, ok := kernel["server_name"].(string); ok && strings.TrimSpace(name) != "" {
		return NormalizeCertIdentifier(name)
	}
	return NormalizeCertIdentifier(host)
}

// NormalizeCertIdentifier 与节点端 pdnd/core.NormalizeCertIdentifier 同一口径，两端必须一致：
// 去首尾空白与 IPv6 方括号；IP 取 net.IP.String()；域名转小写、去掉结尾的点。
func NormalizeCertIdentifier(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(strings.TrimPrefix(id, "["), "]")
	if ip := net.ParseIP(id); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(strings.ToLower(id), ".")
}

// CertIdentifierProblem 校验 acme 证书名字，返回中文错误，合格返回 ""。
func CertIdentifierProblem(id string) string {
	id = NormalizeCertIdentifier(id)
	if id == "" {
		return "缺少证书名字：填「客户端 SNI」或节点的连接地址"
	}
	if len(id) > 253 {
		return "自动申请证书要用公网域名（如 node1.example.com），不能用通配符、单段名字或内网后缀"
	}
	if ip := net.ParseIP(id); ip != nil {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || certIdentifierCGNAT.Contains(netip.MustParseAddr(ip.String())) {
			return "自动申请证书要用公网 IP：" + id + " 是内网或保留地址"
		}
		return ""
	}
	lower := strings.ToLower(id)
	if !validServerName(id) || strings.Contains(id, "*") {
		return "自动申请证书要用公网域名（如 node1.example.com），不能用通配符、单段名字或内网后缀"
	}
	labels := strings.Split(lower, ".")
	if len(labels) < 2 {
		return "自动申请证书要用公网域名（如 node1.example.com），不能用通配符、单段名字或内网后缀"
	}
	last := labels[len(labels)-1]
	allDigit := true
	for _, r := range last {
		if r < '0' || r > '9' {
			allDigit = false
			break
		}
	}
	if allDigit && len(last) > 0 {
		return "自动申请证书要用公网域名（如 node1.example.com），不能用通配符、单段名字或内网后缀"
	}
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".lan") || strings.HasSuffix(lower, ".home.arpa") {
		return "自动申请证书要用公网域名（如 node1.example.com），不能用通配符、单段名字或内网后缀"
	}
	return ""
}

// ValidateCertIdentity 在协议配置本身已校验通过之后、按节点地址再查一遍 acme 证书名字（service 保存节点时调用）。
func ValidateCertIdentity(nodeType, host string, raw json.RawMessage) map[string]string {
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil || stored == nil {
		return nil
	}
	canonical := CanonicalType(nodeType)
	kernel := toKernelConfig(canonical, stored)
	if CertMode(canonical, kernel) != CertModeACME {
		return nil
	}
	host = strings.TrimSpace(host)
	sni, hasSNI := kernel["server_name"].(string)
	hasSNI = hasSNI && strings.TrimSpace(sni) != ""
	if !hasSNI && host == "" {
		return nil
	}
	problem := CertIdentifierProblem(CertIdentifier(kernel, host))
	if problem == "" {
		return nil
	}
	key := "server_host"
	if hasSNI {
		key = "protocol_config.tls_settings.server_name"
	}
	return map[string]string{key: problem}
}

// certModeOf 校验内核形状里 cert_mode 的取值并报告是不是 acme，供 validate_kernel.go 各分支用。
func certModeOf(fields map[string]string, mode, certPath, keyPath string) bool {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return false
	}
	if strings.EqualFold(mode, CertModeACME) {
		if strings.TrimSpace(certPath) != "" || strings.TrimSpace(keyPath) != "" {
			fields["protocol_config.cert_path"] = "自动申请证书时不填证书路径"
		}
		return true
	}
	if strings.EqualFold(mode, CertModeFile) {
		return false
	}
	fields["protocol_config.cert_mode"] = "只能是 acme（自动申请）或 file（手填路径）"
	return false
}
