package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// Telegram Bot 配置（对标 Xboard config/setTelegramWebhook）。

// loadTelegramAdminChat 读管理员群组 chat id，目前只作测试发送的默认目标。
func (h *handlers) loadTelegramAdminChat(r *http.Request) (*int64, error) {
	return h.d.Notify.TelegramAdminChat(r.Context(), httpx.TenantIDFrom(r.Context()))
}

// getTelegramSettingsResponse 的 admin_chat_id 未设置时回 null，故不带 omitempty。
type getTelegramSettingsResponse struct {
	AdminChatID *int64 `json:"admin_chat_id"`
	BotUsername string `json:"bot_username"`
	Enabled     bool   `json:"enabled"`
	HasToken    bool   `json:"has_token"`
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
	httpx.OK(w, getTelegramSettingsResponse{
		Enabled:     cfg.Enabled,
		BotUsername: cfg.BotUsername,
		HasToken:    cfg.BotToken != "",
		AdminChatID: adminChat,
	})
}

type telegramSettingsReq struct {
	Enabled     bool   `json:"enabled"`
	BotUsername string `json:"bot_username"`
	BotToken    string `json:"bot_token"` // 空表示不修改
	// AdminChatID 缺省不修改，null 清空，数字（非 0 整数）保存
	AdminChatID json.RawMessage `json:"admin_chat_id"`
}

type setTelegramSettingsResponse struct {
	Enabled bool `json:"enabled"`
	OK      bool `json:"ok"`
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

	err := h.d.Notify.SaveTelegramSettings(r.Context(), notify.TelegramSettingsInput{
		TenantID: tenantID, ActorID: actor,
		Enabled: req.Enabled, BotUsername: req.BotUsername, BotToken: req.BotToken,
		SetAdminChat: setAdminChat, AdminChat: adminChat, Envelope: h.d.Envelope,
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
	httpx.OK(w, setTelegramSettingsResponse{Enabled: req.Enabled, OK: true})
}

type telegramTestReq struct {
	// ChatID 省略（或 0）时发往已保存的管理员群组
	ChatID int64 `json:"chat_id"`
}

type testTelegramResponse struct {
	Sent bool `json:"sent"`
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
	// chat id 可以随便填：发之前先留痕（审计台账 2.3 第 5 条），写不进去就不发
	if err := h.d.Ops.RecordTestSend(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, adminops.TestSendTelegram, itoa64(req.ChatID), nil); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
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
	httpx.OK(w, testTelegramResponse{Sent: true})
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
