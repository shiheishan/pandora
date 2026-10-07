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
// sing-geosite / sing-geoip 的 rule-set 分支；经代理下载，国内直连 GitHub 常常不通。
// 开了 cache_file：规则集下载一次后缓存，客户端每次启动不必重新下载。
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

// singboxFrame 在出站列表外面套上 log、dns、inbounds、route 与 experimental。proxy 是默认出站：
// 有节点时是「节点选择」，一个节点都没有时是 direct（这时境外 DNS 与规则集下载也只能直连）。
func singboxFrame(outbounds []map[string]any, proxy string) map[string]any {
	t := currentSingboxTemplate()
	viaProxy := func(m map[string]any, key string) map[string]any {
		if proxy != "direct" {
			m[key] = proxy
		}
		return m
	}
	remoteRuleSet := func(tag, prefix string) map[string]any {
		return viaProxy(map[string]any{
			"type": "remote", "tag": tag, "format": "binary", "url": prefix + tag + ".srs",
		}, "download_detour")
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

	return map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": map[string]any{
			"servers": []map[string]any{
				viaProxy(map[string]any{"type": "https", "tag": singboxDNSRemoteTag, "server": singboxDNSRemoteServer}, "detour"),
				{"type": "https", "tag": singboxDNSDirectTag, "server": singboxDNSDirectServer},
			},
			"rules": dnsRules,
			"final": singboxDNSRemoteTag,
		},
		"inbounds": []map[string]any{{
			"type": "tun", "tag": singboxTunTag,
			"address":      []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
			"auto_route":   true,
			"strict_route": true,
		}},
		"outbounds": outbounds,
		"route": map[string]any{
			"rules":                   routeRules,
			"rule_set":                ruleSets,
			"final":                   proxy,
			"auto_detect_interface":   true,
			"default_domain_resolver": singboxDNSDirectTag,
		},
		"experimental": map[string]any{
			"cache_file": map[string]any{"enabled": true},
		},
	}
}
