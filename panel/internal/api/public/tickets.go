// [INPUT]: 依赖 domain/support 的用户侧工单用例（*Atomic 版本），依赖 platform 的 httpx/realtime 与 middleware 的幂等认领
// [OUTPUT]: 对外提供 handlers 的 listTicketCategories、createTicket、listTickets、getTicket、replyTicket、closeTicket；包内 publishTicket
// [POS]: api/public 的工单（OPS-001）：从 handlers.go 拆出。写操作消费幂等认领并写出事务内的预制响应；成功后经 publishTicket 给本人推实时事件；撤回在 selfservice.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

//------------------------------------------------------------------------------
// 工单（OPS-001）
//------------------------------------------------------------------------------

func (h *handlers) listTicketCategories(w http.ResponseWriter, r *http.Request) {
	type cat struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	// 固定顺序输出：map 遍历顺序随机，会让前端下拉框每次刷新都换位置
	order := []string{"general", "technical", "subscription", "billing", "account", "abuse"}
	out := make([]cat, 0, len(order))
	for _, c := range order {
		if n, ok := support.Categories[c]; ok {
			out = append(out, cat{Code: c, Name: n})
		}
	}
	httpx.OK(w, map[string]any{"categories": out})
}

type createTicketReq struct {
	Subject  string `json:"subject"`
	Category string `json:"category"`
	Body     string `json:"body"`
	OrderID  string `json:"order_id"`
}

func (h *handlers) createTicket(w http.ResponseWriter, r *http.Request) {
	var req createTicketReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("support ticket claim missing")))
		return
	}
	out, err := h.d.Support.CreateAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		support.CreateInput{
			UserID:   httpx.PrincipalFrom(r.Context()).UserID,
			Subject:  req.Subject,
			Category: req.Category,
			Body:     req.Body,
			OrderID:  req.OrderID,
		}, claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

func (h *handlers) listTickets(w http.ResponseWriter, r *http.Request) {
	ts, err := h.d.Support.ListForUser(r.Context(),
		httpx.TenantIDFrom(r.Context()), httpx.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"tickets": ts})
}

func (h *handlers) getTicket(w http.ResponseWriter, r *http.Request) {
	t, err := h.d.Support.GetForUser(r.Context(),
		httpx.TenantIDFrom(r.Context()), httpx.PrincipalFrom(r.Context()).UserID,
		chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, t)
}

type ticketReplyReq struct {
	Body string `json:"body"`
}

func (h *handlers) replyTicket(w http.ResponseWriter, r *http.Request) {
	var req ticketReplyReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	ticketID := chi.URLParam(r, "id")
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("support reply claim missing")))
		return
	}
	out, err := h.d.Support.ReplyAsUserAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		p.UserID, ticketID, req.Body, claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 推给自己：同一个人可能开着多个标签页或换了设备，
	// 一处发言其它几处应当立刻跟上
	h.publishTicket(r.Context(), p.TenantID, p.UserID, ticketID)
	httpx.WritePrepared(w, out.PreparedResponse())
}

func (h *handlers) closeTicket(w http.ResponseWriter, r *http.Request) {
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("support close claim missing")))
		return
	}
	out, err := h.d.Support.CloseByUserAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"), claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

// publishTicket 推一条工单变更通知。
//
// 只带 ticket_id，不带内容 —— 前端收到后重新拉该工单，
// 走的是既有的鉴权路径，不必在推送这条链路上再做一遍权限判断。
func (h *handlers) publishTicket(ctx context.Context, tenantID, userID, ticketID string) {
	if h.d.Realtime == nil {
		return
	}
	h.d.Realtime.Publish(ctx, realtime.ChannelUser(tenantID, userID),
		"ticket.updated", map[string]any{"ticket_id": ticketID})
}
