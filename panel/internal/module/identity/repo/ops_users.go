package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BulkQuery 是批量运营的人群条件（service 已校验）；零值不筛。
type BulkQuery struct {
	Status     string
	GroupID    *uuid.UUID
	Pattern    string
	SubFilter  bool
	SubIDs     []uuid.UUID
	SubExclude bool
}

// ExportUserRow 是导出的一行。
type ExportUserRow struct {
	ID          uuid.UUID  `gorm:"column:id"`
	Email       string     `gorm:"column:email"`
	Status      string     `gorm:"column:status"`
	GroupName   string     `gorm:"column:group_name"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	LastLoginAt *time.Time `gorm:"column:last_login_at"`
}

// CountBulk 统计批量运营筛选命中人数。
func (r *Repo) CountBulk(ctx context.Context, q BulkQuery) (int64, error) {
	var n int64
	err := applyBulkWhere(r.d.Conn(ctx).Table("users u"), q).Count(&n).Error
	return n, err
}

// BulkUsers 按注册时间倒序取前 limit 个用户的 id 与邮箱。
func (r *Repo) BulkUsers(ctx context.Context, q BulkQuery, limit int) ([]UserRef, error) {
	var rows []UserRef
	err := applyBulkWhere(r.d.Conn(ctx).Table("users u"), q).
		Select("u.id, u.email::text AS email").
		Order("u.created_at DESC, u.id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []UserRef{}, nil
	}
	return rows, nil
}

// ExportBulk 导出批量运营筛选命中的用户行（含组名）。
func (r *Repo) ExportBulk(ctx context.Context, q BulkQuery, limit int) ([]ExportUserRow, error) {
	var rows []ExportUserRow
	err := applyBulkWhere(r.d.Conn(ctx).Table("users u"), q).
		Select("u.id, u.email::text AS email, u.status, COALESCE(g.name, '') AS group_name, u.created_at, u.last_login_at").
		Joins("LEFT JOIN user_groups g ON g.id = u.user_group_id").
		Order("u.created_at DESC, u.id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ExportUserRow{}, nil
	}
	return rows, nil
}

func applyBulkWhere(tx *gorm.DB, q BulkQuery) *gorm.DB {
	if q.Status != "" {
		tx = tx.Where("u.status = ?", q.Status)
	} else {
		// 批量运营一律不圈已注销的账号（占位邮箱不可投递）
		tx = tx.Where("u.status <> 'deleted'")
	}
	if q.GroupID != nil {
		tx = tx.Where("u.user_group_id = ?", *q.GroupID)
	}
	if q.Pattern != "" {
		tx = tx.Where("u.email::text ILIKE ? ESCAPE '\\'", q.Pattern)
	}
	return applySubIDs(tx, q.SubFilter, q.SubIDs, q.SubExclude)
}

// applySubIDs 按订阅圈人结果过滤用户 id。
func applySubIDs(tx *gorm.DB, filter bool, ids []uuid.UUID, exclude bool) *gorm.DB {
	if !filter {
		return tx
	}
	if !exclude && len(ids) == 0 {
		return tx.Where("false")
	}
	if exclude && len(ids) == 0 {
		return tx
	}
	lit := uuidArray(ids)
	if exclude {
		return tx.Where("NOT (u.id = ANY(CAST(? AS uuid[])))", lit)
	}
	return tx.Where("u.id = ANY(CAST(? AS uuid[]))", lit)
}

func uuidArray(ids []uuid.UUID) string {
	if len(ids) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id.String())
	}
	b.WriteByte('}')
	return b.String()
}
