package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/google/uuid"
)

// InsertDeliveries 批量写投递（ID 由调用方生成）。
func (r *Repo) InsertDeliveries(ctx context.Context, ds []model.Delivery) error {
	if len(ds) == 0 {
		return nil
	}
	return r.d.Conn(ctx).CreateInBatches(&ds, 500).Error
}

// MarketingEmailOptOuts 在 userIDs 里查出关闭了营销邮件偏好的用户（一条 SQL）。
func (r *Repo) MarketingEmailOptOuts(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(
		`SELECT user_id FROM notification_preferences
		  WHERE category = 'marketing' AND channel = 'email' AND NOT enabled
		    AND user_id = ANY(CAST(? AS uuid[]))`,
		uuidArray(userIDs),
	).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
