package repo

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// ExpireOverdueRows 找到期时间早于 before 仍是 active 的订阅（到期扫描该改 expired 而没改）。
// 走 subscriptions_active_end_idx（current_period_end，WHERE status = 'active'）的范围扫描。样例是订阅 id。
func (r *Repo) ExpireOverdueRows(ctx context.Context, before time.Time) ([]integrity.Row, error) {
	var out []integrity.Row
	err := r.d.Conn(ctx).Raw(`
		SELECT id::text AS id, count(*) OVER () AS n
		  FROM subscriptions
		 WHERE status = 'active' AND current_period_end < ?
		 ORDER BY id DESC
		 LIMIT ?`, before, integrity.SampleLimit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// CloseOverdueRows 找已过期、续费窗口没关、到期时间早于 before 的订阅（到期扫描该写 renewal_closed_at、吊销凭据而没做）。
// 走 subscriptions_expired_open_idx（current_period_end，WHERE status = 'expired' AND renewal_closed_at IS NULL）。样例是订阅 id。
func (r *Repo) CloseOverdueRows(ctx context.Context, before time.Time) ([]integrity.Row, error) {
	var out []integrity.Row
	err := r.d.Conn(ctx).Raw(`
		SELECT id::text AS id, count(*) OVER () AS n
		  FROM subscriptions
		 WHERE status = 'expired' AND renewal_closed_at IS NULL AND current_period_end < ?
		 ORDER BY id DESC
		 LIMIT ?`, before, integrity.SampleLimit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
