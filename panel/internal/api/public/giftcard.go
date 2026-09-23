package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type giftCodeReq struct {
	Code string `json:"code"`
}

// previewGiftCard 让用户兑换前先看清这张卡送什么。
func (h *handlers) previewGiftCard(w http.ResponseWriter, r *http.Request) {
	var req giftCodeReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	t, err := h.d.GiftCard.PreviewCode(r.Context(), httpx.TenantIDFrom(r.Context()), req.Code)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"card": t})
}

func (h *handlers) redeemGiftCard(w http.ResponseWriter, r *http.Request) {
	var req giftCodeReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	out, err := h.d.GiftCard.Redeem(r.Context(), httpx.TenantIDFrom(r.Context()),
		principal.UserID, req.Code)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) myGiftRedemptions(w http.ResponseWriter, r *http.Request) {
	principal := httpx.PrincipalFrom(r.Context())
	rows, err := h.d.GiftCard.MyRedemptions(r.Context(),
		httpx.TenantIDFrom(r.Context()), principal.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"redemptions": rows})
}
