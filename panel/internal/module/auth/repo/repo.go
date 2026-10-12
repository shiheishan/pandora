// Package repo 是 auth 模块唯一使用 GORM 的地方。
package repo

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/auth/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// adminSetLockKey 是管理员集合变动的锁锚；所有改管理员集合的路径都先取它，只在事务里调用。
const adminSetLockKey int64 = 0x70616e646f7261

// Repo 是 auth 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// AdminRow 是管理员名单的一行。
type AdminRow struct {
	UserID    uuid.UUID
	CreatedAt time.Time
}

// IsAdmin 报告用户是否在管理员名单里（不看账号状态）。
func (r *Repo) IsAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	var n int64
	err := r.d.Conn(ctx).Model(&model.Admin{}).Where("user_id = ?", userID).Count(&n).Error
	return n > 0, err
}

// InsertAdmin 把用户加进管理员名单；已在名单里返回 false（不改原记录）。
func (r *Repo) InsertAdmin(ctx context.Context, userID uuid.UUID, grantedBy *uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.Admin{UserID: userID, GrantedBy: grantedBy})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteAdmin 把用户移出管理员名单；本来不在返回 false。
func (r *Repo) DeleteAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Where("user_id = ?", userID).Delete(&model.Admin{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Admins 返回管理员名单（按加入时间、用户 ID 排序）；名单是人工维护的几个人，不分页。
func (r *Repo) Admins(ctx context.Context) ([]AdminRow, error) {
	var rows []model.Admin
	if err := r.d.Conn(ctx).Order("created_at, user_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]AdminRow, 0, len(rows))
	for _, a := range rows {
		out = append(out, AdminRow{UserID: a.UserID, CreatedAt: a.CreatedAt})
	}
	return out, nil
}

// LockAdminSet 在当前事务里取管理员集合的事务级咨询锁。
func (r *Repo) LockAdminSet(ctx context.Context) error {
	return r.d.Conn(ctx).Exec("SELECT pg_advisory_xact_lock(?)", adminSetLockKey).Error
}
