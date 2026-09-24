// [INPUT]: 依赖 manual_order.go 的 CreateManualOrder 与 ManualSettlement*，依赖 release.go 的 CancelOrder，依赖 order_release_pg18_test.go 的一次性租户夹具与 orderReleasePG18Claim
// [OUTPUT]: 对包内提供 orderReleasePG18ManualOrderCases，挂在 TestOrderReleasePG18（run-pg18-gates.sh 的 order_release 域）
// [POS]: billing 人工单结算方式的 PG18 证明：赠送当场履约、待用户支付留下一张带开单人的待支付单、线下已收款暂不接受
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
	create := func(t *testing.T, settlement, label string) (*CreateOrderOutput, error) {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, operator, CheckoutIdempotencyScope, label)
		return service.CreateManualOrder(ctx, fx.tenant, CreateManualOrderInput{
			UserID: target, PlanID: fx.plan, PriceID: fx.price,
			Reason: "PG18 人工单结算方式测试", ActorID: operator,
			Settlement: settlement, Claim: claim,
		})
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

	// 线下已收款暂不接受（会绕开标记已支付的重认证）；未知取值同样 422。
	for _, bad := range []string{"offline", "balance", "cash"} {
		var he *httpx.Error
		if _, err := create(t, bad, "manual-"+bad); !errors.As(err, &he) ||
			he.Code != httpx.CodeValidationFailed || he.Fields["settlement"] == "" {
			t.Fatalf("settlement=%s err=%v, want 422 on settlement", bad, err)
		}
	}
	t.Log("marker=manual_order_offline_rejected_ok")
}
