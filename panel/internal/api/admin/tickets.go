// [INPUT]: 依赖 domain/support 的客服侧用例（*Atomic 版本）与负责人目录，依赖 platform/httpx 与 middleware 的幂等认领
// [OUTPUT]: 对外提供 handlers 的 ticketAssignees、ticketQueue、ticketDetail、ticketReply、ticketAssign、ticketStatus、ticketEscalate
// [POS]: api/admin 的客服工单（OPS-001）：从 handlers.go 拆出。负责人候选只来自专用的权限过滤查询；写操作消费幂等认领并写出事务内的预制响应；人工升级供演示与排障立即生效
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

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
	// 非 uuid 的 id 直接 404：交给 SQL 会变成 500（契约 §1.4）
	if _, err := uuid.Parse(chi.URLParam(r, "id")); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeNotFound, "工单不存在"))
		return
	}
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
