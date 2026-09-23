package geoip

import "testing"

func TestParseRegionFillsDisplayAndDropsZeroSegments(t *testing.T) {
	// ip2region 用字面量 "0" 表示缺失，直接展示会冒出一串没意义的零。
	cases := []struct {
		raw         string
		wantDisplay string
		wantCity    string
	}{
		{"中国|北京|北京市|电信|CN", "中国 北京 北京市 电信", "北京市"},
		{"United States|California|0|Google LLC|US", "United States California Google LLC", ""},
		{"中国|0|0|中国教育网|CN", "中国 中国教育网", ""},
	}
	for _, tc := range cases {
		got := parseRegion(tc.raw)
		if got.Display != tc.wantDisplay {
			t.Errorf("%q 的展示串 = %q，期望 %q", tc.raw, got.Display, tc.wantDisplay)
		}
		if got.City != tc.wantCity {
			t.Errorf("%q 的城市 = %q，期望 %q", tc.raw, got.City, tc.wantCity)
		}
	}
}

func TestClassifyNetworkKind(t *testing.T) {
	cases := map[string]NetworkKind{
		"电信":         KindResidential,
		"联通":         KindResidential,
		"中国教育网":      KindEducation,
		"阿里云":        KindDatacenter,
		"华为":         KindUnknown, // 只写「华为」判不出是不是云，不硬猜
		"华为云":        KindDatacenter,
		"Google LLC": KindDatacenter,
		"移动":         KindMobile,
		"":           KindUnknown,
	}
	for isp, want := range cases {
		if got := classify(isp); got != want {
			t.Errorf("classify(%q) = %q，期望 %q", isp, got, want)
		}
	}
}

// 云厂商的名字里常常带「电信」这类字样（例如电信云计算），机房必须先判，
// 否则会被误标成住宅宽带——那正好是风控最不该搞错的一类。
func TestClassifyDatacenterWinsOverResidentialKeyword(t *testing.T) {
	if got := classify("中国电信云计算公司"); got != KindDatacenter {
		t.Fatalf("电信云计算被判成 %q，应当是机房", got)
	}
}

func TestLookupHandlesSpecialAddresses(t *testing.T) {
	var r *Resolver // nil Resolver：没配数据库时不能 panic
	if got := r.Lookup("127.0.0.1"); got.Kind != KindLoopback {
		t.Errorf("回环地址判成 %q", got.Kind)
	}
	if got := r.Lookup("192.168.1.1"); got.Kind != KindPrivate {
		t.Errorf("内网地址判成 %q", got.Kind)
	}
	if got := r.Lookup("不是IP"); got.Kind != KindUnknown || got.Display != "" {
		t.Errorf("非法输入返回了 %+v", got)
	}
	// 公网地址在没有数据库时应当安静地返回空，而不是崩掉——归属地是锦上
	// 添花，缺了它日志照记。
	if got := r.Lookup("8.8.8.8"); got.Display != "" {
		t.Errorf("无数据库时返回了 %+v", got)
	}
}
