package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// LockDueExpiries 锁住一批已到期仍是 active 的订阅（current_period_end <= now），按到期时间、id 排；
// 别的事务锁着的跳过（SKIP LOCKED），下一轮再看。锁强度与 ForNoKeyUpdate 相同。
func (r *Repo) LockDueExpiries(ctx context.Context, now time.Time, limit int) ([]model.Subscription, error) {
	var out []model.Subscription
	err := r.d.Conn(ctx).Raw(
		`SELECT * FROM subscriptions WHERE status = 'active' AND current_period_end <= ? ORDER BY current_period_end, id LIMIT ? FOR NO KEY UPDATE SKIP LOCKED`,
		now, limit,
	).Scan(&out).Error
	return out, mapErr(err)
}

// LockDueRenewalCloses 锁住一批该关窗的订阅：已过期、窗口没关、到期时间不晚于 cutoff；SKIP LOCKED。
func (r *Repo) LockDueRenewalCloses(ctx context.Context, cutoff time.Time, limit int) ([]model.Subscription, error) {
	var out []model.Subscription
	err := r.d.Conn(ctx).Raw(
		`SELECT * FROM subscriptions WHERE status = 'expired' AND renewal_closed_at IS NULL AND current_period_end <= ? ORDER BY current_period_end, id LIMIT ? FOR NO KEY UPDATE SKIP LOCKED`,
		cutoff, limit,
	).Scan(&out).Error
	return out, mapErr(err)
}

// TransitionStatus 带旧状态条件把一组订阅的 status 从 from 改成 to，返回实际改到的 id。
func (r *Repo) TransitionStatus(ctx context.Context, ids []uuid.UUID, from, to string, at time.Time) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	var rows []struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	err := r.d.Conn(ctx).Raw(
		`UPDATE subscriptions SET status = ?, updated_at = ? WHERE id IN ? AND status = ? RETURNING id`,
		to, at, ids, from,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out, nil
}

// CloseRenewalWindows 给一组已过期、窗口没关的订阅写 renewal_closed_at，返回实际改到的 id。
func (r *Repo) CloseRenewalWindows(ctx context.Context, ids []uuid.UUID, at time.Time) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	var rows []struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	err := r.d.Conn(ctx).Raw(
		`UPDATE subscriptions SET renewal_closed_at = ?, updated_at = ? WHERE id IN ? AND status = 'expired' AND renewal_closed_at IS NULL RETURNING id`,
		at, at, ids,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out, nil
}

// RevokedCredential 是关窗时被吊销的一条凭据。
type RevokedCredential struct {
	ID             uuid.UUID `gorm:"column:id"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	UserID         uuid.UUID `gorm:"column:user_id"`
}

// RevokeCredentialsOnClose 吊销一组订阅的有效链接凭据（关窗后旧链接作废），返回被吊销的每条凭据。
func (r *Repo) RevokeCredentialsOnClose(ctx context.Context, subIDs []uuid.UUID, at time.Time, reason string) ([]RevokedCredential, error) {
	if len(subIDs) == 0 {
		return []RevokedCredential{}, nil
	}
	var rows []RevokedCredential
	err := r.d.Conn(ctx).Raw(
		`UPDATE subscription_credentials SET status = 'revoked', revoked_at = ?, revoked_reason = ? WHERE subscription_id IN ? AND status = 'active' RETURNING id, subscription_id, user_id`,
		at, reason, subIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}
