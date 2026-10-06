package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// queryOrderPayment 向订单发起过支付的渠道查单，查到已付就按回调同一条主链补记，并记操作人审计。
func (h *handlers) queryOrderPayment(w http.ResponseWriter, r *http.Request) {
	// 后台入口带操作人：查单之后写一条 order.payment_queried 审计（成败都记）
	out, err := h.d.Payments.AdminQueryOrderPayment(r.Context(),
		httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), httpx.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
