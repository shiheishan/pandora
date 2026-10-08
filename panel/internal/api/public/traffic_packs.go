package public

import (
	"errors"
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
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
	PackID string `json:"pack_id"`
	// SubscriptionID 是加到哪一份（必填，购买模型统一 Q5）
	SubscriptionID string `json:"subscription_id"`
	UseBalance     int64  `json:"use_balance"`
	CouponCode     string `json:"coupon_code"`
	quoteConfirmReq
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
	missing := map[string]string{}
	if req.PackID == "" {
		missing["pack_id"] = "必填"
	}
	if req.SubscriptionID == "" {
		missing["subscription_id"] = "请选择加到哪一份"
	}
	if len(missing) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(missing))
		return
	}
	out, err := h.d.Billing.CreateTrafficPackOrder(r.Context(), p.TenantID,
		billing.CreateTrafficPackOrderInput{
			UserID: p.UserID, SubscriptionID: req.SubscriptionID, PackID: req.PackID,
			UseBalance: req.UseBalance, CouponCode: req.CouponCode, Claim: claim,
			Expect: req.expectation(),
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

// trafficPackTransferReq：from_subscription_id 为 null 表示「还没加到任何一份」的流量包。
type trafficPackTransferReq struct {
	FromSubscriptionID *string `json:"from_subscription_id"`
	ToSubscriptionID   string  `json:"to_subscription_id"`
}

// transferTrafficPacks 把未分配或已彻底停用那份上的流量包转到一份在用的上（设计稿 2.7）。
// 不要幂等键：重复调用第二次没有东西可转，moved_bytes=0。转过之后提交后通知节点重拉名单。
func (h *handlers) transferTrafficPacks(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	var req trafficPackTransferReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.ToSubscriptionID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"to_subscription_id": "请选择转到哪一份"}))
		return
	}
	out, err := h.d.Billing.TransferTrafficPacks(r.Context(), p.TenantID, billing.TrafficPackTransferInput{
		UserID: p.UserID, From: req.FromSubscriptionID, To: req.ToSubscriptionID,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if out.MovedBytes > 0 && h.d.Realtime != nil {
		h.d.Realtime.Publish(r.Context(), realtime.ChannelNodeAll(p.TenantID),
			realtime.TopicNodeUsersChanged, map[string]any{})
	}
	httpx.OK(w, out)
}
