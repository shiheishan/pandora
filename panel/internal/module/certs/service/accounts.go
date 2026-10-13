package service

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/certs/model"
	"github.com/go-acme/lego/v4/acme"
	"github.com/google/uuid"
)

// acmeAccount 是解开的 ACME 账号。
type acmeAccount struct {
	id  uuid.UUID
	key crypto.PrivateKey
	url string
}

// loadAccount 读（目录 + 联系邮箱 + EAB KID）对应的有效账号并解开私钥；没有回 nil。
func (s *Service) loadAccount(ctx context.Context, target caTarget, email string) (*acmeAccount, error) {
	row, ok, err := s.store.ValidAccount(ctx, target.Directory, email, target.eabKID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	der, err := s.sealer.Open(row.AccountKeySealed, accountAAD(row.ID))
	if err != nil {
		return nil, fmt.Errorf("open acme account key: %w", err)
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	return &acmeAccount{id: row.ID, key: key, url: row.AccountURL}, nil
}

// ensureAccount 取有效账号，没有就在 CA 注册并落库。两个任务同时注册时唯一键（只管 valid 的行）只留先写的
// 那个，后写的用库里那个（它在 CA 注册的账号闲置，无害）。
func (s *Service) ensureAccount(ctx context.Context, target caTarget, email string) (*acmeAccount, error) {
	if acct, err := s.loadAccount(ctx, target, email); err != nil || acct != nil {
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
	id := uuid.Must(uuid.NewV7())
	sealed, err := s.sealer.Seal(der, accountAAD(id))
	if err != nil {
		return nil, err
	}
	var eabKID *string
	if target.eabKID != "" {
		eabKID = &target.eabKID
	}
	inserted := false
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.store.InsertAccountIfAbsent(ctx, &model.ACMEAccount{
			ID: id, CA: target.CA, DirectoryURL: target.Directory, ContactEmail: email,
			AccountKeySealed: sealed, AccountURL: url, EABKID: eabKID,
		})
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		inserted = true
		rid := id
		return s.writeAudit(ctx, "acme_account.created", "acme_account", &rid, "", map[string]any{
			"ca": target.CA, "directory_url": target.Directory, "contact_email": email, "account_url": url,
		})
	})
	if err != nil {
		return nil, err
	}
	if !inserted {
		acct, err := s.loadAccount(ctx, target, email)
		if err == nil && acct == nil {
			err = errors.New("ACME 账号刚注册却在库里找不到有效的那一行")
		}
		return acct, err
	}
	return &acmeAccount{id: id, key: key, url: url}, nil
}

// accountGone 判断 CA 是否表示账号已不可用。
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
func (s *Service) deactivateAccount(ctx context.Context, id uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		changed, err := s.store.DeactivateAccount(ctx, id)
		if err != nil || !changed {
			return err
		}
		rid := id
		return s.writeAudit(ctx, "acme_account.deactivated", "acme_account", &rid, auditsvc.OutcomeSuccess, nil)
	})
}
