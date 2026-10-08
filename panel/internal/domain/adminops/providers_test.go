package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 后台渠道列表的 min_amount 与站点最低付款额（billing 的 minPaymentSQL）是同一个 CASE：
// 一边改了口径另一边没跟，渠道页显示的最低额就和结账时真正用的对不上。
func TestProviderMinAmountMatchesBilling(t *testing.T) {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	mine := norm(strings.TrimSuffix(providerMinAmountSQL, "::bigint"))
	billing := norm(sourcetest.Load(t, "../billing").Decl("minPaymentSQL"))
	if !strings.HasPrefix(mine, "CASE ") || !strings.HasSuffix(mine, " END") || !strings.Contains(billing, "max("+mine+")") {
		t.Fatalf("providerMinAmountSQL drifted from billing.minPaymentSQL\nadminops: %s\nbilling:  %s", mine, billing)
	}
	if !strings.Contains(sourcetest.Load(t, ".").Decl("Service.ListProviders"), "`+providerMinAmountSQL+`") {
		t.Fatal("ListProviders must read min_amount through providerMinAmountSQL")
	}
}
