package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

type handlers struct{ d Deps }

func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, map[string]string{"status": "ok"})
}

func (h *handlers) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutCtx(r, 3*time.Second)
	defer cancel()
	if err := h.d.Pool.Ping(ctx); err != nil {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.OK(w, map[string]string{"status": "ok"})
}

//------------------------------------------------------------------------------
// 认证
//------------------------------------------------------------------------------

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type reauthReq struct {
	Password string `json:"password"`
}

// reauth 用当前口令换一枚 rat 刷新过的令牌。
//
// 53 条写路由挂着 RequireRecentReauth，要求令牌里的 rat 在 15 分钟以内。
// 而 rat 只在登录那一刻写入，令牌本身却活 720 小时 —— 登录满一刻钟，
// 后台就事实上变成只读：接入命令、编辑保存、删除、开单全部报「此操作
// 需要重新验证身份」，而在这个接口之前，系统里没有任何地方能完成那个
// 「重新验证」。门一直在，钥匙从来没配过。
//
// 这不是重新登录：不换 refresh token、不轮换会话，只是给同一个会话
// 重新盖一次「刚刚确认过是本人」的时间戳。
func (h *handlers) reauth(w http.ResponseWriter, r *http.Request) {
	var req reauthReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Identity.Reauth(r.Context(),
		httpx.TenantIDFrom(r.Context()), identity.ReauthInput{
			UserID:    p.UserID,
			SessionID: p.SessionID,
			Password:  req.Password,
			Audience:  string(configDomainAdmin),
			APIDomain: "admin",
			IPHash:    crypto.HashIdentifier(h.d.Cfg.MasterKey, httpx.ClientIP(r)),
			IP:        httpx.ClientIP(r),
			UserAgent: r.UserAgent(),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"access_token": out.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   out.ExpiresIn,
	})
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	out, err := h.d.Identity.Login(r.Context(),
		httpx.TenantIDFrom(r.Context()), identity.LoginInput{
			Email:     req.Email,
			Password:  req.Password,
			IPHash:    crypto.HashIdentifier(h.d.Cfg.MasterKey, httpx.ClientIP(r)),
			IP:        httpx.ClientIP(r),
			UserAgent: r.UserAgent(),
			// 关键：签发 admin 域会话。无角色的账号即便口令正确也会被拒，
			// 且对外错误与口令错误完全一致（IAM-006）。
			Audience: string(configDomainAdmin),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.OK(w, map[string]any{
		"access_token": out.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   out.ExpiresIn,
		"user_id":      out.UserID,
		// 前端据此决定显示哪些菜单；真正的拦截在网关，这里只是体验
		"permissions": out.Permissions,
	})
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Identity.LogoutCurrentSession(r.Context(), httpx.TenantIDFrom(r.Context()), identity.LogoutInput{
		UserID:    p.UserID,
		SessionID: p.SessionID,
		Audience:  p.Audience,
	}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.NoContent(w)
}

func (h *handlers) me(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	httpx.OK(w, map[string]any{
		"user_id":     p.UserID,
		"kind":        p.Kind,
		"permissions": p.Permissions,
		"reauthed":    p.ReauthedRecently,
	})
}

// changePassword rotates the currently authenticated administrator's password.
// The identity service revokes every session and active refresh token in the
// same transaction, including the current administrator session.
func (h *handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"old_password": "当前密码必填",
			"new_password": "新密码必填",
		}))
		return
	}
	if err := h.d.Identity.ChangePassword(r.Context(), p.TenantID, identity.ChangePasswordInput{
		UserID:      p.UserID,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
		APIDomain:   "admin",
		IP:          httpx.ClientIP(r),
		UserAgent:   r.UserAgent(),
	}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "reauthenticate": true})
}

//------------------------------------------------------------------------------
// 仪表盘
//------------------------------------------------------------------------------

func (h *handlers) overview(w http.ResponseWriter, r *http.Request) {
	o, err := h.d.Ops.Overview(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, o)
}

//------------------------------------------------------------------------------
// 用户
//------------------------------------------------------------------------------

func (h *handlers) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, total, err := h.d.Ops.ListUsers(r.Context(), httpx.TenantIDFrom(r.Context()),
		adminops.ListUsersInput{
			Query:  q.Get("q"),
			Status: q.Get("status"),
			Limit:  atoiDefault(q.Get("limit"), 25),
			Offset: atoiDefault(q.Get("offset"), 0),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"users": rows, "total": total})
}

func (h *handlers) getUser(w http.ResponseWriter, r *http.Request) {
	d, err := h.d.Ops.GetUser(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, d)
}

type setStatusReq struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type rotateSubReq struct {
	Reason string `json:"reason"`
}

// rotateSubscriptionLink 管理员替用户换一条订阅链接。
//
// 订阅链接泄露是用户自己最搞不定的事：他把链接发到群里，回头流量被别人
// 用光，来找客服——而客服这边原先没有任何按钮。用户自助那条
// （/v1/me/subscriptions/{id}/rotate）带所有权校验，管理员用不了。
func (h *handlers) rotateSubscriptionLink(w http.ResponseWriter, r *http.Request) {
	var req rotateSubReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if utf8.RuneCountInString(strings.TrimSpace(req.Reason)) < 5 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"reason": "请写清为什么要换这条订阅链接，至少 5 个字。这条会进审计"}))
		return
	}
	out, err := h.d.Subscription.AdminRotate(r.Context(),
		httpx.TenantIDFrom(r.Context()), subscription.AdminRotateInput{
			SubscriptionID: chi.URLParam(r, "id"),
			ActorID:        httpx.PrincipalFrom(r.Context()).UserID,
			Reason:         strings.TrimSpace(req.Reason),
			APIDomain:      "admin",
			IP:             httpx.ClientIP(r),
			UserAgent:      r.UserAgent(),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 令牌明文只在这一次返回：库里存的是哈希与信封密文，关掉这个响应
	// 就再也取不出来。界面上要提示管理员立刻转交给用户。
	httpx.OK(w, map[string]any{
		"token": out.Token, "user_email": out.UserEmail, "old_revoked": true})
}

type adminResetPasswordReq struct {
	NewPassword string `json:"new_password"`
	Reason      string `json:"reason"`
}

// resetUserPassword 管理员替用户重置密码。
//
// 在这之前用户忘了密码就没救了：找回密码没实现、改密码要填旧密码、
// 管理员也插不上手，而这台机器 SMTP 出站被封、邮件发不出去。付费用户
// 换台设备想不起密码，只能退款。
//
// xboard 后台编辑用户时就有个 password 字段，管理员填了就改。用户找
// 客服，客服改完告诉他。不是最好的体验，但不依赖任何外部服务。
func (h *handlers) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	var req adminResetPasswordReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if utf8.RuneCountInString(strings.TrimSpace(req.Reason)) < 5 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"reason": "请写清为什么要改这个用户的密码，5 到 500 字。这条会进审计"}))
		return
	}
	if err := h.d.Identity.AdminResetPassword(r.Context(),
		httpx.TenantIDFrom(r.Context()), identity.AdminResetPasswordInput{
			TargetUserID: chi.URLParam(r, "id"),
			ActorID:      httpx.PrincipalFrom(r.Context()).UserID,
			NewPassword:  req.NewPassword,
			Reason:       strings.TrimSpace(req.Reason),
			APIDomain:    "admin",
			IP:           httpx.ClientIP(r),
			UserAgent:    r.UserAgent(),
		}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 不回显新密码：管理员自己填的，本来就知道；写进响应体就会顺着
	// 日志、浏览器历史、截图流出去。
	httpx.OK(w, map[string]any{"ok": true, "sessions_revoked": true})
}

func (h *handlers) setUserStatus(w http.ResponseWriter, r *http.Request) {
	var req setStatusReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	err := h.d.Ops.SetUserStatus(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"),
		req.Status, req.Reason)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "status": req.Status})
}

//------------------------------------------------------------------------------
// 订单
//------------------------------------------------------------------------------

func (h *handlers) listOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, total, err := h.d.Ops.ListOrders(r.Context(), httpx.TenantIDFrom(r.Context()),
		adminops.ListOrdersInput{
			Query:  q.Get("q"),
			Status: q.Get("status"),
			From:   parseDayStart(q.Get("from")),
			To:     parseDayEnd(q.Get("to")),
			Limit:  atoiDefault(q.Get("limit"), 25),
			Offset: atoiDefault(q.Get("offset"), 0),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"orders": rows, "total": total})
}

func (h *handlers) getOrder(w http.ResponseWriter, r *http.Request) {
	order, err := h.d.Ops.GetOrder(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"order": order})
}

func (h *handlers) getOrderPayments(w http.ResponseWriter, r *http.Request) {
	history, err := h.d.Ops.GetOrderPaymentHistory(r.Context(),
		httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, history)
}

func (h *handlers) cancelOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedStateVersion int64  `json:"expected_state_version"`
		Reason               string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Billing.AdminCancelOrder(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.AdminCancelOrderInput{
			OrderID: chi.URLParam(r, "id"), ActorID: httpx.PrincipalFrom(r.Context()).UserID,
			ExpectedStateVersion: req.ExpectedStateVersion, Reason: req.Reason,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"order": out, "already_terminal": out.AlreadyTerminal})
}

//------------------------------------------------------------------------------
// 套餐
//------------------------------------------------------------------------------

func (h *handlers) listPlans(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.Ops.ListPlans(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"plans": rows})
}

//------------------------------------------------------------------------------
// 支付渠道
//------------------------------------------------------------------------------

func (h *handlers) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.Ops.ListProviders(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"providers": rows})
}

type toggleProviderReq struct {
	Enabled      bool `json:"enabled"`
	AcceptingNew bool `json:"accepting_new"`
}

func (h *handlers) toggleProvider(w http.ResponseWriter, r *http.Request) {
	var req toggleProviderReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	err := h.d.Ops.SetProviderEnabled(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "code"),
		req.Enabled, req.AcceptingNew)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

//------------------------------------------------------------------------------
// 审计与开关
//------------------------------------------------------------------------------

func (h *handlers) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, total, err := h.d.Ops.ListAudit(r.Context(), httpx.TenantIDFrom(r.Context()),
		atoiDefault(q.Get("limit"), 50), atoiDefault(q.Get("offset"), 0),
		adminops.AuditFilter{
			ActionPrefix: q.Get("action"),
			ActorKind:    q.Get("actor_kind"),
			Outcome:      q.Get("outcome"),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"events": rows, "total": total})
}

func (h *handlers) listSwitches(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.Ops.ListSwitches(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"switches": rows})
}

type switchReq struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

func (h *handlers) setSwitch(w http.ResponseWriter, r *http.Request) {
	var req switchReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	err := h.d.Ops.SetSwitch(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "code"),
		req.Enabled, req.Reason)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "enabled": req.Enabled})
}

//------------------------------------------------------------------------------

// parseDayStart / parseDayEnd 把 YYYY-MM-DD 变成半开区间 [from, to)。
//
// 前端给的是日期不是时刻，「到 8 月 20 日」在人的理解里包含那一整天。
// 如果直接拿 2026-08-20T00:00:00 当上界，那天的单会全部消失 —— 这类
// 差一天的过滤最难发现，因为结果看起来完全合理，只是少了一天。
// 所以上界取次日零点，用 < 而不是 <=。
//
// 解析不了就当没填。日期是过滤条件不是必填项，为一个打错的日期把整页
// 变成报错，不如让人看到全量再改。
func parseDayStart(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &t
}

func parseDayEnd(s string) *time.Time {
	t := parseDayStart(s)
	if t == nil {
		return nil
	}
	next := t.AddDate(0, 0, 1)
	return &next
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

//------------------------------------------------------------------------------
// 工单（OPS-001）
//------------------------------------------------------------------------------

// ticketAssignees 返回当前租户内可被指派工单的客服目录。
// 候选人来源必须是专用权限过滤查询，不能从普通用户列表推断。
func (h *handlers) ticketAssignees(w http.ResponseWriter, r *http.Request) {
	assignees, err := h.d.Support.ListEligibleAssignees(
		r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"assignees": assignees})
}

func (h *handlers) ticketQueue(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ts, total, err := h.d.Support.ListForAgent(r.Context(), httpx.TenantIDFrom(r.Context()),
		support.ListFilter{
			Status:     q.Get("status"),
			Priority:   q.Get("priority"),
			Category:   q.Get("category"),
			AssignedTo: q.Get("assigned_to"),
			Query:      q.Get("q"),
			OnlyBreach: q.Get("breached") == "1",
			Limit:      atoiDefault(q.Get("limit"), 25),
			Offset:     atoiDefault(q.Get("offset"), 0),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"tickets": ts, "total": total})
}

func (h *handlers) ticketDetail(w http.ResponseWriter, r *http.Request) {
	t, err := h.d.Support.GetForAgent(r.Context(),
		httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, t)
}

type agentReplyReq struct {
	Body         string `json:"body"`
	InternalNote bool   `json:"internal_note"`
}

func (h *handlers) ticketReply(w http.ResponseWriter, r *http.Request) {
	var req agentReplyReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("admin ticket reply claim missing")))
		return
	}
	out, err := h.d.Support.ReplyAsAgentAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		support.AgentReplyInput{
			AgentID:      httpx.PrincipalFrom(r.Context()).UserID,
			TicketID:     chi.URLParam(r, "id"),
			Body:         req.Body,
			InternalNote: req.InternalNote,
		}, claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	// 客服回复完，立刻推给提问的人 —— 这正是「工单实时」要的那一下。
	// 查归属失败只记日志：回复本身已经成功，不能因为推送失败让接口报错。
	tenantID := httpx.TenantIDFrom(r.Context())
	ticketID := chi.URLParam(r, "id")
	if owner, err := h.d.Support.TicketOwner(r.Context(), tenantID, ticketID); err != nil {
		h.d.Log.Warn("推送工单更新失败：查不到归属", "ticket", ticketID, "err", err)
	} else if h.d.Realtime != nil {
		h.d.Realtime.Publish(r.Context(), realtime.ChannelUser(tenantID, owner),
			"ticket.updated", map[string]any{"ticket_id": ticketID})
	}

	httpx.WritePrepared(w, out.PreparedResponse())
}

type ticketAssignReq struct {
	AssignedTo string `json:"assigned_to"`
}

func (h *handlers) ticketAssign(w http.ResponseWriter, r *http.Request) {
	var req ticketAssignReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("admin ticket assign claim missing")))
		return
	}
	out, err := h.d.Support.AssignAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"),
		req.AssignedTo, claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

type ticketStatusReq struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (h *handlers) ticketStatus(w http.ResponseWriter, r *http.Request) {
	var req ticketStatusReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("admin ticket status claim missing")))
		return
	}
	out, err := h.d.Support.SetStatusAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"),
		req.Status, req.Reason, claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

// ticketEscalate 手工触发 SLA 扫描。后台每 5 分钟也会自动跑一次，
// 这个接口用于演示与排障时立刻看到效果。
func (h *handlers) ticketEscalate(w http.ResponseWriter, r *http.Request) {
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("admin ticket escalation claim missing")))
		return
	}
	out, err := h.d.Support.EscalateOverdueAsAdminAtomic(
		r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, claim,
	)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

//------------------------------------------------------------------------------
// 节点（NODE / AGT）
//------------------------------------------------------------------------------

func (h *handlers) nodeList(w http.ResponseWriter, r *http.Request) {
	includeRetired := r.URL.Query().Get("include_retired") == "1"
	type row struct {
		ID string `json:"id"`
		// NodeNo 是给人看的短编号（101、102…）。UUID 仍然是主键和接口
		// 参数，但后台列表第一列、工单和群里说的都是这个号——
		// 「103 挂了」比念一遍 UUID 快得多，也不会念错。
		NodeNo        int        `json:"node_no"`
		RowVersion    int64      `json:"row_version"`
		Name          string     `json:"name"`
		Status        string     `json:"status"`
		ServingStatus string     `json:"serving_status"`
		ServerID      *string    `json:"server_id"`
		ServerName    *string    `json:"server_name"`
		PoolID        *string    `json:"pool_id"`
		PoolName      *string    `json:"pool_name"`
		AgentVer      *string    `json:"agent_version"`
		Hostname      *string    `json:"hostname"`
		PublicIP      *string    `json:"public_ipv4"`
		CPUCores      *int       `json:"cpu_cores"`
		MemoryMB      *int       `json:"memory_mb"`
		DiskGB        *int       `json:"disk_gb"`
		HealthScore   *int       `json:"health_score"`
		AppliedVer    *int       `json:"applied_config_version"`
		DesiredVer    *int       `json:"desired_config_version"`
		LastBeat      *time.Time `json:"last_heartbeat_at"`
		// Stale 由数据库算：心跳超过 90 秒即视为失联（AGT-004 心跳超时停止新分配）
		Stale bool `json:"stale"`
		// Delivered 回答运营真正关心的那个问题：这个节点现在会不会
		// 出现在用户的订阅里。
		//
		// 它和 Stale 是两回事，不能合并：Stale 是 90 秒的实时视角，
		// 给管理员看「刚刚是不是失联了」；下发用的是 10 分钟窗口，
		// 而且从未心跳过的节点一律不发。只看 Stale 会得出错误结论 ——
		// 一个 stale=true 的节点很可能仍在下发（心跳刚断两分钟），
		// 而一个从未心跳的节点即使刚建好也永远不会下发。
		//
		// 少了这一列，管理员就得自己在脑子里跑一遍下发规则。
		Delivered bool `json:"delivered_to_users"`
		// DeliveryNote 说明「为什么不下发」。没有它，界面上就只是一个
		// 灰点，运营还得来问。
		DeliveryNote string    `json:"delivery_note"`
		Serial       *int      `json:"identity_serial"`
		CreatedAt    time.Time `json:"created_at"`

		// 以下是对外服务配置。表单保存时是全量覆盖，
		// 所以这里必须原样带回，缺一个字段就会在保存时被清掉。
		NodeType              *string         `json:"node_type"`
		ServerHost            *string         `json:"server_host"`
		ServerPort            *int            `json:"server_port"`
		TrafficRate           float64         `json:"traffic_rate"`
		DisplayName           *string         `json:"display_name"`
		Kernel                string          `json:"kernel"`
		Protocol              json.RawMessage `json:"protocol_config"`
		ProtocolSchemaVersion int             `json:"protocol_schema_version"`
		ConfigValidatedAt     *time.Time      `json:"config_validated_at"`
		SortOrder             int             `json:"sort_order"`

		// 运营视角的三项。都是聚合出来的，不是 nodes 表上的列。
		//
		// OnlineUsers 是在线订阅数，OnlineIPs 是在线 IP 数：后者明显大于
		// 前者就说明有人在共享账号，这是两个不能合并的指标。
		OnlineUsers int `json:"online_users"`
		OnlineIPs   int `json:"online_ips"`
		// TrafficBytes 是近 30 天上下行合计（原始量，未乘倍率）。
		// 不做全量累计：node_traffic_reports 每节点每分钟一条，
		// 全表 SUM 会随运行时长线性变慢，而列表页每次打开都要算。
		TrafficBytes int64 `json:"traffic_bytes"`
		// GrantedPlans 是通过所属分组授权到这个节点的套餐。
		// 对应 xboard 的「权限组」——回答「谁能用上这个节点」。
		GrantedPlans []string `json:"granted_plans"`
	}
	out := []row{}
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: httpx.TenantIDFrom(r.Context())},
		func(tx pgx.Tx) error {
			// 在线数与流量先各自聚合成一张小表再 JOIN，而不是给每个节点
			// 挂相关子查询：后者会把同一个时间窗扫 200 遍。
			rows, err := tx.Query(r.Context(), `
				WITH alive AS (
				  SELECT node_id,
				         count(DISTINCT subscription_id)::int AS users,
				         count(*)::int AS ips
				    FROM node_alive_ips
				   WHERE tenant_id = $1 AND last_seen_at > now() - interval '5 minutes'
				   GROUP BY node_id
				), traffic AS (
				  SELECT node_id, sum(total_upload + total_download)::bigint AS bytes
				    FROM node_traffic_reports
				   WHERE tenant_id = $1 AND duplicate_of IS NULL
				     AND received_at > now() - interval '30 days'
				   GROUP BY node_id
				), grants AS (
				  SELECT pnp.pool_id, array_agg(DISTINCT pl.name) AS plans
				    FROM plan_node_pools pnp
				    JOIN plan_versions pv ON pv.tenant_id = pnp.tenant_id
				                         AND pv.id = pnp.plan_version_id
				    JOIN plans pl ON pl.tenant_id = pv.tenant_id AND pl.id = pv.plan_id
				   WHERE pnp.tenant_id = $1
				   GROUP BY pnp.pool_id
				)
				SELECT n.id, n.row_version, n.name, n.status, n.serving_status,
				       n.server_id, s.name, n.pool_id, p.name, n.agent_version, n.hostname,
				       host(n.public_ipv4), n.cpu_cores, n.memory_mb, n.disk_gb,
				       n.health_score, n.applied_config_version, n.desired_config_version,
				       n.last_heartbeat_at,
				       (n.last_heartbeat_at IS NULL
				        OR n.last_heartbeat_at < now() - interval '90 seconds') AS stale,
				       i.serial, n.created_at,
				       n.node_type, n.server_host, n.server_port,
				       n.traffic_rate, n.display_name,
				       coalesce(n.kernel,'auto'), n.protocol_config,
				       n.protocol_schema_version,n.config_validated_at,n.sort_order,n.node_no,
				       coalesce(a.users,0), coalesce(a.ips,0), coalesce(t.bytes,0),
				       coalesce(g.plans, '{}')
				  FROM nodes n
				  LEFT JOIN node_pools p ON p.id = n.pool_id
				  LEFT JOIN servers s ON s.id=n.server_id AND s.tenant_id=n.tenant_id
				  LEFT JOIN node_identities i ON i.node_id = n.id AND i.status = 'active'
				  LEFT JOIN alive a ON a.node_id = n.id
				  LEFT JOIN traffic t ON t.node_id = n.id
				  LEFT JOIN grants g ON g.pool_id = n.pool_id
				 WHERE n.tenant_id = $1 AND n.status <> 'destroyed'
				   AND ($2::boolean OR n.serving_status <> 'retired')
				 ORDER BY n.created_at DESC LIMIT 200`,
				httpx.TenantIDFrom(r.Context()), includeRetired)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var x row
				if err := rows.Scan(&x.ID, &x.RowVersion, &x.Name, &x.Status, &x.ServingStatus,
					&x.ServerID, &x.ServerName, &x.PoolID, &x.PoolName, &x.AgentVer,
					&x.Hostname, &x.PublicIP, &x.CPUCores, &x.MemoryMB, &x.DiskGB,
					&x.HealthScore, &x.AppliedVer, &x.DesiredVer, &x.LastBeat,
					&x.Stale, &x.Serial, &x.CreatedAt,
					&x.NodeType, &x.ServerHost, &x.ServerPort,
					&x.TrafficRate, &x.DisplayName, &x.Kernel, &x.Protocol,
					&x.ProtocolSchemaVersion, &x.ConfigValidatedAt, &x.SortOrder, &x.NodeNo,
					&x.OnlineUsers, &x.OnlineIPs, &x.TrafficBytes,
					&x.GrantedPlans); err != nil {
					return err
				}
				// 心跳的两个事实由 Go 侧从 LastBeat 推出，不在 SQL 里算。
				//
				// 一开始是写成 SQL 布尔列的，结果 last_heartbeat_at 为 NULL 时
				// (NULL >= …) 求值成 NULL 而不是 false，扫进 bool 直接把整个
				// 节点列表打成 500。可以用 COALESCE 补，但那是在给一个本来
				// 就不该存在的三值逻辑打补丁 —— LastBeat 是 *time.Time，
				// 「从未心跳」本来就由 nil 表达得清清楚楚。
				everSeen := x.LastBeat != nil
				beatFresh := everSeen &&
					time.Since(*x.LastBeat) < subscription.HeartbeatFreshWindow
				x.Delivered, x.DeliveryNote = subscription.DeliveryState(
					x.ServingStatus, everSeen, beatFresh)
				x.Protocol = nodefabric.RedactProtocolConfig(x.Protocol)
				out = append(out, x)
			}
			return rows.Err()
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, map[string]any{"nodes": out, "total": len(out)})
}

// panelURLFrom 推导机器该回连的面板地址。
//
// 后台通常挂在反代后面，r.TLS 和 r.Host 都反映不出用户实际访问的入口，
// 所以优先信反代给的 X-Forwarded-*。推错了的后果很直接：命令复制到
// 机器上跑，连的是内网地址，接不进来。
type issueTokenReq struct {
	NodeName   string `json:"node_name"`
	PoolID     string `json:"pool_id"`
	ServerID   string `json:"server_id"`
	TTLMinutes int    `json:"ttl_minutes"`
}

func (h *handlers) nodeIssueToken(w http.ResponseWriter, r *http.Request) {
	var req issueTokenReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	out, err := h.d.Node.IssueBootstrapToken(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.IssueTokenInput{
			ActorID:    httpx.PrincipalFrom(r.Context()).UserID,
			NodeName:   req.NodeName,
			PoolID:     req.PoolID,
			ServerID:   req.ServerID,
			PanelURL:   panelURL,
			TTLMinutes: req.TTLMinutes,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

// serverIssueToken 给一台已建好的服务器签发接入令牌。
//
// 和 nodeIssueToken 是同一件事，区别只在 server_id 从路径来、节点名默认
// 取服务器名。单独开一个入口是为了让「在服务器详情里拿接入命令」这个
// 动作不需要前端自己拼节点名——那正是容易填错、导致同一台机器接出两条
// 记录的地方。
func (h *handlers) serverIssueToken(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	var req issueTokenReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	tenantID := httpx.TenantIDFrom(r.Context())
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	nodeName := strings.TrimSpace(req.NodeName)
	if nodeName == "" {
		srv, err := h.d.Node.GetServer(r.Context(), tenantID, serverID)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, err)
			return
		}
		nodeName = srv.Name
	}
	out, err := h.d.Node.IssueBootstrapToken(r.Context(), tenantID,
		nodefabric.IssueTokenInput{
			ActorID:    httpx.PrincipalFrom(r.Context()).UserID,
			NodeName:   nodeName,
			PoolID:     req.PoolID,
			ServerID:   serverID,
			PanelURL:   panelURL,
			TTLMinutes: req.TTLMinutes,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

type nodeStatusReq struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

func nodeStatusLockSQL() string {
	return `SELECT status,row_version,
		COALESCE(node_type IS NOT NULL
		AND server_port BETWEEN 1 AND 65535
		AND ` + nodefabric.StableProtocolReadySQL("") + `, false)
	FROM nodes WHERE tenant_id=$1 AND id=$2 FOR UPDATE`
}

// nodeSetStatus 推进节点状态。合法性由数据库的 node_transitions 表强制 ——
// 这里不重复实现一遍状态机，避免两处规则漂移（NODE-010）。
func (h *handlers) nodeSetStatus(w http.ResponseWriter, r *http.Request) {
	var req nodeStatusReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	id := chi.URLParam(r, "id")
	actor := httpx.PrincipalFrom(r.Context()).UserID
	tenantID := httpx.TenantIDFrom(r.Context())
	terminal := req.Status == "retired" || req.Status == "destroyed"

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			if terminal {
				if _, err := tx.Exec(r.Context(),
					`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
					"node-config-release/"+tenantID); err != nil {
					return err
				}
			}
			var before string
			var currentVersion int64
			var protocolReady bool
			if err := tx.QueryRow(r.Context(), nodeStatusLockSQL(),
				tenantID, id).Scan(&before, &currentVersion, &protocolReady); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeNotFound, "节点不存在")
				}
				return err
			}
			if req.RowVersion <= 0 {
				return httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
			}
			if currentVersion != req.RowVersion {
				return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
					Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
			}
			servingStatus, serverStatus := projectNodeLifecycle(req.Status, protocolReady)
			if _, err := tx.Exec(r.Context(),
				`UPDATE nodes SET status=$3,serving_status=$4,
				 desired_config_version=CASE WHEN $4='retired' THEN NULL ELSE desired_config_version END,
				 row_version=row_version+1
				 WHERE tenant_id=$1 AND id=$2 AND row_version=$5`,
				tenantID, id, req.Status, servingStatus, currentVersion); err != nil {
				// 违反状态机的跳转由触发器抛 check_violation
				if db.IsCheckViolation(err) {
					return httpx.New(httpx.CodeConflict, db.Message(err))
				}
				return err
			}
			// Server 与 Node 的状态词不同；第二条更新单独写入正确的宿主状态。
			if _, err := tx.Exec(r.Context(), `
				UPDATE servers SET status=$3, status_reason=nullif($4,''),
				       entered_status_at=now(), row_version=row_version+1,
				       retired_at=CASE WHEN $3='retired' THEN now() ELSE retired_at END
				 WHERE tenant_id=$1 AND id=(SELECT server_id FROM nodes WHERE tenant_id=$1 AND id=$2)
				   AND control_node_id=$2 AND deleted_at IS NULL`,
				tenantID, id, serverStatus, req.Reason); err != nil {
				return err
			}
			if terminal {
				if _, err := tx.Exec(r.Context(), `UPDATE node_identities
					SET status='revoked',revoked_at=now(),revoked_reason='节点已退役'
					WHERE tenant_id=$1 AND node_id=$2 AND status='active'`, tenantID, id); err != nil {
					return err
				}
			}
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.status_change", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID:    httpx.RequestIDFrom(r.Context()),
				BeforeDigest: map[string]any{"status": before},
				AfterDigest:  map[string]any{"status": req.Status, "reason": req.Reason},
			})
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "row_version": req.RowVersion + 1})
}

// projectNodeLifecycle keeps the legacy Node state machine and the split
// serving/host state machines in one reviewed mapping. Canary is serviceable
// for validation but remains absent from subscriber delivery; draining keeps
// data-plane authentication alive while stopping new subscription allocation.
func projectNodeLifecycle(nodeStatus string, protocolReady bool) (servingStatus, serverStatus string) {
	var serving, server string
	switch nodeStatus {
	case "active", "canary":
		serving, server = "active", "ready"
	case "draining":
		serving, server = "draining", "draining"
	case "maintenance":
		serving, server = "disabled", "maintenance"
	case "unhealthy":
		serving, server = "disabled", "unhealthy"
	case "quarantined":
		serving, server = "disabled", "quarantined"
	case "retired", "destroyed":
		serving, server = "retired", "retired"
	default:
		serving, server = "draft", "draft"
	}
	if !protocolReady && (serving == "active" || serving == "draining") {
		serving = "disabled"
	}
	return serving, server
}

// nodeRevokeIdentity 吊销节点身份（NODE-014）。
// 吊销后该节点的 Agent 下一次请求就会被拒，必须重新引导。
func (h *handlers) nodeRevokeIdentity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	actor := httpx.PrincipalFrom(r.Context()).UserID
	tenantID := httpx.TenantIDFrom(r.Context())

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			ct, err := tx.Exec(r.Context(), `
				UPDATE node_identities
				   SET status='revoked', revoked_at=now(), revoked_reason='管理员手工吊销'
				 WHERE tenant_id=$1 AND node_id=$2 AND status='active'`, tenantID, id)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return httpx.New(httpx.CodeNotFound, "该节点没有有效身份")
			}
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.identity.revoke", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID: httpx.RequestIDFrom(r.Context()),
			})
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

type publishCfgReq struct {
	Scope    string          `json:"scope"`     // global / pool / node
	ScopeRef string          `json:"scope_ref"` // pool/node 时必填
	Payload  json.RawMessage `json:"payload"`
}

// nodePublishConfig 发布一层配置并签名（AGT-007）。
func (h *handlers) nodePublishConfig(w http.ResponseWriter, r *http.Request) {
	var req publishCfgReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Scope != "global" && req.Scope != "pool" && req.Scope != "node" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"scope": "只支持 global/pool/node"}))
		return
	}
	if req.Scope != "global" && req.ScopeRef == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"scope_ref": "该层级必须指定对象"}))
		return
	}
	var probe map[string]any
	if err := json.Unmarshal(req.Payload, &probe); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"payload": "必须是 JSON 对象"}))
		return
	}

	out, err := h.d.Node.PublishConfig(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.PublishInput{
			ActorID:  httpx.PrincipalFrom(r.Context()).UserID,
			Scope:    req.Scope,
			ScopeRef: req.ScopeRef,
			Payload:  req.Payload,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

type nodeProtoReq struct {
	RowVersion  int64           `json:"row_version"`
	NodeType    string          `json:"node_type"`
	ServerHost  string          `json:"server_host"`
	ServerPort  int             `json:"server_port"`
	TrafficRate float64         `json:"traffic_rate"`
	DisplayName string          `json:"display_name"`
	Kernel      string          `json:"kernel"`
	Protocol    json.RawMessage `json:"protocol_config"`
}

// --- 出站与分流（NODE-012）---

type routingOutbound struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
}

type routingRule struct {
	Priority    int             `json:"priority"`
	Matcher     json.RawMessage `json:"matcher"`
	OutboundTag string          `json:"outbound_tag"`
	Enabled     bool            `json:"enabled"`
	Note        string          `json:"note"`
}

type routingPayload struct {
	RowVersion int64             `json:"row_version"`
	Outbounds  []routingOutbound `json:"outbounds"`
	Routes     []routingRule     `json:"routes"`
}

// nodeGetRouting 读取某节点的出站与分流。
func (h *handlers) nodeGetRouting(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	out := routingPayload{Outbounds: []routingOutbound{}, Routes: []routingRule{}}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT row_version FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, id).Scan(&out.RowVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT tag, type, settings FROM node_outbounds
			 WHERE tenant_id=$1 AND node_id=$2::uuid
			 ORDER BY sort_order, tag`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o routingOutbound
			if err := rows.Scan(&o.Tag, &o.Type, &o.Settings); err != nil {
				return err
			}
			out.Outbounds = append(out.Outbounds, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		rrows, err := tx.Query(r.Context(), `
			SELECT priority, matcher, outbound_tag, enabled, coalesce(note,'')
			  FROM node_routes
			 WHERE tenant_id=$1 AND node_id=$2::uuid
			 ORDER BY priority, created_at`, tenantID, id)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var x routingRule
			if err := rrows.Scan(&x.Priority, &x.Matcher, &x.OutboundTag,
				&x.Enabled, &x.Note); err != nil {
				return err
			}
			out.Routes = append(out.Routes, x)
		}
		return rrows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, out)
}

// nodeSetRouting 全量替换某节点的出站与分流。
//
// 全量而非增量：分流规则是有顺序的整体，增量接口会让「调整顺序」
// 这种最常见的操作变成一串难以原子化的增删。整体替换在一个事务里完成，
// 要么全成要么全不成，也不会出现规则指向刚被删掉的出站这种中间态。
func (h *handlers) nodeSetRouting(w http.ResponseWriter, r *http.Request) {
	var req routingPayload
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	// 先在内存里把引用关系校验掉。留到数据库外键去挡的话，
	// 报出来的是一句 SQL 错误，用户看不懂错在第几条规则
	tags := map[string]bool{"direct": true, "block": true}
	for i := range req.Outbounds {
		o := &req.Outbounds[i]
		o.Tag = strings.TrimSpace(o.Tag)
		o.Type = strings.ToLower(strings.TrimSpace(o.Type))
		if o.Tag == "" || o.Type == "" {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"outbounds": "每条出站都要有 tag 和 type"}))
			return
		}
		tagKey := strings.ToLower(o.Tag)
		if tags[tagKey] {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"outbounds": "出站 tag 重复或占用内置名称：" + o.Tag}))
			return
		}
		tags[tagKey] = true
	}
	lastEnabled := -1
	for i, x := range req.Routes {
		if x.Enabled {
			lastEnabled = i
		}
	}
	for i, x := range req.Routes {
		if !tags[strings.ToLower(strings.TrimSpace(x.OutboundTag))] {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条规则指向不存在的出站 %q", i+1, x.OutboundTag)}))
			return
		}
		empty, err := nodefabric.ValidateRoutingMatcher(x.Matcher)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条规则无法跨内核下发：%v", i+1, err)}))
			return
		}
		if x.Enabled && empty && i != lastEnabled {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条空匹配兜底规则必须放在最后", i+1)}))
			return
		}
	}

	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	actor := httpx.PrincipalFrom(r.Context()).UserID
	if req.RowVersion <= 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"}))
		return
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			// Even an empty replacement must target a real Node. Locking the row also
			// serializes routing replacement with Node lifecycle/move operations.
			var lockedID string
			var currentVersion int64
			if err := tx.QueryRow(r.Context(), `SELECT id FROM nodes
				WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, id).Scan(&lockedID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.NotFoundOrForbidden()
				}
				return err
			}
			if err := tx.QueryRow(r.Context(), `SELECT row_version FROM nodes
				WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, id).Scan(&currentVersion); err != nil {
				return err
			}
			if currentVersion != req.RowVersion {
				return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
					Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
			}
			if _, err := tx.Exec(r.Context(),
				`DELETE FROM node_routes WHERE tenant_id=$1 AND node_id=$2::uuid`,
				tenantID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(),
				`DELETE FROM node_outbounds WHERE tenant_id=$1 AND node_id=$2::uuid`,
				tenantID, id); err != nil {
				return err
			}
			for i, o := range req.Outbounds {
				settings := o.Settings
				if len(settings) == 0 {
					settings = json.RawMessage("{}")
				}
				if _, err := tx.Exec(r.Context(), `
					INSERT INTO node_outbounds (tenant_id, node_id, tag, type, settings, sort_order)
					VALUES ($1,$2::uuid,$3,$4,$5,$6)`,
					tenantID, id, o.Tag, o.Type, settings, i*10); err != nil {
					return err
				}
			}
			for i, x := range req.Routes {
				matcher := x.Matcher
				if len(matcher) == 0 {
					matcher = json.RawMessage("{}")
				}
				pri := x.Priority
				if pri == 0 {
					pri = (i + 1) * 10
				}
				if _, err := tx.Exec(r.Context(), `
					INSERT INTO node_routes
					  (tenant_id, node_id, priority, matcher, outbound_tag, enabled, note)
					VALUES ($1,$2::uuid,$3,$4,$5,$6,nullif($7,''))`,
					tenantID, id, pri, matcher, x.OutboundTag, x.Enabled, x.Note); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(r.Context(), `UPDATE nodes SET row_version=row_version+1,
				config_source_generation=config_source_generation+1
				WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, tenantID, id, currentVersion); err != nil {
				return err
			}
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.routing.update", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID: httpx.RequestIDFrom(r.Context()),
				AfterDigest: map[string]any{
					"outbounds": len(req.Outbounds), "routes": len(req.Routes),
				},
			})
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "row_version": req.RowVersion + 1})
}

// nodeSetProtocol 配置节点的对外服务参数（UniProxy 下发给节点端的内容）。
func (h *handlers) nodeSetProtocol(w http.ResponseWriter, r *http.Request) {
	var req nodeProtoReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.TrafficRate <= 0 {
		req.TrafficRate = 1
	}
	if req.Kernel == "" {
		req.Kernel = "auto"
	}
	if len(req.Protocol) == 0 {
		req.Protocol = json.RawMessage(`{}`)
	}
	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	actor := httpx.PrincipalFrom(r.Context()).UserID
	req.NodeType = nodefabric.CanonicalNodeType(req.NodeType)
	// Keep the compatibility endpoint, but route it through the same stable
	// schema validation, optimistic lock and audit path as PATCH /nodes/{id}.
	out, err := h.d.Node.PatchAdminNode(r.Context(), tenantID, id, nodefabric.PatchAdminNodeInput{
		ActorID: actor, RowVersion: req.RowVersion, NodeType: &req.NodeType,
		ServerHost: &req.ServerHost, ServerPort: &req.ServerPort, Kernel: &req.Kernel,
		TrafficRate: &req.TrafficRate, DisplayName: &req.DisplayName,
		ProtocolConfig: &req.Protocol,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) nodeProtocolSchemas(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, map[string]any{"schemas": nodefabric.ProtocolSchemas()})
}

// nodeIssueServerToken 签发 UniProxy 接入令牌，明文只返回一次。
func (h *handlers) nodeIssueServerToken(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	tok, nodeType, err := h.d.Node.IssueServerToken(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, nodeID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if nodeType == "" {
		nodeType = "vless"
	}
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 一并给出可直接粘贴的安装命令。令牌只显示这一次；命令本身通过
	// 终端读取令牌，不把运行凭据嵌进 argv 或 shell history。
	install := nodefabric.RenderLegacyInstallCommand(panelURL, nodeID, nodeType)
	httpx.Created(w, map[string]any{
		"token":           tok,
		"node_type":       nodeType,
		"panel_url":       panelURL,
		"install_command": install,
		"hint":            "该令牌只显示一次。重新签发会立即作废旧令牌——正在运行的节点会拉配置失败（401）直到用新令牌重装，请确认后再执行安装命令。",
	})
}

func nullStrAdmin(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nodeMetrics 返回节点探针曲线。
func (h *handlers) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := h.d.Node.FetchMetrics(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"), atoiDefault(r.URL.Query().Get("minutes"), 60))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, m)
}

// nodeRealityKeypair 生成一对 REALITY 用的 x25519 密钥。
//
// 放在服务端而不是让管理员自己跑 xray x25519：那条路要求他能登上某台机器，
// 而且生成的私钥会经过他的剪贴板和终端历史。这里私钥只在这一次响应里出现，
// 保存后就只以密文形式留在协议配置里，读接口不回显。
func (h *handlers) nodeRealityKeypair(w http.ResponseWriter, r *http.Request) {
	priv, pub, err := nodefabric.GenerateRealityKeypair()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	// short id 一并给出：它不是密钥，但少了它客户端连不上，
	// 而「自己想一个十六进制串」是很容易填错的一步。
	var sid [4]byte
	if _, err := rand.Read(sid[:]); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, map[string]any{
		"private_key": priv,
		"public_key":  pub,
		"short_id":    hex.EncodeToString(sid[:]),
		"hint":        "私钥只在这一次返回，保存后无法再查看",
	})
}

type nodeDeleteReq struct {
	RowVersion int64  `json:"row_version"`
	Reason     string `json:"reason"`
}

// nodeDelete 删除一个已下线的逻辑节点。
func (h *handlers) nodeDelete(w http.ResponseWriter, r *http.Request) {
	var req nodeDeleteReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Node.DeleteNode(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.DeleteNodeInput{
			ID: chi.URLParam(r, "id"), RowVersion: req.RowVersion,
			ActorID: p.UserID, Reason: req.Reason,
		}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"deleted": true})
}
