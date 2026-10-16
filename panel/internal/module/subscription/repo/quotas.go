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

// DeleteQuotas 删掉这些配额行（换套餐后新版本没有定义的行）。
func (r *Repo) DeleteQuotas(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return mapErr(r.d.Conn(ctx).Exec(`DELETE FROM quota_balances WHERE id IN ?`, ids).Error)
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

// AddQuotaBonus 把 bytes 饱和加到一条配额行的 bonus_value 上，返回加完后的值；行不存在回错误。只在事务里、行已锁住时调用。
func (r *Repo) AddQuotaBonus(ctx context.Context, id uuid.UUID, bytes int64) (int64, error) {
	var bonus int64
	res := r.d.Conn(ctx).Raw(`
		UPDATE quota_balances
		   SET bonus_value = CASE WHEN bonus_value > 9223372036854775807 - ? THEN 9223372036854775807 ELSE bonus_value + ? END,
		       updated_at = ?
		 WHERE id = ?
		RETURNING bonus_value`, bytes, bytes, now(), id).Scan(&bonus)
	if res.Error != nil {
		return 0, mapErr(res.Error)
	}
	if res.RowsAffected != 1 {
		return 0, fmt.Errorf("subscription: 配额行 %s 不存在", id)
	}
	return bonus, nil
}
