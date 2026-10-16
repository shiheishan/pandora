package repo

import (
	"context"

	"github.com/google/uuid"
)

// GroupRefCountRow 是一个用户组被套餐可见名单引用的个数。
type GroupRefCountRow struct {
	UserGroupID uuid.UUID `gorm:"column:user_group_id"`
	Plans       int64     `gorm:"column:plans"`
}

// GroupRefCounts 统计各用户组被 plan_visible_groups 引用的次数（含已归档套餐）。
func (r *Repo) GroupRefCounts(ctx context.Context) ([]GroupRefCountRow, error) {
	var rows []GroupRefCountRow
	if err := r.d.Conn(ctx).Raw(
		"SELECT user_group_id, count(*)::bigint AS plans FROM plan_visible_groups GROUP BY user_group_id",
	).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []GroupRefCountRow{}, nil
	}
	return rows, nil
}
