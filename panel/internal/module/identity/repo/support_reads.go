package repo

import (
	"context"

	"github.com/google/uuid"
)

// ContactRow 是用户的联系信息（邮箱与显示名）。
type ContactRow struct {
	ID          uuid.UUID `gorm:"column:id"`
	Email       string    `gorm:"column:email"`
	DisplayName *string   `gorm:"column:display_name"`
}

// Contacts 批量查用户邮箱与显示名（一条 SQL）；不存在的 id 不在结果里。
func (r *Repo) Contacts(ctx context.Context, ids []uuid.UUID) ([]ContactRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []ContactRow
	err := r.d.Conn(ctx).Raw(
		"SELECT id, email::text AS email, display_name FROM users WHERE id IN ?", ids,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
