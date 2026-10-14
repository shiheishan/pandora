package repo

import (
	"context"

	"github.com/google/uuid"
)

type groupCountRow struct {
	UserGroupID uuid.UUID `gorm:"column:user_group_id"`
	N           int64     `gorm:"column:n"`
}

// CouponCountsByGroup 返回各用户组被优惠券限定的个数（不分券状态）；没被限定的组不在结果里。
func (r *Repo) CouponCountsByGroup(ctx context.Context) (map[uuid.UUID]int64, error) {
	var rows []groupCountRow
	err := r.d.Conn(ctx).Raw(
		`SELECT user_group_id, count(*) AS n FROM coupons WHERE user_group_id IS NOT NULL GROUP BY user_group_id`,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}

	out := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		out[row.UserGroupID] = row.N
	}
	return out, nil
}
