package render

// Stash 的 YAML 是 Clash 系写法，字段按 Stash 文档（https://stash.wiki/en/proxy-protocols/proxy-types）在 Meta 形状上调整；
// Stash 不认 v2ray-http-upgrade，没有 naive 出站。

import "strings"

// nodeToStash 返回一条 Stash 的 proxy 映射；不能渲染时返回 nil 和跳过原因。
func nodeToStash(n Node, uuid string) (map[string]any, string) {
	if n.Type == "naive" {
		return nil, "Stash 没有 naive 出站"
	}
	// Stash 文档的 2022 只写单个 password，没提 SIP023 多用户（iPSK:uPSK）；真机确认能连后再放开。
	if n.Type == "shadowsocks" && isSS2022(ssMethod(n.Config)) {
		return nil, "Stash 暂不支持 Shadowsocks 2022 多用户密钥（未经真机确认）"
	}
	if n.Type == "juicity" {
		p := map[string]any{
			"name":     n.Name,
			"type":     "juicity",
			"server":   n.Host,
			"port":     n.Port,
			"uuid":     uuid,
			"password": uuid,
			"udp":      true,
		}
		setClashTLSHints(p, parseTLSHints(n.Config))
		return p, ""
	}
	if n.Type == "vless" || n.Type == "vmess" || n.Type == "trojan" {
		o := parseStream(n, uuid)
		if o.Network == "httpupgrade" {
			return nil, "Stash 不支持 HTTP Upgrade 传输"
		}
	}

	p, reason := nodeToClash(n, uuid, false)
	if p == nil {
		return nil, strings.ReplaceAll(reason, "mihomo", "Stash")
	}
	delete(p, "ech-opts")

	switch n.Type {
	case "hysteria2":
		p["auth"] = p["password"]
		delete(p, "password")
	case "tuic":
		p["version"] = 5
	case "vless", "vmess":
		if sn, ok := p["servername"].(string); ok && sn != "" {
			p["sni"] = sn
		}
	}

	return p, ""
}
