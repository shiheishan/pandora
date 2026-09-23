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
	UserID  string `json:"user_id"`
	PlanID  string `json:"plan_id"`
	PriceID string `json:"price_id"`
	Reason  string `json:"reason"`
}

// createManualOrder 给用户直接开一张已履约的订单（赠送 / 补偿 / 线下成交）。
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
			Claim: claim,
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
