package kernel

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

func TestBuildDirectOnlyAndSelect(t *testing.T) {
	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Select(route.Meta{Domain: "example.com", Network: "tcp"}); got != DirectTag {
		t.Fatalf("default route = %q, want %q", got, DirectTag)
	}
}

func TestBuildStrictRejectsUnknownTargetAndProtocol(t *testing.T) {
	tests := []struct {
		name string
		cfg  *core.Routing
		want string
	}{
		{
			name: "unknown rule target",
			cfg: &core.Routing{Routes: []core.Route{{
				Matcher: map[string]any{"domain": "example.com"}, OutboundTag: "missing",
			}}},
			want: "不存在的出站",
		},
		{
			name: "unsupported outbound",
			cfg: &core.Routing{Outbounds: []core.Outbound{{
				Tag: "relay", Type: "vmess", Settings: map[string]any{},
			}}},
			want: "不支持的出站",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestBuildUsesSourceAndPortMatchers(t *testing.T) {
	r, err := Build(&core.Routing{
		Outbounds: []core.Outbound{{
			Tag: "relay", Type: "block",
		}},
		Routes: []core.Route{{
			Matcher: map[string]any{
				"source_cidrs": []any{"10.0.0.0/8"},
				"ports":        []any{"443"},
			},
			OutboundTag: "relay",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Select(route.Meta{
		SourceIP: netip.MustParseAddr("10.1.2.3"), Port: 443,
	}); got != "relay" {
		t.Fatalf("matched route = %q, want relay", got)
	}
	if got := r.Select(route.Meta{
		SourceIP: netip.MustParseAddr("192.0.2.1"), Port: 443,
	}); got != DirectTag {
		t.Fatalf("non-matching source = %q, want direct", got)
	}
}

func TestRuntimeBlockDialUsesSelectedOutbound(t *testing.T) {
	r, err := Build(&core.Routing{
		Routes: []core.Route{{Matcher: map[string]any{"domain": "blocked.example"}, OutboundTag: BlockTag}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.DialTCP(context.Background(), route.Meta{Domain: "blocked.example", Network: "tcp"}, M.ParseSocksaddrHostPort("blocked.example", 443))
	if !errors.Is(err, outbound.ErrBlocked) {
		t.Fatalf("blocked dial error = %v, want ErrBlocked", err)
	}
}
