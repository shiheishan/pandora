package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PaidNoticeRow 是一张已履约订单的通知素材。
type PaidNoticeRow struct {
	ID             uuid.UUID `gorm:"column:id"`
	UserID         uuid.UUID `gorm:"column:user_id"`
	OrderNo        string    `gorm:"column:order_no"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	FulfilledAt    time.Time `gorm:"column:fulfilled_at"`
}

// PaidForNotify 取 (fulfilled_at, id) 在 (afterAt, afterID) 之后、挂在订阅上的已履约订单（充值单没有订阅，不在内），
// 按 (fulfilled_at, id) 升序，至多 limit 张。走 orders_fulfilled_idx。
func (r *Repo) PaidForNotify(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int) ([]PaidNoticeRow, error) {
	var out []PaidNoticeRow
	err := r.d.Conn(ctx).Raw(`
		SELECT id, user_id, order_no, subscription_id, fulfilled_at
		  FROM orders
		 WHERE status = 'fulfilled' AND subscription_id IS NOT NULL AND (fulfilled_at, id) > (?, ?)
		 ORDER BY fulfilled_at, id
		 LIMIT ?`, afterAt, afterID, limit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
