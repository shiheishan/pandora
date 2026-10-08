package billing

// 门户「另买一份」与防重复下单（设计稿 2.4，原型 S3 / 第 4 轮反馈 3）。

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// newCopyLabel 规范化新购的备注名并核对：
//   - 名字已经用在这个人的另一份上：422 fields.label（数据库的唯一索引按同一口径，不区分大小写）；
//   - 另买一份同款、又没起名，而已有那份同款也没起名：App 里会出现两个同名的配置，422 要求起名。
func newCopyLabel(ctx context.Context, tx pgx.Tx, tenantID string, in CreateOrderInput,
	planName string) (string, error) {
	label, err := purchase.NormalizeLabel(in.Label)
	if err != nil {
		return "", httpx.Invalid(map[string]string{"label": err.Error()})
	}
	if label != "" {
		var taken bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM subscriptions
			                WHERE tenant_id = $1 AND user_id = $2::uuid
			                  AND label IS NOT NULL AND lower(label) = lower($3))`,
			tenantID, in.UserID, label).Scan(&taken); err != nil {
			return "", err
		}
		if taken {
			return "", httpx.Invalid(map[string]string{"label": "这个名字已经用在另一份上了，换一个吧"})
		}
		return label, nil
	}
	if !in.NewCopy {
		return "", nil
	}
	var clash bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM subscriptions s
		                WHERE s.tenant_id = $1 AND s.user_id = $2::uuid AND s.plan_id = $3::uuid
		                  AND s.label IS NULL AND `+liveOrRevivableSQL+`)`,
		tenantID, in.UserID, in.PlanID).Scan(&clash); err != nil {
		return "", err
	}
	if clash {
		return "", httpx.Invalid(map[string]string{
			"label": "不起名的话，App 里会有两个「" + planName + "」，分不清哪个是哪个，先给它起个名字"})
	}
	return "", nil
}

// ensureNoPendingNewOrder 拒绝同一套餐的第二张未付款新购单（409 order_pending，Fields 带
// 那张单的 order_id，前端显示「继续付款或取消」的入口；那张已超过付款期限时另带 lapsed=true，
// 前端只给「取消它」）。
//
// 已过付款期限、还没被释放任务关掉的那张也算：它的支付回调可能晚到，而结算按订单状态走——
// 仍是待支付就照常履约。若这时允许同款再下一张，两张都付了就是两份。等它被关掉（expired /
// cancelled）之后，晚到的钱进挂账（released_order），不会再开一份；用户也可以直接取消它。
// 查询走 idx_orders_user 加 idx_order_items_order，不用新索引。
func ensureNoPendingNewOrder(ctx context.Context, tx pgx.Tx, tenantID, userID, planID,
	planName string) error {
	var orderID string
	var lapsed bool
	err := tx.QueryRow(ctx, `
		SELECT o.id::text, coalesce(o.expires_at <= now(), false) FROM orders o
		 WHERE o.tenant_id = $1 AND o.user_id = $2::uuid AND o.kind = 'new'
		   AND o.status IN ('draft', 'pending_payment', 'processing')
		   AND EXISTS (SELECT 1 FROM order_items oi
		                WHERE oi.tenant_id = o.tenant_id AND oi.order_id = o.id
		                  AND oi.plan_id = $3::uuid)
		 ORDER BY o.created_at DESC
		 LIMIT 1`, tenantID, userID, planID).Scan(&orderID, &lapsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	msg := "你有一张还没付款的「" + planName + "」订单，继续付款或取消后再买"
	if lapsed {
		msg = "你有一张已超过付款期限的「" + planName + "」订单，取消后再买"
	}
	e := httpx.New(httpx.CodeOrderPending, msg)
	e.Fields = map[string]string{"order_id": orderID}
	if lapsed {
		e.Fields["lapsed"] = "true"
	}
	return e
}

// waiveIntoDiscount 把 SmallDue 免掉的那点钱并进订单折扣（用户 8.1 第 1 题推荐 A）。用了优惠券的单
// 并进券的折扣：数据库要求券核销的折扣与订单折扣相等（00036 的预留不变量），免单没有自己的科目。
func waiveIntoDiscount(coupon *couponMatch, discount, total, waived int64) (int64, int64) {
	if coupon != nil {
		coupon.Discount += waived
	}
	return discount + waived, total - waived
}
