package repo

import (
	"context"

	"github.com/google/uuid"
)

// GroupRefCountRow 是一个用户组被套餐可见名单、组专属价格引用的个数。
type GroupRefCountRow struct {
	UserGroupID uuid.UUID `gorm:"column:user_group_id"`
	Plans       int64     `gorm:"column:plans"`
	Prices      int64     `gorm:"column:prices"`
}

const groupRefCountsSQL = `
SELECT user_group_id, sum(plans)::bigint AS plans, sum(prices)::bigint AS prices FROM (
  SELECT user_group_id, count(*) AS plans, 0 AS prices FROM plan_visible_groups GROUP BY user_group_id
  UNION ALL
  SELECT user_group_id, 0, count(*) FROM prices WHERE user_group_id IS NOT NULL GROUP BY user_group_id
) t GROUP BY user_group_id`

// GroupRefCounts 统计各用户组被 plan_visible_groups 与 prices.user_group_id 引用的次数。
func (r *Repo) GroupRefCounts(ctx context.Context) ([]GroupRefCountRow, error) {
	var rows []GroupRefCountRow
	if err := r.d.Conn(ctx).Raw(groupRefCountsSQL).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []GroupRefCountRow{}, nil
	}
	return rows, nil
}
