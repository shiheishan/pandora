// [INPUT]: 依赖 Deps 注入的 identity（邀请码）与 billing（佣金、提现）用例，依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers 的 myInviteCode、myCommission、requestWithdrawal
// [POS]: api/public 的邀请与分销佣金：从 handlers.go 拆出。邀请码与已邀请人数、佣金概况（付费好友、累计佣金与转出记录）、提现申请（路由挂独立幂等域，契约 7.3）；佣金转余额在 selfservice.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// myInviteCode 返回当前用户的邀请码与已邀请人数。
func (h *handlers) myInviteCode(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	sum, err := h.d.Identity.MyInviteCode(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	list, err := h.d.Identity.ListInvitees(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"invite": sum, "invitees": list})
}

//------------------------------------------------------------------------------
// 分销佣金
//------------------------------------------------------------------------------

func (h *handlers) myCommission(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	sum, err := h.d.Billing.CommissionSummary(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	entries, err := h.d.Billing.ListMyCommissions(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	wds, err := h.d.Billing.ListMyWithdrawals(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	transfers, err := h.d.Billing.ListMyCommissionTransfers(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"summary": sum, "entries": entries, "withdrawals": wds, "transfers": transfers,
	})
}

func (h *handlers) requestWithdrawal(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	var req struct {
		Amount int64  `json:"amount"`
		Payout string `json:"payout_detail"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Payout == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"payout_detail": "请填写收款方式"}))
		return
	}
	id, err := h.d.Billing.RequestWithdrawal(r.Context(), p.TenantID, p.UserID,
		req.Amount, req.Payout)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"id": id})
}
