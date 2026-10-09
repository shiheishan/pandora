package certs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ACME 设置放 system_settings 的 acme.* 键（设计稿 §2.1：不放环境变量）。ZeroSSL 的 EAB HMAC
// 是秘密：is_secret + 信封密文，读接口只回「配没配」。
const (
	settingContactEmail   = "acme.contact_email"
	settingUseStaging     = "acme.use_staging"
	settingZeroSSLEnabled = "acme.zerossl_enabled"
	settingZeroSSLKID     = "acme.zerossl_eab_kid"
	settingZeroSSLHMAC    = "acme.zerossl_eab_hmac"
)

// ACMESettings 是后台「ACME 设置」的读形状。没有任何秘密字段。
type ACMESettings struct {
	ContactEmail   string `json:"contact_email"`
	UseStaging     bool   `json:"use_staging"`
	ZeroSSLEnabled bool   `json:"zerossl_enabled"`
	ZeroSSLEABKID  string `json:"zerossl_eab_kid"`
	// ZeroSSLEABHMACSet 只说 HMAC 配没配，不回明文也不回密文
	ZeroSSLEABHMACSet bool `json:"zerossl_eab_hmac_set"`
	// DirectoryOverride 为 true 表示这台面板的签发被非生产环境的目录覆盖（pebble 等）接管，
	// 上面的 CA 选择不生效
	DirectoryOverride bool `json:"directory_override"`
}

// ACMESettingsInput 是整体保存。ZeroSSLEABHMAC：nil 保留原值，"" 清空，其余替换。
type ACMESettingsInput struct {
	ContactEmail   string  `json:"contact_email"`
	UseStaging     bool    `json:"use_staging"`
	ZeroSSLEnabled bool    `json:"zerossl_enabled"`
	ZeroSSLEABKID  string  `json:"zerossl_eab_kid"`
	ZeroSSLEABHMAC *string `json:"zerossl_eab_hmac"`
}

// acmeConfig 是 worker 用的完整设置（含解开的 HMAC）。
type acmeConfig struct {
	ACMESettings
	eabHMAC string
}

func hmacAAD(tenantID string) []byte {
	return []byte("acme_settings:" + tenantID + ":zerossl_eab_hmac")
}

func (s *Service) loadACMEConfig(ctx context.Context, tx pgx.Tx, tenantID string, withSecret bool) (acmeConfig, error) {
	var cfg acmeConfig
	rows, err := tx.Query(ctx, `
		SELECT key, value, secret_encrypted FROM system_settings
		 WHERE tenant_id = $1 AND key = ANY($2::text[])`, tenantID,
		[]string{settingContactEmail, settingUseStaging, settingZeroSSLEnabled, settingZeroSSLKID, settingZeroSSLHMAC})
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	var sealed []byte
	for rows.Next() {
		var key string
		var raw []byte
		var secret []byte
		if err := rows.Scan(&key, &raw, &secret); err != nil {
			return cfg, err
		}
		switch key {
		case settingContactEmail:
			_ = json.Unmarshal(raw, &cfg.ContactEmail)
		case settingUseStaging:
			_ = json.Unmarshal(raw, &cfg.UseStaging)
		case settingZeroSSLEnabled:
			_ = json.Unmarshal(raw, &cfg.ZeroSSLEnabled)
		case settingZeroSSLKID:
			_ = json.Unmarshal(raw, &cfg.ZeroSSLEABKID)
		case settingZeroSSLHMAC:
			sealed = secret
		}
	}
	if err := rows.Err(); err != nil {
		return cfg, err
	}
	cfg.ZeroSSLEABHMACSet = len(sealed) > 0
	cfg.DirectoryOverride = s.opts.DirectoryOverride != ""
	if withSecret && len(sealed) > 0 {
		plain, err := s.sealer.Open(sealed, hmacAAD(tenantID))
		if err != nil {
			return cfg, err
		}
		cfg.eabHMAC = string(plain)
	}
	return cfg, nil
}

// ACMESettings 读后台的 ACME 设置。
func (s *Service) ACMESettings(ctx context.Context, tenantID string) (ACMESettings, error) {
	var out ACMESettings
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		cfg, err := s.loadACMEConfig(ctx, tx, tenantID, false)
		out = cfg.ACMESettings
		return err
	})
	return out, err
}

// SaveACMESettings 整体保存 ACME 设置并写审计（HMAC 只记换没换）。
func (s *Service) SaveACMESettings(ctx context.Context, tenantID string, actor Actor, in ACMESettingsInput) (ACMESettings, error) {
	in.ContactEmail = strings.TrimSpace(in.ContactEmail)
	in.ZeroSSLEABKID = strings.TrimSpace(in.ZeroSSLEABKID)
	bad := map[string]string{}
	if in.ContactEmail != "" {
		if a, err := mail.ParseAddress(in.ContactEmail); err != nil || a.Address != in.ContactEmail || len(in.ContactEmail) > 254 {
			bad["contact_email"] = "邮箱格式不对"
		}
	}
	if len(in.ZeroSSLEABKID) > 256 {
		bad["zerossl_eab_kid"] = "太长"
	}
	var newHMAC string
	if in.ZeroSSLEABHMAC != nil {
		newHMAC = strings.TrimSpace(*in.ZeroSSLEABHMAC)
		if newHMAC != "" {
			if _, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(newHMAC, "=")); err != nil || len(newHMAC) > 512 {
				bad["zerossl_eab_hmac"] = "EAB HMAC 应是 ZeroSSL 后台给的 base64url 字符串"
			}
		}
	}
	var out ACMESettings
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		old, err := s.loadACMEConfig(ctx, tx, tenantID, false)
		if err != nil {
			return err
		}
		hmacSet := old.ZeroSSLEABHMACSet
		if in.ZeroSSLEABHMAC != nil {
			hmacSet = newHMAC != ""
		}
		if in.ZeroSSLEnabled && (in.ZeroSSLEABKID == "" || !hmacSet) {
			if in.ZeroSSLEABKID == "" {
				bad["zerossl_eab_kid"] = "启用 ZeroSSL 要填 EAB KID"
			}
			if !hmacSet {
				bad["zerossl_eab_hmac"] = "启用 ZeroSSL 要填 EAB HMAC"
			}
		}
		if len(bad) > 0 {
			return httpx.Invalid(bad)
		}
		set := func(key string, val any) error {
			raw, err := json.Marshal(val)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO system_settings (tenant_id, key, value)
				VALUES ($1, $2, $3::jsonb)
				ON CONFLICT (tenant_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				tenantID, key, string(raw))
			return err
		}
		for _, kv := range []struct {
			k string
			v any
		}{{settingContactEmail, in.ContactEmail}, {settingUseStaging, in.UseStaging},
			{settingZeroSSLEnabled, in.ZeroSSLEnabled}, {settingZeroSSLKID, in.ZeroSSLEABKID}} {
			if err := set(kv.k, kv.v); err != nil {
				return err
			}
		}
		if in.ZeroSSLEABHMAC != nil {
			if newHMAC == "" {
				// 不删行：设置的修订记录是追加写，删设置行会级联删修订而被拒。清掉密文、取消秘密标记即可
				if _, err := tx.Exec(ctx, `
					UPDATE system_settings SET secret_encrypted = NULL, is_secret = false, updated_at = now()
					 WHERE tenant_id = $1 AND key = $2`, tenantID, settingZeroSSLHMAC); err != nil {
					return err
				}
			} else {
				sealed, err := s.sealer.Seal([]byte(newHMAC), hmacAAD(tenantID))
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO system_settings (tenant_id, key, value, is_secret, secret_encrypted)
					VALUES ($1, $2, to_jsonb(''::text), true, $3)
					ON CONFLICT (tenant_id, key) DO UPDATE
					   SET secret_encrypted = EXCLUDED.secret_encrypted, is_secret = true, updated_at = now()`,
					tenantID, settingZeroSSLHMAC, sealed); err != nil {
					return err
				}
			}
		}
		out = ACMESettings{ContactEmail: in.ContactEmail, UseStaging: in.UseStaging, ZeroSSLEnabled: in.ZeroSSLEnabled,
			ZeroSSLEABKID: in.ZeroSSLEABKID, ZeroSSLEABHMACSet: hmacSet, DirectoryOverride: s.opts.DirectoryOverride != ""}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "acme_settings.updated",
			ResourceType: "system_settings", APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"contact_email": old.ContactEmail, "use_staging": old.UseStaging,
				"zerossl_enabled": old.ZeroSSLEnabled, "zerossl_eab_kid": old.ZeroSSLEABKID},
			AfterDigest: map[string]any{"contact_email": in.ContactEmail, "use_staging": in.UseStaging,
				"zerossl_enabled": in.ZeroSSLEnabled, "zerossl_eab_kid": in.ZeroSSLEABKID,
				"zerossl_eab_hmac_changed": in.ZeroSSLEABHMAC != nil},
		})
	})
	return out, err
}
