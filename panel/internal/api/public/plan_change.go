package public

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// planChangeReq 是换套餐建单。试算接口 change-plan/preview 已退役，金额来自
// POST v1/me/checkout/quote（action=change），确认时带回 as_of 与 expect。
type planChangeReq struct {
	PlanID     string `json:"plan_id"`
	PriceID    string `json:"price_id"`
	UseBalance int64  `json:"use_balance"`
	CouponCode string `json:"coupon_code"`
	quoteConfirmReq
}

// decodePlanChange 读出请求体并拼成领域输入；缺必填项时已经写好了 422。
func (h *handlers) decodePlanChange(w http.ResponseWriter, r *http.Request,
	p *httpx.Principal) (billing.PlanChangeInput, bool) {
	var req planChangeReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return billing.PlanChangeInput{}, false
	}
	missing := map[string]string{}
	if req.PlanID == "" {
		missing["plan_id"] = "必填"
	}
	if req.PriceID == "" {
		missing["price_id"] = "必填"
	}
	if len(missing) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(missing))
		return billing.PlanChangeInput{}, false
	}
	return billing.PlanChangeInput{
		UserID: p.UserID, SubscriptionID: chi.URLParam(r, "id"),
		PlanID: req.PlanID, PriceID: req.PriceID,
		UseBalance: req.UseBalance, CouponCode: req.CouponCode,
		Expect: req.expectation(),
	}, true
}

func (h *handlers) createPlanChange(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(
			errors.New("create plan change: missing idempotency claim")))
		return
	}
	in, ok := h.decodePlanChange(w, r, p)
	if !ok {
		return
	}
	in.Claim = claim
	out, err := h.d.Billing.CreatePlanChange(r.Context(), p.TenantID, in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}
