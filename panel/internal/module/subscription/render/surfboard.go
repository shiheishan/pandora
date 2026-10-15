package render

// Surfboard 整份配置，写法按 Surfboard 文档（https://getsurfboard.com/docs/profile-format/proxy/external-proxy/<协议>/）。
// 字段之间用 `, `（逗号加空格）。

import (
	"strconv"
	"strings"
)

const defaultSurfboardTemplate = `#!MANAGED-CONFIG {{subscribe_url}} interval=43200 strict=false

[General]
loglevel = notify
dns-server = system, 223.5.5.5, 119.29.29.29
skip-proxy = 127.0.0.1, 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 100.64.0.0/10, localhost, *.local
proxy-test-url = http://www.gstatic.com/generate_204
internet-test-url = http://www.gstatic.com/generate_204

[Proxy]
{{proxies}}

[Proxy Group]
{{proxy_groups}}

[Rule]
GEOIP,CN,DIRECT
FINAL,节点选择
`

func renderSurfboard(rc renderCtx) ([]byte, string, int) {
	info, real := rc.splitInfo()
	var realLines []string
	var realNames []string
	for _, n := range real {
		line, _ := nodeToSurfboard(n, rc.uuid)
		if line != "" {
			realLines = append(realLines, line)
			realNames = append(realNames, n.Name)
		}
	}
	lines := realLines
	var infoNames []string
	if len(realLines) > 0 {
		var infoLines []string
		for _, n := range info {
			line, _ := nodeToSurfboard(n, rc.uuid)
			if line != "" {
				infoLines = append(infoLines, line)
				infoNames = append(infoNames, n.Name)
			}
		}
		lines = append(infoLines, realLines...)
	}
	tmpl := rc.tmpl
	if tmpl == "" {
		tmpl = defaultSurfboardTemplate
	}
	if rc.subscribeURL == "" {
		tmpl = dropLinesWith(tmpl, phSubscribeURL)
	}
	var proxyGroups string
	if len(realNames) == 0 {
		proxyGroups = "节点选择 = select, DIRECT"
	} else {
		auto := "自动选择 = url-test, " + strings.Join(realNames, ", ") + ", url=http://www.gstatic.com/generate_204, interval=600"
		selectNames := append([]string{"自动选择"}, infoNames...)
		selectNames = append(selectNames, realNames...)
		manual := "节点选择 = select, " + strings.Join(selectNames, ", ")
		proxyGroups = auto + "\n" + manual
	}
	proxyNames := strings.Join(realNames, ", ")
	if proxyNames == "" {
		proxyNames = "DIRECT"
	}
	body := fillTemplate(tmpl, map[string]string{
		phProxies:      strings.Join(lines, "\n"),
		phProxyGroups:  proxyGroups,
		phProxyNames:   proxyNames,
		phSubscribeURL: rc.subscribeURL,
	})
	return body, "text/plain; charset=utf-8", len(realLines)
}

// nodeToSurfboard 在拼好一行后检查换行：Surfboard 一行一个节点，字段里的 \r/\n 会把配置拆成多行。
func nodeToSurfboard(n Node, uuid string) (string, string) {
	line, reason := surfboardNodeLine(n, uuid)
	if line != "" && strings.ContainsAny(line, "\r\n") {
		return "", "字段里有换行，写不进 Surfboard 的一行"
	}
	return line, reason
}

func surfboardNodeLine(n Node, uuid string) (string, string) {
	port := strconv.Itoa(n.Port)
	name := n.Name

	switch n.Type {
	case "shadowsocks":
		// 节点端的 Shadowsocks 同一端口 TCP、UDP 都听（与 clash.go 的 udp: true 同一口径）
		method := ssMethod(n.Config)
		line := name + " = ss, " + n.Host + ", " + port + ", encrypt-method=" + method + ", password=" + uuid + ", udp-relay=true"
		return line, ""

	case "vmess", "trojan":
		o := parseStream(n, uuid)
		if o.Unsupported != "" {
			return "", o.Unsupported
		}
		if o.Reality {
			return "", "Surfboard 不支持 REALITY"
		}
		if o.Network != "tcp" && o.Network != "ws" {
			return "", "Surfboard 不支持 " + o.Network + " 传输"
		}
		var proto string
		if n.Type == "vmess" {
			proto = "vmess"
		} else {
			proto = "trojan"
		}
		parts := []string{name + " = " + proto, n.Host, port}
		if n.Type == "vmess" {
			parts = append(parts, "username="+uuid)
		} else {
			parts = append(parts, "password="+uuid)
		}
		if o.Network == "ws" {
			parts = append(parts, "ws=true", "ws-path="+o.Path)
			if o.Host != "" {
				parts = append(parts, "ws-headers=Host:"+o.Host)
			}
		}
		if o.TLS {
			parts = append(parts, "tls=true")
			if o.SNI != "" {
				parts = append(parts, "sni="+o.SNI)
			}
			if o.Insecure {
				parts = append(parts, "skip-cert-verify=true")
			}
		}
		if n.Type == "vmess" {
			parts = append(parts, "vmess-aead=true", "udp-relay=true")
		} else {
			parts = append(parts, "udp-relay=true")
		}
		return strings.Join(parts, ", "), ""

	case "vless":
		return "", "Surfboard 没有 vless 节点"

	case "anytls":
		if !anyTLSHasCertificate(n.Config) {
			return "", "AnyTLS 节点没有证书，节点端是明文而客户端强制 TLS"
		}
		h := parseTLSHints(n.Config)
		parts := []string{name + " = anytls", n.Host, port, uuid}
		if h.SNI != "" {
			parts = append(parts, "sni="+h.SNI)
		}
		if h.Insecure {
			parts = append(parts, "skip-cert-verify=true")
		}
		return strings.Join(parts, ", "), ""

	case "hysteria2":
		h := parseTLSHints(n.Config)
		parts := []string{name + " = hysteria2", n.Host, port, "password=" + uuid}
		if h.SNI != "" {
			parts = append(parts, "sni="+h.SNI)
		}
		if h.Insecure {
			parts = append(parts, "skip-cert-verify=true")
		}
		if obfs := hysteria2ObfsPassword(n.Config); obfs != "" {
			parts = append(parts, "salamander-password="+obfs)
		}
		parts = append(parts, "udp-relay=true")
		return strings.Join(parts, ", "), ""

	case "tuic":
		h := parseTLSHints(n.Config)
		parts := []string{
			name + " = tuic-v5", n.Host, port,
			"uuid=" + uuid, "password=" + uuid, "alpn=h3",
		}
		if h.SNI != "" {
			parts = append(parts, "sni="+h.SNI)
		}
		if h.Insecure {
			parts = append(parts, "skip-cert-verify=true")
		}
		parts = append(parts, "udp-relay=true")
		return strings.Join(parts, ", "), ""

	case "socks":
		proto := "socks5"
		if cfgBool(n.Config, "tls") {
			proto = "socks5-tls"
		}
		udp := strings.EqualFold(cfgStr(n.Config, "network", ""), "udp")
		line := name + " = " + proto + ", " + n.Host + ", " + port + ", " + uuid + ", " + uuid + ", udp-relay=" + strconv.FormatBool(udp)
		return line, ""

	case "http":
		proto := "http"
		if cfgBool(n.Config, "tls") {
			proto = "https"
		}
		line := name + " = " + proto + ", " + n.Host + ", " + port + ", " + uuid + ", " + uuid
		return line, ""

	case "shadowtls", "naive", "juicity", "mieru":
		return "", "Surfboard 没有 " + n.Type + " 节点"
	default:
		return "", "Surfboard 没有 " + n.Type + " 节点"
	}
}
