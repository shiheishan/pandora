// Package route 是 pdnd 的分流引擎。
//
// 以前分流有两份实现：sing-box 那条把面板下发的匹配器原样透传给它自己解析，
// xray 那条逐字段翻译成 protobuf。同一条规则在两个内核上的行为由各自的上游
// 决定，我们既没有统一的语义，也没法测 —— 加一种匹配器要改两处，改漏一处的
// 表现是「节点换了内核之后分流悄悄失效」，而日志里什么都看不到。
//
// 这里把它收成一份：规则语义由这个包定义，匹配结果只取决于这里的代码。
//
// 规则求值的顺序语义：**从上到下，第一条命中即返回**。这与 sing-box 和 xray
// 一致，也是运营的直觉 —— 把特例写在前面、兜底写在后面。
package route

import (
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Meta 是一条待分流的连接的特征。
//
// Domain 与 IP 可能只有其一：客户端发来的目标可能是域名（还没解析），
// 也可能直接是 IP。两者都有时（域名已被解析）两类规则都能命中。
type Meta struct {
	Domain     string
	IP         netip.Addr
	Port       uint16
	SourceIP   netip.Addr
	SourcePort uint16
	Network    string // tcp / udp
	Protocol   string // 探测出的应用层协议，可能为空
}

// Rule 是一条编译好的规则。
//
// 编译期把正则、CIDR、端口都解析成可直接比较的形式。每条连接都要过一遍规则
// 表，在这里省下的解析开销是按连接数放大的 —— 一个万人节点每秒几千条连接，
// 每条都重新编译正则是不可接受的。
type Rule struct {
	OutboundTag string

	domains          map[string]bool // 精确匹配
	suffixes         []string
	keywords         []string
	regexps          []*regexp.Regexp
	cidrs            []netip.Prefix
	geoips           []string
	ports            map[uint16]bool
	portRanges       []portRange
	sourceCIDRs      []netip.Prefix
	sourcePorts      map[uint16]bool
	sourcePortRanges []portRange
	networks         map[string]bool
	protos           map[string]bool

	// empty 表示这条规则没有任何条件，无条件命中。
	// 面板用它表达兜底出口。
	empty bool
}

// GeoIP 是国家码查询。
//
// 做成接口而不是内置一个库：geoip 数据库是几十 MB 的外部文件，
// 不是每个部署都需要。没提供实现时带 geoip 的规则永远不命中，
// Compile 会把这件事说出来，而不是让它静默失效。
type GeoIP interface {
	LookupCountry(netip.Addr) string
}

type portRange struct {
	min uint16
	max uint16
}

// Engine 是编译后的规则表。
type Engine struct {
	rules []Rule
	geoip GeoIP
	// final 是所有规则都没命中时的去向。
	final string
}

// Compile 把面板下发的规则编译成引擎。
//
// 返回的 warnings 是「这条规则写了但不会按你以为的方式工作」——
// 比如引用了不存在的出站，或者用了 geoip 但没有数据库。这些不该让整个
// 节点起不来（其余规则仍然有效），但必须让人看见。
func Compile(rules []RawRule, outbounds map[string]bool, final string, geo GeoIP) (*Engine, []string, error) {
	return compile(rules, outbounds, final, geo, false)
}

// CompileStrict is the production/runtime variant of Compile. It rejects a
// configuration whose final outbound, rule target, or GeoIP dependency cannot
// actually be executed. This prevents a bad control-plane update from silently
// becoming direct traffic.
func CompileStrict(rules []RawRule, outbounds map[string]bool, final string, geo GeoIP) (*Engine, error) {
	if final == "" {
		return nil, fmt.Errorf("缺少最终出站")
	}
	if !outbounds[final] {
		return nil, fmt.Errorf("最终出站 %q 不存在", final)
	}
	e, _, err := compile(rules, outbounds, final, geo, true)
	return e, err
}

func compile(rules []RawRule, outbounds map[string]bool, final string, geo GeoIP, strict bool) (*Engine, []string, error) {
	e := &Engine{geoip: geo, final: final}
	var warnings []string

	for i, raw := range rules {
		r, err := compileOne(raw)
		if err != nil {
			return nil, warnings, fmt.Errorf("第 %d 条规则: %w", i+1, err)
		}
		if !outbounds[r.OutboundTag] {
			if strict {
				return nil, warnings, fmt.Errorf("第 %d 条规则指向不存在的出站 %q", i+1, r.OutboundTag)
			}
			// 指向不存在的出站。跳过而不是报错：面板可能正在分两步下发
			// （先规则后出站），整表拒绝会让节点在中间态彻底没有分流。
			warnings = append(warnings,
				fmt.Sprintf("第 %d 条规则指向不存在的出站 %q，已跳过", i+1, r.OutboundTag))
			continue
		}
		if len(r.geoips) > 0 && geo == nil {
			if strict {
				return nil, warnings, fmt.Errorf("第 %d 条规则需要 GeoIP 数据库", i+1)
			}
			warnings = append(warnings,
				fmt.Sprintf("第 %d 条规则用了 geoip 但没有加载数据库，这条永远不会命中", i+1))
		}
		e.rules = append(e.rules, r)
	}
	return e, warnings, nil
}

// RawRule 是面板下发的原始规则。
type RawRule struct {
	Matcher     map[string]any
	OutboundTag string
}

func compileOne(raw RawRule) (Rule, error) {
	r := Rule{OutboundTag: raw.OutboundTag}
	if r.OutboundTag == "" {
		return r, fmt.Errorf("缺少出站标签")
	}
	if len(raw.Matcher) == 0 {
		r.empty = true
		return r, nil
	}

	for key, val := range raw.Matcher {
		list, err := toStrings(val)
		if err != nil {
			return r, fmt.Errorf("匹配器 %q: %w", key, err)
		}
		if len(list) == 0 {
			return r, fmt.Errorf("匹配器 %q 至少需要一个有效值", key)
		}
		switch key {
		case "domain", "domains":
			if r.domains == nil {
				r.domains = make(map[string]bool, len(list))
			}
			for _, s := range list {
				r.domains[strings.ToLower(s)] = true
			}
		case "domain_suffix", "domain_suffixes":
			for _, s := range list {
				// 统一成不带前导点的形式。面板上两种写法都有人用，
				// 不归一化的话 ".example.com" 和 "example.com" 会是两条不同的规则。
				r.suffixes = append(r.suffixes, strings.ToLower(strings.TrimPrefix(s, ".")))
			}
		case "domain_keyword", "domain_keywords":
			for _, s := range list {
				r.keywords = append(r.keywords, strings.ToLower(s))
			}
		case "domain_regex", "domain_regexes":
			for _, s := range list {
				re, err := regexp.Compile(s)
				if err != nil {
					return r, fmt.Errorf("正则 %q 无效: %w", s, err)
				}
				r.regexps = append(r.regexps, re)
			}
		case "ip", "ip_cidr", "ip_cidrs":
			for _, s := range list {
				p, err := parseCIDR(s)
				if err != nil {
					return r, err
				}
				r.cidrs = append(r.cidrs, p)
			}
		case "geoip", "geoips":
			for _, s := range list {
				r.geoips = append(r.geoips, strings.ToUpper(s))
			}
		case "port", "ports":
			if err := addPorts(list, &r.ports, &r.portRanges); err != nil {
				return r, err
			}
		case "source", "source_ip_cidr", "source_cidrs":
			for _, s := range list {
				p, err := parseCIDR(s)
				if err != nil {
					return r, fmt.Errorf("来源 %w", err)
				}
				r.sourceCIDRs = append(r.sourceCIDRs, p)
			}
		case "source_port", "source_ports":
			if err := addPorts(list, &r.sourcePorts, &r.sourcePortRanges); err != nil {
				return r, fmt.Errorf("来源%w", err)
			}
		case "network", "networks":
			if r.networks == nil {
				r.networks = make(map[string]bool, len(list))
			}
			for _, s := range list {
				r.networks[strings.ToLower(s)] = true
			}
		case "protocol", "protocols":
			if r.protos == nil {
				r.protos = make(map[string]bool, len(list))
			}
			for _, s := range list {
				r.protos[strings.ToLower(s)] = true
			}
		default:
			return r, fmt.Errorf("不认识的匹配器 %q", key)
		}
	}
	return r, nil
}

// parseCIDR 同时接受带掩码和不带掩码的写法。
//
// 面板上「封掉这个 IP」写成 1.2.3.4 是最自然的，要求必须写 /32 只会
// 制造错误。单个地址补成 /32 或 /128。
func parseCIDR(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("CIDR %q 无效: %w", s, err)
		}
		// Masked 把 192.168.1.5/24 归一成 192.168.1.0/24。
		// 不归一的话 netip 的 Contains 仍然工作，但两条等价规则
		// 在日志和去重时看起来不同。
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("IP %q 无效: %w", s, err)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func addPorts(list []string, exact *map[uint16]bool, ranges *[]portRange) error {
	for _, s := range list {
		s = strings.TrimSpace(s)
		if lo, hi, ok := strings.Cut(s, "-"); ok {
			a, err1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
			b, err2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 16)
			if err1 != nil || err2 != nil || a > b {
				return fmt.Errorf("端口区间 %q 无效", s)
			}
			*ranges = append(*ranges, portRange{min: uint16(a), max: uint16(b)})
			continue
		}
		v, err := strconv.ParseUint(s, 10, 16)
		if err != nil {
			return fmt.Errorf("端口 %q 无效", s)
		}
		if *exact == nil {
			*exact = make(map[uint16]bool)
		}
		(*exact)[uint16(v)] = true
	}
	return nil
}

// Match 返回这条连接该走的出站标签。
//
// 一条规则内部的多个条件是**与**关系（域名和端口都得对上），
// 同一条件的多个取值是**或**关系（列表里任一命中即可）。
// 这与 sing-box 和 xray 的语义一致。
func (e *Engine) Match(m Meta) string {
	domain := strings.ToLower(strings.TrimSuffix(m.Domain, "."))
	for i := range e.rules {
		if e.rules[i].match(m, domain, e.geoip) {
			return e.rules[i].OutboundTag
		}
	}
	return e.final
}

func (r *Rule) match(m Meta, domain string, geo GeoIP) bool {
	if r.empty {
		return true
	}
	if r.networks != nil && !r.networks[strings.ToLower(m.Network)] {
		return false
	}
	if r.protos != nil && !r.protos[strings.ToLower(m.Protocol)] {
		return false
	}
	if r.ports != nil || len(r.portRanges) > 0 {
		if !matchPort(m.Port, r.ports, r.portRanges) {
			return false
		}
	}
	if r.sourcePorts != nil || len(r.sourcePortRanges) > 0 {
		if !matchPort(m.SourcePort, r.sourcePorts, r.sourcePortRanges) {
			return false
		}
	}
	if r.hasDomainCond() {
		if domain == "" || !r.matchDomain(domain) {
			return false
		}
	}
	if len(r.cidrs) > 0 || len(r.geoips) > 0 {
		if !m.IP.IsValid() || !r.matchIP(m.IP, geo) {
			return false
		}
	}
	if len(r.sourceCIDRs) > 0 {
		if !m.SourceIP.IsValid() || !containsIP(r.sourceCIDRs, m.SourceIP) {
			return false
		}
	}
	return true
}

func (r *Rule) hasDomainCond() bool {
	return len(r.domains) > 0 || len(r.suffixes) > 0 ||
		len(r.keywords) > 0 || len(r.regexps) > 0
}

func matchPort(p uint16, exact map[uint16]bool, ranges []portRange) bool {
	if exact[p] {
		return true
	}
	for _, current := range ranges {
		if p >= current.min && p <= current.max {
			return true
		}
	}
	return false
}

func (r *Rule) matchDomain(d string) bool {
	if r.domains[d] {
		return true
	}
	for _, s := range r.suffixes {
		// 后缀必须落在标签边界上：suffix "example.com" 应当命中
		// "a.example.com" 和 "example.com" 本身，但不能命中
		// "notexample.com" —— 那是两个完全不同的域名。
		if d == s || (len(d) > len(s) && strings.HasSuffix(d, s) &&
			d[len(d)-len(s)-1] == '.') {
			return true
		}
	}
	for _, k := range r.keywords {
		if strings.Contains(d, k) {
			return true
		}
	}
	for _, re := range r.regexps {
		if re.MatchString(d) {
			return true
		}
	}
	return false
}

func (r *Rule) matchIP(ip netip.Addr, geo GeoIP) bool {
	// v4-in-v6 要先还原。客户端给的目标可能是 ::ffff:1.2.3.4，
	// 而规则里写的是 1.2.3.0/24 —— 不还原就永远匹配不上。
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if containsIP(r.cidrs, ip) {
		return true
	}
	if len(r.geoips) > 0 && geo != nil {
		cc := geo.LookupCountry(ip)
		for _, want := range r.geoips {
			if cc == want {
				return true
			}
		}
	}
	return false
}

func containsIP(prefixes []netip.Prefix, ip netip.Addr) bool {
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// toStrings 把面板下发的 JSON 值归一成字符串切片。
//
// 同一个字段面板可能给单值也可能给数组（"port": 443 和 "port": [443, 8443]），
// 数字还会以 float64 到达。都在这里吃掉，规则编译那边只面对 []string。
func toStrings(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, fmt.Errorf("值不能为空")
		}
		return []string{strings.TrimSpace(x)}, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x {
			return nil, fmt.Errorf("数值 %v 必须是整数", x)
		}
		return []string{strconv.FormatInt(int64(x), 10)}, nil
	case int:
		return []string{strconv.Itoa(x)}, nil
	case []string:
		out := make([]string, 0, len(x))
		for _, e := range x {
			e = strings.TrimSpace(e)
			if e == "" {
				return nil, fmt.Errorf("列表不能包含空值")
			}
			out = append(out, e)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			values, err := toStrings(e)
			if err != nil {
				return nil, err
			}
			out = append(out, values...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("不支持的值类型 %T", v)
	}
}
