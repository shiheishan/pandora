package service

import (
	"context"
	"time"
)

// ReportUserCounts 是看板的用户计数。
type ReportUserCounts struct {
	Total         int64 // 全部用户
	Active        int64 // status = active
	CreatedSince  int64 // created_at >= since（站点时区今天零点）
	CreatedRecent int64 // created_at >= recentSince（现在 − 7 天）
}

// ReportUserCounts 一条 SQL 数出看板的用户计数。
func (s *Service) ReportUserCounts(ctx context.Context, since, recentSince time.Time) (ReportUserCounts, error) {
	row, err := s.repo.ReportUserCounts(ctx, since, recentSince)
	if err != nil {
		return ReportUserCounts{}, err
	}
	return ReportUserCounts{
		Total:         row.Total,
		Active:        row.Active,
		CreatedSince:  row.CreatedSince,
		CreatedRecent: row.CreatedRecent,
	}, nil
}
