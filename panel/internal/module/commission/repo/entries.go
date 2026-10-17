package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/commission/model"
)

// OrderHasEntries 这一单是否已经计提过佣金（任何一级）；支付回调重放、人工补结算时防重。走 (order_id, …) 唯一索引。
func (r *Repo) OrderHasEntries(ctx context.Context, orderID uuid.UUID) (bool, error) {
	var ok bool
	err := r.d.Conn(ctx).Raw(
		`SELECT EXISTS (SELECT 1 FROM commission_entries WHERE order_id = ?)`,
		orderID,
	).Scan(&ok).Error
	return ok, mapErr(err)
}

// RefereeHasOtherEntry 被推荐人是否已有除本订单外的佣金条目。
func (r *Repo) RefereeHasOtherEntry(ctx context.Context, refereeID, orderID uuid.UUID) (bool, error) {
	var ok bool
	err := r.d.Conn(ctx).Raw(
		`SELECT EXISTS (SELECT 1 FROM commission_entries WHERE referee_user_id = ? AND order_id <> ?)`,
		refereeID, orderID,
	).Scan(&ok).Error
	return ok, mapErr(err)
}

// InsertEntry 插入佣金条目。
func (r *Repo) InsertEntry(ctx context.Context, e *model.Entry) error {
	if err := checkMoney(e.BaseAmount, e.CommissionAmount); err != nil {
		return err
	}
	if err := r.d.Conn(ctx).Create(e).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// DueEntryIDs 扫描已到冻结期、可自动解冻的 pending 条目 id（不加锁）。
func (r *Repo) DueEntryIDs(ctx context.Context, at time.Time, limit int) ([]uuid.UUID, error) {
	var rows []struct{ ID uuid.UUID }
	err := r.d.Conn(ctx).Raw(`
SELECT id FROM commission_entries
 WHERE status = 'pending' AND NOT review_required AND frozen_until <= ?
 ORDER BY frozen_until, id
 LIMIT ?`, at, limit).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

// LockDueEntry 锁定一条到期可解冻的 pending 条目（SKIP LOCKED）。
func (r *Repo) LockDueEntry(ctx context.Context, id uuid.UUID, at time.Time) (model.Entry, bool, error) {
	var e model.Entry
	err := r.d.Conn(ctx).Raw(`
SELECT * FROM commission_entries
 WHERE id = ? AND status = 'pending' AND NOT review_required AND frozen_until <= ?
 FOR UPDATE SKIP LOCKED`, id, at).Scan(&e).Error
	if err != nil {
		return model.Entry{}, false, mapErr(err)
	}
	if e.ID == uuid.Nil {
		return model.Entry{}, false, nil
	}
	return e, true, nil
}

// MarkEntryAvailable 将 pending 条目标为 available 并写入解冻交易；autoTransfer 为 true 时同时挂上「等自动转余额」标记。
func (r *Repo) MarkEntryAvailable(ctx context.Context, id, settleTxnID uuid.UUID, at time.Time, autoTransfer bool) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Entry{}).
		Where("id = ? AND status = ?", id, "pending").
		Updates(map[string]any{
			"status":            "available",
			"settle_txn_id":     settleTxnID,
			"settled_at":        at,
			"auto_transfer_due": autoTransfer,
			"updated_at":        at,
		})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// AutoTransferDueEntries 不加锁地列出挂着「等自动转余额」标记的条目（先解冻的先转），最多 limit 条。
func (r *Repo) AutoTransferDueEntries(ctx context.Context, limit int) ([]model.Entry, error) {
	var out []model.Entry
	err := r.d.Conn(ctx).
		Where("auto_transfer_due").
		Order("settled_at, id").
		Limit(limit).
		Find(&out).Error
	return out, mapErr(err)
}

// LockAutoTransferEntry 锁定一条仍挂着「等自动转余额」标记的条目（SKIP LOCKED）；标记已清或被别人锁着时 ok 为 false。
func (r *Repo) LockAutoTransferEntry(ctx context.Context, id uuid.UUID) (model.Entry, bool, error) {
	var e model.Entry
	err := r.d.Conn(ctx).Raw(`
SELECT * FROM commission_entries
 WHERE id = ? AND auto_transfer_due
 FOR UPDATE SKIP LOCKED`, id).Scan(&e).Error
	if err != nil {
		return model.Entry{}, false, mapErr(err)
	}
	if e.ID == uuid.Nil {
		return model.Entry{}, false, nil
	}
	return e, true, nil
}

// ClearAutoTransferDue 清掉条目的「等自动转余额」标记；没改到行（标记已清）时返回 false。
func (r *Repo) ClearAutoTransferDue(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Entry{}).
		Where("id = ? AND auto_transfer_due", id).
		Updates(map[string]any{"auto_transfer_due": false, "updated_at": at})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// EntriesOf 某推荐人最近若干条佣金明细。
func (r *Repo) EntriesOf(ctx context.Context, referrerID uuid.UUID, limit int) ([]model.Entry, error) {
	var out []model.Entry
	err := r.d.Conn(ctx).
		Where("referrer_user_id = ?", referrerID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&out).Error
	return out, mapErr(err)
}

// EntryTotals 是某推荐人的佣金汇总。
type EntryTotals struct {
	Pending      decimal.Decimal `gorm:"column:pending"`
	Available    decimal.Decimal `gorm:"column:available"`
	Total        decimal.Decimal `gorm:"column:total"`
	Orders       int64           `gorm:"column:orders"`
	PaidInvitees int64           `gorm:"column:paid_invitees"`
}

// EntryTotalsOf 按推荐人汇总佣金；已作废的条目不计入任何一项。orders 是条目数（含第 2、3 级），
// paid_invitees 只算直接邀请（第 1 级）的人，和「邀请注册」同口径。
func (r *Repo) EntryTotalsOf(ctx context.Context, referrerID uuid.UUID) (EntryTotals, error) {
	var t EntryTotals
	err := r.d.Conn(ctx).Raw(`
SELECT
  COALESCE(sum(commission_amount) FILTER (WHERE status = 'pending'), 0) AS pending,
  COALESCE(sum(commission_amount) FILTER (WHERE status = 'available'), 0) AS available,
  COALESCE(sum(commission_amount) FILTER (WHERE status <> 'voided'), 0) AS total,
  count(*) FILTER (WHERE status <> 'voided') AS orders,
  count(DISTINCT referee_user_id) FILTER (WHERE status <> 'voided' AND level = 1) AS paid_invitees
FROM commission_entries
WHERE referrer_user_id = ?`, referrerID).Scan(&t).Error
	return t, mapErr(err)
}

// earnedRow 是 EarnedByUsers 查询的一行。
type earnedRow struct {
	ReferrerUserID uuid.UUID       `gorm:"column:referrer_user_id"`
	Total          decimal.Decimal `gorm:"column:total"`
}

// EarnedByUsers 批量汇总各推荐人已赚佣金总额（不含已作废）。
func (r *Repo) EarnedByUsers(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]decimal.Decimal{}, nil
	}
	var rows []earnedRow
	err := r.d.Conn(ctx).Raw(`
SELECT referrer_user_id, sum(commission_amount) AS total
FROM commission_entries
WHERE referrer_user_id IN ? AND status <> 'voided'
GROUP BY referrer_user_id`, userIDs).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[uuid.UUID]decimal.Decimal, len(rows))
	for _, row := range rows {
		out[row.ReferrerUserID] = row.Total
	}
	return out, nil
}

// EntryOverview 是全站佣金汇总。
type EntryOverview struct {
	Pending    decimal.Decimal `gorm:"column:pending"`
	Available  decimal.Decimal `gorm:"column:available"`
	ThisMonth  decimal.Decimal `gorm:"column:this_month"`
	Total      decimal.Decimal `gorm:"column:total"`
	Entries    int64           `gorm:"column:entries"`
	NeedReview int64           `gorm:"column:need_review"`
}

// EntryOverview 全站佣金概览（本月按 created_at 统计；已作废的不计入金额与笔数）；没有条目时各金额为 0。
func (r *Repo) EntryOverview(ctx context.Context, monthStart time.Time) (EntryOverview, error) {
	var out EntryOverview
	err := r.d.Conn(ctx).Raw(`
SELECT
  COALESCE(sum(commission_amount) FILTER (WHERE status = 'pending'), 0) AS pending,
  COALESCE(sum(commission_amount) FILTER (WHERE status = 'available'), 0) AS available,
  COALESCE(sum(commission_amount) FILTER (WHERE created_at >= ? AND status <> 'voided'), 0) AS this_month,
  COALESCE(sum(commission_amount) FILTER (WHERE status <> 'voided'), 0) AS total,
  count(*) FILTER (WHERE status <> 'voided') AS entries,
  count(*) FILTER (WHERE review_required AND status = 'pending') AS need_review
FROM commission_entries`, monthStart).Scan(&out).Error
	return out, mapErr(err)
}

// MarkEntryReview 把一条待解冻的条目转人工复核（之后不再参与自动解冻）；只改 pending 且尚未待复核的行。
func (r *Repo) MarkEntryReview(ctx context.Context, id uuid.UUID, reason string, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Entry{}).
		Where("id = ? AND status = ? AND NOT review_required", id, "pending").
		Updates(map[string]any{
			"review_required": true,
			"review_reason":   reason,
			"updated_at":      at,
		})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
