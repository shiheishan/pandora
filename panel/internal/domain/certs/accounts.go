package certs

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// acmeAccount 是解开的 ACME 账号。
type acmeAccount struct {
	id  string
	key crypto.PrivateKey
	url string
}

func accountAAD(tenantID, id string) []byte {
	return []byte("acme_account:" + tenantID + ":" + id)
}

func (s *Service) loadAccount(ctx context.Context, tenantID string, target caTarget, email string) (*acmeAccount, error) {
	var id, url string
	var sealed []byte
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text, account_url, account_key_sealed FROM acme_accounts
			 WHERE tenant_id = $1 AND directory_url = $2 AND contact_email = $3 AND status = 'valid'`,
			tenantID, target.Directory, email).Scan(&id, &url, &sealed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	der, err := s.sealer.Open(sealed, accountAAD(tenantID, id))
	if err != nil {
		return nil, fmt.Errorf("open acme account key: %w", err)
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	return &acmeAccount{id: id, key: key, url: url}, nil
}

// ensureAccount 取（目录 + 联系邮箱）对应的账号，没有就在 CA 注册一个并落库。两个 worker 同时注册时
// 唯一约束只留先写的那个，后写的用库里那个（它在 CA 注册的账号闲置，无害）。
func (s *Service) ensureAccount(ctx context.Context, tenantID string, target caTarget, email string) (*acmeAccount, error) {
	if acct, err := s.loadAccount(ctx, tenantID, target, email); err != nil || acct != nil {
		return acct, err
	}
	key, url, err := s.registerAccount(target, email)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	id := uuid.Must(uuid.NewV7()).String()
	sealed, err := s.sealer.Seal(der, accountAAD(tenantID, id))
	if err != nil {
		return nil, err
	}
	inserted := false
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO acme_accounts (id, tenant_id, ca, directory_url, contact_email, account_key_sealed, account_url, eab_kid)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, nullif($8, ''))
			ON CONFLICT (tenant_id, directory_url, contact_email) DO NOTHING`,
			id, tenantID, target.CA, target.Directory, email, sealed, url, target.eabKID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		inserted = true
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system", Action: "acme_account.created", ResourceType: "acme_account", ResourceID: &id,
			APIDomain: "admin", AfterDigest: map[string]any{"ca": target.CA, "directory_url": target.Directory,
				"contact_email": email, "account_url": url},
		})
	})
	if err != nil {
		return nil, err
	}
	if !inserted {
		return s.loadAccount(ctx, tenantID, target, email)
	}
	return &acmeAccount{id: id, key: key, url: url}, nil
}
