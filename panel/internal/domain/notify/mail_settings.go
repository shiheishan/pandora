package notify

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// MailSettings 是后台邮件页看到的设置；密码只回「是否已设置」，永远不回明文。
type MailSettings struct {
	SMTPHost          string `json:"smtp_host"`
	SMTPPort          int    `json:"smtp_port"`
	Encryption        string `json:"encryption"`
	SMTPUsername      string `json:"smtp_username"`
	HasPassword       bool   `json:"has_password"`
	FromAddress       string `json:"from_address"`
	FromName          string `json:"from_name"`
	EmailVerification bool   `json:"email_verification"`
	RegistrationMode  string `json:"registration_mode"`
}

// MailSettings 读邮件与注册设置。emailVerificationDefault 是 auth.email_verification
// 缺行时的回退值，必须与注册流程同一个（identity.EmailVerificationDefault）。
func (s *Service) MailSettings(ctx context.Context, tenantID string, emailVerificationDefault bool) (*MailSettings, error) {
	var out MailSettings
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_host'), ''),
			       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_port'), 465),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.encryption'), 'ssl'),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_username'), ''),
			       COALESCE((SELECT length(secret_encrypted) > 0 FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_password'), false),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.from_address'), ''),
			       COALESCE((SELECT btrim(value #>> '{}') FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.from_name'), ''),
			       COALESCE((SELECT (value #>> '{}')::boolean FROM system_settings
			                  WHERE tenant_id=$1 AND key='auth.email_verification'), $2::boolean),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='auth.registration_mode'), 'closed')`,
			tenantID, emailVerificationDefault).Scan(&out.SMTPHost, &out.SMTPPort, &out.Encryption,
			&out.SMTPUsername, &out.HasPassword, &out.FromAddress, &out.FromName,
			&out.EmailVerification, &out.RegistrationMode)
		if err != nil || out.FromName != "" {
			return err
		}
		// 没单独设发件人名时，显示实际发信会用的值：生效主题的站点名
		out.FromName, err = appearance.SiteNameTx(ctx, tx, tenantID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// MailSettingsInput 是已校验过的邮件与注册设置写入。
type MailSettingsInput struct {
	TenantID string
	ActorID  *string
	Host     string
	Port     int
	// Encryption 只能是 ssl / tls / none（调用方校验）
	Encryption string
	Username   string
	// Password 为空表示不改，"-" 表示显式清空
	Password    string
	From        string
	FromName    string
	EmailVerify *bool
	// RegistrationMode 为 nil 时不动 auth.registration_mode
	RegistrationMode *string
	// Envelope 密封 SMTP 密码
	Envelope *crypto.Envelope
}

// SaveMailSettings 在一个事务里 upsert 全部设置项，同事务写 mail.settings_changed 审计（摘要不含凭据）。
func (s *Service) SaveMailSettings(ctx context.Context, in MailSettingsInput) error {
	tenantID := in.TenantID
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 自己序列化成 JSON 文本再交给 ::jsonb。
		//
		// 不用 to_jsonb($3)：那样 PostgreSQL 无法推断参数类型，
		// 报的是「could not determine data type」，而调用方看到的只是一个 500
		set := func(key string, val any) error {
			raw, err := json.Marshal(val)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO system_settings (tenant_id, key, value)
				VALUES ($1, $2, $3::jsonb)
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				tenantID, key, string(raw))
			return err
		}
		// Registration reads mode before email verification. Acquire setting
		// locks in the same order so an admin update cannot deadlock completion.
		if in.RegistrationMode != nil {
			if err := set("auth.registration_mode", *in.RegistrationMode); err != nil {
				return err
			}
		}
		if err := set("mail.smtp_host", in.Host); err != nil {
			return err
		}
		if err := set("mail.smtp_port", in.Port); err != nil {
			return err
		}
		if err := set("mail.encryption", in.Encryption); err != nil {
			return err
		}
		if err := set("mail.smtp_username", in.Username); err != nil {
			return err
		}
		if err := set("mail.from_address", in.From); err != nil {
			return err
		}
		if err := set("mail.from_name", in.FromName); err != nil {
			return err
		}
		if in.EmailVerify != nil {
			if err := set("auth.email_verification", *in.EmailVerify); err != nil {
				return err
			}
		}
		if in.Password != "" {
			plain := in.Password
			if plain == "-" {
				plain = "" // 显式清空
			}
			enc, err := SealSMTPPassword(in.Envelope, plain)
			if err != nil {
				return err
			}
			if enc == nil {
				enc = []byte{}
			}
			// upsert（R94）：以前只 UPDATE，缺这一行的租户密码会静默存不上。
			// 新插入时行的形状照 00030 的种子：value 占位空串、带 schema、标为密文项。
			if _, err := tx.Exec(ctx, `
				INSERT INTO system_settings (tenant_id, key, value, value_schema, is_secret, secret_encrypted)
				VALUES ($1, $2, '""'::jsonb, '{"type":"string","title":"密码"}'::jsonb, true, $3)
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET secret_encrypted = EXCLUDED.secret_encrypted, updated_at = now()`,
				tenantID, "mail.smtp_password", enc); err != nil {
				return err
			}
		}

		// 摘要里不放任何凭据，只记改了哪些项
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: in.ActorID,
			Action: "mail.settings_changed", ResourceType: "system_settings",
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"host": in.Host, "port": in.Port, "encryption": in.Encryption,
				"from": in.From, "password_changed": in.Password != "",
				"email_verification": in.EmailVerify,
				"registration_mode":  in.RegistrationMode,
			},
		})
	})
}
