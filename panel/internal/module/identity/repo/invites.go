package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/identity/model"
)

// inviteUsableSQL 邀请码仍可用的条件（Find/Lock/Consume 共用）。
const inviteUsableSQL = "status = 'active' AND (expires_at IS NULL OR expires_at > now()) AND (max_uses IS NULL OR used_count < max_uses)"

// FindUsableInvite 按码查找仍可用的邀请码。
func (r *Repo) FindUsableInvite(ctx context.Context, code string) (model.InviteCode, bool, error) {
	var inv model.InviteCode
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM invite_codes WHERE code = ? AND "+inviteUsableSQL, code,
	).Scan(&inv).Error
	if err != nil {
		return model.InviteCode{}, false, err
	}
	if inv.ID == uuid.Nil {
		return model.InviteCode{}, false, nil
	}
	return inv, true, nil
}

// LockUsableInvite 锁定仍可用的邀请码行。
func (r *Repo) LockUsableInvite(ctx context.Context, id uuid.UUID) (model.InviteCode, bool, error) {
	var inv model.InviteCode
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM invite_codes WHERE id = ? AND "+inviteUsableSQL+" FOR UPDATE", id,
	).Scan(&inv).Error
	if err != nil {
		return model.InviteCode{}, false, err
	}
	if inv.ID == uuid.Nil {
		return model.InviteCode{}, false, nil
	}
	return inv, true, nil
}

// ConsumeInvite 在仍可用时增加使用次数并可能置为 exhausted。
func (r *Repo) ConsumeInvite(ctx context.Context, id uuid.UUID) (bool, error) {
	sql := `UPDATE invite_codes SET used_count = used_count + 1,
	  status = CASE WHEN max_uses IS NOT NULL AND used_count + 1 >= max_uses THEN 'exhausted' ELSE status END
	  WHERE id = ? AND ` + inviteUsableSQL
	res := r.d.Conn(ctx).Exec(sql, id)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// InsertReferral 插入邀请关系。
func (r *Repo) InsertReferral(ctx context.Context, ref *model.Referral) error {
	return mapErr(r.d.Conn(ctx).Create(ref).Error)
}

// ActiveInviteOf 查用户当前 active 的个人邀请码。
func (r *Repo) ActiveInviteOf(ctx context.Context, ownerID uuid.UUID) (model.InviteCode, bool, error) {
	var inv model.InviteCode
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM invite_codes WHERE owner_user_id = ? AND status = 'active' ORDER BY created_at LIMIT 1",
		ownerID,
	).Scan(&inv).Error
	if err != nil {
		return model.InviteCode{}, false, err
	}
	if inv.ID == uuid.Nil {
		return model.InviteCode{}, false, nil
	}
	return inv, true, nil
}

// InsertOwnerInvite 懒创建用户个人邀请码（冲突则不算插入）。
func (r *Repo) InsertOwnerInvite(ctx context.Context, ownerID uuid.UUID, code string) (model.InviteCode, bool, error) {
	var inv model.InviteCode
	res := r.d.Conn(ctx).Raw(
		"INSERT INTO invite_codes (code, owner_user_id) VALUES (?, ?) ON CONFLICT DO NOTHING RETURNING *",
		code, ownerID,
	).Scan(&inv)
	if res.Error != nil {
		return model.InviteCode{}, false, mapErr(res.Error)
	}
	return inv, inv.ID != uuid.Nil, nil
}

// InviteeRow 是我邀请来的一个人。
type InviteeRow struct {
	Email    string    `gorm:"column:email"`
	BoundAt  time.Time `gorm:"column:bound_at"`
	RiskFlag string    `gorm:"column:risk_flag"`
}

// Invitees 列出我邀请的用户。
func (r *Repo) Invitees(ctx context.Context, referrerID uuid.UUID, limit int) ([]InviteeRow, error) {
	var rows []InviteeRow
	err := r.d.Conn(ctx).Raw(
		`SELECT u.email::text AS email, r.bound_at, r.risk_flag FROM referrals r JOIN users u ON u.id = r.referee_user_id
		 WHERE r.referrer_user_id = ? ORDER BY r.bound_at DESC LIMIT ?`,
		referrerID, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []InviteeRow{}, nil
	}
	return rows, nil
}
