package subscription

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 「我的订阅」：在线数用 LATERAL 带上订阅 id，保证条件推进视图（不依赖优化器能否
// 上提视图）；配额一次取齐，不再每条订阅各查一次；整个读取只有两条查询。
func TestMySubscriptionsQueriesStayConstant(t *testing.T) {
	want := "WHERE od.tenant_id = s.tenant_id AND od.subscription_id = s.id"
	if !strings.Contains(mySubscriptionsSQL, "LEFT JOIN LATERAL (") || !strings.Contains(mySubscriptionsSQL, want) {
		t.Fatal("MySubscriptions must read online devices per subscription through LATERAL")
	}
	if !strings.Contains(mySubscriptionsSQL, "FROM traffic_pack_grants g") {
		t.Fatal("pack remaining must be folded into the main query")
	}
	if !strings.Contains(myQuotasSQL, "subscription_id = ANY($2::uuid[])") {
		t.Fatal("quotas must be fetched for all subscriptions in one query")
	}
	body := sourcetest.Load(t, ".").Decl("Service.MySubscriptions")
	if strings.Count(body, "tx.Query(") != 2 || strings.Contains(body, "tx.QueryRow(") {
		t.Fatal("MySubscriptions must run exactly two queries regardless of how many subscriptions there are")
	}
}
