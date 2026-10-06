package public

import (
	"errors"
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type trafficPacksResponse struct {
	Packs []billing.TrafficPack `json:"packs"`
}

func (h *handlers) listTrafficPacks(w http.ResponseWriter, r *http.Request) {
	packs, err := h.d.Billing.ListTrafficPacks(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, trafficPacksResponse{Packs: packs})
}

type trafficPackOrderReq struct {
	PackID     string `json:"pack_id"`
	UseBalance int64  `json:"use_balance"`
	CouponCode string `json:"coupon_code"`
}

func (h *handlers) createTrafficPackOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(
			errors.New("create traffic pack order: missing idempotency claim")))
		return
	}
	var req trafficPackOrderReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.PackID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"pack_id": "必填"}))
		return
	}
	out, err := h.d.Billing.CreateTrafficPackOrder(r.Context(), p.TenantID,
		billing.CreateTrafficPackOrderInput{
			UserID: p.UserID, PackID: req.PackID, UseBalance: req.UseBalance,
			CouponCode: req.CouponCode, Claim: claim,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

func (h *handlers) myTrafficPacks(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	out, err := h.d.Billing.MyTrafficPacks(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
