package repo

import (
	"context"
	"time"
)

// ReportOrderCountsRow 是看板订单计数的 SQL 扫描结果。
type ReportOrderCountsRow struct {
	PaidSince    int64 `gorm:"column:paid_since"`
	Pending      int64 `gorm:"column:pending"`
	ClosedSince  int64 `gorm:"column:closed_since"`
	PendingStale int64 `gorm:"column:pending_stale"`
}

// ReportOrderCounts 一条 SQL 数出看板要的四个订单计数。
func (r *Repo) ReportOrderCounts(ctx context.Context, since, staleBefore time.Time) (ReportOrderCountsRow, error) {
	var out ReportOrderCountsRow
	err := r.d.Conn(ctx).Raw(`
		SELECT count(*) FILTER (WHERE status IN ('paid', 'fulfilled') AND paid_at >= ?) AS paid_since,
		       count(*) FILTER (WHERE status = 'pending_payment') AS pending,
		       count(*) FILTER (WHERE coalesce(cancelled_at, expired_at) >= ?) AS closed_since,
		       count(*) FILTER (WHERE status = 'pending_payment' AND created_at < ?) AS pending_stale
		  FROM orders`, since, since, staleBefore).Scan(&out).Error
	if err != nil {
		return ReportOrderCountsRow{}, mapErr(err)
	}
	return out, nil
}
