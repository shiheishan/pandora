// [INPUT]: 依赖 domain/billing 的 ListMyOrders / MyOrderDetail，依赖 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供 listMyOrders、myOrderDetail 两个处理器
// [POS]: api/public 门户-04 订单的 HTTP 外壳：列表带筛选段计数 counts，status 接受逗号分隔多值；详情不存在与非本人同一个 404
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listMyOrders(w http.ResponseWriter, r *http.Request) {
	principal := httpx.PrincipalFrom(r.Context())
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	orders, total, counts, err := h.d.Billing.ListMyOrders(r.Context(),
		httpx.TenantIDFrom(r.Context()), principal.UserID,
		billing.ListMyOrdersInput{Status: q.Get("status"), Limit: limit, Offset: offset})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"orders": orders, "total": total, "counts": counts})
}

func (h *handlers) myOrderDetail(w http.ResponseWriter, r *http.Request) {
	principal := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Billing.MyOrderDetail(r.Context(),
		httpx.TenantIDFrom(r.Context()), principal.UserID, chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"order": out})
}
