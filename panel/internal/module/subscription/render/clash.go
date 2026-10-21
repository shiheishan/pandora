package render

// Clash / mihomo（YAML）。
//
// 字段名按 mihomo 文档与 adapter/outbound 的 `proxy:` tag：传输是 network，
// 参数在 ws-opts（path、headers.Host；HTTP Upgrade 是 network: ws 加
// v2ray-http-upgrade: true——mihomo 没有 httpupgrade 这个 network）、
// grpc-opts.grpc-service-name、xhttp-opts（path、host、mode）；REALITY 在
// reality-opts（public-key、short-id）；指纹是 client-fingerprint；跳过证书
// 校验是 skip-cert-verify。
//
// Premium 内核（ClashX、Clash for Windows、Clash for Android）只认 ss、vmess、
// trojan、socks5、http 与 ws / grpc 两种传输，见 nodeToClash 的 premium 分支。

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const defaultClashTemplate = "mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: info\n\n{{proxies}}\n\nproxy-groups:\n{{proxy_groups}}\n\nrules:\n  - GEOIP,CN,DIRECT\n  - MATCH,节点选择\n"

// renderClash 渲染 Clash 系配置（Clash Meta / Premium / Stash）。
// 用内置 defaultClashTemplate 做占位替换生成。
// 信息节点（剩余流量等）排在最前，仅进手动选择组（节点选择），不进自动选择组（自动选择）；
// 当没有真实节点时，不输出信息节点，节点数只计真实节点。
func renderClash(rc renderCtx, kind clashKind) ([]byte, string, int) {
	convert := func(n Node) map[string]any {
		if kind == clashStash {
			p, _ := nodeToStash(n, rc.uuid)
			return p
		}
		p, _ := nodeToClash(n, rc.uuid, kind == clashPremium)
		return p
	}

	infoNodes, realNodes := rc.splitInfo()

	realProxies := make([]map[string]any, 0, len(realNodes))
	realNames := make([]string, 0, len(realNodes))
	for _, n := range realNodes {
		p := convert(n)
		if p == nil {
			continue
		}
		realProxies = append(realProxies, p)
		realNames = append(realNames, n.Name)
	}

	// 一个真实节点都写不出时信息节点也不写
	var infoProxies []map[string]any
	var infoNames []string
	if len(realProxies) > 0 {
		for _, n := range infoNodes {
			p := convert(n)
			if p == nil {
				continue
			}
			infoProxies = append(infoProxies, p)
			infoNames = append(infoNames, n.Name)
		}
	}

	// 直接写进一个缓冲：每条节点单独拼成字符串再连接，200 个节点的订阅每次拉取要多分配几百 KB
	var w yamlWriter
	if len(realProxies) == 0 {
		w.b.WriteString("proxies: []")
	} else {
		w.b.WriteString("proxies:")
		for _, list := range [][]map[string]any{infoProxies, realProxies} {
			for _, p := range list {
				w.b.WriteString("\n  - ")
				w.inline(p)
			}
		}
	}
	proxiesVal := w.b.String()

	var proxyGroupsVal string

	if len(realProxies) == 0 {
		proxyGroupsVal = "  - {name: 节点选择, type: select, proxies: [DIRECT]}"
	} else {
		proxyNamesVal := joinQuoted(realNames)
		selectNames := proxyNamesVal
		if len(infoNames) > 0 {
			selectNames = joinQuoted(infoNames) + ", " + proxyNamesVal
		}
		proxyGroupsVal = "  - {name: 自动选择, type: url-test, url: 'http://www.gstatic.com/generate_204', interval: 300, proxies: [" +
			proxyNamesVal + "]}\n" +
			"  - {name: 节点选择, type: select, proxies: [自动选择, " + selectNames + "]}"
	}

	body := fillTemplate(defaultClashTemplate, map[string]string{
		phProxies:     proxiesVal,
		phProxyGroups: proxyGroupsVal,
	})

	return body, "text/yaml; charset=utf-8", len(realProxies)
}

// nodeToClash 返回一条 proxy；不能渲染时返回 nil 和跳过原因。
func nodeToClash(n Node, uuid string, premium bool) (map[string]any, string) {
	if premium {
		if reason := clashPremiumUnsupported(n); reason != "" {
			return nil, reason
		}
	}
	p := map[string]any{"name": n.Name, "server": n.Host, "port": n.Port, "udp": true}

	switch n.Type {
	case "vless", "vmess", "trojan":
		o := parseStream(n, uuid)
		if reason := clashStreamUnsupported(n, o); reason != "" {
			return nil, reason
		}
		switch n.Type {
		case "vless":
			p["type"] = "vless"
			p["uuid"] = uuid
		case "vmess":
			p["type"] = "vmess"
			p["uuid"] = uuid
			p["alterId"] = 0
			p["cipher"] = "auto"
		case "trojan":
			p["type"] = "trojan"
			p["password"] = uuid
		}
		setClashStream(p, n, o, premium)

	case "shadowsocks":
		pw, reason := ssPassword(n.Config, uuid)
		if reason != "" {
			return nil, reason
		}
		p["type"] = "ss"
		p["password"] = pw
		p["cipher"] = ssMethod(n.Config)
		// 节点端的 Shadowsocks 同一端口 TCP、UDP 都听，udp 沿用上面的缺省 true

	case "hysteria2":
		p["type"] = "hysteria2"
		p["password"] = uuid
		setClashTLSHints(p, parseTLSHints(n.Config))
		if o := hysteria2ObfsPassword(n.Config); o != "" {
			p["obfs"] = "salamander"
			p["obfs-password"] = o
		}

	case "tuic":
		p["type"] = "tuic"
		p["uuid"] = uuid
		p["password"] = uuid
		p["alpn"] = []string{"h3"}
		p["congestion-controller"] = cfgStr(n.Config, "congestion_control", "bbr")
		setClashTLSHints(p, parseTLSHints(n.Config))

	case "anytls":
		if !anyTLSHasCertificate(n.Config) {
			return nil, "AnyTLS 节点没有证书，节点端是明文而客户端强制 TLS"
		}
		p["type"] = "anytls"
		p["password"] = uuid
		setClashTLSHints(p, parseTLSHints(n.Config))
		p["client-fingerprint"] = anyTLSFingerprint(n.Config)

	case "mieru":
		// mihomo 原生支持 mieru；transport 必须与服务端一致（TCP / UDP）
		p["type"] = "mieru"
		p["username"] = uuid
		p["password"] = uuid
		p["transport"] = mieruTransport(n.Config)

	case "shadowtls":
		// Clash 里 ShadowTLS 不是独立代理类型，而是挂在内层协议上的一组选项。
		// 我们的内层固定是 Shadowsocks，所以这里出的是一条带
		// shadow-tls 插件的 ss 节点。
		p["type"] = "ss"
		p["password"] = uuid
		p["cipher"] = ssMethod(n.Config)
		// ShadowTLS 只承载 TCP，UDP 不经它走
		p["udp"] = false
		// plugin-opts 的字段名按 mihomo 的 shadowTLSOption（obfs: tag）核对：
		// password / host / version / alpn 等，没有 enable —— 多给一个未知字段
		// 会让 mihomo 解析这条代理时报错，整份订阅跟着导入失败。
		p["plugin"] = "shadow-tls"
		// v3 需要 uTLS 指纹才能把握手伪装成浏览器（mihomo 文档的 shadow-tls 示例）
		p["client-fingerprint"] = "chrome"
		p["plugin-opts"] = map[string]any{
			"password": cfgStr(n.Config, "password", ""),
			"host":     shadowTLSHandshakeServer(n.Config),
			"version":  3,
		}

	case "socks":
		p["type"] = "socks5"
		p["username"] = uuid
		p["password"] = uuid
		p["udp"] = strings.EqualFold(cfgStr(n.Config, "network", ""), "udp")
		if cfgBool(n.Config, "tls") {
			p["tls"] = true
		}

	case "http":
		p["type"] = "http"
		p["username"] = uuid
		p["password"] = uuid
		delete(p, "udp")
		if cfgBool(n.Config, "tls") {
			p["tls"] = true
		}

	case "naive":
		return nil, "mihomo 没有 naive 出站"
	case "juicity":
		return nil, "mihomo 没有 juicity 出站"
	default:
		return nil, n.Type + " 在 Clash 里没有对应类型"
	}
	if !premium && len(n.ECHConfigList) > 0 {
		switch n.Type {
		case "vless", "vmess", "trojan":
			o := parseStream(n, uuid)
			if o.TLS && !o.Reality {
				p["ech-opts"] = clashECHOpts(n.ECHConfigList)
			}
		case "anytls":
			p["ech-opts"] = clashECHOpts(n.ECHConfigList)
		}
	}
	return p, ""
}

// clashPremiumUnsupported 是 Premium 内核不认识的组合。给它任何一条不认识的
// 代理，整份 YAML 加载失败，所以宁可少给。
func clashPremiumUnsupported(n Node) string {
	switch n.Type {
	case "shadowsocks":
		if isSS2022(ssMethod(n.Config)) {
			return "Clash Premium 不支持 Shadowsocks 2022"
		}
		return ""
	case "socks", "http":
		return ""
	case "vmess", "trojan":
		o := parseStream(n, "")
		if o.Reality {
			return "Clash Premium 不支持 REALITY"
		}
		switch o.Network {
		case "tcp", "ws", "grpc":
			return ""
		}
		return "Clash Premium 不支持 " + o.Network + " 传输"
	}
	return "Clash Premium 不支持 " + n.Type
}

// clashStreamUnsupported 是 mihomo 表达不了的传输组合。
func clashStreamUnsupported(n Node, o streamOpts) string {
	if o.Unsupported != "" {
		return o.Unsupported
	}
	switch o.Network {
	case "mkcp":
		// mihomo 没有 mKCP。它的 network 只认 ws / grpc / h2 / http / xhttp，
		// 塞一个不认识的进去，好的情况是被当成 tcp 连不上，坏的情况是整份配置被拒。
		return "mihomo 没有 mKCP 传输"
	case "xhttp":
		if n.Type != "vless" {
			return "mihomo 的 xhttp 只支持 vless"
		}
	case "grpc":
		if reason := grpcAuthorityMismatch(n, o); reason != "" {
			return reason
		}
	}
	return ""
}

// setClashStream 写 vless / vmess / trojan 的传输与安全层字段。
func setClashStream(p map[string]any, n Node, o streamOpts, premium bool) {
	switch {
	case o.Reality:
		// 字段名按 mihomo：reality-opts 里是 public-key 和 short-id，都带连字符。
		// 写成下划线不会报错，只会被忽略 —— 然后客户端拿一个没有 REALITY
		// 参数的节点去连，超时，而用户只看得到「节点不可用」。
		ro := map[string]any{"public-key": o.PublicKey}
		if o.ShortID != "" {
			ro["short-id"] = o.ShortID
		}
		p["reality-opts"] = ro
		p["client-fingerprint"] = o.Fingerprint
		if n.Type != "trojan" {
			p["tls"] = true
		}
		setClashSNI(p, n.Type, o.SNI)
	case o.TLS:
		if n.Type != "trojan" {
			p["tls"] = true
		}
		setClashSNI(p, n.Type, o.SNI)
		if o.Insecure {
			p["skip-cert-verify"] = true
		}
		if o.Fingerprint != "" && !premium {
			p["client-fingerprint"] = o.Fingerprint
		}
	}
	if o.Flow != "" && n.Type == "vless" {
		p["flow"] = o.Flow
	}
	switch o.Network {
	case "ws":
		p["network"] = "ws"
		p["ws-opts"] = map[string]any{"path": o.Path, "headers": map[string]any{"Host": o.Host}}
	case "httpupgrade":
		p["network"] = "ws"
		p["ws-opts"] = map[string]any{
			"path": o.Path, "headers": map[string]any{"Host": o.Host},
			"v2ray-http-upgrade": true,
		}
	case "grpc":
		p["network"] = "grpc"
		p["grpc-opts"] = map[string]any{"grpc-service-name": o.ServiceName}
	case "xhttp":
		p["network"] = "xhttp"
		p["xhttp-opts"] = map[string]any{"path": o.Path, "host": o.Host, "mode": o.Mode}
	default:
		p["network"] = "tcp"
	}
}

// setClashSNI：vless / vmess 的 SNI 字段叫 servername，trojan 叫 sni。
func setClashSNI(p map[string]any, nodeType, sni string) {
	if sni == "" {
		return
	}
	if nodeType == "trojan" {
		p["sni"] = sni
		return
	}
	p["servername"] = sni
}

// clashECHOpts 是 mihomo 的 ech-opts：enable 与 ECHConfigList 的 base64。
func clashECHOpts(list []byte) map[string]any {
	return map[string]any{
		"enable": true,
		"config": base64.StdEncoding.EncodeToString(list),
	}
}

// setClashTLSHints：hysteria2 / tuic / anytls 的 SNI 叫 sni。
func setClashTLSHints(p map[string]any, h tlsHints) {
	if h.SNI != "" {
		p["sni"] = h.SNI
	}
	if h.Insecure {
		p["skip-cert-verify"] = true
	}
}

// inlineYAML 把一个 proxy 映射写成 YAML 的行内流式写法。
//
// 自己拼而不引 YAML 库：proxy 条目的结构很浅（只有标量、字符串数组和一层嵌套），
// 行内写法足以覆盖，省掉一个依赖。键排序是为了输出稳定 ——
// map 遍历随机的话，同一份订阅每次拉取内容都不同，客户端会以为配置变了。
func inlineYAML(m map[string]any) string {
	var w yamlWriter
	w.inline(m)
	return w.b.String()
}

// yamlWriter 把行内 YAML 直接写进一个缓冲；q 是加引号用的暂存，免得每个字符串单独分配。
type yamlWriter struct {
	b strings.Builder
	q []byte
}

func (w *yamlWriter) quote(s string) {
	w.q = strconv.AppendQuote(w.q[:0], s)
	w.b.Write(w.q)
}

func (w *yamlWriter) inline(m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// name / type / server / port 放前面，读起来更像人写的配置；其余按键名排
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(yamlKeyRank(a), yamlKeyRank(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})

	w.b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			w.b.WriteString(", ")
		}
		if yamlPlainKey(k) {
			w.b.WriteString(k)
		} else {
			w.quote(k)
		}
		w.b.WriteString(": ")
		w.value(m[k])
	}
	w.b.WriteByte('}')
}

// yamlPlainKey 报告键能否不加引号（headers 里的 Host 等都是普通标识符）。
func yamlPlainKey(k string) bool {
	for _, r := range k {
		plain := r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !plain {
			return false
		}
	}
	return true
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

func (w *yamlWriter) value(v any) {
	switch t := v.(type) {
	case string:
		w.quote(t)
	case bool:
		w.b.WriteString(strconv.FormatBool(t))
	case int:
		w.q = strconv.AppendInt(w.q[:0], int64(t), 10)
		w.b.Write(w.q)
	case []string:
		w.b.WriteByte('[')
		for i, s := range t {
			if i > 0 {
				w.b.WriteString(", ")
			}
			w.quote(s)
		}
		w.b.WriteByte(']')
	case map[string]any:
		w.inline(t)
	default:
		w.b.WriteString(fmt.Sprintf("%q", fmt.Sprint(t)))
	}
}

// joinQuoted 把名字逐个加引号、用 ", " 连起来（策略组的 proxies 列表）。
func joinQuoted(items []string) string {
	var w yamlWriter
	for i, s := range items {
		if i > 0 {
			w.b.WriteString(", ")
		}
		w.quote(s)
	}
	return w.b.String()
}
