package service

import (
	"context"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/repo"
)

// reportStore 是看板聚合读要的 repo 方法；*repo.Repo 实现它。
type reportStore interface {
	DeliveryBacklog(ctx context.Context, now time.Time) (repo.DeliveryBacklogRow, error)
	DeliveryCountsByChannel(ctx context.Context) ([]repo.ChannelDeliveryCountsRow, error)
}

var _ reportStore = (*repo.Repo)(nil)

// DeliveryBacklog 是投递队列的积压读数（全部渠道）。
type DeliveryBacklog struct {
	Ready          int64      // queued 且 next_retry_at <= now（到期该发）
	ReadyRetry     int64      // 其中 attempts > 0
	Scheduled      int64      // queued 且 next_retry_at > now（还没到期，含认领中）
	ScheduledRetry int64      // 其中 attempts > 0
	Failed         int64      // failed 累计
	Suppressed     int64      // suppressed 累计
	OldestReadyAt  *time.Time // 到期未发里最早的 next_retry_at；没有时 nil
	LastSentAt     *time.Time // 最近一次发出的 sent_at；没有时 nil
}

// DeliveryBacklog 一条 SQL 读投递积压；看板的通知积压卡片与「需要处理」用。
func (s *Service) DeliveryBacklog(ctx context.Context, now time.Time) (DeliveryBacklog, error) {
	rs, ok := s.store.(reportStore)
	if !ok {
		return DeliveryBacklog{}, errors.New("notify: 存储不支持看板读")
	}
	row, err := rs.DeliveryBacklog(ctx, now)
	if err != nil {
		return DeliveryBacklog{}, err
	}
	return DeliveryBacklog{
		Ready:          row.Ready,
		ReadyRetry:     row.ReadyRetry,
		Scheduled:      row.Scheduled,
		ScheduledRetry: row.ScheduledRetry,
		Failed:         row.Failed,
		Suppressed:     row.Suppressed,
		OldestReadyAt:  row.OldestReadyAt,
		LastSentAt:     row.LastSentAt,
	}, nil
}

// ChannelDeliveryCounts 是某个渠道的排队、正在重试与累计失败数。
type ChannelDeliveryCounts struct {
	Queued   int64 // status = queued
	Retrying int64 // queued 且 attempts > 0
	Failed   int64 // status = failed
}

// DeliveryCountsByChannel 按渠道（email、telegram）计数；没有任何投递的渠道不在结果里。系统状态页用。
func (s *Service) DeliveryCountsByChannel(ctx context.Context) (map[string]ChannelDeliveryCounts, error) {
	rs, ok := s.store.(reportStore)
	if !ok {
		return nil, errors.New("notify: 存储不支持看板读")
	}
	rows, err := rs.DeliveryCountsByChannel(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ChannelDeliveryCounts, len(rows))
	for _, row := range rows {
		out[row.Channel] = ChannelDeliveryCounts{
			Queued:   row.Queued,
			Retrying: row.Retrying,
			Failed:   row.Failed,
		}
	}
	return out, nil
}
