package repo

import (
	"context"

	"github.com/google/uuid"
)

// GroupPoolRow 是一个用户组与把它列入仅限名单的一个池。
type GroupPoolRow struct {
	UserGroupID uuid.UUID `gorm:"column:user_group_id"`
	PoolID      uuid.UUID `gorm:"column:pool_id"`
	Name        string    `gorm:"column:name"`
}

// PoolsByUserGroup 查询各用户组被哪些节点池列入仅限名单。
func (r *Repo) PoolsByUserGroup(ctx context.Context) ([]GroupPoolRow, error) {
	var rows []GroupPoolRow
	err := r.d.Conn(ctx).Raw(
		`SELECT g.user_group_id, p.id AS pool_id, p.name FROM node_pool_user_groups g JOIN node_pools p ON p.id = g.pool_id ORDER BY g.user_group_id, p.name, p.id`,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
