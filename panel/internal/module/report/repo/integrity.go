package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/report/model"
)

// LockIntegrityCheck 按代号读一项巡检结果并加行锁（FOR UPDATE）；还没有行时回 false。
// 巡检任务 asynq Unique 防叠跑，行锁只防手动重跑与它并发；首次写入的竞态由 SaveIntegrityCheck 的 upsert 兜底。
func (r *Repo) LockIntegrityCheck(ctx context.Context, code string) (model.IntegrityCheck, bool, error) {
	var out []model.IntegrityCheck
	err := r.d.Conn(ctx).Raw(`
		SELECT code, count, sample::text AS sample, checked_at, failing_since, alerted_count
		  FROM integrity_checks WHERE code = ? FOR UPDATE`, code).Scan(&out).Error
	if err != nil {
		return model.IntegrityCheck{}, false, mapErr(err)
	}
	if len(out) == 0 {
		return model.IntegrityCheck{}, false, nil
	}
	return out[0], true, nil
}

// SaveIntegrityCheck 按代号覆盖写一项巡检结果。
func (r *Repo) SaveIntegrityCheck(ctx context.Context, c model.IntegrityCheck) error {
	return mapErr(r.d.Conn(ctx).Exec(`
		INSERT INTO integrity_checks (code, count, sample, checked_at, failing_since, alerted_count)
		VALUES (?, ?, CAST(? AS jsonb), ?, ?, ?)
		ON CONFLICT (code) DO UPDATE SET
		  count = EXCLUDED.count,
		  sample = EXCLUDED.sample,
		  checked_at = EXCLUDED.checked_at,
		  failing_since = EXCLUDED.failing_since,
		  alerted_count = EXCLUDED.alerted_count`,
		c.Code, c.Count, c.Sample, c.CheckedAt, c.FailingSince, c.AlertedCount).Error)
}

// IntegrityChecks 读全部巡检结果（每项一行，几行），按代号排。
func (r *Repo) IntegrityChecks(ctx context.Context) ([]model.IntegrityCheck, error) {
	var out []model.IntegrityCheck
	err := r.d.Conn(ctx).Raw(`
		SELECT code, count, sample::text AS sample, checked_at, failing_since, alerted_count
		  FROM integrity_checks ORDER BY code`).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
