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
