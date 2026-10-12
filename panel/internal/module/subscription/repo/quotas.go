package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// QuotasBySubscriptions 读一组订阅的配额行，按 id 排序；lock 为真时按 id 顺序加 FOR UPDATE（与节点扣量同序防死锁）。
func (r *Repo) QuotasBySubscriptions(ctx context.Context, subIDs []uuid.UUID, lock bool) ([]model.QuotaBalance, error) {
	if len(subIDs) == 0 {
		return []model.QuotaBalance{}, nil
	}
	q := "SELECT * FROM quota_balances WHERE subscription_id IN ? ORDER BY id"
	if lock {
		q += " FOR UPDATE"
	}
	var out []model.QuotaBalance
	err := r.d.Conn(ctx).Raw(q, subIDs).Scan(&out).Error
	return out, mapErr(err)
}

// InsertQuotas 批量插入配额行。
func (r *Repo) InsertQuotas(ctx context.Context, rows []model.QuotaBalance) error {
	if len(rows) == 0 {
		return nil
	}
	return mapErr(r.d.Conn(ctx).Create(&rows).Error)
}

// UpdateQuota 更新一条配额行的若干列；行不存在返回错误。
func (r *Repo) UpdateQuota(ctx context.Context, id uuid.UUID, cols map[string]any) error {
	cols["updated_at"] = now()
	res := r.d.Conn(ctx).Model(&model.QuotaBalance{}).Where("id = ?", id).Updates(cols)
	if res.Error != nil {
		return mapErr(res.Error)
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("subscription: 配额行 %s 不存在", id)
	}
	return nil
}
