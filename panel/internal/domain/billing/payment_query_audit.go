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

// AdminQueryOrderPayment 是后台的查单入口：照常查单，然后写一条带操作人、订单、
// 渠道与结果的审计。资金相关的人工操作必须能直接查到是谁点的，所以查单失败
// （渠道停用、查询失败、金额不符、从没发起过支付）也记，结果是 failed 并带错误码；
// 只有订单不存在或无权访问时没有可归属的对象，不记。
//
// 审计在查单之后另开一个事务写：补记的结算事务已经提交，审计写不进去时
// 回内部错误而不是假装成功——钱已经按回调同一条主链记上，同一订单再查一次
// 会得到 already_recorded，并补上这条审计。
func (s *PaymentService) AdminQueryOrderPayment(ctx context.Context, tenantID, orderID, actorID string) (*OrderPaymentQuery, error) {
	res, target, err := s.queryOrderPayment(ctx, tenantID, orderID, "")
	if target == nil {
		return res, err
	}

	digest := map[string]any{
		"order_no": target.OrderNo,
		"result":   orderQueryResult(res, err),
	}
	entry := audit.Entry{
		ActorKind: "admin", ActorID: &actorID,
		Action: OrderQueryAuditAction, ResourceType: "order", ResourceID: &orderID,
		APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		Outcome: "success",
	}
	if res != nil {
		digest["provider_code"] = res.ProviderCode
		digest["channel_status"] = res.ChannelStatus
		digest["order_status"] = res.OrderStatus
		if res.QuarantineKind != "" {
			digest["quarantine_kind"] = res.QuarantineKind
		}
	} else {
		digest["providers"] = target.Providers
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

	if werr := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, tenantID, entry)
	}); werr != nil {
		return nil, httpx.Internal(fmt.Errorf("order payment query audit: %w", werr))
	}
	return res, err
}
