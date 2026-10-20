package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LockUserPurchases 拿本人的建单咨询锁（事务级，提交或回滚时释放）：开了购买限制时门户建单先拿它，
// 同一用户并发建单时排队，计数与插单之间不会被另一张插进来。
func (r *Repo) LockUserPurchases(ctx context.Context, userID uuid.UUID) error {
	return mapErr(r.d.Conn(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
		"order:purchases:"+userID.String(),
	).Error)
}

// PendingCounts 是一个用户还没付款、也没过付款期限的订单数：All 是全部种类，New 是其中的新购单。走 orders_user_idx。
type PendingCounts struct {
	All int
	New int
}

type pendingCountsRow struct {
	AllPending int `gorm:"column:all_pending"`
	NewPending int `gorm:"column:new_pending"`
}

// PendingCounts 数 userID 在 now 时刻还没付款、也没过付款期限的订单（status='pending_payment' 且 expires_at > now）。
func (r *Repo) PendingCounts(ctx context.Context, userID uuid.UUID, now time.Time) (PendingCounts, error) {
	var row pendingCountsRow
	err := r.d.Conn(ctx).Raw(`
SELECT count(*) AS all_pending, count(*) FILTER (WHERE kind = 'new') AS new_pending
  FROM orders
 WHERE user_id = ? AND status = 'pending_payment' AND expires_at > ?`,
		userID, now,
	).Scan(&row).Error
	if err != nil {
		return PendingCounts{}, mapErr(err)
	}
	return PendingCounts{All: row.AllPending, New: row.NewPending}, nil
}
