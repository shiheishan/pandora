// [INPUT]: 依赖 domain 的 adminops/billing/identity/subscription 服务、middleware、platform 的 crypto/httpx/realtime（降级开关切换后发 switches.changed）
// [OUTPUT]: 对外提供 handlers 结构与探针、认证（登录、重认证、登出、me、改密）、仪表盘概览、用户（列表、详情、换订阅链接、重置密码、改状态）、订单、套餐、支付渠道、降级开关等核心处理器，adminRotateResponse 与日期 / 整数解析小工具
// [POS]: api/admin 的核心处理器集合，被 router.go 装配；专题处理器分散在同包其它文件，节点在 nodes.go、单节点与全局路由在 node_routing.go、客服工单在 tickets.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/crypto"
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
// 高危写路由（以 admin/router.go 为准）挂着 RequireRecentReauth，要求令牌里的 rat 在 15 分钟以内。
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
	// 令牌里的身份之外，补上侧栏账户块要的邮箱、显示名与角色（只读库，不改令牌）
	prof, err := h.d.Identity.AdminProfile(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"user_id":      p.UserID,
		"kind":         p.Kind,
		"permissions":  p.Permissions,
		"reauthed":     p.ReauthedRecently,
		"email":        prof.Email,
		"display_name": prof.DisplayName,
		"roles":        prof.Roles,
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
			Query:    q.Get("q"),
			Status:   q.Get("status"),
			GroupID:  q.Get("group_id"),
			SubState: q.Get("sub_state"),
			Limit:    atoiDefault(q.Get("limit"), 25),
			Offset:   atoiDefault(q.Get("offset"), 0),
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
	// 不回新令牌（保留规则 2 / D-B-1）：界面提示用户到门户重新复制订阅地址。
	httpx.OK(w, adminRotateResponse(out))
}

// adminRotateResponse 是换发订阅链接的完整响应形状，单独成函数好让测试锁住它。
func adminRotateResponse(out *subscription.AdminRotateOutput) map[string]any {
	return map[string]any{"user_email": out.UserEmail, "old_revoked": true}
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
	// 原因可选（R101，D-B-2）：不填不校验；填了限 500 字，照旧进审计。
	if utf8.RuneCountInString(strings.TrimSpace(req.Reason)) > 500 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"reason": "原因最多 500 字"}))
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
			UserID: q.Get("user_id"),
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
	// 只发管理端频道，不挂表触发器：触发器会把没有 user_id 的变更同时广播到
	// 门户的公共频道
	if h.d.Realtime != nil {
		h.d.Realtime.Publish(r.Context(), realtime.ChannelAdmin(httpx.TenantIDFrom(r.Context())),
			"switches.changed", map[string]any{"code": chi.URLParam(r, "code"), "enabled": req.Enabled})
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
