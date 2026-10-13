package repo

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// UserOrderStatRow 是订单统计聚合 SQL 的行扫描结果。
type UserOrderStatRow struct {
	UserID   uuid.UUID       `gorm:"column:user_id"`
	Currency string          `gorm:"column:currency"`
	Orders   int64           `gorm:"column:orders"`
	Paid     decimal.Decimal `gorm:"column:paid"`
}

// UserOrderStatRows 统计指定用户的订单数与已付总额（按用户和币种分组）。
func (r *Repo) UserOrderStatRows(ctx context.Context, userIDs []uuid.UUID) ([]UserOrderStatRow, error) {
	if len(userIDs) == 0 {
		return []UserOrderStatRow{}, nil
	}
	lit := uuidArray(userIDs)
	var rows []UserOrderStatRow
	err := r.d.Conn(ctx).Raw(`
		SELECT user_id, currency,
		       count(*) AS orders,
		       coalesce(sum(paid_amount) FILTER (WHERE status IN ('paid','fulfilled')), 0) AS paid
		  FROM orders
		 WHERE user_id = ANY(CAST(? AS uuid[]))
		 GROUP BY user_id, currency`, lit).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []UserOrderStatRow{}, nil
	}
	return rows, nil
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
