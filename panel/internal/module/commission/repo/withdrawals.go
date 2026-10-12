package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/commission/model"
)

// InsertWithdrawal 插入提现申请。
func (r *Repo) InsertWithdrawal(ctx context.Context, w *model.Withdrawal) error {
	if err := checkMoney(w.Amount); err != nil {
		return err
	}
	if err := r.d.Conn(ctx).Create(w).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// WithdrawalByID 按 id 读提现单；forUpdate 为 true 时加 FOR UPDATE。
func (r *Repo) WithdrawalByID(ctx context.Context, id uuid.UUID, forUpdate bool) (model.Withdrawal, bool, error) {
	var w model.Withdrawal
	sql := "SELECT * FROM withdrawals WHERE id = ?"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	err := r.d.Conn(ctx).Raw(sql, id).Scan(&w).Error
	if err != nil {
		return model.Withdrawal{}, false, mapErr(err)
	}
	if w.ID == uuid.Nil {
		return model.Withdrawal{}, false, nil
	}
	return w, true, nil
}

// HasInFlightWithdrawal 用户是否有申请中或已批准待打款的提现。
func (r *Repo) HasInFlightWithdrawal(ctx context.Context, userID uuid.UUID) (bool, error) {
	var ok bool
	err := r.d.Conn(ctx).Raw(
		`SELECT EXISTS (SELECT 1 FROM withdrawals WHERE user_id = ? AND status IN ('requested','approved'))`,
		userID,
	).Scan(&ok).Error
	return ok, mapErr(err)
}

// ReviewWithdrawal 审批提现：仅 status = requested 的行可改。
func (r *Repo) ReviewWithdrawal(ctx context.Context, id uuid.UUID, to string, reason *string, by uuid.UUID, at time.Time) (bool, error) {
	cols := map[string]any{
		"status":        to,
		"reject_reason": reason,
		"reviewed_by":   by,
		"reviewed_at":   at,
		"updated_at":    at,
	}
	if to == "rejected" {
		cols["completed_at"] = at
	}
	res := r.d.Conn(ctx).Model(&model.Withdrawal{}).
		Where("id = ? AND status = ?", id, "requested").
		Updates(cols)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// MarkWithdrawalPaid 将已批准的提现标为已打款。
func (r *Repo) MarkWithdrawalPaid(ctx context.Context, id uuid.UUID, reference string, payoutTxnID, by uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Withdrawal{}).
		Where("id = ? AND status = ?", id, "approved").
		Updates(map[string]any{
			"status":           "paid",
			"payout_reference": reference,
			"payout_txn_id":    payoutTxnID,
			"paid_by":          by,
			"completed_at":     at,
			"updated_at":       at,
		})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// WithdrawalsOf 用户最近若干条提现记录。
func (r *Repo) WithdrawalsOf(ctx context.Context, userID uuid.UUID, limit int) ([]model.Withdrawal, error) {
	var out []model.Withdrawal
	err := r.d.Conn(ctx).
		Where("user_id = ?", userID).
		Order("requested_at DESC, id DESC").
		Limit(limit).
		Find(&out).Error
	return out, mapErr(err)
}

// ListWithdrawals 后台列表；status 为空则不筛状态。
func (r *Repo) ListWithdrawals(ctx context.Context, status string, limit int) ([]model.Withdrawal, error) {
	q := r.d.Conn(ctx).Model(&model.Withdrawal{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []model.Withdrawal
	err := q.Order("requested_at DESC, id DESC").Limit(limit).Find(&out).Error
	return out, mapErr(err)
}

// WithdrawalTotals 是某用户某币种的提现汇总：InFlight 申请中与已批准，Paid 已打款。
type WithdrawalTotals struct {
	InFlight decimal.Decimal `gorm:"column:in_flight"`
	Paid     decimal.Decimal `gorm:"column:paid"`
}

// WithdrawalTotalsOf 按用户与币种汇总提现金额。
func (r *Repo) WithdrawalTotalsOf(ctx context.Context, userID uuid.UUID, currency string) (WithdrawalTotals, error) {
	var t WithdrawalTotals
	err := r.d.Conn(ctx).Raw(`
SELECT
  COALESCE(sum(amount) FILTER (WHERE status IN ('requested','approved')), 0) AS in_flight,
  COALESCE(sum(amount) FILTER (WHERE status = 'paid'), 0) AS paid
FROM withdrawals
WHERE user_id = ? AND currency = ?`, userID, currency).Scan(&t).Error
	return t, mapErr(err)
}

// WithdrawalOverview 是全站某币种的提现汇总：PaidOut 已打款总额，Waiting 申请中笔数。
type WithdrawalOverview struct {
	PaidOut decimal.Decimal `gorm:"column:paid_out"`
	Waiting int64           `gorm:"column:waiting"`
}

// WithdrawalOverviewOf 全站提现概览（waiting 统计全币种申请中笔数）。
func (r *Repo) WithdrawalOverviewOf(ctx context.Context, currency string) (WithdrawalOverview, error) {
	var o WithdrawalOverview
	err := r.d.Conn(ctx).Raw(`
SELECT
  COALESCE(sum(amount) FILTER (WHERE status = 'paid' AND currency = ?), 0) AS paid_out,
  count(*) FILTER (WHERE status = 'requested') AS waiting
FROM withdrawals`, currency).Scan(&o).Error
	return o, mapErr(err)
}
