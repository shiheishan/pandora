package repo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/identity/model"
)

// GroupRow 是用户组加组内用户数。
type GroupRow struct {
	model.UserGroup
	Users int64 `gorm:"column:users"`
}

// ListGroups 列出全部用户组及组内人数。
func (r *Repo) ListGroups(ctx context.Context) ([]GroupRow, error) {
	var rows []GroupRow
	err := r.d.Conn(ctx).Raw(
		`SELECT g.*, (SELECT count(*) FROM users u WHERE u.user_group_id = g.id) AS users FROM user_groups g ORDER BY g.created_at, g.id`,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []GroupRow{}, nil
	}
	return rows, nil
}

// InsertGroup 插入用户组。
func (r *Repo) InsertGroup(ctx context.Context, g *model.UserGroup) error {
	return mapErr(r.d.Conn(ctx).Create(g).Error)
}

// UpdateGroup 更新组名与描述。
func (r *Repo) UpdateGroup(ctx context.Context, id uuid.UUID, name string, description *string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE user_groups SET name = ?, description = ?, updated_at = now() WHERE id = ?",
		name, description, id,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// GroupStatus 读用户组的状态并加共享锁（FOR SHARE，持有到事务结束，与停用、删组互斥）；不存在 ok 为 false。
func (r *Repo) GroupStatus(ctx context.Context, id uuid.UUID) (status string, ok bool, err error) {
	err = r.d.Conn(ctx).Raw(
		"SELECT status FROM user_groups WHERE id = ? FOR SHARE", id,
	).Scan(&status).Error
	if err != nil {
		return "", false, err
	}
	if status == "" {
		return "", false, nil
	}
	return status, true, nil
}

// SetGroupStatus 改用户组状态；不存在返回 false。
func (r *Repo) SetGroupStatus(ctx context.Context, id uuid.UUID, status string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE user_groups SET status = ?, updated_at = now() WHERE id = ?",
		status, id,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// LockGroup 锁定用户组行是否存在。
func (r *Repo) LockGroup(ctx context.Context, id uuid.UUID) (bool, error) {
	var one int
	err := r.d.Conn(ctx).Raw(
		"SELECT 1 FROM user_groups WHERE id = ? FOR UPDATE", id,
	).Scan(&one).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return one == 1, nil
}

// GroupUserCount 统计组内用户数。
func (r *Repo) GroupUserCount(ctx context.Context, id uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		"SELECT count(*) FROM users WHERE user_group_id = ?", id,
	).Scan(&n).Error
	return n, err
}

// DeleteGroup 删除用户组。
func (r *Repo) DeleteGroup(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec("DELETE FROM user_groups WHERE id = ?", id)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
