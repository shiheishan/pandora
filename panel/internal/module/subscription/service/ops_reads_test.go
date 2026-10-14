package service

import (
	"testing"
)

func TestNormalizeIPForHMAC(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "spaces", in: "   ", want: ""},
		{name: "clean ipv4", in: "1.2.3.4", want: "1.2.3.4"},
		{name: "ipv4 with space", in: "  1.2.3.4  ", want: "1.2.3.4"},
		{name: "clean ipv6", in: "2001:db8::1", want: "2001:db8::1"},
		{name: "uncompressed ipv6", in: "2001:0db8:0000:0000:0000:0000:0000:0001", want: "2001:db8::1"},
		{name: "loopback ipv6", in: "::1", want: "::1"},
		{name: "invalid ip fallback to raw", in: "not-an-ip", want: "not-an-ip"},
		{name: "invalid ip with space fallback", in: "  not-an-ip  ", want: "not-an-ip"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeIPForHMAC(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeIPForHMAC(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
