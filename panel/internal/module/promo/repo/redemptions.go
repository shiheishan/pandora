package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/promo/model"
)

// InsertRedemption 插入一条占用记录。
func (r *Repo) InsertRedemption(ctx context.Context, rd *model.Redemption) error {
	if err := checkMoney(rd.DiscountAmount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(rd).Error)
}

// CountActiveByUser 统计用户在某券上 held 或 captured 的条数。
func (r *Repo) CountActiveByUser(ctx context.Context, couponID, userID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*) FROM coupon_redemptions WHERE coupon_id = ? AND user_id = ? AND status IN ('held','captured')`,
		couponID, userID,
	).Scan(&n).Error
	return n, err
}

// FinishHeld 把订单上 held 的占用改为 captured 或 released，并返回券 ID。
func (r *Repo) FinishHeld(ctx context.Context, orderID uuid.UUID, to string, reason *string) (couponID uuid.UUID, ok bool, err error) {
	var sql string
	var args []any
	switch to {
	case model.RedemptionCaptured:
		sql = `UPDATE coupon_redemptions SET status = 'captured', captured_at = now()
			WHERE order_id = ? AND status = 'held' RETURNING coupon_id`
		args = []any{orderID}
	case model.RedemptionReleased:
		sql = `UPDATE coupon_redemptions SET status = 'released', released_at = now(), release_reason = ?
			WHERE order_id = ? AND status = 'held' RETURNING coupon_id`
		args = []any{reason, orderID}
	default:
		return uuid.Nil, false, fmt.Errorf("promo: 非法占用终态 %q", to)
	}

	var ids []uuid.UUID
	if err := r.d.Conn(ctx).Raw(sql, args...).Scan(&ids).Error; err != nil {
		return uuid.Nil, false, err
	}
	if len(ids) == 0 {
		return uuid.Nil, false, nil
	}
	return ids[0], true, nil
}

// RedemptionRow 是单张券的一条核销明细行。
type RedemptionRow struct {
	UserID         uuid.UUID       `gorm:"column:user_id"`
	OrderNo        string          `gorm:"column:order_no"`
	DiscountAmount decimal.Decimal `gorm:"column:discount_amount"`
	Currency       string          `gorm:"column:currency"`
	Status         string          `gorm:"column:status"`
	CreatedAt      time.Time       `gorm:"column:created_at"`
}

// RedemptionsByCoupon 按券查最近若干条核销明细。
func (r *Repo) RedemptionsByCoupon(ctx context.Context, couponID uuid.UUID, limit int) ([]RedemptionRow, error) {
	var rows []RedemptionRow
	err := r.d.Conn(ctx).Raw(
		`SELECT user_id, order_no, discount_amount, currency, status, created_at
		FROM coupon_redemptions WHERE coupon_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		couponID, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []RedemptionRow{}, nil
	}
	return rows, nil
}
