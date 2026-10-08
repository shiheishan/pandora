package subscription

// sing-box（JSON）。
//
// 字段名按 sing-box 的 option 包（1.12 / 1.13）：传输在 transport 里，
// ws 是 {type, path, headers.Host}、httpupgrade 是 {type, host, path}、grpc 是
// {type, service_name}；sing-box 没有 xhttp 与 mKCP，这两类节点跳过。TLS 在
// tls 里（server_name、insecure、utls、reality）。ShadowTLS 是独立出站，内层
// Shadowsocks 经 detour 串上去，两个出站缺一不可：只有内层时 sing-box 启动报
// dependency not found，整份配置起不来、所有节点一起失效。

import (
	"encoding/json"
	"fmt"
	"strings"
)

func renderSingbox(nodes []Node, uuid string, dialect singboxDialect) ([]byte, string, int) {
	outs := []map[string]any{}
	var helpers []map[string]any
	var tags []string
	used := map[string]bool{"direct": true, "节点选择": true, "自动选择": true}
	for _, n := range nodes {
		used[n.Name] = true
	}

	for _, n := range nodes {
		o, _ := nodeToSingbox(n, uuid)
		if o == nil {
			continue
		}
		if n.Type == "shadowtls" {
			// 外层出站的 tag 要避开所有节点名与内置名，否则两个出站同名
			stlsTag := uniqueTag(n.Name+" · shadowtls", used)
			o["detour"] = stlsTag
			helpers = append(helpers, singboxShadowTLSOutbound(n, stlsTag))
		}
		outs = append(outs, o)
		tags = append(tags, n.Name)
	}

	all := append([]map[string]any(nil), outs...)
	all = append(all, helpers...)
	finalOutbound, ruleSetVia := "direct", ""
	if len(tags) > 0 {
		selector := map[string]any{
			"type": "selector", "tag": "节点选择",
			"outbounds": append([]string{"自动选择"}, tags...),
		}
		urltest := map[string]any{
			"type": "urltest", "tag": "自动选择",
			"outbounds": tags, "url": "http://www.gstatic.com/generate_204", "interval": "5m",
		}
		all = append([]map[string]any{selector, urltest}, all...)
		finalOutbound, ruleSetVia = "节点选择", "自动选择"
	}
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})

	// 入站、DNS 与路由模板见 render_singbox_template.go
	body, err := json.MarshalIndent(singboxFrame(all, finalOutbound, ruleSetVia, dialect), "", "  ")
	if err != nil {
		return []byte("{}"), "application/json; charset=utf-8", 0
	}
	return body, "application/json; charset=utf-8", len(outs)
}

func uniqueTag(base string, used map[string]bool) string {
	candidate := base
	for i := 2; used[candidate]; i++ {
		candidate = fmt.Sprintf("%s %d", base, i)
	}
	used[candidate] = true
	return candidate
}

// nodeToSingbox 返回一个出站；不能渲染时返回 nil 和跳过原因。
// ShadowTLS 的外层出站由 renderSingbox 补（它要知道全部 tag 才能取名）。
func nodeToSingbox(n Node, uuid string) (map[string]any, string) {
	o := map[string]any{"tag": n.Name, "server": n.Host, "server_port": n.Port}

	switch n.Type {
	case "vless", "vmess", "trojan":
		s := parseStream(n, uuid)
		if reason := singboxStreamUnsupported(n, s); reason != "" {
			return nil, reason
		}
		switch n.Type {
		case "vless":
			o["type"] = "vless"
			o["uuid"] = uuid
			if s.Flow != "" {
				o["flow"] = s.Flow
			}
		case "vmess":
			o["type"] = "vmess"
			o["uuid"] = uuid
			o["security"] = "auto"
		case "trojan":
			o["type"] = "trojan"
			o["password"] = uuid
		}
		if t := singboxStreamTLS(s); t != nil {
			o["tls"] = t
		}
		if t := singboxTransport(s); t != nil {
			o["transport"] = t
		}

	case "shadowsocks":
		if ssConflict(n.Config) {
			return nil, "Shadowsocks 的 cipher 与 method 冲突，节点端拒绝下发"
		}
		o["type"] = "shadowsocks"
		o["method"] = ssMethod(n.Config)
		o["password"] = uuid
		// 节点端只监听 TCP（见 render_clash.go 同一处），声明只走 TCP，
		// UDP 交给路由的其它出站，而不是发到没人听的端口
		o["network"] = "tcp"

	case "hysteria2":
		o["type"] = "hysteria2"
		o["password"] = uuid
		o["tls"] = singboxTLS(parseTLSHints(n.Config))
		if ob := hysteria2ObfsPassword(n.Config); ob != "" {
			o["obfs"] = map[string]any{"type": "salamander", "password": ob}
		}

	case "tuic":
		o["type"] = "tuic"
		o["uuid"] = uuid
		o["password"] = uuid
		o["congestion_control"] = cfgStr(n.Config, "congestion_control", "bbr")
		t := singboxTLS(parseTLSHints(n.Config))
		t["alpn"] = []string{"h3"}
		o["tls"] = t

	case "anytls":
		if !anyTLSHasCertificate(n.Config) {
			return nil, "AnyTLS 节点没有证书，节点端是明文而 sing-box 强制 TLS"
		}
		o["type"] = "anytls"
		o["password"] = uuid
		t := singboxTLS(parseTLSHints(n.Config))
		t["utls"] = map[string]any{"enabled": true, "fingerprint": anyTLSFingerprint(n.Config)}
		o["tls"] = t

	case "naive":
		h := parseTLSHints(n.Config)
		if h.Insecure {
			return nil, "Naive 客户端（Chromium 网络栈）不支持跳过证书校验"
		}
		o["type"] = "naive"
		o["username"] = uuid
		o["password"] = uuid
		o["tls"] = singboxTLS(h)

	case "shadowtls":
		// 内层 Shadowsocks；server / server_port 保留（sing-box 要求有），实际
		// 连接经 detour 交给外层 ShadowTLS 出站。
		o["type"] = "shadowsocks"
		o["method"] = ssMethod(n.Config)
		o["password"] = uuid
		o["network"] = "tcp"

	case "socks":
		if cfgBool(n.Config, "tls") {
			return nil, "sing-box 的 socks 出站不支持 TLS"
		}
		o["type"] = "socks"
		o["version"] = "5"
		o["username"] = uuid
		o["password"] = uuid
		if !strings.EqualFold(cfgStr(n.Config, "network", ""), "udp") {
			o["network"] = "tcp"
		}

	case "http":
		o["type"] = "http"
		o["username"] = uuid
		o["password"] = uuid
		if cfgBool(n.Config, "tls") {
			o["tls"] = map[string]any{"enabled": true}
		}

	case "mieru":
		return nil, "sing-box 没有 mieru 出站"
	case "juicity":
		return nil, "sing-box 没有 juicity 出站"
	default:
		return nil, n.Type + " 在 sing-box 里没有对应出站"
	}
	return o, ""
}

// singboxShadowTLSOutbound 是 ShadowTLS v3 外层出站：口令是节点的共享外层
// 密码，SNI 是借用握手的站点，uTLS 让握手长得像浏览器。
func singboxShadowTLSOutbound(n Node, tag string) map[string]any {
	return map[string]any{
		"type": "shadowtls", "tag": tag,
		"server": n.Host, "server_port": n.Port,
		"version":  3,
		"password": cfgStr(n.Config, "password", ""),
		"tls": map[string]any{
			"enabled":     true,
			"server_name": shadowTLSHandshakeServer(n.Config),
			"utls":        map[string]any{"enabled": true, "fingerprint": "chrome"},
		},
	}
}

// singboxStreamUnsupported 是 sing-box 表达不了的传输组合。
func singboxStreamUnsupported(n Node, s streamOpts) string {
	if s.Unsupported != "" {
		return s.Unsupported
	}
	switch s.Network {
	case "mkcp":
		// sing-box 的 v2ray transport 只有 http / ws / quic / grpc / httpupgrade，
		// 没有 kcp。不跳过的话它不会报错，只会拿一个没有 transport 的出站去连
		// UDP 端口，当成裸 TCP：节点存在、能选中、就是连不上。
		return "sing-box 没有 mKCP 传输"
	case "xhttp":
		// 同理：当成裸 vless 去连 XHTTP 入站，连不上
		return "sing-box 没有 XHTTP 传输"
	case "grpc":
		if reason := grpcAuthorityMismatch(n, s); reason != "" {
			return reason
		}
	}
	return ""
}

// singboxStreamTLS 是 vless / vmess / trojan 的 tls 段；不加密时返回 nil。
func singboxStreamTLS(s streamOpts) map[string]any {
	switch {
	case s.Reality:
		// sing-box 把 REALITY 放在 tls.reality 下面，另外要开 utls ——
		// 没有 utls 的话客户端用的是 Go 自己的 TLS 指纹，
		// 而那个指纹和它声称伪装的浏览器对不上，等于白伪装。
		t := map[string]any{"enabled": true}
		if s.SNI != "" {
			t["server_name"] = s.SNI
		}
		r := map[string]any{"enabled": true, "public_key": s.PublicKey}
		if s.ShortID != "" {
			r["short_id"] = s.ShortID
		}
		t["reality"] = r
		t["utls"] = map[string]any{"enabled": true, "fingerprint": s.Fingerprint}
		return t
	case s.TLS:
		t := singboxTLS(tlsHints{SNI: s.SNI, Insecure: s.Insecure})
		if s.Fingerprint != "" {
			t["utls"] = map[string]any{"enabled": true, "fingerprint": s.Fingerprint}
		}
		return t
	}
	return nil
}

func singboxTLS(h tlsHints) map[string]any {
	t := map[string]any{"enabled": true}
	if h.SNI != "" {
		t["server_name"] = h.SNI
	}
	if h.Insecure {
		t["insecure"] = true
	}
	return t
}

// singboxTransport 是 v2ray transport 段；tcp 返回 nil。
func singboxTransport(s streamOpts) map[string]any {
	switch s.Network {
	case "ws":
		return map[string]any{"type": "ws", "path": s.Path, "headers": map[string]any{"Host": s.Host}}
	case "httpupgrade":
		return map[string]any{"type": "httpupgrade", "path": s.Path, "host": s.Host}
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": s.ServiceName}
	}
	return nil
}
