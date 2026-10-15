package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PaidNoticeRow 是一张已履约订单的通知素材。
type PaidNoticeRow struct {
	ID      uuid.UUID `gorm:"column:id"`
	UserID  uuid.UUID `gorm:"column:user_id"`
	OrderNo string    `gorm:"column:order_no"`
	Kind    string    `gorm:"column:kind"`
	// TrafficBytes 是流量包订单行的流量（其余种类为 nil）
	TrafficBytes   *int64    `gorm:"column:traffic_bytes"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	FulfilledAt    time.Time `gorm:"column:fulfilled_at"`
	// TotalAmount、Currency 是订单总额与币种（管理员收款提醒用）
	TotalAmount decimal.Decimal `gorm:"column:total_amount"`
	Currency    string          `gorm:"column:currency"`
}

// PaidForNotify 取 (fulfilled_at, id) 在 (afterAt, afterID) 之后、挂在订阅上的已履约订单（充值单没有订阅，不在内），
// 按 (fulfilled_at, id) 升序，至多 limit 张。走 orders_fulfilled_idx。
func (r *Repo) PaidForNotify(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int) ([]PaidNoticeRow, error) {
	var out []PaidNoticeRow
	err := r.d.Conn(ctx).Raw(`
		SELECT o.id, o.user_id, o.order_no, o.kind, o.subscription_id, o.fulfilled_at, o.total_amount, o.currency,
		       (SELECT i.snapshot_traffic_bytes FROM order_items i WHERE i.order_id = o.id LIMIT 1) AS traffic_bytes
		  FROM orders o
		 WHERE o.status = 'fulfilled' AND o.subscription_id IS NOT NULL AND (o.fulfilled_at, o.id) > (?, ?)
		 ORDER BY o.fulfilled_at, o.id
		 LIMIT ?`, afterAt, afterID, limit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
