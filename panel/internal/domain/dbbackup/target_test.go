package dbbackup

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if addrs, ok := r[host]; ok {
		return addrs, nil
	}
	return nil, errors.New("not found")
}

func TestParseTargetRejectsUnsafeEndpoints(t *testing.T) {
	resolver := staticResolver{
		"public.example":  {netip.MustParseAddr("8.8.8.8")},
		"private.example": {netip.MustParseAddr("10.0.0.8")},
		"mixed.example":   {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")},
	}
	tests := []struct {
		name, endpoint string
		private        bool
	}{
		{"plain HTTP", "http://public.example", false},
		{"userinfo", "https://user:pass@public.example", false},
		{"query", "https://public.example?x=1", false},
		{"fragment", "https://public.example#x", false},
		{"endpoint path", "https://public.example/dav", false},
		{"localhost", "https://localhost", true},
		{"loopback", "https://127.0.0.1", true},
		{"metadata", "https://169.254.169.254", true},
		{"private default", "https://private.example", false},
		{"mixed DNS", "https://mixed.example", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTarget(context.Background(), resolver, tc.endpoint, "/backups", "", "", tc.private); err == nil {
				t.Fatalf("ParseTarget(%q) succeeded", tc.endpoint)
			}
		})
	}
}

func TestParseTargetAllowsExplicitPrivateAndNormalizes(t *testing.T) {
	resolver := staticResolver{"nas.example": {netip.MustParseAddr("192.168.1.9")}}
	target, err := ParseTarget(context.Background(), resolver, "https://NAS.EXAMPLE:8443/", " /db/daily/ ", "u", "p", true)
	if err != nil {
		t.Fatal(err)
	}
	if target.Origin.String() != "https://nas.example:8443" || target.BasePath != "/db/daily" || !target.AllowPrivate {
		t.Fatalf("target=%+v", target)
	}
}

func TestNormalizeBasePath(t *testing.T) {
	for _, bad := range []string{"/", "relative", "/a/../b", "/a/%2e%2e", "/a\\b", "/a?b", "/a\x00b"} {
		if _, err := NormalizeBasePath(bad); err == nil {
			t.Fatalf("NormalizeBasePath(%q) succeeded", bad)
		}
	}
	if got, err := NormalizeBasePath(" /pandora//daily/ "); err != nil || got != "/pandora/daily" {
		t.Fatalf("NormalizeBasePath got=%q err=%v", got, err)
	}
}

func TestAllowedAddressMatrix(t *testing.T) {
	for _, raw := range []string{"0.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254", "192.0.2.1", "198.18.0.1", "224.0.0.1", "::1", "fe80::1", "fc00::1", "64:ff9b:1::1", "64:ff9b:1:ffff::1", "::ffff:127.0.0.1"} {
		if allowedAddress(netip.MustParseAddr(raw), false) {
			t.Fatalf("address %s allowed", raw)
		}
	}
	if !allowedAddress(netip.MustParseAddr("8.8.8.8"), false) {
		t.Fatal("public address rejected")
	}
	if !allowedAddress(netip.MustParseAddr("10.0.0.2"), true) {
		t.Fatal("explicit private address rejected")
	}
	if allowedAddress(netip.MustParseAddr("fd00:ec2::254"), true) {
		t.Fatal("IPv6 metadata address allowed")
	}
	if allowedAddress(netip.MustParseAddr("64:ff9b:1::1"), true) {
		t.Fatal("RFC 8215 local-use NAT64 prefix bypassed the private-network policy")
	}
}

func TestTargetFormattingRedactsCredentials(t *testing.T) {
	target := Target{username: "backup-user", password: "top-secret"}
	for _, formatted := range []string{fmt.Sprintf("%v", target), fmt.Sprintf("%+v", target), fmt.Sprintf("%#v", target)} {
		if strings.Contains(formatted, "top-secret") || strings.Contains(formatted, "backup-user") {
			t.Fatalf("credential leaked in formatted target: %s", formatted)
		}
	}
}
