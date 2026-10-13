package repo

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/aegispanel/aegis/internal/module/plugin/model"
	"github.com/google/uuid"
)

// EnqueueEvent 为订阅该事件的启用钩子各插一条 queued 投递，去重冲突忽略。
func (r *Repo) EnqueueEvent(ctx context.Context, event, dedupeKey, payload string) (int64, error) {
	res := r.d.Conn(ctx).Exec(
		`INSERT INTO plugin_hook_deliveries (hook_id, event, dedupe_key, payload, max_attempts)
		 SELECT h.id, ?, ?, ?::jsonb, h.max_attempts
		   FROM plugin_hooks h
		  WHERE h.enabled AND h.events @> jsonb_build_array(?::text)
		 ON CONFLICT (hook_id, dedupe_key) DO NOTHING`,
		event, dedupeKey, payload, event)
	return res.RowsAffected, res.Error
}

// ClaimDue 认领到期的 queued 投递（钩子仍启用），并延长租约、增加 attempts。
func (r *Repo) ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]model.Delivery, error) {
	out := []model.Delivery{}
	err := r.d.Conn(ctx).Raw(
		`UPDATE plugin_hook_deliveries d
		    SET next_retry_at = now() + make_interval(secs => ?), attempts = d.attempts + 1, updated_at = now()
		   FROM (SELECT d2.id
		           FROM plugin_hook_deliveries d2
		           JOIN plugin_hooks h ON h.id = d2.hook_id
		          WHERE d2.status = 'queued' AND d2.next_retry_at <= now() AND d2.attempts < d2.max_attempts AND h.enabled
		          ORDER BY d2.next_retry_at, d2.id
		          LIMIT ?
		          FOR UPDATE OF d2 SKIP LOCKED) due
		  WHERE d.id = due.id
		RETURNING d.*`, lease.Seconds(), limit).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return bytes.Compare(out[i].ID[:], out[j].ID[:]) < 0
	})
	return out, nil
}

// FailExhausted 把尝试次数用尽且租约已过的 queued 行置为 failed。
func (r *Repo) FailExhausted(ctx context.Context) (int, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE plugin_hook_deliveries
		    SET status = 'failed', next_retry_at = NULL, error_message = '多次处理中断', updated_at = now()
		  WHERE status = 'queued' AND attempts >= max_attempts AND next_retry_at <= now()`)
	return int(res.RowsAffected), res.Error
}

// MarkSent 将 queued 行标记为已送达。
func (r *Repo) MarkSent(ctx context.Context, id uuid.UUID, code int, durationMS *int) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE plugin_hook_deliveries
		    SET status = 'sent', sent_at = now(), next_retry_at = NULL, response_code = ?, error_message = '', last_duration_ms = ?, updated_at = now()
		  WHERE id = ? AND status = 'queued'`, code, durationMS, id)
	return res.RowsAffected == 1, res.Error
}

// MarkRetry 仍为 queued，按 delay 推迟下次认领并记录失败信息。
func (r *Repo) MarkRetry(ctx context.Context, id uuid.UUID, delay time.Duration, code *int, msg string, durationMS *int) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE plugin_hook_deliveries
		    SET next_retry_at = now() + make_interval(secs => ?), response_code = ?, error_message = ?, last_duration_ms = ?, updated_at = now()
		  WHERE id = ? AND status = 'queued'`,
		delay.Seconds(), code, truncateRunes(msg, maxErrorRunes), durationMS, id)
	return res.RowsAffected == 1, res.Error
}

// MarkFailed 将 queued 行标记为终态 failed。
func (r *Repo) MarkFailed(ctx context.Context, id uuid.UUID, code *int, msg string, durationMS *int) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE plugin_hook_deliveries
		    SET status = 'failed', next_retry_at = NULL, response_code = ?, error_message = ?, last_duration_ms = ?, updated_at = now()
		  WHERE id = ? AND status = 'queued'`, code, truncateRunes(msg, maxErrorRunes), durationMS, id)
	return res.RowsAffected == 1, res.Error
}

// DeliveriesByHook 按钩子倒序取投递记录。
func (r *Repo) DeliveriesByHook(ctx context.Context, hookID uuid.UUID, limit int) ([]model.Delivery, error) {
	var rows []model.Delivery
	err := r.d.Conn(ctx).Where("hook_id = ?", hookID).Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Delivery{}, nil
	}
	return rows, nil
}

// PurgeDeliveries 批量删除早于 before 的投递记录，不看状态。
//
// 90 天前仍是 queued 的行只会出现在钩子停用之后（启用的钩子最多重试十几小时就会 sent 或 failed），
// 一并删掉：钩子停用期间的旧事件重新启用后也不该再补发。去重不受影响：去重键都带业务 id
// （订单、订阅、工单、礼品卡码等）或业务时刻，发事件的一方本身也只在状态变化时发一次
// （订单状态机、notify 扫描自己的通知记录），同一个业务事件不会在 90 天后再次 Emit，删掉旧行不会让它重投。
func (r *Repo) PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		`DELETE FROM plugin_hook_deliveries WHERE id IN (
		   SELECT id FROM plugin_hook_deliveries WHERE created_at < ? ORDER BY created_at LIMIT ?)`,
		before, batch)
	return int(res.RowsAffected), res.Error
}
