// [INPUT]: 依赖 domain/giftcard 的 PreviewCode / Redeem / MyRedemptions，依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers 的 previewGiftCard / redeemGiftCard / myGiftRedemptions
// [POS]: api/public 的礼品卡：预览卡面、兑换（路由上挂 marketing.giftcard.redeem 开关与幂等）、我的兑换记录；判断该不该发在 giftcard，怎么发交回 billing

package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type giftCodeReq struct {
	Code string `json:"code"`
}

type giftCardPreviewResponse struct {
	Card *giftcard.CardPreview `json:"card"`
}

type giftRedemptionsResponse struct {
	Redemptions []giftcard.MyRedemption `json:"redemptions"`
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
	httpx.OK(w, giftCardPreviewResponse{Card: t})
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
	httpx.OK(w, giftRedemptionsResponse{Redemptions: rows})
}
