// [INPUT]: 依赖 domain/billing 的 PortalCatalog（SQL 与可见性过滤都在那里），依赖 platform/httpx 取租户与看的人
// [OUTPUT]: 对外提供 handlers.listPlans（GET 套餐目录）
// [POS]: api/public 的套餐目录：从 handlers.go 拆出，读模型在 billing/portal_catalog.go。只列当前用户可见、已发布且有可用币种（CNY / USD）适用价格的套餐，取当前发布版本的额度、限速 throttle_kbps（R99）与重置策略，带卖点 highlights 与推荐 recommended（R100）以及续费、变更开关
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
