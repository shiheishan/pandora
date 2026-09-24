// [INPUT]: 依赖 domain/notify 的 Telegram 配置读取与发信器，依赖 platform 的 db/httpx；读写 system_settings 的 telegram.* 键
// [OUTPUT]: 对外提供 handlers 的 getTelegramSettings / setTelegramSettings / testTelegram
// [POS]: api/admin 的 Telegram 渠道配置：Token 只进不出（信封加密），管理员群组 chat id 作测试发送的默认目标
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// Telegram Bot 配置（对标 Xboard config/setTelegramWebhook）。

// loadTelegramAdminChat 读管理员群组 chat id（system_settings 的 telegram.admin_chat_id）。
// 目前只作测试发送的默认目标；推送管理告警见待决 D-A-4，未定前不做。
func (h *handlers) loadTelegramAdminChat(r *http.Request) (*int64, error) {
	var chat *int64
	tenantID := httpx.TenantIDFrom(r.Context())
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			SELECT (value #>> '{}')::bigint FROM system_settings
			 WHERE tenant_id = $1 AND key = 'telegram.admin_chat_id'`, tenantID).Scan(&chat)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return chat, err
}

func (h *handlers) getTelegramSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := notify.LoadTelegramConfig(r.Context(), h.d.Pool, h.d.Envelope,
		httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	adminChat, err := h.loadTelegramAdminChat(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// Token 只回「配没配」，不回内容。回明文等于把它摊在任何能打开
	// 后台的人面前，也会随着浏览器缓存和截图扩散出去。
	httpx.OK(w, map[string]any{
		"enabled":       cfg.Enabled,
		"bot_username":  cfg.BotUsername,
		"has_token":     cfg.BotToken != "",
		"admin_chat_id": adminChat,
	})
}

type telegramSettingsReq struct {
	Enabled     bool   `json:"enabled"`
	BotUsername string `json:"bot_username"`
	BotToken    string `json:"bot_token"` // 空表示不修改
	// AdminChatID 缺省不修改，null 清空，数字（非 0 整数）保存
	AdminChatID json.RawMessage `json:"admin_chat_id"`
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
	var adminChat *int64
	setAdminChat := len(req.AdminChatID) > 0
	if setAdminChat && string(req.AdminChatID) != "null" {
		var v int64
		if err := json.Unmarshal(req.AdminChatID, &v); err != nil || v == 0 {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"admin_chat_id": "管理员群组 chat id 必须是非 0 整数"}))
			return
		}
		adminChat = &v
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
			if setAdminChat {
				if err := set("telegram.admin_chat_id", adminChat); err != nil {
					return err
				}
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
	// ChatID 省略（或 0）时发往已保存的管理员群组
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
		adminChat, err := h.loadTelegramAdminChat(r)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, err)
			return
		}
		if adminChat == nil || *adminChat == 0 {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"chat_id": "请填写要接收测试消息的 chat id，或先保存管理员群组 chat id"}))
			return
		}
		req.ChatID = *adminChat
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
