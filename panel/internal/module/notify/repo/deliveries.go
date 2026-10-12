package repo

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/google/uuid"
)

// maxErrorRunes 与表上 error_message 的 CHECK 一致。
const maxErrorRunes = 500

// InsertDelivery 写一条投递（ID 由调用方生成）。
func (r *Repo) InsertDelivery(ctx context.Context, d *model.Delivery) error {
	return r.d.Conn(ctx).Create(d).Error
}

// ClaimDeliveries 认领到期的 queued 行：next_retry_at 推到 now()+lease，租约内别的实例看不到它们；按 created_at 升序返回。
func (r *Repo) ClaimDeliveries(ctx context.Context, channel string, limit int, lease time.Duration) ([]model.Delivery, error) {
	out := []model.Delivery{}
	err := r.d.Conn(ctx).Raw(
		`UPDATE notification_deliveries d
		    SET next_retry_at = now() + make_interval(secs => ?), updated_at = now()
		   FROM (SELECT id FROM notification_deliveries
		          WHERE status = 'queued' AND channel = ? AND next_retry_at <= now()
		          ORDER BY created_at
		          LIMIT ?
		          FOR UPDATE SKIP LOCKED) due
		  WHERE d.id = due.id
		RETURNING d.*`, lease.Seconds(), channel, limit).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	// RETURNING 不保证顺序
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// MarkSent 置为 sent 并清空封存内容；行已不是 queued 时返回 false。
func (r *Repo) MarkSent(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE notification_deliveries
		    SET status = 'sent', sent_at = now(), attempts = attempts + 1,
		        error_message = NULL, payload_enc = NULL, updated_at = now()
		  WHERE id = ? AND status = 'queued'`, id)
	return res.RowsAffected == 1, res.Error
}

// MarkRetry 记一次失败，delay 之后可再认领；仍为 queued。
func (r *Repo) MarkRetry(ctx context.Context, id uuid.UUID, delay time.Duration, msg string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE notification_deliveries
		    SET attempts = attempts + 1, error_message = ?,
		        next_retry_at = now() + make_interval(secs => ?), updated_at = now()
		  WHERE id = ? AND status = 'queued'`, truncateRunes(msg, maxErrorRunes), delay.Seconds(), id)
	return res.RowsAffected == 1, res.Error
}

// MarkFinal 置为终态 failed / suppressed 并清空封存内容；countAttempt 为真时 attempts 加 1。
func (r *Repo) MarkFinal(ctx context.Context, id uuid.UUID, status, msg string, countAttempt bool) (bool, error) {
	if status != model.StatusFailed && status != model.StatusSuppressed {
		return false, fmt.Errorf("notify repo: 终态只能是 failed 或 suppressed，收到 %q", status)
	}
	inc := 0
	if countAttempt {
		inc = 1
	}
	res := r.d.Conn(ctx).Exec(
		`UPDATE notification_deliveries
		    SET status = ?, error_message = ?, payload_enc = NULL,
		        attempts = attempts + ?, updated_at = now()
		  WHERE id = ? AND status = 'queued'`, status, truncateRunes(msg, maxErrorRunes), inc, id)
	return res.RowsAffected == 1, res.Error
}

// PurgeDeliveries 删 created_at < before 的投递，每次至多 batch 行，返回删除行数。
func (r *Repo) PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		`DELETE FROM notification_deliveries WHERE id IN (
		   SELECT id FROM notification_deliveries WHERE created_at < ? ORDER BY created_at LIMIT ?)`,
		before, batch)
	return int(res.RowsAffected), res.Error
}

// truncateRunes 按字符截断到 n。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
