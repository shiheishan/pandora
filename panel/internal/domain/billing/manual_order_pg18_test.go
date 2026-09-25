// [INPUT]: 依赖 manual_order.go 的 CreateManualOrder 与 ManualSettlement*，依赖 release.go 的 CancelOrder，依赖 order_release_pg18_test.go 的一次性租户夹具与 orderReleasePG18Claim
// [OUTPUT]: 对包内提供 orderReleasePG18ManualOrderCases，挂在 TestOrderReleasePG18（run-pg18-gates.sh 的 order_release 域）
// [POS]: billing 人工单结算方式的 PG18 证明：赠送当场履约、待用户支付留下一张带开单人的待支付单、线下已收款在建单事务里按 offline 渠道结清（收入、佣金、履约、幂等记录与标记已支付同口径，凭证号重复整单回滚）、从余额扣除仍拒绝
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func orderReleasePG18ManualOrderCases(t *testing.T, ctx context.Context, admin *pgx.Conn,
	service *Service, fx orderReleasePG18Fixture) {

	t.Helper()
	// 目标用户单独建一个，免得赠送出去的订阅和待支付单影响别的子测试。
	target := uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO users(id,tenant_id,email,display_name,status)
		VALUES ($1,$2,$3,'Manual Target','active')`, target, fx.tenant,
		"manual-"+target[:8]+"@example.test"); err != nil {
		t.Fatalf("seed manual target: %v", err)
	}
	// 发起人是「管理员」：领域层不看角色，只看幂等声明归属与 created_by。
	operator := fx.referrer
	createRef := func(t *testing.T, settlement, reference, label string) (*CreateOrderOutput, error) {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, operator, CheckoutIdempotencyScope, label)
		return service.CreateManualOrder(ctx, fx.tenant, CreateManualOrderInput{
			UserID: target, PlanID: fx.plan, PriceID: fx.price,
			Reason: "PG18 人工单结算方式测试", ActorID: operator,
			Settlement: settlement, Reference: reference, Claim: claim,
		})
	}
	create := func(t *testing.T, settlement, label string) (*CreateOrderOutput, error) {
		t.Helper()
		return createRef(t, settlement, "", label)
	}
	row := func(t *testing.T, orderID string) (status string, payable int64, createdBy, reason *string) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT status, payable_amount, created_by::text, manual_reason
			FROM orders WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, orderID).
			Scan(&status, &payable, &createdBy, &reason); err != nil {
			t.Fatalf("read manual order: %v", err)
		}
		return
	}

	// 待用户支付：原价挂账给用户付，开单人与原因照样落库。
	pending, err := create(t, ManualSettlementPending, "manual-pending")
	if err != nil {
		t.Fatalf("pending manual order: %v", err)
	}
	status, payable, createdBy, reason := row(t, pending.OrderID)
	if pending.Status != "pending_payment" || pending.PayableAmount != 1000 || pending.DiscountAmount != 0 ||
		status != "pending_payment" || payable != 1000 ||
		createdBy == nil || *createdBy != operator || reason == nil {
		t.Fatalf("pending manual order out=%#v db status=%s payable=%d created_by=%v reason=%v",
			pending, status, payable, createdBy, reason)
	}
	// 用户自己可以取消它（释放预留），与自助下单的待支付单一样。
	if out, err := service.CancelOrder(ctx, fx.tenant, target, pending.OrderID); err != nil || out == nil || out.Status != "cancelled" {
		t.Fatalf("cancel pending manual order out=%#v err=%v", out, err)
	}
	t.Log("marker=manual_order_pending_settlement_ok")

	// 缺省是赠送：全额减免、当场履约。
	grant, err := create(t, "", "manual-grant")
	if err != nil {
		t.Fatalf("grant manual order: %v", err)
	}
	if status, payable, createdBy, _ := row(t, grant.OrderID); grant.Status != "fulfilled" ||
		status != "fulfilled" || payable != 0 || createdBy == nil || *createdBy != operator {
		t.Fatalf("grant manual order out=%#v db status=%s payable=%d", grant, status, payable)
	}
	t.Log("marker=manual_order_grant_default_ok")

	// 从余额扣除（D-C-3 未决）与未知取值仍 422；线下已收款不带凭证号 422。
	for _, bad := range []string{"balance", "cash"} {
		var he *httpx.Error
		if _, err := create(t, bad, "manual-"+bad); !errors.As(err, &he) ||
			he.Code != httpx.CodeValidationFailed || he.Fields["settlement"] == "" {
			t.Fatalf("settlement=%s err=%v, want 422 on settlement", bad, err)
		}
	}
	var he *httpx.Error
	if _, err := create(t, ManualSettlementOffline, "manual-offline-noref"); !errors.As(err, &he) ||
		he.Code != httpx.CodeValidationFailed || he.Fields["reference"] == "" {
		t.Fatalf("offline without reference err=%v, want 422 on reference", err)
	}
	t.Log("marker=manual_order_balance_rejected_ok")

	orderReleasePG18ManualOfflineCases(t, ctx, admin, fx, target, operator, createRef, row)
}

// 线下已收款：建单与结清在同一个事务里，与标记已支付同一条结算链路。
func orderReleasePG18ManualOfflineCases(t *testing.T, ctx context.Context, admin *pgx.Conn,
	fx orderReleasePG18Fixture, target, operator string,
	createRef func(*testing.T, string, string, string) (*CreateOrderOutput, error),
	row func(*testing.T, string) (string, int64, *string, *string)) {

	t.Helper()
	// 一次性租户是迁移之后才建的：线下渠道由建租户触发器种下，这里不再手工补，
	// 标记已支付 / 线下已收款走通即证明新租户能用（R94 同批）。
	// 再给目标用户挂一个推荐人，证明佣金与在线支付同样计提。
	referrer := uuid.NewString()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,tenant_id,email,display_name,status)
			VALUES ($1,$2,$3,'Manual Offline Referrer','active')`,
			[]any{referrer, fx.tenant, "manual-ref-" + referrer[:8] + "@example.test"}},
		{`INSERT INTO referrals(tenant_id,referee_user_id,referrer_user_id,channel,campaign)
			VALUES ($1,$2,$3,'pg18','manual-offline')`, []any{fx.tenant, target, referrer}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed manual offline fixture: %v\nSQL: %s", err, seed.sql)
		}
	}

	reference := "BANK-" + uuid.NewString()[:8]
	paid, err := createRef(t, ManualSettlementOffline, reference, "manual-offline")
	if err != nil {
		t.Fatalf("offline manual order: %v", err)
	}
	status, payable, createdBy, _ := row(t, paid.OrderID)
	if paid.Status != "fulfilled" || paid.PayableAmount != 1000 || paid.DiscountAmount != 0 ||
		status != "fulfilled" || payable != 1000 || createdBy == nil || *createdBy != operator {
		t.Fatalf("offline manual order out=%#v db status=%s payable=%d created_by=%v",
			paid, status, payable, createdBy)
	}

	// 收款：offline 渠道一笔 1000，凭证号即渠道流水号；订单 paid_amount 同步。
	var provider, providerPaymentID string
	var amount, paidAmount int64
	if err := admin.QueryRow(ctx, `
		SELECT pp.code, p.provider_payment_id, p.amount, o.paid_amount
		  FROM payments p
		  JOIN payment_providers pp ON pp.tenant_id = p.tenant_id AND pp.id = p.provider_id
		  JOIN orders o ON o.tenant_id = p.tenant_id AND o.id = p.order_id
		 WHERE p.tenant_id = $1 AND p.order_id = $2::uuid AND p.status = 'succeeded'`,
		fx.tenant, paid.OrderID).Scan(&provider, &providerPaymentID, &amount, &paidAmount); err != nil {
		t.Fatalf("read offline payment: %v", err)
	}
	if provider != OfflineProviderCode || providerPaymentID != "offline:"+reference ||
		amount != 1000 || paidAmount != 1000 {
		t.Fatalf("offline payment provider=%s id=%s amount=%d paid=%d",
			provider, providerPaymentID, amount, paidAmount)
	}
	// 收入：order_paid 分录贷记平台收入 1000，渠道资金借记 1000（手续费 0）。
	var revenue, channel int64
	if err := admin.QueryRow(ctx, `
		SELECT coalesce(sum(e.amount) FILTER (WHERE a.account_type = $3 AND e.direction = 'credit'), 0),
		       coalesce(sum(e.amount) FILTER (WHERE a.account_type = $4 AND e.direction = 'debit'), 0)
		  FROM ledger_transactions t
		  JOIN ledger_entries e ON e.tenant_id = t.tenant_id AND e.transaction_id = t.id
		  JOIN ledger_accounts a ON a.tenant_id = e.tenant_id AND a.id = e.account_id
		 WHERE t.tenant_id = $1 AND t.source_type = 'order' AND t.source_id = $2::uuid
		   AND t.kind = 'order_paid'`,
		fx.tenant, paid.OrderID, AccountPlatformRevenue, AccountChannelCash).Scan(&revenue, &channel); err != nil {
		t.Fatalf("read offline revenue: %v", err)
	}
	if revenue != 1000 || channel != 1000 {
		t.Fatalf("offline order_paid revenue=%d channel=%d, want 1000/1000", revenue, channel)
	}
	// 佣金：10%，与在线支付同一个计提点。
	var commission int64
	if err := admin.QueryRow(ctx, `SELECT commission_amount FROM commission_entries
		WHERE tenant_id = $1 AND order_id = $2::uuid AND referrer_user_id = $3::uuid`,
		fx.tenant, paid.OrderID, referrer).Scan(&commission); err != nil || commission != 100 {
		t.Fatalf("offline commission=%d err=%v, want 100", commission, err)
	}
	// 履约：开了订阅；幂等记录里存的就是这份「已履约」的 201，重放拿到同样的结果。
	var subscriptions int
	var replayCode int
	var replayBody []byte
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT count(*)::int FROM subscriptions s
		         WHERE s.tenant_id = o.tenant_id AND s.id = o.subscription_id),
		       k.response_code, k.response_payload
		  FROM orders o JOIN idempotency_keys k
		    ON k.tenant_id = o.tenant_id AND k.id = o.idempotency_key_id
		 WHERE o.tenant_id = $1 AND o.id = $2::uuid AND k.status = 'succeeded'`,
		fx.tenant, paid.OrderID).Scan(&subscriptions, &replayCode, &replayBody); err != nil {
		t.Fatalf("read offline fulfilment and idempotency record: %v", err)
	}
	if subscriptions != 1 || replayCode != 201 ||
		string(replayBody) != string(paid.PreparedResponse().BodyBytes()) {
		t.Fatalf("offline subscriptions=%d replay=%d %s, want 1 and the prepared 201 %s",
			subscriptions, replayCode, replayBody, paid.PreparedResponse().BodyBytes())
	}
	// 审计：人工单这一条带凭证号与入账凭据。
	var auditRef, auditPayment string
	if err := admin.QueryRow(ctx, `
		SELECT after_digest->>'reference', after_digest->>'payment_id' FROM audit_events
		 WHERE tenant_id = $1 AND action = 'order.manual_created' AND resource_id = $2::uuid`,
		fx.tenant, paid.OrderID).Scan(&auditRef, &auditPayment); err != nil ||
		auditRef != reference || auditPayment == "" {
		t.Fatalf("offline audit reference=%q payment=%q err=%v", auditRef, auditPayment, err)
	}
	t.Log("marker=manual_order_offline_settled_ok")

	// 同一张凭证不能入账两次：第二张人工单整单回滚，库里不多一张单。
	countOrders := func() int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM orders
			WHERE tenant_id = $1 AND user_id = $2::uuid`, fx.tenant, target).Scan(&n); err != nil {
			t.Fatalf("count target orders: %v", err)
		}
		return n
	}
	before := countOrders()
	var he *httpx.Error
	if _, err := createRef(t, ManualSettlementOffline, reference, "manual-offline-dup"); !errors.As(err, &he) ||
		he.Code != httpx.CodeConflict {
		t.Fatalf("duplicate offline reference err=%v, want 409", err)
	}
	if after := countOrders(); after != before {
		t.Fatalf("duplicate offline reference left an order behind: before=%d after=%d", before, after)
	}
	t.Log("marker=manual_order_offline_duplicate_reference_rolled_back_ok")
}
