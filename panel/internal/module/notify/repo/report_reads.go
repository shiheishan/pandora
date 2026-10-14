package repo

import (
	"context"
	"time"
)

// DeliveryBacklogRow 是投递队列积压的一条 SQL 聚合结果。
type DeliveryBacklogRow struct {
	Ready          int64      `gorm:"column:ready"`
	ReadyRetry     int64      `gorm:"column:ready_retry"`
	Scheduled      int64      `gorm:"column:scheduled"`
	ScheduledRetry int64      `gorm:"column:scheduled_retry"`
	Failed         int64      `gorm:"column:failed"`
	Suppressed     int64      `gorm:"column:suppressed"`
	OldestReadyAt  *time.Time `gorm:"column:oldest_ready_at"`
	LastSentAt     *time.Time `gorm:"column:last_sent_at"`
}

// ChannelDeliveryCountsRow 是按渠道的排队、重试与失败计数。
type ChannelDeliveryCountsRow struct {
	Channel  string `gorm:"column:channel"`
	Queued   int64  `gorm:"column:queued"`
	Retrying int64  `gorm:"column:retrying"`
	Failed   int64  `gorm:"column:failed"`
}

// DeliveryBacklog 一条 SQL 统计 notification_deliveries 积压与终态累计；now 为「到期」判定点。
func (r *Repo) DeliveryBacklog(ctx context.Context, now time.Time) (DeliveryBacklogRow, error) {
	var out DeliveryBacklogRow
	err := r.d.Conn(ctx).Raw(`
		WITH c AS (SELECT ?::timestamptz AS now_at)
		SELECT count(*) FILTER (WHERE d.status = 'queued' AND d.next_retry_at <= c.now_at) AS ready,
		       count(*) FILTER (WHERE d.status = 'queued' AND d.next_retry_at <= c.now_at AND d.attempts > 0) AS ready_retry,
		       count(*) FILTER (WHERE d.status = 'queued' AND d.next_retry_at > c.now_at) AS scheduled,
		       count(*) FILTER (WHERE d.status = 'queued' AND d.next_retry_at > c.now_at AND d.attempts > 0) AS scheduled_retry,
		       count(*) FILTER (WHERE d.status = 'failed') AS failed,
		       count(*) FILTER (WHERE d.status = 'suppressed') AS suppressed,
		       min(d.next_retry_at) FILTER (WHERE d.status = 'queued' AND d.next_retry_at <= c.now_at) AS oldest_ready_at,
		       max(d.sent_at) FILTER (WHERE d.status = 'sent') AS last_sent_at
		  FROM c LEFT JOIN notification_deliveries d ON true
		 GROUP BY c.now_at`, now).Scan(&out).Error
	if err != nil {
		return DeliveryBacklogRow{}, err
	}
	return out, nil
}

// DeliveryCountsByChannel 一条 SQL 按 channel 分组计数；无投递的渠道不在结果里。
func (r *Repo) DeliveryCountsByChannel(ctx context.Context) ([]ChannelDeliveryCountsRow, error) {
	var out []ChannelDeliveryCountsRow
	err := r.d.Conn(ctx).Raw(`
		SELECT channel,
		       count(*) FILTER (WHERE status = 'queued') AS queued,
		       count(*) FILTER (WHERE status = 'queued' AND attempts > 0) AS retrying,
		       count(*) FILTER (WHERE status = 'failed') AS failed
		  FROM notification_deliveries
		 GROUP BY channel`).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}
