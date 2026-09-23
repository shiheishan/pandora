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

	orders, total, err := h.d.Billing.ListMyOrders(r.Context(),
		httpx.TenantIDFrom(r.Context()), principal.UserID,
		billing.ListMyOrdersInput{Status: q.Get("status"), Limit: limit, Offset: offset})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"orders": orders, "total": total})
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
