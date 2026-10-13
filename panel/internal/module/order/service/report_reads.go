package service

import (
	"context"
	"time"
)

// ReportOrderCounts 是看板的订单计数。
type ReportOrderCounts struct {
	PaidSince    int64 // status 为 paid 或 fulfilled、paid_at >= since
	Pending      int64 // status = pending_payment（全部）
	ClosedSince  int64 // since 之后取消或超时关闭的（cancelled_at / expired_at >= since；两列各自只在对应状态下非空）
	PendingStale int64 // status = pending_payment、created_at < staleBefore（下单很久还挂着，说明过期任务没跑或卡住）
}

// ReportOrderCounts 一条 SQL 数出看板要的四个订单计数。since 是站点时区今天零点，staleBefore 是「现在 − 30 分钟」，由调用方算。
func (s *Service) ReportOrderCounts(ctx context.Context, since, staleBefore time.Time) (ReportOrderCounts, error) {
	row, err := s.repo.ReportOrderCounts(ctx, since, staleBefore)
	if err != nil {
		return ReportOrderCounts{}, err
	}
	return ReportOrderCounts{
		PaidSince:    row.PaidSince,
		Pending:      row.Pending,
		ClosedSince:  row.ClosedSince,
		PendingStale: row.PendingStale,
	}, nil
}
