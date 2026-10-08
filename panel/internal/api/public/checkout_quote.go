package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// checkoutQuoteReq 是统一报价的请求（设计稿 2.2）。action 是 renew / change / new / pack；
// change 只给订阅时按这份能换成的每个套餐展开，只给套餐时按每份能换的订阅展开。
type checkoutQuoteReq struct {
	Action         string `json:"action"`
	SubscriptionID string `json:"subscription_id"`
	PlanID         string `json:"plan_id"`
	PackID         string `json:"pack_id"`
	CouponCode     string `json:"coupon_code"`
	NewCopy        bool   `json:"new_copy"`
}

// checkoutQuote 是门户唯一的金额来源：不落库、不要幂等键，挂 checkout 开关。
// 前端只在 with_balance / without_balance 两组数之间切换，不做金额运算。
func (h *handlers) checkoutQuote(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	var req checkoutQuoteReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Billing.Quote(r.Context(), p.TenantID, billing.QuoteInput{
		UserID: p.UserID, Action: req.Action, SubscriptionID: req.SubscriptionID,
		PlanID: req.PlanID, PackID: req.PackID, CouponCode: req.CouponCode, NewCopy: req.NewCopy,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
