// [INPUT]: 依赖 domain/notify 的 SMTP 配置与发信器、MailSettings / SaveMailSettings（读写、upsert 与审计在 notify/mail_settings.go），domain/identity 的 EmailVerificationDefault 与注册模式常量，platform/httpx
// [OUTPUT]: 对外提供 handlers 的 getMailSettings / setMailSettings / testMailSettings；成功响应为具名 DTO（*Response）
// [POS]: api/admin 的邮件与注册设置接口；全部设置项 upsert（SMTP 密码行缺失也能写入，R94）；发件人名缺省显示站点名，测试信主题带发件人名

package admin

// 邮件设置。
//
// 密码只进不出：读接口只回「是否已设置」，永远不回明文。
// 管理后台被 XSS 或者会话被盗时，能改设置已经很糟，
// 但至少不该顺手把发信凭据也送出去 —— 那玩意儿会被拿去群发垃圾邮件，
// 代价是自己的域名进黑名单。

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type getMailSettingsResponse struct {
	EmailVerification bool   `json:"email_verification"`
	Encryption        string `json:"encryption"`
	FromAddress       string `json:"from_address"`
	FromName          string `json:"from_name"`
	HasPassword       bool   `json:"has_password"`
	RegistrationMode  string `json:"registration_mode"`
	SMTPHost          string `json:"smtp_host"`
	SMTPPort          int    `json:"smtp_port"`
	SMTPUsername      string `json:"smtp_username"`
}

func (h *handlers) getMailSettings(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())

	// 邮箱验证缺行时与注册流程同一个回退值
	st, err := h.d.Notify.MailSettings(r.Context(), tenantID, identity.EmailVerificationDefault)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.OK(w, getMailSettingsResponse{
		SMTPHost: st.SMTPHost, SMTPPort: st.SMTPPort, Encryption: st.Encryption,
		SMTPUsername: st.SMTPUsername, HasPassword: st.HasPassword,
		FromAddress: st.FromAddress, FromName: st.FromName,
		EmailVerification: st.EmailVerification,
		RegistrationMode:  st.RegistrationMode,
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

type setMailSettingsResponse struct {
	OK bool `json:"ok"`
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

	err := h.d.Notify.SaveMailSettings(r.Context(), notify.MailSettingsInput{
		TenantID: tenantID, ActorID: actorID,
		Host: req.Host, Port: req.Port, Encryption: req.Encryption, Username: req.Username,
		Password: req.Password, From: req.From, FromName: req.FromName,
		EmailVerify: req.EmailVerify, RegistrationMode: req.RegistrationMode,
		Envelope: h.d.Envelope,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if h.d.SMTPProvider != nil {
		h.d.SMTPProvider.Invalidate()
	}
	httpx.OK(w, setMailSettingsResponse{OK: true})
}

type testMailSettingsResponse struct {
	OK bool   `json:"ok"`
	To string `json:"to"`
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
	httpx.OK(w, testMailSettingsResponse{OK: true, To: req.To})
}
