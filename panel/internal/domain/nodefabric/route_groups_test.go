package nodefabric

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestRouteGroupFields(t *testing.T) {
	name, desc, order := "  香港解锁  ", " 流媒体 ", 10
	if err := normalizeRouteGroupFields(&name, &desc, &order); err != nil || name != "香港解锁" || desc != "流媒体" {
		t.Fatalf("normalize = %q %q %v", name, desc, err)
	}
	long := strings.Repeat("组", 65)
	blank := "   "
	tooBig := maxRouteGroupSortOrder + 1
	for label, call := range map[string]func() error{
		"name too long": func() error { return normalizeRouteGroupFields(&long, nil, nil) },
		"name blank":    func() error { return normalizeRouteGroupFields(&blank, nil, nil) },
		"desc too long": func() error { d := strings.Repeat("x", 501); return normalizeRouteGroupFields(nil, &d, nil) },
		"order too big": func() error { return normalizeRouteGroupFields(nil, nil, &tooBig) },
	} {
		var he *httpx.Error
		if err := call(); !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Errorf("%s: err = %v, want validation_failed", label, err)
		}
	}
	ok := strings.Repeat("组", 64)
	if err := normalizeRouteGroupFields(&ok, nil, nil); err != nil {
		t.Fatalf("64 runes must pass: %v", err)
	}
	if _, err := parseRouteGroupID("not-a-uuid"); err == nil {
		t.Fatal("non-uuid group id must be rejected before SQL")
	}
}

func TestNormalizeIDs(t *testing.T) {
	a := "0199a000-0000-7000-8000-000000000002"
	b := "0199A000-0000-7000-8000-000000000001"
	got, err := normalizeIDs([]string{a, b, a}, "node_ids")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{strings.ToLower(b), a}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeIDs = %v, want %v", got, want)
	}
	if got, _ := normalizeIDs(nil, "node_ids"); got == nil || len(got) != 0 {
		t.Fatalf("nil must normalize to empty, got %#v", got)
	}
	if _, err := normalizeIDs([]string{"x"}, "node_ids"); err == nil {
		t.Fatal("invalid id must be rejected")
	}
}

func TestSymmetricDiff(t *testing.T) {
	if got := symmetricDiff([]string{"a", "b", "c"}, []string{"b", "d"}); !reflect.DeepEqual(got, []string{"a", "c", "d"}) {
		t.Fatalf("symmetricDiff = %v", got)
	}
	if got := symmetricDiff([]string{"a"}, []string{"a"}); len(got) != 0 {
		t.Fatalf("no change must be empty, got %v", got)
	}
}

// 规则可以指向任何一个可见范围的出站，自定义出站按原样精确比较（与下发、pdnd 查表一致）；
// 内置 direct / block 沿用不分大小写
func TestCheckRouteRefsAcrossScopes(t *testing.T) {
	own, err := ValidateRoutingPayload([]RoutingOutbound{{Tag: " Mine ", Type: "socks"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !own["Mine"] || own["mine"] || own["direct"] {
		t.Fatalf("payload tags must be the trimmed originals without builtins, got %v", own)
	}
	global := map[string]bool{"pub": true}
	group := map[string]bool{"UNLOCK": true}
	rule := func(tag string) RoutingRule {
		return RoutingRule{Matcher: json.RawMessage(`{"port":[443]}`), OutboundTag: tag, Enabled: true}
	}
	if err := checkRouteRefs([]RoutingRule{rule("Mine"), rule("pub"), rule("UNLOCK"), rule("block"), rule("Direct")}, own, global, group); err != nil {
		t.Fatalf("visible refs rejected: %v", err)
	}
	for i, tag := range []string{"mine", "PUB", "unlock", "pub ", "other"} {
		err := checkRouteRefs([]RoutingRule{rule("direct"), rule(tag)}, own, global, group)
		var he *httpx.Error
		if !errors.As(err, &he) || !strings.Contains(he.Fields["routes"], "第 2 条规则指向不存在的出站") {
			t.Fatalf("case %d: ref %q must be rejected, err = %v", i, tag, err)
		}
	}
	// 重名与占用内置名仍按不分大小写拒绝
	if _, err := ValidateRoutingPayload([]RoutingOutbound{{Tag: "HK", Type: "socks"}, {Tag: "hk", Type: "socks"}}, nil); err == nil {
		t.Fatal("case-only duplicate outbound tags must be rejected")
	}
	if _, err := ValidateRoutingPayload([]RoutingOutbound{{Tag: "Block", Type: "socks"}}, nil); err == nil {
		t.Fatal("an outbound must not take a builtin name in any case")
	}
}

func TestDanglingRefLabel(t *testing.T) {
	if got := (danglingRef{Kind: "group", Name: "香港", Tag: "unlock"}).String(); got != "路由组 香港 → unlock" {
		t.Fatalf("group label = %q", got)
	}
	if got := (danglingRef{Kind: "node", Name: "hk-01", Tag: "relay"}).String(); got != "节点 hk-01 → relay" {
		t.Fatalf("node label = %q", got)
	}
}

// 00096 的形状被 Go 侧的范围谓词与合并口径依赖：三选一 CHECK、全局 tag 索引收窄到两列都空、组内 tag 唯一
func TestRouteGroupMigrationShape(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/00096_route_groups.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	up = up[:strings.Index(up, "-- +goose Down")]
	for _, want := range []string{
		"node_outbounds_scope_check CHECK (node_id IS NULL OR group_id IS NULL)",
		"node_routes_scope_check CHECK (node_id IS NULL OR group_id IS NULL)",
		"ON node_outbounds (tenant_id, tag) WHERE node_id IS NULL AND group_id IS NULL",
		"ON node_outbounds (tenant_id, group_id, tag) WHERE group_id IS NOT NULL",
		"REFERENCES route_groups (tenant_id, id) ON DELETE CASCADE",
		"REFERENCES nodes (tenant_id, id) ON DELETE CASCADE",
		"app.enable_tenant_rls('route_groups')",
		"app.enable_tenant_rls('route_group_members')",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("00096 Up lost %q", want)
		}
	}
}
