package certs

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// Version 是证书版本的读形状：链与指纹，没有私钥（连密文也不回）。
type Version struct {
	ID              string    `json:"id"`
	Version         int       `json:"version"`
	CA              string    `json:"ca"`
	Identifiers     []string  `json:"identifiers"`
	IsRenewal       bool      `json:"is_renewal"`
	Serial          string    `json:"serial"`
	NotBefore       time.Time `json:"not_before"`
	NotAfter        time.Time `json:"not_after"`
	ChainSHA256     string    `json:"chain_sha256"`
	PublicKeySHA256 string    `json:"public_key_sha256"`
	Current         bool      `json:"current"`
	CreatedAt       time.Time `json:"created_at"`
}

// Order 是签发订单的读形状（详情页的订单历史）。
type Order struct {
	ID          string     `json:"id"`
	Reason      string     `json:"reason"`
	State       string     `json:"state"`
	CA          *string    `json:"ca"`
	Replaces    bool       `json:"replaces"`
	Attempt     int        `json:"attempt"`
	ErrorCode   *string    `json:"error_code"`
	ErrorDetail *string    `json:"error_detail"`
	Version     *int       `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// CertificateDetail 是详情接口的回包：证书、全部版本（新的在前）、最近 50 张订单。
type CertificateDetail struct {
	Certificate Certificate `json:"certificate"`
	Versions    []Version   `json:"versions"`
	Orders      []Order     `json:"orders"`
}

// CertificateDetail 读一张证书的详情。
func (s *Service) CertificateDetail(ctx context.Context, tenantID, id string) (CertificateDetail, error) {
	if err := validID(id); err != nil {
		return CertificateDetail{}, err
	}
	out := CertificateDetail{Versions: []Version{}, Orders: []Order{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		if out.Certificate, err = s.getCertificate(ctx, tx, tenantID, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT v.id::text, v.version, v.ca, v.identifiers, v.is_renewal, v.serial, v.not_before, v.not_after,
			       v.chain_sha256, v.public_key_sha256, v.id = c.current_version_id, v.created_at
			  FROM certificate_versions v
			  JOIN certificates c ON c.tenant_id = v.tenant_id AND c.id = v.certificate_id
			 WHERE v.tenant_id = $1 AND v.certificate_id = $2::uuid
			 ORDER BY v.version DESC`, tenantID, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v Version
			var chain, pub []byte
			var current *bool
			if err := rows.Scan(&v.ID, &v.Version, &v.CA, &v.Identifiers, &v.IsRenewal, &v.Serial, &v.NotBefore,
				&v.NotAfter, &chain, &pub, &current, &v.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			v.ChainSHA256, v.PublicKeySHA256 = hex.EncodeToString(chain), hex.EncodeToString(pub)
			v.Current = current != nil && *current
			out.Versions = append(out.Versions, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `
			SELECT o.id::text, o.reason, o.state, o.ca, o.replaces_ari_id IS NOT NULL, o.attempt, o.error_code,
			       o.error_detail, v.version, o.created_at, o.started_at, o.finished_at
			  FROM certificate_orders o
			  LEFT JOIN certificate_versions v ON v.tenant_id = o.tenant_id AND v.id = o.version_id
			 WHERE o.tenant_id = $1 AND o.certificate_id = $2::uuid
			 ORDER BY o.created_at DESC, o.id DESC LIMIT 50`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o Order
			if err := rows.Scan(&o.ID, &o.Reason, &o.State, &o.CA, &o.Replaces, &o.Attempt, &o.ErrorCode,
				&o.ErrorDetail, &o.Version, &o.CreatedAt, &o.StartedAt, &o.FinishedAt); err != nil {
				return err
			}
			out.Orders = append(out.Orders, o)
		}
		return rows.Err()
	})
	return out, err
}
