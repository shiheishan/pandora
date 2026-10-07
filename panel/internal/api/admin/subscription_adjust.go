package admin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type extendSubscriptionReq struct {
	Days   int    `json:"days"`
	Reason string `json:"reason"`
}

// extendSubscription 给订阅加时长：POST v1/subscriptions/{id}/extend。
//
// 门槛在 router_users.go：billing.adjustment.write → 近期重认证 → 独立幂等 scope。
// 业务层在同一个事务里改订阅、本周期配额、凭据，写审计并完成幂等记录，
// 这里只回写它备好的那一份响应。
func (h *handlers) extendSubscription(w http.ResponseWriter, r *http.Request) {
	var req extendSubscriptionReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("subscription extend claim missing")))
		return
	}
	out, err := h.d.Billing.ExtendSubscriptionAsAdmin(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.AdminExtendInput{
			SubscriptionID: chi.URLParam(r, "id"),
			ActorID:        httpx.PrincipalFrom(r.Context()).UserID,
			Days:           req.Days, Reason: req.Reason, Claim: claim,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}
