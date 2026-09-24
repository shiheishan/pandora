// [INPUT]: 依赖 domain/billing 的 ListTrafficPacks / CreateTrafficPackOrder / MyTrafficPacks，依赖 middleware 的幂等声明与 platform/httpx
// [OUTPUT]: 对包内提供 listTrafficPacks、createTrafficPackOrder、myTrafficPacks 三个处理器
// [POS]: api/public 门户-03 流量包的 HTTP 外壳：目录公开可读，下单与余额要登录；下单与新购共用 order_create 幂等域，路由在 router.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"errors"
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listTrafficPacks(w http.ResponseWriter, r *http.Request) {
	packs, err := h.d.Billing.ListTrafficPacks(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"packs": packs})
}

type trafficPackOrderReq struct {
	PackID     string `json:"pack_id"`
	UseBalance int64  `json:"use_balance"`
	CouponCode string `json:"coupon_code"`
}

func (h *handlers) createTrafficPackOrder(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	out, err := h.d.Billing.MyTrafficPacks(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
