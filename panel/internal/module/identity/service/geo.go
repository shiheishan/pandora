package service

import "github.com/aegispanel/aegis/internal/pkg/geoip"

// UseGeo 注入 GeoIP 解析器，用于访问明细与同源聚类的归属地与网络类型标注。
func (s *Service) UseGeo(r *geoip.Resolver) {
	s.geo = r
}

func (s *Service) locate(ip string) (display, kind string) {
	loc := s.geo.Lookup(ip)
	return loc.Display, string(loc.Kind)
}
