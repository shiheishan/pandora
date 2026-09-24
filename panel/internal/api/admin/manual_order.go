// [INPUT]: 依赖 domain/billing 的 CreateManualOrder / MarkOrderPaid，依赖 middleware 的幂等声明与 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供 createManualOrder、markOrderPaid 两个处理器
// [POS]: api/admin 后台-05 人工开单（settlement: grant | pending）与标记线下已收款的 HTTP 外壳；人工单回放业务层预写的 201 响应，路由在 router.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type manualOrderReq struct {
	UserID     string `json:"user_id"`
	PlanID     string `json:"plan_id"`
	PriceID    string `json:"price_id"`
	Reason     string `json:"reason"`
	Settlement string `json:"settlement"`
}

// createManualOrder 替用户开单：赠送（缺省，当场履约）或待用户支付。
func (h *handlers) createManualOrder(w http.ResponseWriter, r *http.Request) {
	var req manualOrderReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("manual order claim missing")))
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Billing.CreateManualOrder(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.CreateManualOrderInput{
			UserID: req.UserID, PlanID: req.PlanID, PriceID: req.PriceID,
			Reason: req.Reason, ActorID: principal.UserID,
			Settlement: req.Settlement, Claim: claim,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 写业务层备好的那一份响应，而不是另起一个 httpx.OK。
	//
	// CreateOrder 在事务里就把响应体连同状态码（201）写进了幂等记录 ——
	// 重发同一个 Idempotency-Key 时回放的正是它。这里若自己写一个 200，
	// 同一次请求的状态码就有了两份互相矛盾的记录：首次 200、重发 201，
	// 而且幂等中间件事后对不上账，每开一张人工单都要报一条 ERROR
	// （complete idempotency claim failed / resource_bound）。
	//
	// 用户端的 createOrder 一直是这么写的，管理端这条落下了。
	httpx.WritePrepared(w, out.PreparedResponse())
}

type markPaidReq struct {
	Reason    string `json:"reason"`
	Reference string `json:"reference"`
}

// markOrderPaid 把待支付订单按线下已收款结清。
func (h *handlers) markOrderPaid(w http.ResponseWriter, r *http.Request) {
	var req markPaidReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Billing.MarkOrderPaid(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.MarkOrderPaidInput{
			OrderID: chi.URLParam(r, "id"), ActorID: principal.UserID,
			Reason: req.Reason, Reference: req.Reference,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
