package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/certs/model"
)

// NextVersion 是证书的下一个版本号（max(version) + 1，没有版本时 1）。
func (r *Repo) NextVersion(ctx context.Context, certificateID uuid.UUID) (int, error) {
	var n int
	err := r.d.Conn(ctx).Raw(
		`SELECT coalesce(max(version), 0) + 1 FROM certificate_versions WHERE certificate_id = ?`, certificateID,
	).Scan(&n).Error
	return n, err
}

// InsertVersion 插入一个版本（ID 由调用方生成）。
func (r *Repo) InsertVersion(ctx context.Context, v *model.CertificateVersion) error {
	return mapErr(r.d.Conn(ctx).Create(v).Error)
}

// ApplyIssued 把证书切到新版本并清 ARI、失败计数与 alerted_level；pending 转 active。
func (r *Repo) ApplyIssued(ctx context.Context, certificateID uuid.UUID, u SuccessUpdate) error {
	res := r.d.Conn(ctx).Exec(`
		UPDATE certificates
		   SET current_version_id = ?, not_before = ?, not_after = ?, renew_after = ?,
		       ari_window_start = NULL, ari_window_end = NULL, ari_checked_at = NULL,
		       status = CASE WHEN status = 'pending' THEN 'active' ELSE status END,
		       consecutive_failures = 0, next_attempt_at = NULL, last_error_code = NULL, last_error = NULL,
		       alerted_level = 0, row_version = row_version + 1, updated_at = now()
		 WHERE id = ?`, u.VersionID, u.NotBefore, u.NotAfter, u.RenewAfter, certificateID)
	return mapErr(res.Error)
}

// ApplyFailure 记一次失败；Pause 为真时转 paused（paused_reason = failures）。
func (r *Repo) ApplyFailure(ctx context.Context, certificateID uuid.UUID, u FailureUpdate) error {
	res := r.d.Conn(ctx).Exec(`
		UPDATE certificates
		   SET consecutive_failures = ?, next_attempt_at = now() + make_interval(secs => ?),
		       last_error_code = ?, last_error = ?,
		       status = CASE WHEN ? THEN 'paused' ELSE status END,
		       paused_reason = CASE WHEN ? THEN 'failures' ELSE paused_reason END,
		       row_version = row_version + 1, updated_at = now()
		 WHERE id = ?`,
		u.Failures, u.RetryIn.Seconds(), u.Code, u.Detail, u.Pause, u.Pause, certificateID)
	return mapErr(res.Error)
}

// InsertIssuance 写一行签发流水。
func (r *Repo) InsertIssuance(ctx context.Context, i *model.CertificateIssuance) error {
	return mapErr(r.d.Conn(ctx).Create(i).Error)
}

// IssuancesSince 读 created_at > since 的签发流水。
func (r *Repo) IssuancesSince(ctx context.Context, since time.Time) ([]IssuanceRow, error) {
	var rows []IssuanceRow
	err := r.d.Conn(ctx).Raw(`
		SELECT ca, identifiers::text AS identifiers, is_renewal, created_at
		  FROM certificate_issuances WHERE created_at > ?`, since).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []IssuanceRow{}, nil
	}
	return rows, nil
}

// ARIDue 返回该刷新 ARI 的在用证书与当前版本。
func (r *Repo) ARIDue(ctx context.Context, recheck time.Duration, limit int) ([]ARIDueRow, error) {
	var rows []ARIDueRow
	err := r.d.Conn(ctx).Raw(`
		SELECT c.id AS certificate_id, v.id AS version_id, v.ca, v.chain_pem
		  FROM certificates c
		  JOIN certificate_versions v ON v.id = c.current_version_id
		 WHERE c.status = 'active'
		   AND (c.ari_checked_at IS NULL OR c.ari_checked_at < now() - make_interval(secs => ?))
		 ORDER BY c.ari_checked_at NULLS FIRST, c.id
		 LIMIT ?`, recheck.Seconds(), limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ARIDueRow{}, nil
	}
	return rows, nil
}

// RecordARI 写 ari_checked_at = now()；只在当前版本仍是 versionID 时写。
func (r *Repo) RecordARI(ctx context.Context, certificateID, versionID uuid.UUID, start, end, renewAt *time.Time) error {
	res := r.d.Conn(ctx).Exec(`
		UPDATE certificates
		   SET ari_checked_at = now(),
		       ari_window_start = coalesce(?, ari_window_start),
		       ari_window_end = coalesce(?, ari_window_end),
		       renew_after = coalesce(?, renew_after),
		       updated_at = now()
		 WHERE id = ? AND current_version_id = ?`, start, end, renewAt, certificateID, versionID)
	return res.Error
}

// AlertCandidates 返回有当前版本且 alerted_level < 2 的证书。
func (r *Repo) AlertCandidates(ctx context.Context, limit int) ([]AlertRow, error) {
	var rows []AlertRow
	err := r.d.Conn(ctx).Raw(`
		SELECT id, name, not_before, not_after, alerted_level
		  FROM certificates
		 WHERE current_version_id IS NOT NULL AND alerted_level < 2
		 ORDER BY not_after
		 LIMIT ?`, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []AlertRow{}, nil
	}
	return rows, nil
}

// RaiseAlert 只在 alerted_level < level 时把它改成 level；返回是否改了。
func (r *Repo) RaiseAlert(ctx context.Context, id uuid.UUID, level int) (bool, error) {
	res := r.d.Conn(ctx).Exec(`
		UPDATE certificates SET alerted_level = ?, updated_at = now()
		 WHERE id = ? AND alerted_level < ?`, level, id, level)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
