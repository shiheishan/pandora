package service

import (
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/geoip"
)

// fakeGeo 是测试用的归属地查询：按 IP 查表，记调用次数。
type fakeGeo struct {
	locs  map[string]geoip.Location
	calls atomic.Int32
}

func (f *fakeGeo) Lookup(ip string) geoip.Location { f.calls.Add(1); return f.locs[ip] }

func TestNormalizeISP(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"电信", ispTelecom},
		{"中国电信", ispTelecom},
		{" 电信 ", ispTelecom},
		{"联通", ispUnicom},
		{"移动", ispMobile},
		{"铁通", ispMobile},
		{"中移铁通", ispMobile},
		{"教育网", ispEducation},
		{"CERNET", ispEducation},
		{"广电", ispBroadcast},
		{"长城宽带", ispOther},
		{"", ispOther},
	}
	for _, c := range cases {
		if got := normalizeISP(c.in); got != c.want {
			t.Fatalf("normalizeISP(%q) = %q，要 %q", c.in, got, c.want)
		}
	}
}

func TestRegionFromLocation(t *testing.T) {
	cases := []struct {
		name string
		loc  geoip.Location
		want domainRegion
	}{
		{
			"福建电信住宅",
			geoip.Location{Country: "中国", Region: "福建", ISP: "电信", Kind: geoip.KindResidential},
			domainRegion{Province: "福建", ISP: ispTelecom},
		},
		{
			"省份首尾空格",
			geoip.Location{Country: "中国", Region: " 福建 ", ISP: "电信", Kind: geoip.KindResidential},
			domainRegion{Province: "福建", ISP: ispTelecom},
		},
		{
			"北京教育网",
			geoip.Location{Country: "中国", Region: "北京", ISP: "教育网", Kind: geoip.KindEducation},
			domainRegion{Province: "北京", ISP: ispEducation},
		},
		{
			"ISP 空",
			geoip.Location{Country: "中国", Region: "福建", ISP: "", Kind: geoip.KindUnknown},
			domainRegion{Province: "福建", ISP: ispOther},
		},
		{"非中国", geoip.Location{Country: "美国", Region: "福建", ISP: "电信", Kind: geoip.KindResidential}, domainRegion{}},
		{
			"机房",
			geoip.Location{Country: "中国", Region: "福建", ISP: "阿里云", Kind: geoip.KindDatacenter},
			domainRegion{},
		},
		{"本机", geoip.Location{Kind: geoip.KindLoopback}, domainRegion{}},
		{"内网", geoip.Location{Kind: geoip.KindPrivate}, domainRegion{}},
		{"省空", geoip.Location{Country: "中国", Region: "", ISP: "电信", Kind: geoip.KindResidential}, domainRegion{}},
		{
			"32 省字",
			geoip.Location{Country: "中国", Region: strings.Repeat("省", 32), ISP: "电信", Kind: geoip.KindResidential},
			domainRegion{Province: strings.Repeat("省", 32), ISP: ispTelecom},
		},
		{
			"33 省字",
			geoip.Location{Country: "中国", Region: strings.Repeat("省", 33), ISP: "电信", Kind: geoip.KindResidential},
			domainRegion{},
		},
		{"省含竖线", geoip.Location{Country: "中国", Region: "福|建", ISP: "电信", Kind: geoip.KindResidential}, domainRegion{}},
		{"省为星号", geoip.Location{Country: "中国", Region: "*", ISP: "电信", Kind: geoip.KindResidential}, domainRegion{}},
	}
	for _, c := range cases {
		got := regionFromLocation(c.loc)
		if got != c.want {
			t.Fatalf("%s: regionFromLocation = %+v，要 %+v", c.name, got, c.want)
		}
	}
}

func TestDomainRegionFields(t *testing.T) {
	zero := domainRegion{}.fields()
	if !reflect.DeepEqual(zero, []string{domainScoreFieldAll}) {
		t.Fatalf("零值 fields = %v，要 [\"*\"]", zero)
	}
	fj := domainRegion{Province: "福建", ISP: ispTelecom}.fields()
	want := []string{domainScoreFieldAll, "福建", "福建|电信"}
	if !reflect.DeepEqual(fj, want) {
		t.Fatalf("福建电信 fields = %v，要 %v", fj, want)
	}
}

func TestRegionOf(t *testing.T) {
	if got := (&Service{}).regionOf("198.51.100.7"); got != (domainRegion{}) {
		t.Fatalf("无 geo = %+v，要零值", got)
	}
	g := &fakeGeo{locs: map[string]geoip.Location{
		"198.51.100.7": {Country: "中国", Region: "福建", ISP: "电信", Kind: geoip.KindResidential},
	}}
	s := &Service{geo: g}
	if got := s.regionOf("198.51.100.7"); got != (domainRegion{Province: "福建", ISP: ispTelecom}) {
		t.Fatalf("regionOf = %+v，要福建电信", got)
	}
	if got := s.regionOf(""); got != (domainRegion{}) {
		t.Fatalf("空 ip = %+v，要零值", got)
	}
	if g.calls.Load() != 1 {
		t.Fatalf("Lookup 调用次数 = %d，要 1（空 ip 不应查）", g.calls.Load())
	}
}
