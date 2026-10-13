package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/certs/model"
)

const certificateColumns = `c.id, c.name, c.identifiers::text AS identifiers, c.challenge, c.dns_credential_id,
	d.name AS dns_credential_name, d.provider AS dns_provider,
	c.key_type, c.status, c.paused_reason, v.version AS current_version, v.ca AS current_ca,
	c.not_before, c.not_after, c.renew_after, c.ari_window_start, c.ari_window_end, c.next_attempt_at,
	c.consecutive_failures, c.last_error_code, c.last_error,
	o.id AS order_id, o.state AS order_state, o.reason AS order_reason, o.created_at AS order_created_at,
	c.row_version, c.created_at, c.updated_at`

const certificateFrom = ` FROM certificates c
	JOIN dns_credentials d ON d.id = c.dns_credential_id
	LEFT JOIN certificate_versions v ON v.id = c.current_version_id
	LEFT JOIN certificate_orders o ON o.certificate_id = c.id AND o.state IN ('queued', 'running')`

// ListCertificates 按 lower(name), id 排序列出全部证书。
func (r *Repo) ListCertificates(ctx context.Context) ([]CertificateRow, error) {
	var rows []CertificateRow
	err := r.d.Conn(ctx).Raw(`SELECT `+certificateColumns+certificateFrom+` ORDER BY lower(c.name), c.id`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []CertificateRow{}, nil
	}
	return rows, nil
}

// GetCertificate 读一张证书的读形状。
func (r *Repo) GetCertificate(ctx context.Context, id uuid.UUID) (CertificateRow, bool, error) {
	var row CertificateRow
	err := r.d.Conn(ctx).Raw(`SELECT `+certificateColumns+certificateFrom+` WHERE c.id = ?`, id).Scan(&row).Error
	if err != nil {
		return CertificateRow{}, false, err
	}
	if row.ID == uuid.Nil {
		return CertificateRow{}, false, nil
	}
	return row, true, nil
}

// LockCertificate 以 FOR UPDATE 锁住证书行并返回它。
func (r *Repo) LockCertificate(ctx context.Context, id uuid.UUID) (model.Certificate, bool, error) {
	var c model.Certificate
	err := r.d.Conn(ctx).Raw(`SELECT * FROM certificates WHERE id = ? FOR UPDATE`, id).Scan(&c).Error
	if err != nil {
		return model.Certificate{}, false, err
	}
	if c.ID == uuid.Nil {
		return model.Certificate{}, false, nil
	}
	return c, true, nil
}

// InsertCertificate 插入证书；重名回 ErrDuplicate。
func (r *Repo) InsertCertificate(ctx context.Context, c *model.Certificate) error {
	return mapErr(r.d.Conn(ctx).Create(c).Error)
}

// UpdateCertificateSpec 改名、标识、凭据、密钥类型并 row_version + 1；重名回 ErrDuplicate。
func (r *Repo) UpdateCertificateSpec(ctx context.Context, id uuid.UUID, s CertificateSpec) error {
	res := r.d.Conn(ctx).Exec(`UPDATE certificates
	   SET name = ?, identifiers = ?::jsonb, dns_credential_id = ?, key_type = ?,
	       status = CASE WHEN ? AND status = 'blocked_credential'
	                     THEN CASE WHEN current_version_id IS NULL THEN 'pending' ELSE 'active' END
	                     ELSE status END,
	       next_attempt_at = CASE WHEN ? OR ? THEN NULL ELSE next_attempt_at END,
	       row_version = row_version + 1, updated_at = now()
	 WHERE id = ?`,
		s.Name, s.Identifiers, s.DNSCredentialID, s.KeyType,
		s.CredentialChanged, s.Reissue, s.CredentialChanged, id,
	)
	return mapErr(res.Error)
}

// SetCertificateStatus 把状态从 from 改成 to；resetBackoff 为真时清 consecutive_failures 与 next_attempt_at。
func (r *Repo) SetCertificateStatus(ctx context.Context, id uuid.UUID, from, to string, reason *string, resetBackoff bool) (bool, error) {
	var pausedReason any
	if to == "paused" {
		pausedReason = reason
	}
	res := r.d.Conn(ctx).Exec(`UPDATE certificates
	   SET status = ?, paused_reason = ?,
	       consecutive_failures = CASE WHEN ? THEN 0 ELSE consecutive_failures END,
	       next_attempt_at = CASE WHEN ? THEN NULL ELSE next_attempt_at END,
	       row_version = row_version + 1, updated_at = now()
	 WHERE id = ? AND status = ?`,
		to, pausedReason, resetBackoff, resetBackoff, id, from,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// DeleteCertificate 删除证书与它的全部订单、版本。
func (r *Repo) DeleteCertificate(ctx context.Context, id uuid.UUID) error {
	conn := r.d.Conn(ctx)
	for _, q := range []string{
		`UPDATE certificates SET current_version_id = NULL, not_before = NULL, not_after = NULL,
		        status = CASE WHEN status = 'active' THEN 'pending' ELSE status END, updated_at = now()
		  WHERE id = ?`,
		`DELETE FROM certificate_orders WHERE certificate_id = ?`,
		`DELETE FROM certificate_versions WHERE certificate_id = ?`,
		`DELETE FROM certificates WHERE id = ?`,
	} {
		if err := mapErr(conn.Exec(q, id).Error); err != nil {
			return err
		}
	}
	return nil
}

// ListVersions 按版本号降序列出证书的全部版本。
func (r *Repo) ListVersions(ctx context.Context, certificateID uuid.UUID) ([]VersionRow, error) {
	var rows []VersionRow
	err := r.d.Conn(ctx).Raw(`SELECT v.id, v.version, v.ca, v.identifiers::text AS identifiers, v.is_renewal, v.serial,
	       v.not_before, v.not_after, v.chain_sha256, v.public_key_sha256,
	       coalesce(v.id = c.current_version_id, false) AS current, v.created_at
	  FROM certificate_versions v
	  JOIN certificates c ON c.id = v.certificate_id
	 WHERE v.certificate_id = ?
	 ORDER BY v.version DESC`, certificateID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []VersionRow{}, nil
	}
	return rows, nil
}

// ListOrders 按 created_at DESC, id DESC 列出证书最近 limit 张订单。
func (r *Repo) ListOrders(ctx context.Context, certificateID uuid.UUID, limit int) ([]OrderRow, error) {
	var rows []OrderRow
	err := r.d.Conn(ctx).Raw(`SELECT o.id, o.reason, o.state, o.ca, (o.replaces_ari_id IS NOT NULL) AS replaces, o.attempt,
	       o.error_code, o.error_detail, v.version, o.created_at, o.started_at, o.finished_at
	  FROM certificate_orders o
	  LEFT JOIN certificate_versions v ON v.id = o.version_id
	 WHERE o.certificate_id = ?
	 ORDER BY o.created_at DESC, o.id DESC LIMIT ?`, certificateID, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []OrderRow{}, nil
	}
	return rows, nil
}
