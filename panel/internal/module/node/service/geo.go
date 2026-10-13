package service

import (
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/geoip"
)

// UseGeo 注入 GeoIP 解析器，用于节点接入时按公网 IP 自动推导并回填服务器地区。
func (s *Service) UseGeo(r *geoip.Resolver) {
	s.geo = r
}

// regionText 从 GeoIP 解析结果提取国家、省/州、城市三段（非空按顺序空格相连）。
// 运营商段对线路地区名无意义，不参与拼接。
func regionText(loc geoip.Location) string {
	segs := make([]string, 0, 3)
	for _, v := range []string{loc.Country, loc.Region, loc.City} {
		if v != "" {
			segs = append(segs, v)
		}
	}
	out := strings.Join(segs, " ")
	// servers.region 库里限 64 字（CHECK），超长截断，免得接入事务因地区名失败
	if r := []rune(out); len(r) > serverRegionMax {
		out = strings.TrimSpace(string(r[:serverRegionMax]))
	}
	return out
}
