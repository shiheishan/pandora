package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// InsertResetLogs 批量追加流量重置日志。
func (r *Repo) InsertResetLogs(ctx context.Context, rows []model.ResetLog) error {
	if len(rows) == 0 {
		return nil
	}
	return mapErr(r.d.Conn(ctx).Create(&rows).Error)
}

// ResetSince 是一份订阅从哪个时刻起数付费重置次数。
type ResetSince struct {
	SubscriptionID uuid.UUID
	Since          time.Time
}

// PaidResetCounts 批量数每份订阅 Since 之后的付费重置次数（reason = 'order'，同一张单清多行只算一次：count(DISTINCT note)）；
// 没有记录的不在结果里。一条 SQL：VALUES 列表 JOIN traffic_reset_logs，按订阅 id 等值连接走 traffic_reset_logs_sub_idx。
func (r *Repo) PaidResetCounts(ctx context.Context, in []ResetSince) (map[uuid.UUID]int, error) {
	out := make(map[uuid.UUID]int, len(in))
	if len(in) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(in))
	args := make([]any, 0, len(in)*2)
	for i, s := range in {
		placeholders[i] = "(?::uuid, ?::timestamptz)"
		args = append(args, s.SubscriptionID, s.Since)
	}
	q := `SELECT t.subscription_id, count(DISTINCT t.note)::int AS cnt
FROM (VALUES ` + strings.Join(placeholders, ", ") + `) AS q(subscription_id, since)
JOIN traffic_reset_logs t
  ON t.subscription_id = q.subscription_id AND t.reason = 'order' AND t.created_at >= q.since
GROUP BY t.subscription_id`
	var rows []struct {
		SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
		Count          int       `gorm:"column:cnt"`
	}
	if err := r.d.Conn(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.SubscriptionID] = row.Count
	}
	return out, nil
}
