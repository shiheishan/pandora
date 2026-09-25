// [INPUT]: 依赖 domain/notify 的 SMTP 配置与发信器、domain/appearance 的 SiteNameTx、platform 的 db/audit/httpx
// [OUTPUT]: 对外提供 handlers 的 getMailSettings / setMailSettings / testMailSettings
// [POS]: api/admin 的邮件与注册设置接口；全部设置项 upsert（SMTP 密码行缺失也能写入，R94）；发件人名缺省显示站点名，测试信主题带发件人名
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 邮件设置。
//
// 密码只进不出：读接口只回「是否已设置」，永远不回明文。
// 管理后台被 XSS 或者会话被盗时，能改设置已经很糟，
// 但至少不该顺手把发信凭据也送出去 —— 那玩意儿会被拿去群发垃圾邮件，
// 代价是自己的域名进黑名单。

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) getMailSettings(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())

	var host, encryption, username, from, fromName string
	var port int
	var hasPassword, emailVerify bool
	var registrationMode string

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
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
			                  WHERE tenant_id=$1 AND key='auth.email_verification'), false),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='auth.registration_mode'), 'closed')`,
			tenantID).Scan(&host, &port, &encryption, &username, &hasPassword,
			&from, &fromName, &emailVerify, &registrationMode)
		if err != nil || fromName != "" {
			return err
		}
		// 没单独设发件人名时，显示实际发信会用的值：生效主题的站点名
		fromName, err = appearance.SiteNameTx(r.Context(), tx, tenantID)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.OK(w, map[string]any{
		"smtp_host": host, "smtp_port": port, "encryption": encryption,
		"smtp_username": username, "has_password": hasPassword,
		"from_address": from, "from_name": fromName,
		"email_verification": emailVerify,
		"registration_mode":  registrationMode,
	})
}

type mailSettingsReq struct {
	Host       string `json:"smtp_host"`
	Port       int    `json:"smtp_port"`
	Encryption string `json:"encryption"`
	Username   string `json:"smtp_username"`
	// Password 为空表示不改。要清空得显式传 "-"，
	// 否则「不想改密码就不填」这个再自然不过的动作会把密码抹掉
	Password         string  `json:"smtp_password"`
	From             string  `json:"from_address"`
	FromName         string  `json:"from_name"`
	EmailVerify      *bool   `json:"email_verification"`
	RegistrationMode *string `json:"registration_mode"`
}

func (h *handlers) setMailSettings(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req mailSettingsReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	fields := map[string]string{}
	if req.Port < 1 || req.Port > 65535 {
		fields["smtp_port"] = "端口需在 1 到 65535 之间"
	}
	switch req.Encryption {
	case "ssl", "tls", "none":
	default:
		fields["encryption"] = "加密方式只能是 ssl / tls / none"
	}
	if req.RegistrationMode != nil {
		switch *req.RegistrationMode {
		case identity.RegistrationModeClosed, identity.RegistrationModeInviteOnly,
			identity.RegistrationModeOpen:
		default:
			fields["registration_mode"] = "注册模式只能是 closed / invite_only / open"
		}
	}
	// 开启邮箱验证却没有发信配置，等于把注册入口焊死：
	// 验证码发不出去，谁也注册不了，而这个故障只表现为「注册量归零」
	if req.EmailVerify != nil && *req.EmailVerify &&
		(req.Host == "" || req.From == "") {
		fields["email_verification"] = "开启邮箱验证前请先填好 SMTP 服务器与发件人地址"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(fields))
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 自己序列化成 JSON 文本再交给 ::jsonb。
		//
		// 不用 to_jsonb($3)：那样 PostgreSQL 无法推断参数类型，
		// 报的是「could not determine data type」，而调用方看到的只是一个 500
		set := func(key string, val any) error {
			raw, err := json.Marshal(val)
			if err != nil {
				return err
			}
			_, err = tx.Exec(r.Context(), `
				INSERT INTO system_settings (tenant_id, key, value)
				VALUES ($1, $2, $3::jsonb)
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				tenantID, key, string(raw))
			return err
		}
		// Registration reads mode before email verification. Acquire setting
		// locks in the same order so an admin update cannot deadlock completion.
		if req.RegistrationMode != nil {
			if err := set("auth.registration_mode", *req.RegistrationMode); err != nil {
				return err
			}
		}
		if err := set("mail.smtp_host", req.Host); err != nil {
			return err
		}
		if err := set("mail.smtp_port", req.Port); err != nil {
			return err
		}
		if err := set("mail.encryption", req.Encryption); err != nil {
			return err
		}
		if err := set("mail.smtp_username", req.Username); err != nil {
			return err
		}
		if err := set("mail.from_address", req.From); err != nil {
			return err
		}
		if err := set("mail.from_name", req.FromName); err != nil {
			return err
		}
		if req.EmailVerify != nil {
			if err := set("auth.email_verification", *req.EmailVerify); err != nil {
				return err
			}
		}
		if req.Password != "" {
			plain := req.Password
			if plain == "-" {
				plain = "" // 显式清空
			}
			enc, err := notify.SealSMTPPassword(h.d.Envelope, plain)
			if err != nil {
				return err
			}
			if enc == nil {
				enc = []byte{}
			}
			// upsert（R94）：以前只 UPDATE，缺这一行的租户密码会静默存不上。
			// 新插入时行的形状照 00030 的种子：value 占位空串、带 schema、标为密文项。
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO system_settings (tenant_id, key, value, value_schema, is_secret, secret_encrypted)
				VALUES ($1, $2, '""'::jsonb, '{"type":"string","title":"密码"}'::jsonb, true, $3)
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET secret_encrypted = EXCLUDED.secret_encrypted, updated_at = now()`,
				tenantID, "mail.smtp_password", enc); err != nil {
				return err
			}
		}

		// 摘要里不放任何凭据，只记改了哪些项
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "mail.settings_changed", ResourceType: "system_settings",
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{
				"host": req.Host, "port": req.Port, "encryption": req.Encryption,
				"from": req.From, "password_changed": req.Password != "",
				"email_verification": req.EmailVerify,
				"registration_mode":  req.RegistrationMode,
			},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if h.d.SMTPProvider != nil {
		h.d.SMTPProvider.Invalidate()
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// testMailSettings 用当前配置发一封测试邮件。
//
// 直连而不是走通知队列：管理员要的是「现在就告诉我配对了没有」，
// 而队列里的失败要等下一轮投递才看得见，报错也早就被包装过了。
func (h *handlers) testMailSettings(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req struct {
		To string `json:"to"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.To == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"to": "请填写收件地址"}))
		return
	}

	cfg, err := notify.LoadSMTPConfig(r.Context(), h.d.Pool, h.d.Envelope, tenantID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if !cfg.Enabled() {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"SMTP 还没配置好：服务器地址、端口、发件人地址都要填"))
		return
	}

	sender := notify.NewSMTPSender(cfg)
	if sender == nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "SMTP 配置不完整"))
		return
	}
	if err := sender.Send(r.Context(), req.To, cfg.FromName+" 邮件配置测试",
		"这是一封测试邮件。收到它说明面板的发信配置是通的。"); err != nil {
		// 把底层报错原样带出去：管理员要靠它判断是认证失败、
		// 端口不对还是被防火墙挡了。包装成「发送失败」等于什么都没说
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"发送失败："+err.Error()))
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "to": req.To})
}
