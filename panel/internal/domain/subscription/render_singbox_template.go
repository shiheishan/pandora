package subscription

// sing-box 订阅附带的入站、DNS 与路由模板（用户 2026-10-07 定，参照 Xboard 的 sing-box 模板）。
//
// 官方客户端（SFI / SFA / SFM / SFT）把订阅当成整份配置跑：没有 inbound 就不接管任何流量，
// 没有 dns 段就用系统 DNS（国外域名被污染），导入了也用不了。所以订阅里附带：
//   - TUN 入站（auto_route + strict_route），移动端与桌面端的 VPN 模式都靠它；
//   - DNS 国内外分流：geosite-cn 的域名走国内 DoH 直连，其余走境外 DoH 经代理；
//     节点地址本身用国内 DoH 解析（default_domain_resolver），不绕代理去解代理的地址；
//   - 基础路由：先嗅探（TUN 只看得到 IP，按域名分流要先认出 SNI / Host）、劫持 DNS，
//     私网直连、国内（geosite-cn / geoip-cn）直连，其余走「节点选择」；广告拦截可选（默认关）。
// 规则集用远程 rule-set（二进制 .srs），地址前缀可配置，缺省是 SagerNet 公开发布的
// sing-geosite / sing-geoip 的 rule-set 分支；经「自动选择」（urltest）下载（用户 2026-10-08 定），
// 国内直连 GitHub 常常不通，跟着「节点选择」走的话用户手选了一个坏节点规则集就下不来。
// 开了 cache_file：规则集下载一次后缓存，客户端每次启动不必重新下载。
//
// 规则集下载出口的写法分两种（singboxDialect）：
//   - sing-box 1.14 起 download_detour 弃用（1.16 删除），推荐写顶层 http_clients 加
//     route.default_http_client；
//   - 1.13 及以前不认 http_clients（未知字段整份拒载），只能写 download_detour。
// 官方客户端（SFA / SFI / SFM / SFT）的 UA 里带内核版本（「sing-box 1.14.2」），认得出 1.14 起
// 就给新写法；认不出版本的一律给 download_detour：1.14、1.15 只多一条弃用警告，照样能用，
// 而新写法给到旧内核是起不来。
//
// TUN 的 IPv6：TUN 带 IPv6 地址（fdfe:dcba:9876::1/126）是对的，auto_route 也会接管 IPv6 路由，
// 没有它双栈网络上的 IPv6 流量会绕过代理直连。node-e2e（2026-10-08）里 cloudflare / google 连不上
// 的原因是 DNS：被劫持的 AAAA 查询照常拿到 IPv6 地址，应用优先走 IPv6，经代理到了只有 IPv4 出口
// 的节点就断。所以 DNS 全局按 ipv4_only 应答（AAAA 回空），应用只拿到 IPv4；节点地址本身的解析
// 单独按 prefer_ipv4（default_domain_resolver 带 strategy），只有 IPv6 地址的节点照样连得上。
//
// Hiddify、Karing 这类第三方 sing-box 客户端只取订阅里的出站，入站、DNS 与路由用它们自己
// 的设置，附带的模板对它们没有影响；Clash 与 URI 格式不经过这里。
//
// 字段按 sing-box 1.12 / 1.13：DNS 服务器是带 type 的新写法，路由规则用 action（sniff、
// hijack-dns、route、reject），不再有 geoip / geosite 数据库与 block / dns 特殊出站。

import (
	"strings"
	"sync/atomic"
)

const (
	// DefaultSingboxGeositeURLPrefix / DefaultSingboxGeoIPURLPrefix 是规则集地址的缺省前缀，
	// 后面拼 geosite-cn.srs 这类文件名。
	DefaultSingboxGeositeURLPrefix = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"
	DefaultSingboxGeoIPURLPrefix   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/"

	singboxTunTag       = "tun-in"
	singboxDNSRemoteTag = "dns-remote"
	singboxDNSDirectTag = "dns-direct"
	// 境外 DoH 经代理、国内 DoH 直连。两者都按 IP 写，不需要先解析 DoH 服务器自己的域名。
	singboxDNSRemoteServer = "1.1.1.1"
	singboxDNSDirectServer = "223.5.5.5"

	// singboxRuleSetHTTPClient 是 1.14 起规则集下载用的 HTTP 客户端 tag（顶层 http_clients）。
	singboxRuleSetHTTPClient = "rule-set-download"

	singboxGeositeCN  = "geosite-cn"
	singboxGeoIPCN    = "geoip-cn"
	singboxGeositeAds = "geosite-category-ads-all"
)

// SingboxTemplate 是模板的可配项。空字段用缺省值。
type SingboxTemplate struct {
	// GeositeURLPrefix / GeoIPURLPrefix 是规则集地址前缀（含结尾的 /），换成镜像时改它。
	GeositeURLPrefix string
	GeoIPURLPrefix   string
	// BlockAds 为真时按 geosite-category-ads-all 拦截广告域名（DNS 与路由两处都拦）。
	BlockAds bool
}

// singboxTemplate 由 ConfigureSingboxTemplate 在进程启动时设一次；没设时用缺省值。
var singboxTemplate atomic.Pointer[SingboxTemplate]

// ConfigureSingboxTemplate 设置 sing-box 订阅模板的可配项（aegis-public 启动时按 platform/config 调用）。
func ConfigureSingboxTemplate(t SingboxTemplate) {
	t.GeositeURLPrefix = strings.TrimSpace(t.GeositeURLPrefix)
	t.GeoIPURLPrefix = strings.TrimSpace(t.GeoIPURLPrefix)
	singboxTemplate.Store(&t)
}

func currentSingboxTemplate() SingboxTemplate {
	var t SingboxTemplate
	if p := singboxTemplate.Load(); p != nil {
		t = *p
	}
	if t.GeositeURLPrefix == "" {
		t.GeositeURLPrefix = DefaultSingboxGeositeURLPrefix
	}
	if t.GeoIPURLPrefix == "" {
		t.GeoIPURLPrefix = DefaultSingboxGeoIPURLPrefix
	}
	return t
}

// singboxDialect 是规则集下载出口的写法，见文件头。
type singboxDialect int

const (
	// singboxDialectCompat：download_detour，1.13 及以前与认不出版本的客户端。
	singboxDialectCompat singboxDialect = iota
	// singboxDialectHTTPClients：顶层 http_clients + route.default_http_client，1.14 起。
	singboxDialectHTTPClients
)

// singboxDialectFor 按 UA 里的内核版本选写法。官方客户端的 UA 形如
// 「SFA/1.14.2 (614; sing-box 1.14.2; language zh_CN)」；只认「sing-box」后紧跟的版本号，
// 认不出就回 Compat。
func singboxDialectFor(ua string) singboxDialect {
	l := strings.ToLower(ua)
	for {
		i := strings.Index(l, "sing-box")
		if i < 0 {
			return singboxDialectCompat
		}
		l = l[i+len("sing-box"):]
		rest := strings.TrimLeft(l, " /v")
		major, rest, ok := leadingInt(rest)
		if !ok || !strings.HasPrefix(rest, ".") {
			continue
		}
		minor, _, ok := leadingInt(rest[1:])
		if !ok {
			continue
		}
		if major > 1 || (major == 1 && minor >= 14) {
			return singboxDialectHTTPClients
		}
		return singboxDialectCompat
	}
}

// leadingInt 读出开头的十进制数（至多 4 位），返回剩余部分。
func leadingInt(s string) (int, string, bool) {
	n, i := 0, 0
	for i < len(s) && i < 4 && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
	}
	return n, s[i:], i > 0
}

// singboxFrame 在出站列表外面套上 log、dns、inbounds、route 与 experimental。proxy 是默认出站：
// 有节点时是「节点选择」，一个节点都没有时是 direct（这时境外 DNS 与规则集下载也只能直连）。
// ruleSetVia 是规则集下载出口：有节点时是「自动选择」，没有时为空（直连）。
func singboxFrame(outbounds []map[string]any, proxy, ruleSetVia string, dialect singboxDialect) map[string]any {
	t := currentSingboxTemplate()
	viaProxy := func(m map[string]any, key string) map[string]any {
		if proxy != "direct" {
			m[key] = proxy
		}
		return m
	}
	remoteRuleSet := func(tag, prefix string) map[string]any {
		rs := map[string]any{"type": "remote", "tag": tag, "format": "binary", "url": prefix + tag + ".srs"}
		if dialect == singboxDialectCompat && ruleSetVia != "" {
			rs["download_detour"] = ruleSetVia
		}
		return rs
	}

	dnsRules := []map[string]any{}
	routeRules := []map[string]any{
		{"action": "sniff"},
		{"protocol": "dns", "action": "hijack-dns"},
		{"ip_is_private": true, "action": "route", "outbound": "direct"},
	}
	ruleSets := []map[string]any{}
	if t.BlockAds {
		dnsRules = append(dnsRules, map[string]any{"rule_set": []string{singboxGeositeAds}, "action": "reject"})
		routeRules = append(routeRules, map[string]any{"rule_set": []string{singboxGeositeAds}, "action": "reject"})
		ruleSets = append(ruleSets, remoteRuleSet(singboxGeositeAds, t.GeositeURLPrefix))
	}
	dnsRules = append(dnsRules, map[string]any{
		"rule_set": []string{singboxGeositeCN}, "action": "route", "server": singboxDNSDirectTag,
	})
	routeRules = append(routeRules, map[string]any{
		"rule_set": []string{singboxGeositeCN, singboxGeoIPCN}, "action": "route", "outbound": "direct",
	})
	ruleSets = append(ruleSets,
		remoteRuleSet(singboxGeositeCN, t.GeositeURLPrefix),
		remoteRuleSet(singboxGeoIPCN, t.GeoIPURLPrefix))

	route := map[string]any{
		"rules":                 routeRules,
		"rule_set":              ruleSets,
		"final":                 proxy,
		"auto_detect_interface": true,
		// 节点地址用国内 DoH 解析；strategy 单独给，不继承 DNS 全局的 ipv4_only（见文件头）
		"default_domain_resolver": map[string]any{"server": singboxDNSDirectTag, "strategy": "prefer_ipv4"},
	}
	frame := map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": map[string]any{
			"servers": []map[string]any{
				viaProxy(map[string]any{"type": "https", "tag": singboxDNSRemoteTag, "server": singboxDNSRemoteServer}, "detour"),
				{"type": "https", "tag": singboxDNSDirectTag, "server": singboxDNSDirectServer},
			},
			"rules": dnsRules,
			"final": singboxDNSRemoteTag,
			// 应用拿到的只有 IPv4 地址（AAAA 回空），见文件头「TUN 的 IPv6」
			"strategy": "ipv4_only",
		},
		"inbounds": []map[string]any{{
			"type": "tun", "tag": singboxTunTag,
			"address":      []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
			"auto_route":   true,
			"strict_route": true,
		}},
		"outbounds": outbounds,
		"route":     route,
		"experimental": map[string]any{
			"cache_file": map[string]any{"enabled": true},
		},
	}
	if dialect == singboxDialectHTTPClients {
		client := map[string]any{"tag": singboxRuleSetHTTPClient}
		if ruleSetVia != "" {
			client["detour"] = ruleSetVia
		}
		frame["http_clients"] = []map[string]any{client}
		route["default_http_client"] = singboxRuleSetHTTPClient
	}
	return frame
}
