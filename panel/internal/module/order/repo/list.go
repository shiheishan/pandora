package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/order/model"
)

// ListParams 是订单列表的查询条件；零值字段不参与过滤。
type ListParams struct {
	UserID        *uuid.UUID
	Statuses      []string    // 空 = 不按状态过滤
	OrderNoLike   string      // 非空时与 UserIDs 组成 (order_no ILIKE 包含 OR user_id IN UserIDs)
	UserIDs       []uuid.UUID // 只在 OrderNoLike 非空时使用；为空时只按订单号匹配
	From, To      *time.Time  // created_at >= From AND created_at < To
	Limit, Offset int
}

// ListOrders 分页列订单；返回当前页与满足条件的总数。
func (r *Repo) ListOrders(ctx context.Context, p ListParams) ([]model.Order, int64, error) {
	q := r.d.Conn(ctx).Model(&model.Order{})
	q = applyListFilters(q, p)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, mapErr(err)
	}

	var out []model.Order
	err := applyListFilters(r.d.Conn(ctx).Model(&model.Order{}), p).
		Order("created_at DESC, id DESC").
		Limit(p.Limit).Offset(p.Offset).
		Find(&out).Error
	return out, total, mapErr(err)
}

func applyListFilters(q *gorm.DB, p ListParams) *gorm.DB {
	if p.UserID != nil {
		q = q.Where("user_id = ?", *p.UserID)
	}
	if len(p.Statuses) > 0 {
		q = q.Where("status IN ?", p.Statuses)
	}
	if p.From != nil {
		q = q.Where("created_at >= ?", *p.From)
	}
	if p.To != nil {
		q = q.Where("created_at < ?", *p.To)
	}
	if p.OrderNoLike != "" {
		pat := likePattern(p.OrderNoLike)
		if len(p.UserIDs) > 0 {
			q = q.Where("(order_no ILIKE ? ESCAPE '\\' OR user_id IN ?)", pat, p.UserIDs)
		} else {
			q = q.Where("order_no ILIKE ? ESCAPE '\\'", pat)
		}
	}
	return q
}

// StatusCountsForUser 按状态统计某用户的订单数量。
func (r *Repo) StatusCountsForUser(ctx context.Context, userID uuid.UUID) (map[string]int64, error) {
	var rows []struct {
		Status string
		Count  int64
	}
	err := r.d.Conn(ctx).Raw(
		`SELECT status, count(*) AS count FROM orders WHERE user_id = ? GROUP BY status`,
		userID,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Count
	}
	return out, nil
}
