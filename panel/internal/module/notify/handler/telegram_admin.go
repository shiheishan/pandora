package handler

import (
	"encoding/json"
	"io"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

const telegramWebhookMaxBody = 64 << 10

type telegramSettingsResponse struct {
	Enabled     bool   `json:"enabled"`
	BotUsername string `json:"bot_username"`
	HasToken    bool   `json:"has_token"`
	AdminChatID *int64 `json:"admin_chat_id"`
}

type setTelegramSettingsRequest struct {
	Enabled     bool            `json:"enabled"`
	BotUsername string          `json:"bot_username"`
	BotToken    string          `json:"bot_token"`
	AdminChatID json.RawMessage `json:"admin_chat_id"`
}

type setTelegramSettingsResponse struct {
	OK      bool `json:"ok"`
	Enabled bool `json:"enabled"`
}

type testTelegramRequest struct {
	ChatID int64 `json:"chat_id"`
}

type testTelegramResponse struct {
	Sent bool `json:"sent"`
}

func parseAdminChatID(raw json.RawMessage) (set bool, id *int64, err error) {
	if len(raw) == 0 {
		return false, nil, nil
	}
	if string(raw) == "null" {
		return true, nil, nil
	}
	var v int64
	if json.Unmarshal(raw, &v) != nil || v == 0 {
		return false, nil, errs.Invalid(map[string]string{
			"admin_chat_id": "管理员群组 chat id 必须是非 0 整数",
		})
	}
	return true, &v, nil
}

func (h *Handler) getTelegramSettings(c *gin.Context) {
	ctx := c.Request.Context()
	st, err := h.svc.TelegramSettings(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, telegramSettingsResponse{
		Enabled:     st.Enabled,
		BotUsername: st.BotUsername,
		HasToken:    st.HasToken,
		AdminChatID: st.AdminChatID,
	})
}

func (h *Handler) setTelegramSettings(c *gin.Context) {
	ctx := c.Request.Context()
	var req setTelegramSettingsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	adminSet, adminID, err := parseAdminChatID(req.AdminChatID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SaveTelegramSettings(ctx, service.TelegramSettingsInput{
		Enabled:      req.Enabled,
		BotUsername:  req.BotUsername,
		BotToken:     req.BotToken,
		AdminChatSet: adminSet,
		AdminChatID:  adminID,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, setTelegramSettingsResponse{OK: true, Enabled: req.Enabled})
}

func (h *Handler) testTelegram(c *gin.Context) {
	ctx := c.Request.Context()
	var chatID int64
	if c.Request.ContentLength != 0 {
		var req testTelegramRequest
		if err := httpx.BindJSON(c, &req); err != nil {
			httpx.Fail(c, err)
			return
		}
		chatID = req.ChatID
	}
	if err := h.svc.TestTelegram(ctx, chatID); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, testTelegramResponse{Sent: true})
}

func (h *Handler) telegramWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, telegramWebhookMaxBody+1))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(body) > telegramWebhookMaxBody {
		httpx.Fail(c, errs.New(errs.BadRequest))
		return
	}
	secret := c.GetHeader("X-Telegram-Bot-Api-Secret-Token")
	if err := h.svc.HandleTelegramUpdate(ctx, secret, body); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}
