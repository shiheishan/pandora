package render

// Loon 节点订阅，一行一个节点，写法按 Loon 文档「单节点配置」（https://nsloon.app/docs/Node/）。

import (
	"strconv"
	"strings"
)

const defaultLoonTemplate = "{{proxies}}\n"

func renderLoon(rc renderCtx) ([]byte, string, int) {
	info, real := rc.splitInfo()
	var realLines []string
	for _, n := range real {
		line, _ := nodeToLoon(n, rc.uuid)
		if line != "" {
			realLines = append(realLines, line)
		}
	}
	lines := realLines
	if len(realLines) > 0 {
		var infoLines []string
		for _, n := range info {
			line, _ := nodeToLoon(n, rc.uuid)
			if line != "" {
				infoLines = append(infoLines, line)
			}
		}
		lines = append(infoLines, realLines...)
	}
	tmpl := rc.tmpl
	if tmpl == "" {
		tmpl = defaultLoonTemplate
	}
	body := fillTemplate(tmpl, map[string]string{
		phProxies: strings.Join(lines, "\n"),
	})
	return []byte(body), "text/plain; charset=utf-8", len(realLines)
}

func loonTLSProfile(fp string) string {
	switch strings.ToLower(strings.TrimSpace(fp)) {
	case "chrome":
		return "chrome"
	case "safari", "ios":
		return "safari-ios18"
	default:
		return ""
	}
}

func loonQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, "") + `"`
}

func loonField(s string) string {
	if strings.Contains(s, ",") {
		return loonQuote(s)
	}
	return s
}

// nodeToLoon 在拼好一行后检查换行：Loon 一行一个节点，字段里的 \r/\n 会把配置拆成多行。
func nodeToLoon(n Node, uuid string) (string, string) {
	line, reason := loonNodeLine(n, uuid)
	if line != "" && strings.ContainsAny(line, "\r\n") {
		return "", "字段里有换行，写不进 Loon 的一行"
	}
	return line, reason
}

func loonNodeLine(n Node, uuid string) (string, string) {
	port := strconv.Itoa(n.Port)
	name := n.Name

	switch n.Type {
	case "shadowsocks":
		// 节点端的 Shadowsocks 同一端口 TCP、UDP 都听（与 clash.go 的 udp: true 同一口径）
		method := ssMethod(n.Config)
		// Loon 文档只给了单个 base64 密码的 2022 示例，SIP023 的 iPSK:uPSK 写法没有依据；真机确认能连后再放开。
		if isSS2022(method) {
			return "", "Loon 暂不支持 Shadowsocks 2022 多用户密钥（未经真机确认）"
		}
		pw, reason := ssPassword(n.Config, uuid)
		if reason != "" {
			return "", reason
		}
		line := name + " = Shadowsocks," + n.Host + "," + port + "," + method + "," + loonQuote(pw) + ",udp=true"
		return line, ""

	case "shadowtls":
		method := ssMethod(n.Config)
		stlsPass := cfgStr(n.Config, "password", "")
		stlsSNI := shadowTLSHandshakeServer(n.Config)
		line := name + " = Shadowsocks," + n.Host + "," + port + "," + method + "," + loonQuote(uuid) +
			",shadow-tls-password=" + loonQuote(stlsPass) +
			",shadow-tls-sni=" + loonField(stlsSNI) +
			",shadow-tls-version=3,udp=false"
		return line, ""

	case "vmess", "vless", "trojan":
		o := parseStream(n, uuid)
		if o.Unsupported != "" {
			return "", o.Unsupported
		}
		if o.Network != "tcp" && o.Network != "ws" {
			return "", "Loon 不支持 " + o.Network + " 传输"
		}
		var proto string
		switch n.Type {
		case "vmess":
			proto = "VMess"
		case "vless":
			proto = "VLESS"
		case "trojan":
			proto = "Trojan"
		}
		parts := []string{name + " = " + proto, n.Host, port}
		if n.Type == "vmess" {
			parts = append(parts, "auto", loonQuote(uuid), "transport="+o.Network, "alterId=0")
		} else {
			parts = append(parts, loonQuote(uuid), "transport="+o.Network)
		}
		if n.Type == "vless" && o.Flow != "" {
			parts = append(parts, "flow="+o.Flow)
		}
		if o.Network == "ws" {
			parts = append(parts, "path="+loonField(o.Path))
			if o.Host != "" {
				parts = append(parts, "host="+loonField(o.Host))
			}
		}
		parts = append(parts, loonStreamSecurity(n.Type, o)...)
		parts = append(parts, "udp=true")
		return strings.Join(parts, ","), ""

	case "hysteria2":
		h := parseTLSHints(n.Config)
		parts := []string{name + " = Hysteria2", n.Host, port, loonQuote(uuid)}
		if h.SNI != "" {
			parts = append(parts, "sni="+h.SNI)
		}
		if h.Insecure {
			parts = append(parts, "skip-cert-verify=true")
		}
		if obfs := hysteria2ObfsPassword(n.Config); obfs != "" {
			parts = append(parts, "salamander-password="+obfs)
		}
		parts = append(parts, "udp=true")
		return strings.Join(parts, ","), ""

	case "anytls":
		if !anyTLSHasCertificate(n.Config) {
			return "", "AnyTLS 节点没有证书，节点端是明文而客户端强制 TLS"
		}
		h := parseTLSHints(n.Config)
		parts := []string{name + " = AnyTLS", n.Host, port, loonQuote(uuid)}
		if h.SNI != "" {
			parts = append(parts, "sni="+h.SNI)
		}
		if h.Insecure {
			parts = append(parts, "skip-cert-verify=true")
		}
		if prof := loonTLSProfile(anyTLSFingerprint(n.Config)); prof != "" {
			parts = append(parts, "tls-profile="+prof)
		}
		parts = append(parts, "udp=true")
		return strings.Join(parts, ","), ""

	case "http":
		scheme := "http"
		if cfgBool(n.Config, "tls") {
			scheme = "https"
		}
		line := name + " = " + scheme + "," + n.Host + "," + port + "," + uuid + "," + loonQuote(uuid)
		return line, ""

	case "socks":
		parts := []string{name + " = socks5", n.Host, port, uuid, loonQuote(uuid)}
		if cfgBool(n.Config, "tls") {
			parts = append(parts, "over-tls=true")
		}
		udp := strings.EqualFold(cfgStr(n.Config, "network", ""), "udp")
		parts = append(parts, "udp="+strconv.FormatBool(udp))
		return strings.Join(parts, ","), ""

	case "tuic", "naive", "juicity", "mieru":
		return "", "Loon 没有 " + n.Type + " 节点"
	default:
		return "", "Loon 没有 " + n.Type + " 节点"
	}
}

func loonStreamSecurity(nodeType string, o streamOpts) []string {
	var parts []string
	switch {
	case o.Reality:
		parts = append(parts, "public-key="+loonQuote(o.PublicKey))
		if o.ShortID != "" {
			parts = append(parts, "short-id="+o.ShortID)
		}
		if nodeType == "vmess" || nodeType == "vless" {
			parts = append(parts, "over-tls=true")
		}
		if o.SNI != "" {
			parts = append(parts, "sni="+o.SNI)
		}
		if prof := loonTLSProfile(o.Fingerprint); prof != "" {
			parts = append(parts, "tls-profile="+prof)
		}
	case o.TLS:
		if nodeType == "vmess" || nodeType == "vless" {
			parts = append(parts, "over-tls=true")
		}
		if o.SNI != "" {
			parts = append(parts, "sni="+o.SNI)
		}
		if o.Insecure {
			parts = append(parts, "skip-cert-verify=true")
		}
		if prof := loonTLSProfile(o.Fingerprint); prof != "" {
			parts = append(parts, "tls-profile="+prof)
		}
	default:
		if nodeType == "vmess" || nodeType == "vless" {
			parts = append(parts, "over-tls=false")
		}
	}
	return parts
}
