package subscription

import (
	"encoding/json"
	"strings"
	"testing"
)

type singboxTemplateDoc struct {
	DNS struct {
		Servers []map[string]any `json:"servers"`
		Rules   []map[string]any `json:"rules"`
		Final   string           `json:"final"`
	} `json:"dns"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Route     struct {
		Rules                 []map[string]any `json:"rules"`
		RuleSet               []map[string]any `json:"rule_set"`
		Final                 string           `json:"final"`
		AutoDetectInterface   bool             `json:"auto_detect_interface"`
		DefaultDomainResolver string           `json:"default_domain_resolver"`
	} `json:"route"`
	Experimental struct {
		CacheFile struct {
			Enabled bool `json:"enabled"`
		} `json:"cache_file"`
	} `json:"experimental"`
}

func renderSingboxDoc(t *testing.T, nodes []Node) (singboxTemplateDoc, string) {
	t.Helper()
	body, _, _ := Render(FormatSingbox, nodes, fixtureUUID)
	var doc singboxTemplateDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("invalid sing-box JSON: %v\n%s", err, body)
	}
	return doc, string(body)
}

// 引用必须指向存在的东西：出站 tag（路由、DNS detour、规则集下载）、DNS 服务器 tag、规则集 tag。
// 任何一处悬空，sing-box 启动失败，整份订阅一起失效。
func assertSingboxReferencesResolve(t *testing.T, doc singboxTemplateDoc, body string) {
	t.Helper()
	outbounds := map[string]bool{}
	for _, o := range doc.Outbounds {
		outbounds[o["tag"].(string)] = true
	}
	servers := map[string]bool{}
	for _, s := range doc.DNS.Servers {
		servers[s["tag"].(string)] = true
		if d, ok := s["detour"].(string); ok && !outbounds[d] {
			t.Fatalf("dns server %v detours to missing outbound %q\n%s", s["tag"], d, body)
		}
	}
	sets := map[string]bool{}
	for _, rs := range doc.Route.RuleSet {
		sets[rs["tag"].(string)] = true
		if d, ok := rs["download_detour"].(string); ok && !outbounds[d] {
			t.Fatalf("rule-set %v downloads via missing outbound %q\n%s", rs["tag"], d, body)
		}
	}
	checkSets := func(rule map[string]any) {
		if raw, ok := rule["rule_set"].([]any); ok {
			for _, tag := range raw {
				if !sets[tag.(string)] {
					t.Fatalf("rule references missing rule-set %q\n%s", tag, body)
				}
			}
		}
	}
	for _, r := range doc.DNS.Rules {
		checkSets(r)
		if s, ok := r["server"].(string); ok && !servers[s] {
			t.Fatalf("dns rule routes to missing server %q\n%s", s, body)
		}
	}
	for _, r := range doc.Route.Rules {
		checkSets(r)
		if o, ok := r["outbound"].(string); ok && !outbounds[o] {
			t.Fatalf("route rule routes to missing outbound %q\n%s", o, body)
		}
	}
	if !outbounds[doc.Route.Final] || !servers[doc.DNS.Final] || !servers[doc.Route.DefaultDomainResolver] {
		t.Fatalf("final / resolver dangling: route=%q dns=%q resolver=%q\n%s",
			doc.Route.Final, doc.DNS.Final, doc.Route.DefaultDomainResolver, body)
	}
}

// sing-box 订阅附带 TUN、DNS 国内外分流与基础路由（用户 2026-10-07 定）：官方客户端把订阅当整份
// 配置跑，缺了入站不接管流量、缺了 DNS 段国外域名被污染。
func TestSingboxTemplateCarriesTunDNSAndRoutes(t *testing.T) {
	ConfigureSingboxTemplate(SingboxTemplate{})
	t.Cleanup(func() { ConfigureSingboxTemplate(SingboxTemplate{}) })
	doc, body := renderSingboxDoc(t, formNodes(t))

	if len(doc.Inbounds) != 1 || doc.Inbounds[0]["type"] != "tun" || doc.Inbounds[0]["auto_route"] != true ||
		doc.Inbounds[0]["strict_route"] != true {
		t.Fatalf("want exactly one auto-route TUN inbound: %v", doc.Inbounds)
	}
	if doc.Route.Final != "节点选择" || !doc.Route.AutoDetectInterface || doc.Route.DefaultDomainResolver != "dns-direct" {
		t.Fatalf("route frame = final %q auto_detect %v resolver %q", doc.Route.Final, doc.Route.AutoDetectInterface, doc.Route.DefaultDomainResolver)
	}
	// 规则顺序：嗅探 → 劫持 DNS → 私网直连 → 国内直连
	want := []string{"sniff", "hijack-dns", "private", "cn"}
	var got []string
	for _, r := range doc.Route.Rules {
		switch {
		case r["action"] == "sniff":
			got = append(got, "sniff")
		case r["action"] == "hijack-dns" && r["protocol"] == "dns":
			got = append(got, "hijack-dns")
		case r["ip_is_private"] == true && r["outbound"] == "direct":
			got = append(got, "private")
		case r["outbound"] == "direct" && strings.Contains(strings.Join(anyStrings(r["rule_set"]), ","), "geosite-cn,geoip-cn"):
			got = append(got, "cn")
		default:
			got = append(got, "?")
		}
	}
	if strings.Join(got, ">") != strings.Join(want, ">") {
		t.Fatalf("route rules = %v, want %v\n%s", got, want, body)
	}
	// DNS：国内域名走国内 DoH 直连，其余走境外 DoH 经代理
	if doc.DNS.Final != "dns-remote" || len(doc.DNS.Rules) != 1 || doc.DNS.Rules[0]["server"] != "dns-direct" {
		t.Fatalf("dns split = final %q rules %v", doc.DNS.Final, doc.DNS.Rules)
	}
	for _, s := range doc.DNS.Servers {
		if s["tag"] == "dns-remote" && s["detour"] != "节点选择" {
			t.Fatalf("remote DNS must go through the proxy: %v", s)
		}
		if s["tag"] == "dns-direct" && s["detour"] != nil {
			t.Fatalf("domestic DNS must be direct: %v", s)
		}
	}
	// 规则集：缺省 SagerNet 公开地址、二进制格式、经代理下载；没开广告拦截就没有广告规则集
	urls := map[string]string{}
	for _, rs := range doc.Route.RuleSet {
		if rs["type"] != "remote" || rs["format"] != "binary" || rs["download_detour"] != "节点选择" {
			t.Fatalf("rule-set shape: %v", rs)
		}
		urls[rs["tag"].(string)] = rs["url"].(string)
	}
	if len(urls) != 2 || urls["geosite-cn"] != DefaultSingboxGeositeURLPrefix+"geosite-cn.srs" ||
		urls["geoip-cn"] != DefaultSingboxGeoIPURLPrefix+"geoip-cn.srs" {
		t.Fatalf("default rule-sets = %v", urls)
	}
	if !doc.Experimental.CacheFile.Enabled {
		t.Fatal("cache_file must be on so rule-sets are not re-downloaded on every start")
	}
	// 1.11 起移除的写法不能回来
	for _, removed := range []string{`"geoip":`, `"geosite":`, `"type": "block"`, `"type": "dns"`, `"inet4_address"`} {
		if strings.Contains(body, removed) {
			t.Fatalf("removed sing-box field %s returned", removed)
		}
	}
	assertSingboxReferencesResolve(t, doc, body)
}

// 规则集地址可配置（镜像）；广告拦截可选，开了之后 DNS 与路由两处都拦，且排在国内直连之前。
func TestSingboxTemplateConfigurableRuleSetsAndAds(t *testing.T) {
	ConfigureSingboxTemplate(SingboxTemplate{
		GeositeURLPrefix: "https://mirror.example.test/geosite/",
		GeoIPURLPrefix:   "https://mirror.example.test/geoip/",
		BlockAds:         true,
	})
	t.Cleanup(func() { ConfigureSingboxTemplate(SingboxTemplate{}) })
	doc, body := renderSingboxDoc(t, formNodes(t))
	urls := map[string]string{}
	for _, rs := range doc.Route.RuleSet {
		urls[rs["tag"].(string)] = rs["url"].(string)
	}
	if urls["geosite-cn"] != "https://mirror.example.test/geosite/geosite-cn.srs" ||
		urls["geoip-cn"] != "https://mirror.example.test/geoip/geoip-cn.srs" ||
		urls["geosite-category-ads-all"] != "https://mirror.example.test/geosite/geosite-category-ads-all.srs" {
		t.Fatalf("configured rule-sets = %v", urls)
	}
	adsAt, cnAt := -1, -1
	for i, r := range doc.Route.Rules {
		sets := strings.Join(anyStrings(r["rule_set"]), ",")
		if sets == "geosite-category-ads-all" && r["action"] == "reject" {
			adsAt = i
		}
		if strings.HasPrefix(sets, "geosite-cn") {
			cnAt = i
		}
	}
	if adsAt < 0 || cnAt < 0 || adsAt > cnAt {
		t.Fatalf("ad blocking must reject before the domestic rule: ads=%d cn=%d\n%s", adsAt, cnAt, body)
	}
	if len(doc.DNS.Rules) != 2 || doc.DNS.Rules[0]["action"] != "reject" {
		t.Fatalf("dns must reject ad domains first: %v", doc.DNS.Rules)
	}
	assertSingboxReferencesResolve(t, doc, body)
}

// 没有节点时整份配置仍要能起：境外 DNS 与规则集下载直连，不指向不存在的「节点选择」。
func TestSingboxTemplateWithoutNodes(t *testing.T) {
	ConfigureSingboxTemplate(SingboxTemplate{BlockAds: true})
	t.Cleanup(func() { ConfigureSingboxTemplate(SingboxTemplate{}) })
	doc, body := renderSingboxDoc(t, nil)
	if doc.Route.Final != "direct" {
		t.Fatalf("empty subscription final = %q", doc.Route.Final)
	}
	assertSingboxReferencesResolve(t, doc, body)
}

func anyStrings(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
