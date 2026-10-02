// [INPUT]: 依赖 domain/billing 的 PaymentService.QueryOrderPayment，依赖 platform/httpx 的主体与租户、chi 的路径参数
// [OUTPUT]: 对包内提供 queryMyOrderPayment 处理器
// [POS]: api/public 订单详情「我已支付，刷新状态」的 HTTP 外壳；只许查本人订单（他人与不存在同一个 404），路由在 router.go（按账号单独限流），与后台 api/admin/order_query.go 共用同一个领域用例
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) queryMyOrderPayment(w http.ResponseWriter, r *http.Request) {
	principal := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Payments.QueryOrderPayment(r.Context(),
		httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), principal.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
