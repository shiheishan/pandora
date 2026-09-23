package identity

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const (
	RegistrationModeClosed     = "closed"
	RegistrationModeInviteOnly = "invite_only"
	RegistrationModeOpen       = "open"
)

var ErrRegistrationUnavailable = httpx.New(httpx.CodeForbidden,
	"注册当前不可用或邀请码无效")

type RegistrationPolicy struct {
	Mode              string
	EmailVerification bool
}

func loadRegistrationMode(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	// feature_switches.auth.registration 是紧急总开关。缺行、关闭或尚未配置
	// 都按关闭处理，避免部署/迁移顺序或读到不完整配置时意外开放公网注册。
	var enabled bool
	err := tx.QueryRow(ctx, `
		SELECT enabled
		  FROM feature_switches
		 WHERE tenant_id = $1 AND code = 'auth.registration'
		 FOR SHARE`, tenantID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return RegistrationModeClosed, nil
	}
	if err != nil {
		return "", err
	}

	var mode string
	err = tx.QueryRow(ctx, `
		SELECT value #>> '{}'
		  FROM system_settings
		 WHERE tenant_id = $1 AND key = 'auth.registration_mode'
		 FOR SHARE`, tenantID).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return RegistrationModeClosed, nil
	}
	if err != nil {
		return "", err
	}
	switch mode {
	case RegistrationModeClosed, RegistrationModeInviteOnly, RegistrationModeOpen:
		return mode, nil
	default:
		return "", errors.New("identity: invalid auth.registration_mode")
	}
}

func resolveAvailableInvite(ctx context.Context, tx pgx.Tx, tenantID, code string) (string, error) {
	code = strings.TrimSpace(strings.ToUpper(code))
	if code == "" {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text
		  FROM invite_codes
		 WHERE tenant_id = $1
		   AND upper(code) = $2
		   AND status = 'active'
		   AND (expires_at IS NULL OR expires_at > now())
		   AND (max_uses IS NULL OR used_count < max_uses)
		 FOR SHARE`, tenantID, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func enforceRegistrationStartPolicy(
	ctx context.Context, tx pgx.Tx, tenantID, inviteCode string,
) (mode string, inviteID string, err error) {
	mode, err = loadRegistrationMode(ctx, tx, tenantID)
	if err != nil {
		return "", "", err
	}
	switch mode {
	case RegistrationModeClosed:
		return mode, "", ErrRegistrationUnavailable
	case RegistrationModeInviteOnly:
		inviteID, err = resolveAvailableInvite(ctx, tx, tenantID, inviteCode)
		if err != nil {
			return "", "", err
		}
		if inviteID == "" {
			return mode, "", ErrRegistrationUnavailable
		}
	case RegistrationModeOpen:
		if strings.TrimSpace(inviteCode) != "" {
			inviteID, err = resolveAvailableInvite(ctx, tx, tenantID, inviteCode)
			if err != nil {
				return "", "", err
			}
			if inviteID == "" {
				return mode, "", ErrRegistrationUnavailable
			}
		}
	}
	return mode, inviteID, nil
}

func enforceRegistrationCompletePolicy(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	mode, err := loadRegistrationMode(ctx, tx, tenantID)
	if err != nil {
		return "", err
	}
	if mode == RegistrationModeClosed {
		return mode, ErrRegistrationUnavailable
	}
	return mode, nil
}

func (s *Service) RegistrationPolicy(ctx context.Context, tenantID string) (RegistrationPolicy, error) {
	out := RegistrationPolicy{Mode: RegistrationModeClosed, EmailVerification: true}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		mode, err := loadRegistrationMode(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		out.Mode = mode
		out.EmailVerification, err = emailVerifyEnabled(ctx, tx, tenantID)
		return err
	})
	return out, err
}
