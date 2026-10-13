package handler

import (
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// portalUserID 取门户登录主体；没有主体回 401。
func portalUserID(c *gin.Context) (uuid.UUID, bool) {
	p, ok := authn.FromGin(c)
	if !ok || p.UserID == uuid.Nil {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return uuid.Nil, false
	}
	return p.UserID, true
}

type portalAnnouncementDTO struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Severity    string    `json:"severity"`
	Pinned      bool      `json:"pinned"`
	PublishedAt time.Time `json:"published_at"`
}

type portalAnnouncementsResponse struct {
	Announcements []portalAnnouncementDTO `json:"announcements"`
}

type inboxNotificationDTO struct {
	ID      string     `json:"id"`
	Code    string     `json:"code"`
	Subject string     `json:"subject"`
	Body    string     `json:"body"`
	SentAt  *time.Time `json:"sent_at"`
	ReadAt  *time.Time `json:"read_at"`
}

type listNotificationsResponse struct {
	Notifications []inboxNotificationDTO `json:"notifications"`
	Unread        int64                  `json:"unread"`
}

type notificationPreferenceDTO struct {
	Category string `json:"category"`
	Channel  string `json:"channel"`
	Enabled  bool   `json:"enabled"`
	Locked   bool   `json:"locked"`
}

type notificationPreferencesResponse struct {
	Preferences []notificationPreferenceDTO `json:"preferences"`
}

type setPreferenceRequest struct {
	Category string `json:"category"`
	Channel  string `json:"channel"`
	Enabled  bool   `json:"enabled"`
}

type telegramBindingResponse struct {
	Bound       bool   `json:"bound"`
	Username    string `json:"username,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type bindCodeResponse struct {
	Code        string    `json:"code"`
	BotUsername string    `json:"bot_username"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type telegramUnbindResponse struct {
	Unbound bool `json:"unbound"`
}

func inboxLimitFromQuery(c *gin.Context) int {
	limit := 30
	v, err := strconv.Atoi(c.Query("limit"))
	if err == nil && v > 0 && v <= 100 {
		limit = v
	}
	return limit
}

func toInboxNotificationDTO(entry service.InboxEntry) inboxNotificationDTO {
	var sentAt *time.Time
	if !entry.SentAt.IsZero() {
		sentAt = &entry.SentAt
	}
	return inboxNotificationDTO{
		ID:      entry.ID.String(),
		Code:    entry.Code,
		Subject: entry.Subject,
		Body:    entry.Body,
		SentAt:  sentAt,
		ReadAt:  entry.ReadAt,
	}
}

func (h *Handler) myAnnouncements(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	rows, err := h.svc.MyAnnouncements(c.Request.Context(), uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]portalAnnouncementDTO, len(rows))
	for i, a := range rows {
		items[i] = portalAnnouncementDTO{
			ID:          a.ID.String(),
			Title:       a.Title,
			Body:        a.Body,
			Severity:    a.Severity,
			Pinned:      a.Pinned,
			PublishedAt: a.PublishedAt,
		}
	}
	if items == nil {
		items = []portalAnnouncementDTO{}
	}
	httpx.OK(c, portalAnnouncementsResponse{Announcements: items})
}

func (h *Handler) listNotifications(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	onlyUnread := c.Query("unread") == "1"
	limit := inboxLimitFromQuery(c)
	entries, unread, err := h.svc.Inbox(c.Request.Context(), uid, onlyUnread, limit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]inboxNotificationDTO, len(entries))
	for i, e := range entries {
		items[i] = toInboxNotificationDTO(e)
	}
	httpx.OK(c, listNotificationsResponse{Notifications: items, Unread: unread})
}

func (h *Handler) markNotificationRead(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	if err := h.svc.MarkInboxRead(c.Request.Context(), uid, id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

func (h *Handler) markAllNotificationsRead(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	if err := h.svc.MarkAllInboxRead(c.Request.Context(), uid); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

func (h *Handler) getPreferences(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	rows, err := h.svc.Preferences(c.Request.Context(), uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]notificationPreferenceDTO, len(rows))
	for i, p := range rows {
		items[i] = notificationPreferenceDTO{
			Category: p.Category,
			Channel:  p.Channel,
			Enabled:  p.Enabled,
			Locked:   p.Locked,
		}
	}
	httpx.OK(c, notificationPreferencesResponse{Preferences: items})
}

func (h *Handler) setPreference(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	var req setPreferenceRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetPreference(c.Request.Context(), uid, req.Category, req.Channel, req.Enabled); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

func (h *Handler) myTelegram(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	info, err := h.svc.TelegramBinding(c.Request.Context(), uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, telegramBindingResponse{
		Bound:       info.Bound,
		Username:    info.Username,
		BotUsername: info.BotUsername,
		Enabled:     info.Enabled,
	})
}

func (h *Handler) telegramBindCode(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	code, err := h.svc.IssueBindCode(c.Request.Context(), uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, bindCodeResponse{
		Code:        code.Code,
		BotUsername: code.BotUsername,
		ExpiresAt:   code.ExpiresAt,
	})
}

func (h *Handler) telegramUnbind(c *gin.Context) {
	uid, ok := portalUserID(c)
	if !ok {
		return
	}
	if err := h.svc.UnbindTelegram(c.Request.Context(), uid); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, telegramUnbindResponse{Unbound: true})
}
