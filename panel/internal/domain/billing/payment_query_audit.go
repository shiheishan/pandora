package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// OrderQueryAuditAction 是后台查单的审计动作。order. 前缀让访问日志把它归进「订单」分类。
const OrderQueryAuditAction = "order.payment_queried"

// orderQueryResult 把一次查单归成审计里的 result：
// reconciled 已补记 / already_recorded 已入账 / paid（渠道说已付但没补记）/ unpaid / not_found / failed。
func orderQueryResult(res *OrderPaymentQuery, err error) string {
	switch {
	case err != nil || res == nil:
		return "failed"
	case res.Reconciled:
		return "reconciled"
	case res.AlreadyRecorded:
		return "already_recorded"
	default:
		return res.ChannelStatus
	}
}

// AdminQueryOrderPayment 是后台的查单入口：照常查单，并写一条带操作人、订单、
// 渠道与结果的审计。资金相关的人工操作必须能直接查到是谁点的，所以查单失败
// （渠道停用、查询失败、金额不符、从没发起过支付）也记，结果是 failed 并带错误码；
// 只有订单不存在或无权访问时没有可归属的对象，不记。
//
// 查到已付、走了补记时，审计与补记的结算写在同一个事务里（审计台账 2.3 第 4 条）：
// 不会出现「钱记上了、却查不到是谁点的」。没走补记（未付、查不到、查单失败、补记被
// 拒绝回滚）时没有业务写入，审计单独一个事务写；写不进去回内部错误而不是假装成功。
func (s *PaymentService) AdminQueryOrderPayment(ctx context.Context, tenantID, orderID, actorID string) (*OrderPaymentQuery, error) {
	audited := false
	res, target, err := s.queryOrderPayment(ctx, tenantID, orderID, "",
		func(ctx context.Context, tx pgx.Tx, res *OrderPaymentQuery) error {
			if err := audit.Write(ctx, tx, tenantID, orderQueryAuditEntry(ctx, orderID, actorID, nil, res, nil)); err != nil {
				return err
			}
			audited = true
			return nil
		})
	if target == nil {
		return res, err
	}
	if audited && err == nil {
		return res, nil
	}
	entry := orderQueryAuditEntry(ctx, orderID, actorID, target, res, err)
	if werr := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, tenantID, entry)
	}); werr != nil {
		return nil, httpx.Internal(fmt.Errorf("order payment query audit: %w", werr))
	}
	return res, err
}

// orderQueryAuditEntry 是一次后台查单的审计。
func orderQueryAuditEntry(ctx context.Context, orderID, actorID string, target *queryTarget,
	res *OrderPaymentQuery, err error) audit.Entry {
	digest := map[string]any{"result": orderQueryResult(res, err)}
	switch {
	case target != nil:
		digest["order_no"] = target.OrderNo
	case res != nil:
		digest["order_no"] = res.OrderNo
	}
	entry := audit.Entry{
		ActorKind: "admin", ActorID: &actorID,
		Action: OrderQueryAuditAction, ResourceType: "order", ResourceID: &orderID,
		APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx), Outcome: "success",
	}
	if res != nil {
		digest["provider_code"] = res.ProviderCode
		digest["channel_status"] = res.ChannelStatus
		digest["order_status"] = res.OrderStatus
		if res.QuarantineKind != "" {
			digest["quarantine_kind"] = res.QuarantineKind
		}
	} else {
		if target != nil {
			digest["providers"] = target.Providers
		}
		entry.Outcome = "failure"
		var he *httpx.Error
		if errors.As(err, &he) {
			entry.ErrorCode = string(he.Code)
			digest["error"] = he.Message
		} else {
			entry.ErrorCode = string(httpx.CodeInternal)
		}
	}
	entry.AfterDigest = digest
	return entry
}
