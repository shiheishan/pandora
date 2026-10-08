package public

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type createOrderReq struct {
	PlanID     string `json:"plan_id"`
	PriceID    string `json:"price_id"`
	UseBalance int64  `json:"use_balance"`
	CouponCode string `json:"coupon_code"`
	// NewCopy 是「另买一份」（新链接）的显式意图；不带时遇到同套餐回 409 改走续费
	NewCopy bool `json:"new_copy"`
	// Label 是给新的一份起的名字（可空；另买同款且会和已有一份重名时必填）
	Label string `json:"label"`
	quoteConfirmReq
}

func (h *handlers) createOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(
			errors.New("create order: missing idempotency claim"),
		))
		return
	}
	if err := middleware.ValidateIdempotencyClaim(
		claim, p.TenantID, p.UserID, billing.CheckoutIdempotencyScope,
	); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	var req createOrderReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	fields := map[string]string{}
	if req.PlanID == "" {
		fields["plan_id"] = "必填"
	}
	if req.PriceID == "" {
		fields["price_id"] = "必填"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(fields))
		return
	}

	out, err := h.d.Billing.CreateOrder(r.Context(), p.TenantID, billing.CreateOrderInput{
		UserID:     p.UserID,
		PlanID:     req.PlanID,
		PriceID:    req.PriceID,
		UseBalance: req.UseBalance,
		CouponCode: req.CouponCode,
		Claim:      claim,
		// 同套餐只续不新开（规则 3）：已有可原地续费的同套餐订阅时回 409，门户改走续费；
		// 带 new_copy 是显式「另买一份」，不拦
		RejectSamePlan: true,
		NewCopy:        req.NewCopy,
		Label:          req.Label,
		Expect:         req.expectation(),
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.WritePrepared(w, out.PreparedResponse())
}

// createRenewal 为已有订阅续费。
//
// 与新购分开一个接口，是因为两者的输入本来就不同：
// 新购要选套餐，续费只需要指明续哪一条订阅。
func (h *handlers) createRenewal(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(
			errors.New("create renewal: missing idempotency claim"),
		))
		return
	}
	if err := middleware.ValidateIdempotencyClaim(
		claim, p.TenantID, p.UserID, billing.RenewalIdempotencyScope,
	); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	subID := chi.URLParam(r, "id")
	var req struct {
		PriceID    string `json:"price_id"`
		UseBalance int64  `json:"use_balance"`
		CouponCode string `json:"coupon_code"`
		quoteConfirmReq
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Billing.CreateRenewal(r.Context(), p.TenantID, billing.CreateRenewalInput{
		UserID: p.UserID, SubscriptionID: subID, PriceID: req.PriceID,
		UseBalance: req.UseBalance, CouponCode: req.CouponCode, Claim: claim,
		Expect: req.expectation(),
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}
