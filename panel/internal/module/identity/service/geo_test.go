package service

import (
	"testing"
)

func TestServiceLocateWithNilGeo(t *testing.T) {
	s := &Service{} // s.geo 为 nil

	cases := []struct {
		name        string
		ip          string
		wantDisplay string
		wantKind    string
	}{
		{
			name:        "回环 IPv4",
			ip:          "127.0.0.1",
			wantDisplay: "本机回环地址",
			wantKind:    "loopback",
		},
		{
			name:        "回环 IPv6",
			ip:          "::1",
			wantDisplay: "本机回环地址",
			wantKind:    "loopback",
		},
		{
			name:        "私有内网 IPv4",
			ip:          "192.168.1.1",
			wantDisplay: "内网地址",
			wantKind:    "private",
		},
		{
			name:        "私有内网 10 段",
			ip:          "10.0.0.5",
			wantDisplay: "内网地址",
			wantKind:    "private",
		},
		{
			name:        "公网 IP 在 geo 为 nil 时返回空",
			ip:          "8.8.8.8",
			wantDisplay: "",
			wantKind:    "",
		},
		{
			name:        "非法 IP 返回空",
			ip:          "not-an-ip",
			wantDisplay: "",
			wantKind:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			display, kind := s.locate(tc.ip)
			if display != tc.wantDisplay {
				t.Errorf("ip %q display = %q, want %q", tc.ip, display, tc.wantDisplay)
			}
			if kind != tc.wantKind {
				t.Errorf("ip %q kind = %q, want %q", tc.ip, kind, tc.wantKind)
			}
		})
	}
}
