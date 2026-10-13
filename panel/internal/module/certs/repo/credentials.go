package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/certs/model"
)

const credentialSelect = `c.id, c.name, c.provider, c.zone, c.secret_hint, c.verify_status, c.verified_at,
	c.verify_error, c.visible_zones, c.row_version, c.created_at, c.updated_at,
	(SELECT count(*)::int FROM certificates x WHERE x.dns_credential_id = c.id) AS certificate_count`

// ListCredentials 按 lower(name), id 排序列出全部凭据。
func (r *Repo) ListCredentials(ctx context.Context) ([]CredentialRow, error) {
	var rows []CredentialRow
	err := r.d.Conn(ctx).Raw(`SELECT ` + credentialSelect + ` FROM dns_credentials c ORDER BY lower(c.name), c.id`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []CredentialRow{}, nil
	}
	return rows, nil
}

// GetCredential 读一个凭据；lock 为真时 FOR UPDATE 锁住凭据行。
func (r *Repo) GetCredential(ctx context.Context, id uuid.UUID, lock bool) (CredentialRow, bool, error) {
	var row CredentialRow
	q := `SELECT ` + credentialSelect + ` FROM dns_credentials c WHERE c.id = ?`
	if lock {
		q += ` FOR UPDATE OF c`
	}
	err := r.d.Conn(ctx).Raw(q, id).Scan(&row).Error
	if err != nil {
		return CredentialRow{}, false, err
	}
	if row.ID == uuid.Nil {
		return CredentialRow{}, false, nil
	}
	return row, true, nil
}

// CredentialSecret 读凭据的封存密文。
func (r *Repo) CredentialSecret(ctx context.Context, id uuid.UUID) ([]byte, bool, error) {
	// 扫进结构体：GORM 的 Scan 不认 *[]byte，会把它当多行切片
	var row struct {
		Sealed []byte `gorm:"column:secret_sealed"`
	}
	res := r.d.Conn(ctx).Raw(`SELECT secret_sealed FROM dns_credentials WHERE id = ?`, id).Scan(&row)
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, false, nil
	}
	return row.Sealed, true, nil
}

// CredentialExists 报告凭据是否存在。
func (r *Repo) CredentialExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var ok bool
	err := r.d.Conn(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM dns_credentials WHERE id = ?)`, id).Scan(&ok).Error
	return ok, err
}

// InsertCredential 插入凭据；重名回 ErrDuplicate。
func (r *Repo) InsertCredential(ctx context.Context, c *model.DNSCredential) error {
	return mapErr(r.d.Conn(ctx).Create(c).Error)
}

// UpdateCredential 改名、改 zone、按需换密钥并 row_version + 1；重名回 ErrDuplicate。
func (r *Repo) UpdateCredential(ctx context.Context, id uuid.UUID, u CredentialUpdate) error {
	// 密文参数不紧跟左括号：GORM 会把左括号后的 []byte 展开成逐字节的列表
	res := r.d.Conn(ctx).Exec(`UPDATE dns_credentials
	   SET name = ?, zone = ?,
	       secret_sealed = CASE WHEN ? THEN ?::bytea ELSE secret_sealed END, secret_hint = ?,
	       verify_status = CASE WHEN ? THEN 'unverified' ELSE verify_status END,
	       verified_at = CASE WHEN ? THEN NULL ELSE verified_at END,
	       verify_error = CASE WHEN ? THEN NULL ELSE verify_error END,
	       visible_zones = CASE WHEN ? THEN NULL ELSE visible_zones END,
	       row_version = row_version + 1, updated_at = now()
	 WHERE id = ?`,
		u.Name, u.Zone, u.SecretSealed != nil, u.SecretSealed, u.SecretHint,
		u.Reverify, u.Reverify, u.Reverify, u.Reverify, id,
	)
	return mapErr(res.Error)
}

// DeleteCredential 删除凭据。
func (r *Repo) DeleteCredential(ctx context.Context, id uuid.UUID) error {
	res := r.d.Conn(ctx).Exec(`DELETE FROM dns_credentials WHERE id = ?`, id)
	return mapErr(res.Error)
}

// CertificateNamesByCredential 返回引用该凭据的证书名（按 lower(name) 排序，最多 limit 个）。
func (r *Repo) CertificateNamesByCredential(ctx context.Context, id uuid.UUID, limit int) ([]string, error) {
	var names []string
	err := r.d.Conn(ctx).Raw(
		`SELECT name FROM certificates WHERE dns_credential_id = ? ORDER BY lower(name) LIMIT ?`, id, limit,
	).Scan(&names).Error
	if err != nil {
		return nil, err
	}
	if names == nil {
		return []string{}, nil
	}
	return names, nil
}

// RecordCredentialVerdict 只在 row_version 仍等于 rowVersion 时写校验结论；返回是否写入。
func (r *Repo) RecordCredentialVerdict(ctx context.Context, id uuid.UUID, rowVersion int64, v CredentialVerdict) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE dns_credentials
	   SET verify_status = ?, verified_at = CASE WHEN ? = 'ok' THEN now() ELSE verified_at END,
	       verify_error = ?, visible_zones = coalesce(?, visible_zones), updated_at = now()
	 WHERE id = ? AND row_version = ?`,
		v.Status, v.Status, v.VerifyError, v.VisibleZones, id, rowVersion,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// BlockCertificatesByCredential 把引用该凭据、处于 pending / active 的证书转 blocked_credential。
func (r *Repo) BlockCertificatesByCredential(ctx context.Context, credentialID uuid.UUID) error {
	res := r.d.Conn(ctx).Exec(`UPDATE certificates SET status = 'blocked_credential', row_version = row_version + 1, updated_at = now()
	 WHERE dns_credential_id = ? AND status IN ('pending', 'active')`, credentialID)
	return mapErr(res.Error)
}

// ResumeCertificatesByCredential 把引用该凭据、停在 blocked_credential 的证书恢复；返回恢复的张数。
func (r *Repo) ResumeCertificatesByCredential(ctx context.Context, credentialID uuid.UUID) (int, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE certificates
	   SET status = CASE WHEN current_version_id IS NULL THEN 'pending' ELSE 'active' END,
	       next_attempt_at = NULL, row_version = row_version + 1, updated_at = now()
	 WHERE dns_credential_id = ? AND status = 'blocked_credential'`, credentialID)
	if res.Error != nil {
		return 0, mapErr(res.Error)
	}
	return int(res.RowsAffected), nil
}
