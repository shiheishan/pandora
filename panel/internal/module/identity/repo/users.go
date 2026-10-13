package repo

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/identity/model"
)

// UserListQuery 是后台用户列表的筛选；零值不筛。
type UserListQuery struct {
	Pattern    string      // 非空时：email::text ILIKE Pattern ESCAPE '\' OR coalesce(display_name,'') ILIKE Pattern ESCAPE '\'
	ExactID    *uuid.UUID  // 非空时与 Pattern 的条件 OR 在一起：OR u.id = ExactID
	Statuses   []string    // 非空时 u.status IN ?
	GroupID    *uuid.UUID  // 非空时 u.user_group_id = ?
	NoGroup    bool        // true 时 u.user_group_id IS NULL
	SubFilter  bool        // true 时按订阅条件圈人（见 applySubIDs）
	SubIDs     []uuid.UUID // subscription 按订阅条件给出的用户 id
	SubExclude bool        // true 时排除 SubIDs，false 时只保留 SubIDs
}

// UserListRow 是列表的一行：users 的列加组名（左连 user_groups）。
type UserListRow struct {
	model.User
	GroupName *string `gorm:"column:group_name"`
}

// UserRef 是一个用户的 id 与邮箱。
type UserRef struct {
	ID    uuid.UUID `gorm:"column:id"`
	Email string    `gorm:"column:email"`
}

// LikePattern 导出给 service：生成 ILIKE 包含模式。
func LikePattern(q string) string { return likePattern(q) }

// UserByID 按主键查用户。
func (r *Repo) UserByID(ctx context.Context, id uuid.UUID) (model.User, bool, error) {
	var u model.User
	err := r.d.Conn(ctx).Where("id = ?", id).Take(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.User{}, false, nil
		}
		return model.User{}, false, err
	}
	return u, true, nil
}

// UsersByIDs 批量按 id 查用户。
func (r *Repo) UsersByIDs(ctx context.Context, ids []uuid.UUID) ([]model.User, error) {
	if len(ids) == 0 {
		return []model.User{}, nil
	}
	var users []model.User
	err := r.d.Conn(ctx).Where("id = ANY(CAST(? AS uuid[]))", uuidArray(ids)).Find(&users).Error
	if err != nil {
		return nil, err
	}
	if users == nil {
		return []model.User{}, nil
	}
	return users, nil
}

// UserByEmail 按邮箱查用户（citext，不分大小写）。
func (r *Repo) UserByEmail(ctx context.Context, email string) (model.User, bool, error) {
	var u model.User
	err := r.d.Conn(ctx).Where("email = ?", email).Take(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.User{}, false, nil
		}
		return model.User{}, false, err
	}
	return u, true, nil
}

// LockUser 锁定用户行（FOR NO KEY UPDATE）。
func (r *Repo) LockUser(ctx context.Context, id uuid.UUID) (model.User, bool, error) {
	var u model.User
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM users WHERE id = ? FOR NO KEY UPDATE", id,
	).Scan(&u).Error
	if err != nil {
		return model.User{}, false, err
	}
	if u.ID == uuid.Nil {
		return model.User{}, false, nil
	}
	return u, true, nil
}

// EmailTaken 邮箱是否已被占用。
func (r *Repo) EmailTaken(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := r.d.Conn(ctx).Raw(
		"SELECT EXISTS(SELECT 1 FROM users WHERE email = ?)", email,
	).Scan(&exists).Error
	return exists, err
}

// InsertUser 插入用户；id、created_at 由库生成并回填。
func (r *Repo) InsertUser(ctx context.Context, u *model.User) error {
	return mapErr(r.d.Conn(ctx).Create(u).Error)
}

// SetUserStatus 更新用户状态。
func (r *Repo) SetUserStatus(ctx context.Context, id uuid.UUID, status string) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE users SET status = ?, updated_at = now() WHERE id = ?", status, id,
	).Error
}

// SetUserGroup 设置用户所属组。
func (r *Repo) SetUserGroup(ctx context.Context, id uuid.UUID, groupID *uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE users SET user_group_id = ?, updated_at = now() WHERE id = ?", groupID, id,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// TouchLastLogin 更新最近登录时间（不改 updated_at）。
func (r *Repo) TouchLastLogin(ctx context.Context, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE users SET last_login_at = now() WHERE id = ?", id,
	).Error
}

// Emails 批量查用户邮箱文本。
func (r *Repo) Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	var rows []UserRef
	err := r.d.Conn(ctx).Raw(
		"SELECT id, email::text AS email FROM users WHERE id = ANY(CAST(? AS uuid[]))", uuidArray(ids),
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Email
	}
	return out, nil
}

// IDsByEmailLike 按邮箱模糊匹配查 id 列表。
func (r *Repo) IDsByEmailLike(ctx context.Context, q string, limit int) ([]uuid.UUID, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	var rows []UserRef
	err := r.d.Conn(ctx).Raw(
		"SELECT id FROM users WHERE email::text ILIKE ? ESCAPE '\\' ORDER BY id LIMIT ?",
		likePattern(q), limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids, nil
}

// CountUsers 统计列表命中数。
func (r *Repo) CountUsers(ctx context.Context, q UserListQuery) (int64, error) {
	var n int64
	err := applyUserListWhere(r.d.Conn(ctx).Table("users u"), q).Count(&n).Error
	return n, err
}

// ListUsers 分页列出用户及组名。
func (r *Repo) ListUsers(ctx context.Context, q UserListQuery, limit, offset int) ([]UserListRow, error) {
	var rows []UserListRow
	err := applyUserListWhere(r.d.Conn(ctx).Table("users u"), q).
		Select("u.*, g.name AS group_name").
		Joins("LEFT JOIN user_groups g ON g.id = u.user_group_id").
		Order("u.created_at DESC, u.id DESC").
		Limit(limit).Offset(offset).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []UserListRow{}, nil
	}
	return rows, nil
}

// Referrer 查被邀请人的上级。
func (r *Repo) Referrer(ctx context.Context, refereeID uuid.UUID) (UserRef, bool, error) {
	var ref UserRef
	err := r.d.Conn(ctx).Raw(
		"SELECT u.id, u.email::text AS email FROM referrals r JOIN users u ON u.id = r.referrer_user_id WHERE r.referee_user_id = ?",
		refereeID,
	).Scan(&ref).Error
	if err != nil {
		return UserRef{}, false, err
	}
	if ref.ID == uuid.Nil {
		return UserRef{}, false, nil
	}
	return ref, true, nil
}

// ReferralCount 统计某人邀请的人数。
func (r *Repo) ReferralCount(ctx context.Context, referrerID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		"SELECT count(*) FROM referrals WHERE referrer_user_id = ?", referrerID,
	).Scan(&n).Error
	return n, err
}

func applyUserListWhere(tx *gorm.DB, q UserListQuery) *gorm.DB {
	if q.Pattern != "" {
		pat := q.Pattern
		cond := "(u.email::text ILIKE ? ESCAPE '\\' OR coalesce(u.display_name, '') ILIKE ? ESCAPE '\\')"
		args := []any{pat, pat}
		if q.ExactID != nil {
			cond = "(" + cond + " OR u.id = ?)"
			args = append(args, *q.ExactID)
		}
		tx = tx.Where(cond, args...)
	} else if q.ExactID != nil {
		tx = tx.Where("u.id = ?", *q.ExactID)
	}
	if len(q.Statuses) > 0 {
		tx = tx.Where("u.status IN ?", q.Statuses)
	}
	if q.GroupID != nil {
		tx = tx.Where("u.user_group_id = ?", *q.GroupID)
	}
	if q.NoGroup {
		tx = tx.Where("u.user_group_id IS NULL")
	}
	return applySubIDs(tx, q.SubFilter, q.SubIDs, q.SubExclude)
}

// TotalReferrals 统计全站邀请关系总数。
func (r *Repo) TotalReferrals(ctx context.Context) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw("SELECT count(*) FROM referrals").Scan(&n).Error
	return n, err
}
