package certs

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/go-acme/lego/v4/acme"
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
			 WHERE tenant_id = $1 AND directory_url = $2 AND contact_email = $3
			   AND coalesce(eab_kid, '') = $4 AND status = 'valid'`,
			tenantID, target.Directory, email, target.eabKID).Scan(&id, &url, &sealed)
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

// ensureAccount 取（目录 + 联系邮箱 + EAB KID）对应的有效账号，没有就在 CA 注册一个并落库。两个 worker
// 同时注册时唯一键（00152：只管 status='valid' 的行）只留先写的那个，后写的用库里那个（它在 CA 注册的
// 账号闲置，无害）；连库里那个也读不到时报错，不返回空账号。
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
			ON CONFLICT (tenant_id, directory_url, contact_email, (coalesce(eab_kid, ''))) WHERE status = 'valid'
			DO NOTHING`,
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
		acct, err := s.loadAccount(ctx, tenantID, target, email)
		if err == nil && acct == nil {
			err = errors.New("ACME 账号刚注册却在库里找不到有效的那一行")
		}
		return acct, err
	}
	return &acmeAccount{id: id, key: key, url: url}, nil
}

// accountGone 判断 CA 是不是说这个账号已经不能用了：accountDoesNotExist，或 unauthorized 且明说账号
// 失效、不存在（验证失败、replaces 指向别的账号签的证书也是 unauthorized，那些不算，否则会白白重注册）。
func accountGone(err error) bool {
	var prob *acme.ProblemDetails
	if !errors.As(err, &prob) {
		return false
	}
	switch prob.Type {
	case "urn:ietf:params:acme:error:accountDoesNotExist":
		return true
	case "urn:ietf:params:acme:error:unauthorized":
		if len(prob.SubProblems) > 0 {
			return false
		}
		d := strings.ToLower(prob.Detail)
		for _, hint := range []string{"deactivated", "account is not valid", "account does not exist", "account not found", "revoked"} {
			if strings.Contains(d, hint) {
				return true
			}
		}
		return false
	}
	return false
}

// deactivateAccount 把账号标失效；下一单 ensureAccount 找不到有效账号就重新注册。
func (s *Service) deactivateAccount(ctx context.Context, tenantID, id string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE acme_accounts SET status = 'deactivated'
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'valid'`, tenantID, id)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system", Action: "acme_account.deactivated", ResourceType: "acme_account", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
		})
	})
}
