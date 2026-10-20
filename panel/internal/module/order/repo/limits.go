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

// PendingCounts 是一个用户待付（pending_payment）的订单数，两项口径不同。走 orders_user_idx。
type PendingCounts struct {
	All int // 全部种类、且没过付款期限的：未支付订单上限用
	New int // 新购单，不看付款期限：订阅数上限用（过期未扫的单迟到回调仍会结算出一份订阅）
}

type pendingCountsRow struct {
	AllPending int `gorm:"column:all_pending"`
	NewPending int `gorm:"column:new_pending"`
}

// PendingCounts 数 userID 的待付订单：All 只数 expires_at > now 的，New 数全部待付新购单（不看 expires_at）。
func (r *Repo) PendingCounts(ctx context.Context, userID uuid.UUID, now time.Time) (PendingCounts, error) {
	var row pendingCountsRow
	err := r.d.Conn(ctx).Raw(`
SELECT count(*) FILTER (WHERE expires_at > ?) AS all_pending, count(*) FILTER (WHERE kind = 'new') AS new_pending
  FROM orders
 WHERE user_id = ? AND status = 'pending_payment'`,
		now, userID,
	).Scan(&row).Error
	if err != nil {
		return PendingCounts{}, mapErr(err)
	}
	return PendingCounts{All: row.AllPending, New: row.NewPending}, nil
}
