// 订阅域名分数按「全国 / 省 / 省+运营商」三级分桶存储与读取。
// 地区来自访客 IP 的 ip2region 归属地，只认中国大陆（含港澳台，库内国家均为「中国」）的非机房 IP；
// 判不出、机房、本机、内网一律只记、只读全国一级（field "*"）。

package service

import (
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/geoip"
)

// geoLocator 是 IP 归属地查询面；*geoip.Resolver 直接满足。
type geoLocator interface {
	Lookup(ip string) geoip.Location
}

const (
	// domainScoreFieldAll 是全国一级的 field。
	domainScoreFieldAll = "*"
	// regionProvinceMaxRunes 是省份名的长度上限（字符数），超了当地区不可用。
	regionProvinceMaxRunes = 32
)

// 运营商规整后的 6 类
const (
	ispTelecom   = "电信"
	ispUnicom    = "联通"
	ispMobile    = "移动" // 铁通并入移动
	ispEducation = "教育网"
	ispBroadcast = "广电"
	ispOther     = "其他" // 含空
)

// domainRegion 是分数分桶用的访客地区；零值（Province 为空）表示不可用，只记、只读全国一级。
type domainRegion struct {
	Province string
	ISP      string // 上面 6 类之一
}

func (r domainRegion) known() bool { return r.Province != "" }

// fields 从粗到细：不可用 → ["*"]；可用 → ["*", 省, 省+"|"+运营商]。
func (r domainRegion) fields() []string {
	if !r.known() {
		return []string{domainScoreFieldAll}
	}
	return []string{domainScoreFieldAll, r.Province, r.Province + "|" + r.ISP}
}

// normalizeISP 把 ip2region 的 ISP 规整成 6 类。
func normalizeISP(isp string) string {
	lower := strings.ToLower(strings.TrimSpace(isp))
	switch {
	case strings.Contains(lower, "教育网") || strings.Contains(lower, "cernet"):
		return ispEducation
	case strings.Contains(lower, "电信"):
		return ispTelecom
	case strings.Contains(lower, "联通"):
		return ispUnicom
	case strings.Contains(lower, "移动") || strings.Contains(lower, "铁通"):
		return ispMobile
	case strings.Contains(lower, "广电"):
		return ispBroadcast
	default:
		return ispOther
	}
}

// regionFromLocation 从归属地判定分桶用的地区。
func regionFromLocation(loc geoip.Location) domainRegion {
	if loc.Country != "中国" {
		return domainRegion{}
	}
	switch loc.Kind {
	case geoip.KindDatacenter, geoip.KindLoopback, geoip.KindPrivate:
		return domainRegion{}
	}
	prov := strings.TrimSpace(loc.Region)
	if prov == "" {
		return domainRegion{}
	}
	if utf8.RuneCountInString(prov) > regionProvinceMaxRunes {
		return domainRegion{}
	}
	if strings.ContainsAny(prov, "|*") {
		return domainRegion{}
	}
	return domainRegion{Province: prov, ISP: normalizeISP(loc.ISP)}
}

// regionOf 按 IP 查归属地并规整；未注入 geo 或 ip 为空回零值。
func (s *Service) regionOf(ip string) domainRegion {
	if s.geo == nil || strings.TrimSpace(ip) == "" {
		return domainRegion{}
	}
	return regionFromLocation(s.geo.Lookup(ip))
}
