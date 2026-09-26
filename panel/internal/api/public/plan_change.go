// [INPUT]: 依赖 domain/billing 的 PreviewPlanChange / CreatePlanChange，依赖 middleware 的幂等声明与 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供 previewPlanChange、createPlanChange 两个处理器
// [POS]: api/public 门户-03 变更套餐（D-E-2）的 HTTP 外壳：试算不落库、不要幂等键；下单走独立幂等域 subscription_change_plan_create，路由在 router.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type planChangeReq struct {
	PlanID     string `json:"plan_id"`
	PriceID    string `json:"price_id"`
	UseBalance int64  `json:"use_balance"`
	CouponCode string `json:"coupon_code"`
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
	}, true
}

func (h *handlers) previewPlanChange(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	in, ok := h.decodePlanChange(w, r, p)
	if !ok {
		return
	}
	out, err := h.d.Billing.PreviewPlanChange(r.Context(), p.TenantID, in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) createPlanChange(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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
