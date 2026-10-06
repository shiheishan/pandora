package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 目录与账户
//------------------------------------------------------------------------------

type plansResponse struct {
	Plans []billing.CatalogPlan `json:"plans"`
}

func (h *handlers) listPlans(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := httpx.TenantIDFrom(ctx)
	authed := !httpx.PrincipalFrom(ctx).IsAnonymous()
	var viewerID string
	if p := httpx.PrincipalFrom(ctx); p != nil && p.UserID != "" {
		viewerID = p.UserID
	}

	out, err := h.d.Billing.PortalCatalog(ctx, tenantID, authed, viewerID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, plansResponse{Plans: out})
}
