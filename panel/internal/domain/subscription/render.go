package subscription

// 订阅格式转换。
//
// 同一批节点要变成三种完全不同的东西：Clash 的 YAML、sing-box 的 JSON、
// 以及一串 base64 编码的 URI。字段名不是猜的 —— Clash 那套按 mihomo
// adapter/outbound 里的 `proxy:` tag 核对过，sing-box 按 option 包核对过。
// 写错一个字段的后果是用户导入失败，而错误信息出现在他的客户端里，我们看不到。
//
// 不是每种协议在每种格式里都有对应物（Clash 没有 naive，URI 没有 shadowtls）。
// 遇到这种情况跳过该节点而不是编一个近似的写法：一条导入后连不上的线路
// 比少一条线路更糟，用户会以为是节点坏了。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Format 是输出格式。
type Format string

const (
	FormatClash   Format = "clash"
	FormatSingbox Format = "singbox"
	FormatURI     Format = "uri"
)

// DetectFormat 按 User-Agent 判断客户端想要什么。
//
// 这是「一个链接到处能用」的实现：用户拿到的订阅地址只有一个，
// 什么客户端来就给什么格式，不需要他自己去挑。显式参数只是给
// 特殊情况留的后门（比如在浏览器里想看某个具体格式）。
//
// # 认不出来的一律给 URI
//
// 判断错的代价不对称：给 Clash 用户一份 base64，它还能当订阅链接
// 直接导入；给一个只吃 base64 的客户端一份 YAML，它直接报错。
// 所以只有明确认识的 UA 才映射到 Clash / sing-box，其余全部回落到
// URI —— 那是兼容面最宽的一种。
//
// # 顺序有讲究
//
// Stash、FlClash 这些 UA 里都带 clash 字样，Hiddify 有些版本会同时
// 带 clash 和 sing-box。更具体的匹配必须排在前面，否则会被通配的
// clash 分支先截走。
func DetectFormat(ua, explicit string) Format {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "clash", "meta", "clashmeta", "clash-meta", "mihomo", "verge", "stash":
		return FormatClash
	case "singbox", "sing-box", "singbox-json", "hiddify", "karing":
		return FormatSingbox
	case "uri", "base64", "v2ray", "v2rayn", "v2rayng", "shadowrocket", "general":
		return FormatURI
	}

	l := strings.ToLower(ua)
	switch {
	// sing-box 内核系。SFI/SFA/SFM/SFT 分别是它的 iOS/Android/macOS/tvOS
	// 官方客户端，UA 里只有这四个缩写，认不出就只能回落。
	case strings.Contains(l, "sing-box"), strings.Contains(l, "sfi/"),
		strings.Contains(l, "sfa/"), strings.Contains(l, "sfm/"),
		strings.Contains(l, "sft/"), strings.Contains(l, "hiddify"),
		strings.Contains(l, "karing"):
		return FormatSingbox

	// 只认 URI/base64 的一批。放在 clash 分支之前：其中几个的 UA
	// 里也带 clash 字样，被通配截走就会拿到一份用不了的 YAML。
	case strings.Contains(l, "shadowrocket"), strings.Contains(l, "quantumult"),
		strings.Contains(l, "surge"), strings.Contains(l, "loon"),
		strings.Contains(l, "v2rayn"), strings.Contains(l, "v2rayng"),
		strings.Contains(l, "v2rayu"), strings.Contains(l, "qv2ray"),
		strings.Contains(l, "nekobox"), strings.Contains(l, "nekoray"),
		strings.Contains(l, "matsuri"), strings.Contains(l, "sagernet"),
		strings.Contains(l, "streisand"), strings.Contains(l, "potatso"),
		strings.Contains(l, "oneclick"), strings.Contains(l, "shadowsocks"):
		return FormatURI

	// Clash 内核系。
	case strings.Contains(l, "clash"), strings.Contains(l, "mihomo"),
		strings.Contains(l, "stash"), strings.Contains(l, "meta"),
		strings.Contains(l, "flclash"), strings.Contains(l, "nyanpasu"):
		return FormatClash

	default:
		return FormatURI
	}
}

// UAFamily 返回用于审计统计的客户端家族，不含任何可识别信息。
func UAFamily(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "sing-box"), strings.Contains(l, "sfi/"), strings.Contains(l, "sfa/"):
		return "sing-box"
	case strings.Contains(l, "shadowrocket"):
		return "shadowrocket"
	case strings.Contains(l, "clash"), strings.Contains(l, "mihomo"), strings.Contains(l, "stash"):
		return "clash"
	case strings.Contains(l, "quantumult"):
		return "quantumult"
	case strings.Contains(l, "surge"):
		return "surge"
	case strings.Contains(l, "v2ray"):
		return "v2ray"
	case l == "":
		return "none"
	default:
		return "other"
	}
}

// Render 把节点渲染成指定格式。
// 返回内容、Content-Type，以及实际写进去的节点数（可能少于传入的）。
func Render(f Format, nodes []Node, uuid string) ([]byte, string, int) {
	nodes = uniqueNodeNames(nodes)
	switch f {
	case FormatClash:
		return renderClash(nodes, uuid)
	case FormatSingbox:
		return renderSingbox(nodes, uuid)
	default:
		return renderURI(nodes, uuid)
	}
}

// uniqueNodeNames prevents duplicate or reserved Clash proxy names and sing-box
// outbound tags. ListNodes has deterministic ordering, so suffixes stay stable.
func uniqueNodeNames(nodes []Node) []Node {
	out := append([]Node(nil), nodes...)
	baseNames := make([]string, len(out))
	counts := make(map[string]int, len(out))
	for i := range out {
		base := strings.TrimSpace(out[i].Name)
		if base == "" {
			base = "节点"
		}
		baseNames[i] = base
		counts[base]++
	}
	reserved := map[string]bool{
		"direct": true, "block": true, "自动选择": true, "节点选择": true,
	}
	used := make(map[string]bool, len(out)+len(reserved))
	for name := range reserved {
		used[name] = true
	}
	// Preserve genuinely unique, non-reserved labels and reserve them before
	// assigning suffixes, so "香港, 香港, 香港 · 1" can never collide.
	for _, name := range baseNames {
		if counts[name] == 1 && !reserved[name] {
			used[name] = true
		}
	}
	next := make(map[string]int, len(counts))
	for i := range out {
		base := baseNames[i]
		if counts[base] == 1 && !reserved[base] {
			out[i].Name = base
			continue
		}
		index := next[base]
		if index < 1 {
			index = 1
		}
		candidate := ""
		for {
			candidate = fmt.Sprintf("%s · %d", base, index)
			index++
			if !used[candidate] {
				break
			}
		}
		next[base] = index
		used[candidate] = true
		out[i].Name = candidate
	}
	return out
}

//-----------------------------------------------------------------------------
// URI 列表（V2rayN / Shadowrocket / Quantumult 等）
//-----------------------------------------------------------------------------

func renderURI(nodes []Node, uuid string) ([]byte, string, int) {
	var lines []string
	for _, n := range nodes {
		if u := nodeToURI(n, uuid); u != "" {
			lines = append(lines, u)
		}
	}
	body := strings.Join(lines, "\n")
	// 整体 base64：这是这类客户端的既定约定，不是为了隐藏什么
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	return []byte(enc), "text/plain; charset=utf-8", len(lines)
}

func nodeToURI(n Node, uuid string) string {
	// 掩码参数在分享链接里无处安放，给出去的链接一定连不上。
	// 返回空串，renderURI 会跳过它。
	if hasUnshareableMask(n.Config) {
		return ""
	}
	addr := net.JoinHostPort(n.Host, strconv.Itoa(n.Port))
	frag := "#" + url.PathEscape(n.Name)
	q := url.Values{}

	switch n.Type {
	case "vless":
		q.Set("encryption", "none")
		q.Set("type", shareNetwork(n.Config))
		if cfgStr(n.Config, "security", "") == "reality" {
			// REALITY 的客户端参数：公钥和 short id 缺一不可，
			// 少了任何一个客户端都握不上手，而且报错通常只是「超时」。
			q.Set("security", "reality")
			q.Set("pbk", cfgStr(n.Config, "public_key", ""))
			if names := cfgStrings(n.Config, "server_names"); len(names) > 0 {
				// 只发第一个：SNI 是单值。多个 server_names 是给服务端
				// 用来接受多种握手的，客户端挑一个用就行。
				q.Set("sni", names[0])
			}
			if ids := cfgStrings(n.Config, "short_ids"); len(ids) > 0 {
				q.Set("sid", ids[0])
			}
			// 指纹决定客户端伪装成哪种浏览器。不给的话各家客户端默认值
			// 不一致，同一条订阅在不同客户端上表现会不一样。
			q.Set("fp", cfgStr(n.Config, "fingerprint", "chrome"))
		} else if cfgBool(n.Config, "tls") {
			q.Set("security", "tls")
			if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
				q.Set("sni", sni)
			}
		}
		if flow := cfgStr(n.Config, "flow", ""); flow != "" {
			q.Set("flow", flow)
		}
		if p := cfgStr(n.Config, "path", ""); p != "" {
			q.Set("path", p)
		}
		return "vless://" + uuid + "@" + addr + "?" + q.Encode() + frag

	case "vmess":
		// VMess 用的是自成一体的 base64(JSON)，不是标准 URI 查询串
		m := map[string]any{
			"v": "2", "ps": n.Name, "add": n.Host, "port": strconv.Itoa(n.Port),
			"id": uuid, "aid": "0", "scy": "auto",
			"net": shareNetwork(n.Config), "type": "none",
			"host": cfgStr(n.Config, "host", ""), "path": cfgStr(n.Config, "path", ""),
		}
		if cfgBool(n.Config, "tls") {
			m["tls"] = "tls"
			m["sni"] = cfgStr(n.Config, "server_name", "")
		}
		body, err := json.Marshal(m)
		if err != nil {
			return ""
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(body)

	case "trojan":
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			q.Set("sni", sni)
		}
		q.Set("type", shareNetwork(n.Config))
		return "trojan://" + url.QueryEscape(uuid) + "@" + addr + "?" + q.Encode() + frag

	case "shadowsocks":
		// SIP002：base64url(method:password) 放在 userinfo 位
		method := cfgStr(n.Config, "method", "aes-256-gcm")
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + uuid))
		return "ss://" + userinfo + "@" + addr + frag

	case "hysteria2":
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			q.Set("sni", sni)
		}
		if o := cfgStr(n.Config, "obfs_password", ""); o != "" {
			q.Set("obfs", "salamander")
			q.Set("obfs-password", o)
		}
		return "hysteria2://" + url.QueryEscape(uuid) + "@" + addr + "?" + q.Encode() + frag

	case "tuic":
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			q.Set("sni", sni)
		}
		q.Set("congestion_control", cfgStr(n.Config, "congestion_control", "bbr"))
		q.Set("alpn", "h3")
		// TUIC v5 的凭据是 uuid:password，我们两者同值
		return "tuic://" + uuid + ":" + url.QueryEscape(uuid) + "@" + addr + "?" + q.Encode() + frag

	case "anytls":
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			q.Set("sni", sni)
		}
		return "anytls://" + url.QueryEscape(uuid) + "@" + addr + "?" + q.Encode() + frag

	default:
		// naive / mieru / shadowtls / juicity 等没有被广泛接受的 URI 写法。
		// 编一个出来只会让客户端解析失败，不如不发。
		return ""
	}
}

//-----------------------------------------------------------------------------
// Clash / mihomo（YAML）
//-----------------------------------------------------------------------------

func renderClash(nodes []Node, uuid string) ([]byte, string, int) {
	var proxies []map[string]any
	var names []string

	for _, n := range nodes {
		p := nodeToClash(n, uuid)
		if p == nil {
			continue
		}
		proxies = append(proxies, p)
		names = append(names, n.Name)
	}

	var b strings.Builder
	b.WriteString("mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: info\n\nproxies:\n")
	for _, p := range proxies {
		b.WriteString("  - " + inlineYAML(p) + "\n")
	}

	// 策略组：一个自动选优、一个手动。没有策略组的订阅在 Clash 里
	// 只是一堆节点，用户没法切换，等于不可用。
	b.WriteString("\nproxy-groups:\n")
	b.WriteString("  - {name: 自动选择, type: url-test, url: 'http://www.gstatic.com/generate_204', interval: 300, proxies: [")
	b.WriteString(strings.Join(quoteAll(names), ", "))
	b.WriteString("]}\n")
	b.WriteString("  - {name: 节点选择, type: select, proxies: [自动选择, ")
	b.WriteString(strings.Join(quoteAll(names), ", "))
	b.WriteString("]}\n")
	b.WriteString("\nrules:\n  - GEOIP,CN,DIRECT\n  - MATCH,节点选择\n")

	return []byte(b.String()), "text/yaml; charset=utf-8", len(proxies)
}

func nodeToClash(n Node, uuid string) map[string]any {
	// mihomo 没有 mKCP。它的 network 只认 ws / grpc / h2 / http，塞一个
	// 不认识的进去，坏掉的不是这一个节点——整份 YAML 解析失败，用户的
	// 所有节点一起消失。跳过它，让订阅里其余节点还能用。
	if isMKCPNetwork(cfgStr(n.Config, "network", "")) {
		return nil
	}
	p := map[string]any{"name": n.Name, "server": n.Host, "port": n.Port, "udp": true}

	switch n.Type {
	case "vless":
		p["type"] = "vless"
		p["uuid"] = uuid
		p["network"] = cfgStr(n.Config, "network", "tcp")
		if cfgStr(n.Config, "security", "") == "reality" {
			// 字段名按 mihomo 的 vless adapter 来：reality-opts 里是
			// public-key 和 short-id，都带连字符。写成下划线不会报错，
			// 只会被忽略 —— 然后客户端拿一个没有 REALITY 参数的 vless
			// 去连，超时，而用户只看得到「节点不可用」。
			p["tls"] = true
			if names := cfgStrings(n.Config, "server_names"); len(names) > 0 {
				p["servername"] = names[0]
			}
			ro := map[string]any{"public-key": cfgStr(n.Config, "public_key", "")}
			if ids := cfgStrings(n.Config, "short_ids"); len(ids) > 0 {
				ro["short-id"] = ids[0]
			}
			p["reality-opts"] = ro
			p["client-fingerprint"] = cfgStr(n.Config, "fingerprint", "chrome")
		} else if cfgBool(n.Config, "tls") {
			p["tls"] = true
			if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
				p["servername"] = sni
			}
		}
		if flow := cfgStr(n.Config, "flow", ""); flow != "" {
			p["flow"] = flow
		}

	case "vmess":
		p["type"] = "vmess"
		p["uuid"] = uuid
		p["alterId"] = 0
		p["cipher"] = "auto"
		p["network"] = cfgStr(n.Config, "network", "tcp")
		if cfgBool(n.Config, "tls") {
			p["tls"] = true
			if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
				p["servername"] = sni
			}
		}

	case "trojan":
		p["type"] = "trojan"
		p["password"] = uuid
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			p["sni"] = sni
		}

	case "shadowsocks":
		p["type"] = "ss"
		p["password"] = uuid
		p["cipher"] = cfgStr(n.Config, "method", "aes-256-gcm")

	case "hysteria2":
		p["type"] = "hysteria2"
		p["password"] = uuid
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			p["sni"] = sni
		}
		if o := cfgStr(n.Config, "obfs_password", ""); o != "" {
			p["obfs"] = "salamander"
			p["obfs-password"] = o
		}

	case "tuic":
		p["type"] = "tuic"
		p["uuid"] = uuid
		p["password"] = uuid
		p["alpn"] = []string{"h3"}
		p["congestion-controller"] = cfgStr(n.Config, "congestion_control", "bbr")
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			p["sni"] = sni
		}

	case "anytls":
		p["type"] = "anytls"
		p["password"] = uuid
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			p["sni"] = sni
		}

	case "mieru":
		// mihomo 原生支持 mieru，字段与其它协议一致
		p["type"] = "mieru"
		p["username"] = uuid
		p["password"] = uuid
		p["transport"] = "TCP"

	case "shadowtls":
		// Clash 里 ShadowTLS 不是独立代理类型，而是挂在内层协议上的一组选项。
		// 我们的内层固定是 Shadowsocks，所以这里出的是一条带
		// shadow-tls-opts 的 ss 节点。
		p["type"] = "ss"
		p["password"] = uuid
		p["cipher"] = cfgStr(n.Config, "method", "aes-256-gcm")
		// plugin-opts 的字段名按 mihomo 的 shadowTLSOption（obfs: tag）核对：
		// password / host / version / alpn 等，没有 enable —— 多给一个未知字段
		// 会让 mihomo 解析这条代理时报错，整份订阅跟着导入失败。
		p["plugin"] = "shadow-tls"
		p["plugin-opts"] = map[string]any{
			"password": cfgStr(n.Config, "password", ""),
			"host":     cfgStr(n.Config, "handshake_server", ""),
			"version":  3,
		}

	default:
		// naive / juicity / socks / http：Clash 没有对等类型或极少使用
		return nil
	}
	return p
}

//-----------------------------------------------------------------------------
// sing-box（JSON）
//-----------------------------------------------------------------------------

func renderSingbox(nodes []Node, uuid string) ([]byte, string, int) {
	outs := []map[string]any{}
	var tags []string

	for _, n := range nodes {
		o := nodeToSingbox(n, uuid)
		if o == nil {
			continue
		}
		outs = append(outs, o)
		tags = append(tags, n.Name)
	}

	all := append([]map[string]any(nil), outs...)
	finalOutbound := "direct"
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
		finalOutbound = "节点选择"
	}
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})

	cfg := map[string]any{
		"log":       map[string]any{"level": "warn"},
		"outbounds": all,
		"route": map[string]any{
			"rules": []map[string]any{
				{"ip_is_private": true, "action": "route", "outbound": "direct"},
			},
			"final": finalOutbound,
		},
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return []byte("{}"), "application/json; charset=utf-8", 0
	}
	return body, "application/json; charset=utf-8", len(outs)
}

func nodeToSingbox(n Node, uuid string) map[string]any {
	// sing-box 也没有 mKCP。它的 v2ray transport 只有 http / ws / quic /
	// grpc / httpupgrade——没有 kcp 这一项，配置里根本无处安放。
	//
	// 不跳过的后果比 Clash 那边更隐蔽：sing-box 不会报错，它只会拿一个
	// 没有 transport 字段的 vless 出站去连 UDP 端口，当成裸 TCP。用户看到
	// 的是节点存在、能选中、就是连不上。
	if isMKCPNetwork(cfgStr(n.Config, "network", "")) {
		return nil
	}
	o := map[string]any{"tag": n.Name, "server": n.Host, "server_port": n.Port}
	tls := func() map[string]any {
		t := map[string]any{"enabled": true}
		if sni := cfgStr(n.Config, "server_name", ""); sni != "" {
			t["server_name"] = sni
		}
		return t
	}

	switch n.Type {
	case "vless":
		o["type"] = "vless"
		o["uuid"] = uuid
		if flow := cfgStr(n.Config, "flow", ""); flow != "" {
			o["flow"] = flow
		}
		if cfgStr(n.Config, "security", "") == "reality" {
			// sing-box 把 REALITY 放在 tls.reality 下面，另外要开 utls ——
			// 没有 utls 的话客户端用的是 Go 自己的 TLS 指纹，
			// 而那个指纹和它声称伪装的浏览器对不上，等于白伪装。
			t := map[string]any{"enabled": true}
			if names := cfgStrings(n.Config, "server_names"); len(names) > 0 {
				t["server_name"] = names[0]
			}
			r := map[string]any{
				"enabled":    true,
				"public_key": cfgStr(n.Config, "public_key", ""),
			}
			if ids := cfgStrings(n.Config, "short_ids"); len(ids) > 0 {
				r["short_id"] = ids[0]
			}
			t["reality"] = r
			t["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": cfgStr(n.Config, "fingerprint", "chrome"),
			}
			o["tls"] = t
		} else if cfgBool(n.Config, "tls") {
			o["tls"] = tls()
		}
	case "vmess":
		o["type"] = "vmess"
		o["uuid"] = uuid
		o["security"] = "auto"
		if cfgBool(n.Config, "tls") {
			o["tls"] = tls()
		}
	case "trojan":
		o["type"] = "trojan"
		o["password"] = uuid
		o["tls"] = tls()
	case "shadowsocks":
		o["type"] = "shadowsocks"
		o["method"] = cfgStr(n.Config, "method", "aes-256-gcm")
		o["password"] = uuid
	case "hysteria2":
		o["type"] = "hysteria2"
		o["password"] = uuid
		o["tls"] = tls()
		if ob := cfgStr(n.Config, "obfs_password", ""); ob != "" {
			o["obfs"] = map[string]any{"type": "salamander", "password": ob}
		}
	case "tuic":
		o["type"] = "tuic"
		o["uuid"] = uuid
		o["password"] = uuid
		o["congestion_control"] = cfgStr(n.Config, "congestion_control", "bbr")
		t := tls()
		t["alpn"] = []string{"h3"}
		o["tls"] = t
	case "anytls":
		o["type"] = "anytls"
		o["password"] = uuid
		o["tls"] = tls()
	case "naive":
		o["type"] = "naive"
		o["username"] = uuid
		o["password"] = uuid
		o["tls"] = tls()
	case "shadowtls":
		// sing-box 里 ShadowTLS 是一个独立出站，内层协议通过 detour 串联。
		// 这里出两个出站：ss 走 detour 指向 shadowtls。
		inner := map[string]any{
			"type": "shadowsocks", "tag": n.Name,
			"method": cfgStr(n.Config, "method", "aes-256-gcm"), "password": uuid,
			"detour": n.Name + "-stls",
		}
		return inner
	default:
		return nil
	}
	return o
}

//-----------------------------------------------------------------------------

func cfgStr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

// cfgBool 读一个布尔配置。
// 兼容 JSON 里可能出现的三种写法：真布尔、数字 1/0、字符串 "true"。
// 面板下发的 protocol_config 是 jsonb，历史数据里三种都存在过。
func cfgBool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case float64:
		return v > 0
	case string:
		return v == "true" || v == "1"
	}
	return false
}

// inlineYAML 把一个 proxy 映射写成 YAML 的行内流式写法。
//
// 自己拼而不引 YAML 库：proxy 条目的结构很浅（只有标量、字符串数组和一层嵌套），
// 行内写法足以覆盖，省掉一个依赖。键排序是为了输出稳定 ——
// map 遍历随机的话，同一份订阅每次拉取内容都不同，客户端会以为配置变了。
func inlineYAML(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// name / type / server / port 放前面，读起来更像人写的配置
	sort.SliceStable(keys, func(i, j int) bool {
		return yamlKeyRank(keys[i]) < yamlKeyRank(keys[j])
	})

	parts := make([]string, 0, len(m))
	for _, k := range keys {
		parts = append(parts, k+": "+yamlValue(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func yamlKeyRank(k string) int {
	switch k {
	case "name":
		return 0
	case "type":
		return 1
	case "server":
		return 2
	case "port":
		return 3
	default:
		return 10
	}
}

func yamlValue(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case []string:
		return "[" + strings.Join(quoteAll(t), ", ") + "]"
	case map[string]any:
		return inlineYAML(t)
	default:
		return fmt.Sprintf("%q", fmt.Sprint(t))
	}
}

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = strconv.Quote(s)
	}
	return out
}

// cfgStrings 读一个字符串数组配置。
//
// protocol_config 是 jsonb，反序列化后数组元素是 any，
// 所以不能直接断言成 []string。顺手兼容单个字符串的写法 ——
// server_names 只有一项时，手写配置的人很容易漏掉方括号。
func cfgStrings(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

// hasUnshareableMask 判断这个节点是否配了无法通过分享链接传达的掩码。
//
// mKCP 的掩码（finalmask）是 xray 新加的功能，而 vless:// 这类分享链接的
// 参数是早就定死的那一套，没有地方放掩码类型和密码——客户端从链接里
// 读不到这些，只会用裸 mKCP 去连一个开着加密的服务端。
//
// 结果不是「降级」而是「完全连不上」，且客户端不会报出有用的错：包发出去
// 服务端解不开直接丢弃，用户看到的就是节点转圈。所以这种节点必须整个从
// 订阅里去掉，而不是给一条注定失败的链接。
//
// 要用掩码的话，目前只能让用户手动往客户端配置文件里写 finalmask。等
// 分享链接规范补上对应参数，这里再放开。
func hasUnshareableMask(config map[string]any) bool {
	if !isMKCPNetwork(cfgStr(config, "network", "")) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfgStr(config, "mask", ""))) {
	case "", "none", "mkcp-original":
		return false
	}
	return true
}

// isMKCPNetwork 判断传输是不是 mKCP。面板里存 "mkcp"，从客户端配置抄
// 过来的写法是 "kcp"，两种都得认。
func isMKCPNetwork(network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "mkcp", "kcp", "m-kcp":
		return true
	}
	return false
}

// shareNetwork 是分享链接里该写的传输名。
//
// 面板内部统一用 "mkcp"，但 v2rayN 那一系的客户端只认 "kcp"——写 mkcp
// 它会当成未知传输，回退到 tcp，然后连不上。
func shareNetwork(config map[string]any) string {
	network := cfgStr(config, "network", "tcp")
	if isMKCPNetwork(network) {
		return "kcp"
	}
	return network
}
