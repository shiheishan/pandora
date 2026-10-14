package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// InsertResetLogs 批量追加流量重置日志。
func (r *Repo) InsertResetLogs(ctx context.Context, rows []model.ResetLog) error {
	if len(rows) == 0 {
		return nil
	}
	return mapErr(r.d.Conn(ctx).Create(&rows).Error)
}

// ResetLogQuery 是重置日志列表的筛选与分页。
type ResetLogQuery struct {
	UserID *uuid.UUID
	Reason string
	Limit  int
	Offset int
}

// filter 按筛选条件组装查询。
func (q ResetLogQuery) filter(conn *gorm.DB) *gorm.DB {
	conn = conn.Model(&model.ResetLog{})
	if q.UserID != nil {
		conn = conn.Where("user_id = ?", *q.UserID)
	}
	if q.Reason != "" {
		conn = conn.Where("reason = ?", q.Reason)
	}
	return conn
}

// ListResetLogs 分页读重置日志，创建时间倒序，同时返回总数。
func (r *Repo) ListResetLogs(ctx context.Context, q ResetLogQuery) ([]model.ResetLog, int64, error) {
	conn := r.d.Conn(ctx)
	var total int64
	if err := q.filter(conn).Count(&total).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	var out []model.ResetLog
	err := q.filter(conn).Order("created_at DESC, id DESC").Limit(q.Limit).Offset(q.Offset).Find(&out).Error
	if err != nil {
		return nil, 0, mapErr(err)
	}
	return out, total, nil
}

// ReasonStat 是按原因汇总的重置次数与释放字节。
type ReasonStat struct {
	Reason string `gorm:"column:reason"`
	Count  int    `gorm:"column:count"`
	Freed  int64  `gorm:"column:freed"`
}

// ResetStatsSince 汇总 since 之后各原因的重置次数与释放字节，按原因排序。
func (r *Repo) ResetStatsSince(ctx context.Context, since time.Time) ([]ReasonStat, error) {
	var out []ReasonStat
	err := r.d.Conn(ctx).Raw(
		`SELECT reason, count(*)::int AS count, coalesce(sum(consumed_before), 0)::bigint AS freed
		   FROM traffic_reset_logs WHERE created_at > ? GROUP BY reason ORDER BY reason`, since,
	).Scan(&out).Error
	return out, mapErr(err)
}
