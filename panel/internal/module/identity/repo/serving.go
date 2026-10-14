package repo

import (
	"context"

	"github.com/google/uuid"
)

// servingChunk 是一条 IN 查询最多带的 id 数（PG 一条语句最多 65535 个参数，留足余量）。
const servingChunk = 10000

// ActiveUserGroupRow 是一个 active 账号与它所在的用户组（nil = 默认组）。
type ActiveUserGroupRow struct {
	ID          uuid.UUID  `gorm:"column:id"`
	UserGroupID *uuid.UUID `gorm:"column:user_group_id"`
}

// ActiveUserGroups 批量取这些用户里 status = 'active' 的账号与用户组；按主键查，每 servingChunk 个 id 一条 SQL。
func (r *Repo) ActiveUserGroups(ctx context.Context, ids []uuid.UUID) ([]ActiveUserGroupRow, error) {
	out := []ActiveUserGroupRow{}
	for start := 0; start < len(ids); start += servingChunk {
		end := min(start+servingChunk, len(ids))
		var rows []ActiveUserGroupRow
		err := r.d.Conn(ctx).Raw(
			"SELECT id, user_group_id FROM users WHERE id IN ? AND status = 'active'", ids[start:end],
		).Scan(&rows).Error
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, rows...)
	}
	return out, nil
}
