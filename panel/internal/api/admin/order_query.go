// [INPUT]: 依赖 domain/billing 的 PaymentService.AdminQueryOrderPayment（查单 + 操作人审计），依赖 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供 queryOrderPayment 处理器
// [POS]: api/admin 订单抽屉「向渠道查单」（PAY-009）的 HTTP 外壳；路由在 router_billing.go（订单写权限 + 幂等，不挂重认证），与门户 api/public/order_query.go 共用同一个查单用例，后台多一条带操作人的审计
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
