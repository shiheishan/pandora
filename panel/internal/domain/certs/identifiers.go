package certs

import (
	"net/netip"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"
)

const maxIdentifiers = 20

// normalizeIdentifiers 把后台填的域名规范成库里的形状：去空白、转小写、去末尾点、去重、排序。
// 只收 DNS 名（含最左一级通配符），不收 IP（IP 证书不做，用户 10-08 定）；返回的错误文案
// 直接给表单。
func normalizeIdentifiers(raw []string) ([]string, string) {
	if len(raw) == 0 {
		return nil, "至少填一个域名"
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		id := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r)), ".")
		if id == "" {
			continue
		}
		if msg := checkIdentifier(id); msg != "" {
			return nil, msg
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, "至少填一个域名"
	}
	if len(out) > maxIdentifiers {
		return nil, "一张证书最多 20 个域名"
	}
	slices.Sort(out)
	return out, ""
}

func checkIdentifier(id string) string {
	if _, err := netip.ParseAddr(strings.Trim(id, "[]")); err == nil {
		return "不支持 IP 证书：" + id
	}
	if len(id) > 253 {
		return "域名太长：" + id
	}
	name := id
	if strings.HasPrefix(id, "*.") {
		name = id[2:]
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "不是完整的域名：" + id
	}
	for _, l := range labels {
		if !validLabel(l) {
			return "域名格式不对（只能有字母、数字、连字符，通配符只能在最左边）：" + id
		}
	}
	// 通配符不能直接盖在公共后缀上（*.com、*.co.uk）：CA 一定拒签
	if strings.HasPrefix(id, "*.") {
		if suffix, _ := publicsuffix.PublicSuffix(name); suffix == name {
			return "通配符不能直接用在公共后缀上：" + id
		}
	}
	if _, err := publicsuffix.EffectiveTLDPlusOne(name); err != nil {
		return "不是可以签证书的域名：" + id
	}
	return ""
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// registeredDomain 是 Let's Encrypt「每注册域每周 50 张」限额的计数单位（eTLD+1）。
func registeredDomain(id string) string {
	name := strings.TrimPrefix(id, "*.")
	if rd, err := publicsuffix.EffectiveTLDPlusOne(name); err == nil {
		return rd
	}
	return name
}

// hasWildcard 报告标识里有没有通配符：通配符证书一般被多台服务器共用，界面要提示私钥共享的风险。
func hasWildcard(ids []string) bool {
	for _, id := range ids {
		if strings.HasPrefix(id, "*.") {
			return true
		}
	}
	return false
}

// underZone 报告标识是否在凭据的 zone 里（等于 zone 或是它的子域）。不在也能签——
// _acme-challenge 用 CNAME 委托到这个 zone 时——所以只用来提示，不拒绝。
func underZone(id, zone string) bool {
	name := strings.TrimPrefix(id, "*.")
	return name == zone || strings.HasSuffix(name, "."+zone)
}

// normalizeZone 规范 zone 名，错误文案给表单。
func normalizeZone(raw string) (string, string) {
	z := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if z == "" {
		return "", "填写这个凭据管理的域名（zone），例如 example.com"
	}
	if strings.HasPrefix(z, "*.") {
		return "", "zone 不能带通配符"
	}
	if msg := checkIdentifier(z); msg != "" {
		return "", msg
	}
	return z, ""
}
