package subscription

import (
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 「我的订阅」：在线数用 LATERAL 带上订阅 id，保证条件推进视图（不依赖优化器能否
// 上提视图）；配额一次取齐，不再每条订阅各查一次；有订阅时整个读取只有两条查询，
// 一份都没有时才另查一次未分配的流量包余量。
func TestMySubscriptionsQueriesStayConstant(t *testing.T) {
	want := "WHERE od.tenant_id = s.tenant_id AND od.subscription_id = s.id"
	if !strings.Contains(mySubscriptionsSQL, "LEFT JOIN LATERAL (") || !strings.Contains(mySubscriptionsSQL, want) {
		t.Fatal("MySubscriptions must read online devices per subscription through LATERAL")
	}
	// 流量包按份挂：每份的余量按订阅取，未分配的余量作为不相关子查询并进主查询
	if !strings.Contains(mySubscriptionsSQL, "g.subscription_id = s.id") ||
		!strings.Contains(mySubscriptionsSQL, unattachedPacksSQL) ||
		!strings.Contains(unattachedPacksSQL, "g.subscription_id IS NULL") {
		t.Fatal("pack remaining must be per subscription, unattached packs folded into the main query")
	}
	// 可挪一次的旧包：与这一份的余量同一次聚合（不加查询），只认只有回填流水、没被挪过的
	if !strings.Contains(mySubscriptionsSQL, "FILTER (WHERE "+legacyMovablePackSQL+")") ||
		!strings.Contains(legacyMovablePackSQL, "t.actor_kind = 'migration'") ||
		!strings.Contains(legacyMovablePackSQL, "AND NOT EXISTS") || !strings.Contains(legacyMovablePackSQL, "t.actor_kind <> 'migration'") {
		t.Fatal("legacy movable packs must be summed in the per-subscription pack aggregate, migration-only grants")
	}
	if strings.Contains(mySubscriptionsSQL, "g.user_id = s.user_id") {
		t.Fatal("a subscription must not show packs attached to the owner's other subscriptions")
	}
	if !strings.Contains(myQuotasSQL, "subscription_id = ANY($2::uuid[])") {
		t.Fatal("quotas must be fetched for all subscriptions in one query")
	}
	body := sourcetest.Load(t, ".").Decl("Service.MySubscriptions")
	if strings.Count(body, "tx.Query(") != 2 || strings.Count(body, "tx.QueryRow(") != 1 {
		t.Fatal("MySubscriptions must run exactly two queries regardless of how many subscriptions there are")
	}
	fallback := body[strings.Index(body, "if len(out.Subscriptions) == 0 {"):]
	if !strings.HasPrefix(strings.TrimSpace(fallback[strings.Index(fallback, "{")+1:]),
		"return tx.QueryRow(ctx, unattachedPacksSQL") {
		t.Fatal("the unattached-pack query only runs when the user has no subscriptions")
	}
}

// renew_until 与履约 renewalBase 同口径：到期日在将来的接在后面，已过期的从现在起算；
// 不能续时为 nil。
func TestRenewUntil(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	month := &MyRenewalPrice{BillingInterval: "month", IntervalCount: 1, Available: true}
	future := now.Add(5 * 24 * time.Hour)
	past := now.Add(-3 * 24 * time.Hour)
	if got := renewUntil(true, month, &future, now); got == nil || !got.Equal(future.AddDate(0, 1, 0)) {
		t.Fatalf("live subscription renews from its end: %v", got)
	}
	if got := renewUntil(true, month, &past, now); got == nil || !got.Equal(now.AddDate(0, 1, 0)) {
		t.Fatalf("expired subscription renews from now: %v", got)
	}
	if got := renewUntil(true, month, nil, now); got == nil || !got.Equal(now.AddDate(0, 1, 0)) {
		t.Fatalf("subscription without an end renews from now: %v", got)
	}
	unavailable := *month
	unavailable.Available = false
	for name, got := range map[string]*time.Time{
		"not renewable":     renewUntil(false, month, &future, now),
		"no price":          renewUntil(true, nil, &future, now),
		"price unavailable": renewUntil(true, &unavailable, &future, now),
	} {
		if got != nil {
			t.Errorf("%s: renew_until=%v, want null", name, got)
		}
	}
}

// 门户「可挪一次的旧包」与 billing 真正放行挪动的判定（legacyMovableSQL，与 00137 守卫情形 3 同口径）
// 必须是同一个条件：一边改了另一边没跟，页面上写着能挪，点了却被 422 拒绝。
func TestLegacyMovablePackMatchesBilling(t *testing.T) {
	norm := func(s string) string {
		s = strings.Join(strings.Fields(s), " ")
		s = strings.ReplaceAll(s, "( ", "(")
		return strings.ReplaceAll(s, " )", ")")
	}
	billing := norm(sourcetest.Load(t, "../billing").Decl("legacyMovableSQL"))
	if mine := norm(legacyMovablePackSQL); !strings.Contains(billing, "`("+mine+")`") {
		t.Fatalf("legacyMovablePackSQL drifted from billing.legacyMovableSQL\nsubscription: %s\nbilling:      %s", mine, billing)
	}
}
