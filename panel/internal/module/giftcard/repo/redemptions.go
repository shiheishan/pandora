package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/giftcard/model"
)

// StatsRow 是后台统计要的原始数。
type StatsRow struct {
	Templates     int64
	CodesTotal    int64
	CodesUsed     int64
	CodesUnused   int64
	TrafficOut    int64
	BalanceOut    decimal.Decimal
	BalanceIssued decimal.Decimal
}

// MyRedemptionRow 是门户兑换记录的一行（JOIN 模板与码）。
type MyRedemptionRow struct {
	TemplateName  string           `gorm:"column:template_name"`
	Type          string           `gorm:"column:type"`
	CodeMask      string           `gorm:"column:code_mask"`
	PrizeLabel    *string          `gorm:"column:prize_label"`
	BalanceAmount *decimal.Decimal `gorm:"column:balance_amount"`
	TrafficBytes  *int64           `gorm:"column:traffic_bytes"`
	ExpireDays    *int             `gorm:"column:expire_days"`
	RedeemedAt    time.Time        `gorm:"column:redeemed_at"`
}

// UsageRow 是后台使用明细的一行（JOIN 模板与码）。
type UsageRow struct {
	TemplateName  string           `gorm:"column:template_name"`
	CodeMask      string           `gorm:"column:code_mask"`
	UserID        uuid.UUID        `gorm:"column:user_id"`
	PrizeLabel    *string          `gorm:"column:prize_label"`
	BalanceAmount *decimal.Decimal `gorm:"column:balance_amount"`
	TrafficBytes  *int64           `gorm:"column:traffic_bytes"`
	ExpireDays    *int             `gorm:"column:expire_days"`
	QuotaReset    bool             `gorm:"column:quota_reset"`
	RedeemedAt    time.Time        `gorm:"column:redeemed_at"`
}

// InsertRedemption 插入一条兑换流水。
func (r *Repo) InsertRedemption(ctx context.Context, m *model.Redemption) error {
	if err := checkMoney(m.BalanceAmount, m.RefundAmount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(m).Error)
}

// LockUserTemplate 在当前事务里取 (用户, 模板) 的事务级咨询锁。
func (r *Repo) LockUserTemplate(ctx context.Context, userID, templateID uuid.UUID) error {
	key := "gift_card:" + templateID.String() + ":" + userID.String()
	return r.d.Conn(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error
}

// UserTemplateUsage 数用户在某模板上的兑换次数。
func (r *Repo) UserTemplateUsage(ctx context.Context, userID, templateID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*) FROM gift_card_redemptions WHERE user_id = ? AND template_id = ?`,
		userID, templateID,
	).Row().Scan(&n)
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// Stats 取后台统计的原始数。
func (r *Repo) Stats(ctx context.Context) (StatsRow, error) {
	conn := r.d.Conn(ctx)
	var row StatsRow

	// 计数一条 SQL 取完：模板数（未归档）、码总数、已用、未用、送出流量
	if err := conn.Raw(`
		SELECT (SELECT count(*) FROM gift_card_templates WHERE status <> 'archived'),
		       count(*),
		       count(*) FILTER (WHERE status = 'used'),
		       count(*) FILTER (WHERE status = 'unused'),
		       (SELECT coalesce(sum(traffic_bytes), 0) FROM gift_card_redemptions)
		FROM gift_card_codes`).Row().Scan(&row.Templates, &row.CodesTotal, &row.CodesUsed, &row.CodesUnused, &row.TrafficOut); err != nil {
		return StatsRow{}, mapErr(err)
	}

	// 送出余额与发出余额各一条合计，都是单行单列
	if err := conn.Raw(`SELECT coalesce(sum(balance_amount), 0) FROM gift_card_redemptions WHERE balance_amount IS NOT NULL`).Row().Scan(&row.BalanceOut); err != nil {
		return StatsRow{}, mapErr(err)
	}
	if err := conn.Raw(`SELECT coalesce(sum(t.balance_amount), 0) FROM gift_card_codes c JOIN gift_card_templates t ON t.id = c.template_id WHERE t.type = 'general' AND t.balance_amount IS NOT NULL`).Row().Scan(&row.BalanceIssued); err != nil {
		return StatsRow{}, mapErr(err)
	}
	return row, nil
}

// MyRedemptions 取用户最近的兑换记录。
func (r *Repo) MyRedemptions(ctx context.Context, userID uuid.UUID, limit int) ([]MyRedemptionRow, error) {
	var rows []MyRedemptionRow
	err := r.d.Conn(ctx).Raw(`
		SELECT t.name AS template_name, t.type, c.code_mask, r.prize_label,
		       r.balance_amount, r.traffic_bytes, r.expire_days, r.redeemed_at
		FROM gift_card_redemptions r
		JOIN gift_card_templates t ON t.id = r.template_id
		JOIN gift_card_codes c ON c.id = r.code_id
		WHERE r.user_id = ?
		ORDER BY r.redeemed_at DESC, r.id DESC
		LIMIT ?`, userID, limit).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []MyRedemptionRow{}, nil
	}
	return rows, nil
}

// Usages 取使用明细。
func (r *Repo) Usages(ctx context.Context, templateID *uuid.UUID, limit int) ([]UsageRow, error) {
	where := "true"
	var args []any
	if templateID != nil {
		where = "r.template_id = ?"
		args = append(args, *templateID)
	}
	args = append(args, limit)
	var rows []UsageRow
	err := r.d.Conn(ctx).Raw(`
		SELECT t.name AS template_name, c.code_mask, r.user_id, r.prize_label,
		       r.balance_amount, r.traffic_bytes, r.expire_days, r.quota_reset, r.redeemed_at
		FROM gift_card_redemptions r
		JOIN gift_card_templates t ON t.id = r.template_id
		JOIN gift_card_codes c ON c.id = r.code_id
		WHERE `+where+`
		ORDER BY r.redeemed_at DESC, r.id DESC
		LIMIT ?`, args...).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []UsageRow{}, nil
	}
	return rows, nil
}
