// [INPUT]: 依赖 domain/notify 的 Inbox / MarkInboxRead / MarkAllInboxRead / PreferenceOverrides / SetPreference，依赖 platform/httpx、同包 handlers.go 的 isUUID
// [OUTPUT]: 对外提供 handlers 的 listNotifications / markNotificationRead / markAllNotificationsRead / getNotificationPreferences / setNotificationPreference
// [POS]: api/public 的站内信与通知偏好；单条标已读的非 UUID id 回 404（R84）；偏好目录与锁定项在这里，SQL 在 notify/inbox.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

// 站内信与通知偏好：读写都在 notify（inbox.go），这里只做参数、偏好目录与响应。

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// listNotifications 返回站内信收件箱。
func (h *handlers) listNotifications(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}

	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	onlyUnread := r.URL.Query().Get("unread") == "1"

	out, unread, err := h.d.Notify.Inbox(r.Context(), p.TenantID, p.UserID, onlyUnread, limit)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"notifications": out, "unread": unread})
}

// markNotificationRead 标记单条已读。
func (h *handlers) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	// 不是 UUID 的 id 进 SQL 会在 ::uuid 转换处炸成 500（R84），先挡成 404。
	// 合法但不存在或不属于本人的 id 仍回 200：标已读天然幂等，也不借状态码泄露存在性。
	if !isUUID(id) {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	if err := h.d.Notify.MarkInboxRead(r.Context(), p.TenantID, p.UserID, id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// markAllRead 全部标记已读。
func (h *handlers) markAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	if err := h.d.Notify.MarkAllInboxRead(r.Context(), p.TenantID, p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

type notifyPrefReq struct {
	Category string `json:"category"`
	Channel  string `json:"channel"`
	Enabled  bool   `json:"enabled"`
}

type notifyPreference struct {
	Category string `json:"category"`
	Channel  string `json:"channel"`
	Enabled  bool   `json:"enabled"`
	Locked   bool   `json:"locked"`
}

var notifyPreferenceCatalog = []notifyPreference{
	{Category: "transactional", Channel: "email", Enabled: true, Locked: true},
	{Category: "transactional", Channel: "telegram", Enabled: true, Locked: true},
	{Category: "service", Channel: "email", Enabled: true},
	{Category: "service", Channel: "telegram", Enabled: true},
	{Category: "marketing", Channel: "email", Enabled: true},
	{Category: "marketing", Channel: "telegram", Enabled: true},
}

func validNotifyPreference(category, channel string) bool {
	for _, item := range notifyPreferenceCatalog {
		if item.Category == category && item.Channel == channel {
			return true
		}
	}
	return false
}

// getNotificationPreferences 返回当前用户的有效通知偏好。数据库只保存覆盖项，
// 未出现的组合沿用安全默认值；交易类始终开启且由数据库约束再次兜底。
func (h *handlers) getNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}

	stored, err := h.d.Notify.PreferenceOverrides(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	overrides := map[string]bool{}
	for _, o := range stored {
		overrides[o.Category+"\x00"+o.Channel] = o.Enabled
	}

	preferences := make([]notifyPreference, len(notifyPreferenceCatalog))
	copy(preferences, notifyPreferenceCatalog)
	for i := range preferences {
		item := &preferences[i]
		if enabled, ok := overrides[item.Category+"\x00"+item.Channel]; ok && !item.Locked {
			item.Enabled = enabled
		}
	}
	httpx.OK(w, map[string]any{"preferences": preferences})
}

// setNotificationPreference 改通知偏好。
func (h *handlers) setNotificationPreference(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	var req notifyPrefReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if !validNotifyPreference(req.Category, req.Channel) {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"preference": "不支持的通知类别或渠道",
		}))
		return
	}
	if req.Category == "transactional" && !req.Enabled {
		// 交易类通知（支付凭据这些）不允许关闭。数据库上也有约束兜底，
		// 这里先拦一次是为了给出能看懂的提示，而不是一条约束违反错误。
		httpx.Fail(w, r, h.d.Log,
			httpx.New(httpx.CodeValidationFailed, "交易类通知无法关闭"))
		return
	}

	if err := h.d.Notify.SetPreference(r.Context(), p.TenantID, p.UserID,
		req.Category, req.Channel, req.Enabled); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}
