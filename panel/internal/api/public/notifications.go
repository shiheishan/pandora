package public

// 站内信与通知偏好。
//
// 站内信不是单独的一套东西，就是投递记录里 channel='inapp' 的那些。
// 用户拉取即视为送达，读了才标 read_at —— 这两件事分开记，
// 因为「收到了」和「看了」在运营上是不同的信号。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// listNotifications 返回站内信收件箱。
func (h *handlers) listNotifications(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}

	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	onlyUnread := r.URL.Query().Get("unread") == "1"

	type item struct {
		ID      string            `json:"id"`
		Code    string            `json:"code"`
		Subject string            `json:"subject"`
		Body    string            `json:"body"`
		Vars    map[string]string `json:"-"`
		SentAt  any               `json:"sent_at"`
		ReadAt  any               `json:"read_at"`
	}
	out := []item{}
	unread := 0

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: p.TenantID, ActorID: p.UserID},
		func(tx pgx.Tx) error {
			// 模板与投递记录一起查：正文要用记录里的变量渲染模板，
			// 分两次查会让 N 条站内信变成 N+1 次查询
			q := `
				SELECT d.id::text, d.template_code,
				       COALESCE(t.subject,''), COALESCE(t.body,''),
				       COALESCE(d.payload,'{}'::jsonb), d.sent_at, d.read_at
				  FROM notification_deliveries d
				  LEFT JOIN notification_templates t
				    ON t.tenant_id = d.tenant_id AND t.code = d.template_code
				   AND t.channel = 'inapp' AND t.locale = 'zh-CN' AND t.status = 'active'
				 WHERE d.tenant_id = $1 AND d.user_id = $2::uuid
				   AND d.channel = 'inapp' AND d.status = 'sent'`
			if onlyUnread {
				q += ` AND d.read_at IS NULL`
			}
			q += ` ORDER BY d.created_at DESC LIMIT $3`

			rows, err := tx.Query(r.Context(), q, p.TenantID, p.UserID, limit)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var it item
				var vars map[string]any
				if err := rows.Scan(&it.ID, &it.Code, &it.Subject, &it.Body,
					&vars, &it.SentAt, &it.ReadAt); err != nil {
					return err
				}
				it.Subject = renderVars(it.Subject, vars)
				it.Body = renderVars(it.Body, vars)
				out = append(out, it)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			return tx.QueryRow(r.Context(), `
				SELECT count(*) FROM notification_deliveries
				 WHERE tenant_id = $1 AND user_id = $2::uuid
				   AND channel = 'inapp' AND status = 'sent' AND read_at IS NULL`,
				p.TenantID, p.UserID).Scan(&unread)
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"notifications": out, "unread": unread})
}

// markNotificationRead 标记单条已读。
func (h *handlers) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	id := chi.URLParam(r, "id")

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: p.TenantID, ActorID: p.UserID},
		func(tx pgx.Tx) error {
			// 条件里带 user_id：光凭 id 就能改的话，
			// 拿到别人的通知 ID 就能替他标已读
			_, err := tx.Exec(r.Context(), `
				UPDATE notification_deliveries SET read_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
				   AND channel = 'inapp' AND read_at IS NULL`,
				p.TenantID, id, p.UserID)
			return err
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// markAllRead 全部标记已读。
func (h *handlers) markAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: p.TenantID, ActorID: p.UserID},
		func(tx pgx.Tx) error {
			_, err := tx.Exec(r.Context(), `
				UPDATE notification_deliveries SET read_at = now()
				 WHERE tenant_id = $1 AND user_id = $2::uuid
				   AND channel = 'inapp' AND read_at IS NULL`,
				p.TenantID, p.UserID)
			return err
		})
	if err != nil {
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
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}

	overrides := map[string]bool{}
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: p.TenantID, ActorID: p.UserID},
		func(tx pgx.Tx) error {
			rows, err := tx.Query(r.Context(), `
				SELECT category, channel, enabled
				  FROM notification_preferences
				 WHERE tenant_id = $1 AND user_id = $2::uuid`, p.TenantID, p.UserID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var category, channel string
				var enabled bool
				if err := rows.Scan(&category, &channel, &enabled); err != nil {
					return err
				}
				overrides[category+"\x00"+channel] = enabled
			}
			return rows.Err()
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
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
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: p.TenantID, ActorID: p.UserID},
		func(tx pgx.Tx) error {
			_, err := tx.Exec(r.Context(), `
				INSERT INTO notification_preferences
					(tenant_id, user_id, category, channel, enabled)
				VALUES ($1,$2::uuid,$3,$4,$5)
				ON CONFLICT (tenant_id, user_id, category, channel)
				DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
				p.TenantID, p.UserID, req.Category, req.Channel, req.Enabled)
			return err
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// renderVars 用记录里的变量填模板。
func renderVars(tpl string, vars map[string]any) string {
	if len(vars) == 0 {
		return tpl
	}
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", fmt.Sprint(v))
	}
	return out
}
