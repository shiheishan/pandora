package service

import (
	"context"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/module/support/repo"
)

// reportStore 是看板聚合读要的 repo 方法；*repo.Repo 实现它。
type reportStore interface {
	OpenTicketStats(ctx context.Context, now time.Time) (repo.OpenTicketStatsRow, error)
}

var _ reportStore = (*repo.Repo)(nil)

// OpenTicketStats 是待客服处理的工单（open、pending_agent、escalated）统计。
type OpenTicketStats struct {
	Count             int64
	HighPriority      int64  // 优先级 high 或 urgent
	OldestWaitSeconds *int64 // 最久的那张已等待的秒数（从用户最后一次发言算，没发言过从建单算）；没有工单时为 nil
}

// OpenTicketStats 数待客服处理的工单；看板「需要处理」用。now 由调用方给。
func (s *Service) OpenTicketStats(ctx context.Context, now time.Time) (OpenTicketStats, error) {
	rs, ok := s.repo.(reportStore)
	if !ok {
		return OpenTicketStats{}, errors.New("support: 存储不支持看板读")
	}
	row, err := rs.OpenTicketStats(ctx, now)
	if err != nil {
		return OpenTicketStats{}, err
	}
	return OpenTicketStats{
		Count:             row.Count,
		HighPriority:      row.HighPriority,
		OldestWaitSeconds: row.OldestWaitSeconds,
	}, nil
}
