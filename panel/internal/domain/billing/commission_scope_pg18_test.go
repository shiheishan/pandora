// [INPUT]: 依赖 commission.go 的 accrueCommission（经 HandlePaymentWebhook 触发）与 CommissionScope*，依赖 order_release_pg18_test.go 的一次性租户夹具、newOrder 与 webhook
// [OUTPUT]: 对包内提供 orderReleasePG18CommissionScopeCases，挂在 TestOrderReleasePG18（run-pg18-gates.sh 的 order_release 域）
// [POS]: billing 计佣范围的 PG18 证明：first_order 下被推荐人只有第一笔计佣订单返佣，every_order（兜底）每笔都返
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func orderReleasePG18CommissionScopeCases(t *testing.T, ctx context.Context, admin *pgx.Conn,
	service *Service, fx orderReleasePG18Fixture,
	newOrder func(*testing.T, string, string) string,
	webhook func(orderID, eventID, paymentID string, amount int64) PaymentWebhookInput) {

	t.Helper()
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, query, args...); err != nil {
			t.Fatalf("commission scope fixture: %v\nSQL: %s", err, query)
		}
	}
	// 用完恢复成每笔计佣：后面的子测试按它写的。system_settings 的改动记进
	// 追加写的 system_setting_revisions，删不掉，只能改回去。
	defer must(`UPDATE system_settings SET value='"every_order"'::jsonb
		WHERE tenant_id=$1 AND key='commission.scope'`, fx.tenant)

	seq := 0
	pay := func(t *testing.T, userID string) (string, bool) {
		t.Helper()
		seq++
		label := "scope-" + uuid.NewString()[:8]
		orderID := newOrder(t, userID, label)
		capture := webhook(orderID, label+"-event-"+fx.suffix, label+"-payment-"+fx.suffix, 1000)
		if out, err := service.HandlePaymentWebhook(ctx, fx.tenant, capture); err != nil || out == nil || !out.Processed {
			t.Fatalf("pay scope order %d output=%#v err=%v", seq, out, err)
		}
		var accrued bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commission_entries
			WHERE tenant_id=$1 AND order_id=$2::uuid)`, fx.tenant, orderID).Scan(&accrued); err != nil {
			t.Fatalf("read commission for scope order: %v", err)
		}
		return orderID, accrued
	}

	must(`INSERT INTO system_settings(tenant_id,key,value) VALUES ($1,'commission.scope','"first_order"'::jsonb)
		ON CONFLICT (tenant_id,key) DO UPDATE SET value=EXCLUDED.value`, fx.tenant)

	// 老被推荐人前面已经计过佣：再下单不返。
	if _, accrued := pay(t, fx.commissionBuyer); accrued {
		t.Fatal("first_order: a referee with earlier commissions accrued again")
	}
	// 新被推荐人：第一笔返，第二笔不返。
	fresh := uuid.NewString()
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES ($1,$2,$3,'Scope Buyer','active')`,
		fresh, fx.tenant, "scope-"+fresh[:8]+"@example.test")
	must(`INSERT INTO referrals(tenant_id,referee_user_id,referrer_user_id,channel,campaign)
		VALUES($1,$2,$3,'pg18','commission-scope')`, fx.tenant, fresh, fx.referrer)
	if _, accrued := pay(t, fresh); !accrued {
		t.Fatal("first_order: the referee's first order did not accrue")
	}
	if _, accrued := pay(t, fresh); accrued {
		t.Fatal("first_order: the referee's second order accrued")
	}
	t.Log("marker=commission_scope_first_order_ok")

	// 改回每笔订单：同一个被推荐人的下一单照常返佣。
	must(`UPDATE system_settings SET value='"every_order"'::jsonb WHERE tenant_id=$1 AND key='commission.scope'`, fx.tenant)
	if _, accrued := pay(t, fresh); !accrued {
		t.Fatal("every_order: a repeat order did not accrue")
	}
	t.Log("marker=commission_scope_every_order_ok")
}
