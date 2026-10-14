package service

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/geoip"
)

func TestRegionText(t *testing.T) {
	cases := []struct {
		name string
		loc  geoip.Location
		want string
	}{
		{
			name: "三段齐全",
			loc: geoip.Location{
				Country: "中国",
				Region:  "北京",
				City:    "北京市",
				ISP:     "电信",
			},
			want: "中国 北京 北京市",
		},
		{
			name: "缺城市",
			loc: geoip.Location{
				Country: "United States",
				Region:  "California",
				City:    "",
				ISP:     "Google LLC",
			},
			want: "United States California",
		},
		{
			name: "缺省份与城市",
			loc: geoip.Location{
				Country: "新加坡",
				Region:  "",
				City:    "",
				ISP:     "Singtel",
			},
			want: "新加坡",
		},
		{
			name: "全空",
			loc:  geoip.Location{},
			want: "",
		},
		{
			name: "只有运营商（地区三段全空）",
			loc: geoip.Location{
				Country: "",
				Region:  "",
				City:    "",
				ISP:     "Cloudflare",
			},
			want: "",
		},
		{
			name: "缺省份有国家和城市",
			loc: geoip.Location{
				Country: "日本",
				Region:  "",
				City:    "东京",
			},
			want: "日本 东京",
		},
		{
			name: "超长截到 64 字",
			loc:  geoip.Location{Country: strings.Repeat("长", 40), Region: strings.Repeat("名", 40)},
			want: strings.Repeat("长", 40) + " " + strings.Repeat("名", 23),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := regionText(tc.loc)
			if got != tc.want {
				t.Errorf("regionText(%+v) = %q, want %q", tc.loc, got, tc.want)
			}
		})
	}
}
