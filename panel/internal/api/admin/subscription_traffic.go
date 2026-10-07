package admin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type grantSubscriptionTrafficReq struct {
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
}

// grantSubscriptionTraffic 给订阅的用户发一笔不过期的流量包：POST v1/subscriptions/{id}/traffic-pack。
//
// 门槛在 router_users.go：billing.adjustment.write → 近期重认证 → 独立幂等 scope。
// 业务层在同一个事务里发放、写审计并完成幂等记录（同加时长），这里只回写它备好的那一份响应。
func (h *handlers) grantSubscriptionTraffic(w http.ResponseWriter, r *http.Request) {
	var req grantSubscriptionTrafficReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("subscription traffic grant claim missing")))
		return
	}
	out, err := h.d.Billing.GrantTrafficPackAsAdmin(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.AdminTrafficGrantInput{
			SubscriptionID: chi.URLParam(r, "id"),
			ActorID:        httpx.PrincipalFrom(r.Context()).UserID,
			Bytes:          req.Bytes, Reason: req.Reason, Claim: claim,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}
