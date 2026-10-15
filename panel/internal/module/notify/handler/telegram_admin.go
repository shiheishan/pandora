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

func parseChatIDField(raw json.RawMessage, field, label string) (set bool, id *int64, err error) {
	if len(raw) == 0 {
		return false, nil, nil
	}
	if string(raw) == "null" {
		return true, nil, nil
	}
	// 群组与超级群的 chat id 都是负数；填成正数（私聊）时提醒能发出，但在私聊里回复会被当成绑定码，所以直接拒绝
	var v int64
	if json.Unmarshal(raw, &v) != nil || v >= 0 {
		return false, nil, errs.Invalid(map[string]string{
			field: label + "必须是负数（群组的 chat id 以 - 开头）",
		})
	}
	return true, &v, nil
}

func parseAdminChatID(raw json.RawMessage) (set bool, id *int64, err error) {
	return parseChatIDField(raw, "admin_chat_id", "管理员群组 chat id ")
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

type telegramExtrasResponse struct {
	SubCommand        bool   `json:"sub_command_enabled"`
	StartMessage      string `json:"start_message"`
	AdminPaidNotify   bool   `json:"admin_paid_notify"`
	AdminTicketNotify bool   `json:"admin_ticket_notify"`
	JoinReview        bool   `json:"join_review_enabled"`
	UserGroupChatID   *int64 `json:"user_group_chat_id"`
	DiscussLink       string `json:"discuss_link"`
}

type setTelegramExtrasRequest struct {
	SubCommand        *bool           `json:"sub_command_enabled"`
	StartMessage      *string         `json:"start_message"`
	AdminPaidNotify   *bool           `json:"admin_paid_notify"`
	AdminTicketNotify *bool           `json:"admin_ticket_notify"`
	JoinReview        *bool           `json:"join_review_enabled"`
	UserGroupChatID   json.RawMessage `json:"user_group_chat_id"` // 不传不改；null 清除；负数（群组）
	DiscussLink       *string         `json:"discuss_link"`
}

func (h *Handler) getTelegramExtras(c *gin.Context) {
	ctx := c.Request.Context()
	ex, err := h.svc.TelegramExtras(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, telegramExtrasResponse{
		SubCommand:        ex.SubCommand,
		StartMessage:      ex.StartMessage,
		AdminPaidNotify:   ex.AdminPaidNotify,
		AdminTicketNotify: ex.AdminTicketNotify,
		JoinReview:        ex.JoinReview,
		UserGroupChatID:   ex.UserGroupChatID,
		DiscussLink:       ex.DiscussLink,
	})
}

func (h *Handler) setTelegramExtras(c *gin.Context) {
	ctx := c.Request.Context()
	var req setTelegramExtrasRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	userGroupSet, userGroupID, err := parseChatIDField(req.UserGroupChatID, "user_group_chat_id", "用户群 chat id ")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SaveTelegramExtras(ctx, service.TelegramExtrasInput{
		SubCommand:        req.SubCommand,
		StartMessage:      req.StartMessage,
		AdminPaidNotify:   req.AdminPaidNotify,
		AdminTicketNotify: req.AdminTicketNotify,
		JoinReview:        req.JoinReview,
		UserGroupChatSet:  userGroupSet,
		UserGroupChatID:   userGroupID,
		DiscussLink:       req.DiscussLink,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	h.getTelegramExtras(c)
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
