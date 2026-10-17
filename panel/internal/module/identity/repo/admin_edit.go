package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SetUserEmail 改邮箱并清掉邮箱验证时间；撞唯一约束回 ErrDuplicate（经 mapErr）。
func (r *Repo) SetUserEmail(ctx context.Context, id uuid.UUID, email string) error {
	return mapErr(r.d.Conn(ctx).Exec(
		"UPDATE users SET email = ?, email_verified_at = NULL, updated_at = now() WHERE id = ?",
		email, id,
	).Error)
}

// SetUserNote 改显示名与后台备注；nil 表示存 NULL。
func (r *Repo) SetUserNote(ctx context.Context, id uuid.UUID, displayName, adminNote *string) error {
	return mapErr(r.d.Conn(ctx).Exec(
		"UPDATE users SET display_name = ?, admin_note = ?, updated_at = now() WHERE id = ?",
		displayName, adminNote, id,
	).Error)
}

// AnonymizeUser 把用户行改成已注销：email 换成占位值，display_name、admin_note、email_verified_at 置空，status='deleted'、deleted_at=at。
func (r *Repo) AnonymizeUser(ctx context.Context, id uuid.UUID, placeholderEmail string, at time.Time) error {
	return mapErr(r.d.Conn(ctx).Exec(
		`UPDATE users SET email = ?, display_name = NULL, admin_note = NULL, email_verified_at = NULL,
		 status = 'deleted', deleted_at = ?, updated_at = now() WHERE id = ?`,
		placeholderEmail, at, id,
	).Error)
}

// DeletePassword 删掉用户的口令行（没有也不算错）。
func (r *Repo) DeletePassword(ctx context.Context, userID uuid.UUID) error {
	return mapErr(r.d.Conn(ctx).Exec(
		"DELETE FROM user_passwords WHERE user_id = ?", userID,
	).Error)
}

// DisableOwnerInvites 把某人名下仍 active 的个人邀请码一律置 disabled，返回改了几个。
func (r *Repo) DisableOwnerInvites(ctx context.Context, ownerID uuid.UUID) (int64, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE invite_codes SET status = 'disabled' WHERE owner_user_id = ? AND status = 'active'",
		ownerID,
	)
	if res.Error != nil {
		return 0, mapErr(res.Error)
	}
	return res.RowsAffected, nil
}
