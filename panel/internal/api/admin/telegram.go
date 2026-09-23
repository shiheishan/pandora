package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// Telegram Bot 配置（对标 Xboard config/setTelegramWebhook）。

func (h *handlers) getTelegramSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := notify.LoadTelegramConfig(r.Context(), h.d.Pool, h.d.Envelope,
		httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// Token 只回「配没配」，不回内容。回明文等于把它摊在任何能打开
	// 后台的人面前，也会随着浏览器缓存和截图扩散出去。
	httpx.OK(w, map[string]any{
		"enabled":      cfg.Enabled,
		"bot_username": cfg.BotUsername,
		"has_token":    cfg.BotToken != "",
	})
}

type telegramSettingsReq struct {
	Enabled     bool   `json:"enabled"`
	BotUsername string `json:"bot_username"`
	BotToken    string `json:"bot_token"` // 空表示不修改
}

func (h *handlers) setTelegramSettings(w http.ResponseWriter, r *http.Request) {
	var req telegramSettingsReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.BotUsername = strings.TrimPrefix(strings.TrimSpace(req.BotUsername), "@")
	req.BotToken = strings.TrimSpace(req.BotToken)

	if req.Enabled && req.BotUsername == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"bot_username": "启用前要填 Bot 用户名，用户要靠它找到你的 bot"}))
		return
	}

	tenantID := httpx.TenantIDFrom(r.Context())
	p := httpx.PrincipalFrom(r.Context())
	actor := p.UserID

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			set := func(key string, val any) error {
				raw, err := json.Marshal(val)
				if err != nil {
					return err
				}
				_, err = tx.Exec(r.Context(), `
					INSERT INTO system_settings (tenant_id, key, value)
					VALUES ($1,$2,$3::jsonb)
					ON CONFLICT (tenant_id, key)
					DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
					tenantID, key, string(raw))
				return err
			}
			if err := set("telegram.enabled", req.Enabled); err != nil {
				return err
			}
			if err := set("telegram.bot_username", req.BotUsername); err != nil {
				return err
			}
			// 空 token 表示「不修改」—— 界面上不回显已有 token，
			// 提交时留空就该保留原值，而不是把它清掉。
			if req.BotToken != "" {
				sealed, err := h.d.Envelope.Seal([]byte(req.BotToken), []byte("telegram"))
				if err != nil {
					return err
				}
				if _, err := tx.Exec(r.Context(), `
					INSERT INTO system_settings
						(tenant_id, key, value, is_secret, secret_encrypted)
					VALUES ($1,'telegram.bot_token',to_jsonb(''::text),true,$2)
					ON CONFLICT (tenant_id, key)
					DO UPDATE SET secret_encrypted = EXCLUDED.secret_encrypted,
					              is_secret = true, updated_at = now()`,
					tenantID, sealed); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	// 让发送器的配置缓存立刻失效，否则改完要等 30 秒才生效，
	// 管理员会以为没保存上。
	if h.d.TelegramSender != nil {
		h.d.TelegramSender.Invalidate()
	}
	httpx.OK(w, map[string]any{"ok": true, "enabled": req.Enabled})
}

type telegramTestReq struct {
	ChatID int64 `json:"chat_id"`
}

// testTelegram 往指定 chat 发一条测试消息。
func (h *handlers) testTelegram(w http.ResponseWriter, r *http.Request) {
	var req telegramTestReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.ChatID == 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"chat_id": "请填写要接收测试消息的 chat id"}))
		return
	}
	cfg, err := notify.LoadTelegramConfig(r.Context(), h.d.Pool, h.d.Envelope,
		httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if !cfg.Ready() {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"Telegram 还没配置好：启用开关、Bot 用户名、Bot Token 都要有"))
		return
	}
	sender := notify.NewTelegramSender(cfg.BotToken)
	if sender == nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "Bot Token 无效"))
		return
	}
	if err := sender.Send(r.Context(), itoa64(req.ChatID), "配置测试",
		"收到这条消息说明面板的 Telegram 通知已经通了。"); err != nil {
		// 原样带出 Telegram 的报错：管理员要靠它区分 token 错、
		// chat 不存在、还是被拉黑。
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"发送失败："+err.Error()))
		return
	}
	httpx.OK(w, map[string]any{"sent": true})
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
