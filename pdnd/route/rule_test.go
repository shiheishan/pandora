package route

import (
	"net/netip"
	"testing"
)

// 分流的错不会报错。规则写错、语义理解偏了，表现只是「某些流量走错了出口」——
// 用户那边是「这个网站怎么不走代理」，我们这边日志一切正常。
// 所以这里逐条钉死语义，尤其是边界：后缀的标签边界、v4-in-v6、条件之间的与或。

func mustEngine(t *testing.T, rules []RawRule, final string) *Engine {
	t.Helper()
	outs := map[string]bool{"direct": true, "block": true, "relay": true}
	e, warns, err := Compile(rules, outs, final, nil)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	for _, w := range warns {
		t.Logf("警告: %s", w)
	}
	return e
}

func TestDomainSuffix_必须落在标签边界(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain_suffix": []any{"example.com"}}, OutboundTag: "block"},
	}, "direct")

	cases := map[string]string{
		"example.com":     "block",  // 后缀本身
		"a.example.com":   "block",  // 子域
		"a.b.example.com": "block",  // 多级子域
		"notexample.com":  "direct", // 这是另一个域名，不能命中
		"example.com.cn":  "direct", // 同理
		"xexample.com":    "direct",
	}
	for d, want := range cases {
		if got := e.Match(Meta{Domain: d, Network: "tcp"}); got != want {
			t.Errorf("%s → %s，期望 %s", d, got, want)
		}
	}
}

func TestDomainSuffix_带不带前导点等价(t *testing.T) {
	a := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain_suffix": ".example.com"}, OutboundTag: "block"}}, "direct")
	b := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain_suffix": "example.com"}, OutboundTag: "block"}}, "direct")
	for _, d := range []string{"example.com", "a.example.com", "other.com"} {
		if a.Match(Meta{Domain: d}) != b.Match(Meta{Domain: d}) {
			t.Errorf("%s 上两种写法结果不同", d)
		}
	}
}

func TestDomain_大小写与末尾点(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain": "Example.COM"}, OutboundTag: "block"}}, "direct")
	for _, d := range []string{"example.com", "EXAMPLE.COM", "example.com."} {
		if got := e.Match(Meta{Domain: d}); got != "block" {
			t.Errorf("%q → %s，期望 block", d, got)
		}
	}
}

func TestIPCIDR(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"ip_cidr": []any{"10.0.0.0/8", "1.2.3.4"}}, OutboundTag: "block"},
	}, "direct")
	cases := map[string]string{
		"10.1.2.3": "block",
		"1.2.3.4":  "block", // 不带掩码的单个地址
		"1.2.3.5":  "direct",
		"11.0.0.1": "direct",
	}
	for ip, want := range cases {
		if got := e.Match(Meta{IP: netip.MustParseAddr(ip)}); got != want {
			t.Errorf("%s → %s，期望 %s", ip, got, want)
		}
	}
}

// 客户端给的目标可能是 ::ffff:1.2.3.4 形式，而规则写的是 v4 网段。
// 不还原就永远匹配不上，而且这种漏匹配极难从现象反推。
func TestIPCIDR_v4in6(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"ip_cidr": "10.0.0.0/8"}, OutboundTag: "block"}}, "direct")
	if got := e.Match(Meta{IP: netip.MustParseAddr("::ffff:10.1.2.3")}); got != "block" {
		t.Errorf("v4-in-v6 地址没命中 v4 网段，得到 %s", got)
	}
}

// 掩码位之外还有比特时（192.168.1.5/24）应当归一，不影响匹配。
func TestIPCIDR_非规范写法(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"ip_cidr": "192.168.1.5/24"}, OutboundTag: "block"}}, "direct")
	if got := e.Match(Meta{IP: netip.MustParseAddr("192.168.1.99")}); got != "block" {
		t.Errorf("同网段没命中，得到 %s", got)
	}
}

func TestPort_单值列表与区间(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"port": []any{float64(443), "8000-8100"}}, OutboundTag: "block"},
	}, "direct")
	cases := map[uint16]string{443: "block", 8000: "block", 8050: "block",
		8100: "block", 80: "direct", 8101: "direct"}
	for p, want := range cases {
		if got := e.Match(Meta{Port: p}); got != want {
			t.Errorf("端口 %d → %s，期望 %s", p, got, want)
		}
	}
}

// 一条规则内多个条件是「与」：域名对了但端口不对，不该命中。
func TestMultiCondition_是与关系(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{
			"domain_suffix": "example.com",
			"port":          float64(443),
		}, OutboundTag: "block"},
	}, "direct")

	if got := e.Match(Meta{Domain: "a.example.com", Port: 443}); got != "block" {
		t.Errorf("都对上却没命中: %s", got)
	}
	if got := e.Match(Meta{Domain: "a.example.com", Port: 80}); got != "direct" {
		t.Errorf("端口不对却命中了: %s", got)
	}
	if got := e.Match(Meta{Domain: "other.com", Port: 443}); got != "direct" {
		t.Errorf("域名不对却命中了: %s", got)
	}
}

// 同一条件的多个取值是「或」。
func TestSameCondition_是或关系(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain": []any{"a.com", "b.com"}}, OutboundTag: "block"}}, "direct")
	for _, d := range []string{"a.com", "b.com"} {
		if e.Match(Meta{Domain: d}) != "block" {
			t.Errorf("%s 没命中", d)
		}
	}
	if e.Match(Meta{Domain: "c.com"}) != "direct" {
		t.Error("c.com 不该命中")
	}
}

func TestOrder_第一条命中即返回(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain_suffix": "example.com"}, OutboundTag: "relay"},
		{Matcher: map[string]any{"domain": "a.example.com"}, OutboundTag: "block"},
	}, "direct")
	// 两条都能命中 a.example.com，应当取第一条
	if got := e.Match(Meta{Domain: "a.example.com"}); got != "relay" {
		t.Errorf("没有取第一条命中的规则，得到 %s", got)
	}
}

func TestEmptyMatcher_是兜底(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain": "a.com"}, OutboundTag: "block"},
		{Matcher: nil, OutboundTag: "relay"},
	}, "direct")
	if got := e.Match(Meta{Domain: "a.com"}); got != "block" {
		t.Errorf("特例规则没命中: %s", got)
	}
	if got := e.Match(Meta{Domain: "zzz.com"}); got != "relay" {
		t.Errorf("空匹配器应当兜底: %s", got)
	}
}

func TestNoRuleMatched_走兜底(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain": "a.com"}, OutboundTag: "block"}}, "direct")
	if got := e.Match(Meta{Domain: "b.com"}); got != "direct" {
		t.Errorf("应当落到 final，得到 %s", got)
	}
}

// 只有 IP 没有域名时，域名类规则不该命中（而不是把空串拿去匹配）。
func TestDomainRule_没有域名时不命中(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain_keyword": "goog"}, OutboundTag: "block"}}, "direct")
	if got := e.Match(Meta{IP: netip.MustParseAddr("8.8.8.8")}); got != "direct" {
		t.Errorf("没有域名却命中了域名规则: %s", got)
	}
}

func TestNetworkAndProtocol(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"network": "udp"}, OutboundTag: "block"}}, "direct")
	if e.Match(Meta{Network: "udp"}) != "block" {
		t.Error("udp 没命中")
	}
	if e.Match(Meta{Network: "tcp"}) != "direct" {
		t.Error("tcp 不该命中")
	}
}

// 指向不存在的出站要跳过并示警，而不是让整表编译失败 ——
// 面板可能分两步下发，整表拒绝会让节点在中间态完全没有分流。
func TestUnknownOutbound_跳过并示警(t *testing.T) {
	outs := map[string]bool{"direct": true}
	e, warns, err := Compile([]RawRule{
		{Matcher: map[string]any{"domain": "a.com"}, OutboundTag: "不存在"},
		{Matcher: map[string]any{"domain": "b.com"}, OutboundTag: "direct"},
	}, outs, "direct", nil)
	if err != nil {
		t.Fatalf("不该整表失败: %v", err)
	}
	if len(warns) == 0 {
		t.Error("跳过了规则却没有示警")
	}
	if len(e.rules) != 1 {
		t.Errorf("应当只留下 1 条有效规则，实际 %d", len(e.rules))
	}
}

// geoip 没有数据库时要明确示警。静默失效是最坏的情况：
// 规则写了、看起来生效了、实际一次都没命中。
func TestGeoIP_无数据库时示警(t *testing.T) {
	_, warns, err := Compile([]RawRule{
		{Matcher: map[string]any{"geoip": "cn"}, OutboundTag: "direct"},
	}, map[string]bool{"direct": true}, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) == 0 {
		t.Error("用了 geoip 却没有数据库，应当示警")
	}
}

type fakeGeo map[string]string

func (f fakeGeo) LookupCountry(a netip.Addr) string { return f[a.String()] }

func TestGeoIP_有数据库时可用(t *testing.T) {
	geo := fakeGeo{"1.1.1.1": "AU", "8.8.8.8": "US"}
	e, _, err := Compile([]RawRule{
		{Matcher: map[string]any{"geoip": "us"}, OutboundTag: "block"},
	}, map[string]bool{"block": true}, "direct", geo)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Match(Meta{IP: netip.MustParseAddr("8.8.8.8")}); got != "block" {
		t.Errorf("US 地址没命中，得到 %s", got)
	}
	if got := e.Match(Meta{IP: netip.MustParseAddr("1.1.1.1")}); got != "direct" {
		t.Errorf("AU 地址不该命中，得到 %s", got)
	}
}

func TestCompile_坏输入(t *testing.T) {
	outs := map[string]bool{"direct": true}
	bad := []struct {
		name string
		m    map[string]any
	}{
		{"正则语法错", map[string]any{"domain_regex": "["}},
		{"CIDR 无效", map[string]any{"ip_cidr": "999.1.1.1/24"}},
		{"端口越界", map[string]any{"port": "70000"}},
		{"端口区间反了", map[string]any{"port": "8100-8000"}},
		{"不认识的匹配器", map[string]any{"user_agent": "curl"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Compile([]RawRule{{Matcher: tc.m, OutboundTag: "direct"}},
				outs, "direct", nil); err == nil {
				t.Error("坏规则应当在编译期报错")
			}
		})
	}
}

func TestRegex(t *testing.T) {
	e := mustEngine(t, []RawRule{
		{Matcher: map[string]any{"domain_regex": `^ads?\..*\.com$`}, OutboundTag: "block"}}, "direct")
	for _, d := range []string{"ad.foo.com", "ads.bar.com"} {
		if e.Match(Meta{Domain: d}) != "block" {
			t.Errorf("%s 没命中", d)
		}
	}
	if e.Match(Meta{Domain: "adx.foo.com"}) != "direct" {
		t.Error("adx.foo.com 不该命中")
	}
}
