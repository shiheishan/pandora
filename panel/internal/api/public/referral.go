package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// inviteResponse 的 invitees 是 identity 按行拼好的好友列表，原样透传。
type inviteResponse struct {
	Invite   *identity.InviteSummary `json:"invite"`
	Invitees []map[string]any        `json:"invitees"`
}

// myInviteCode 返回当前用户的邀请码与已邀请人数。
func (h *handlers) myInviteCode(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
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
	httpx.OK(w, inviteResponse{Invite: sum, Invitees: list})
}

//------------------------------------------------------------------------------
// 分销佣金
//------------------------------------------------------------------------------

// commissionResponse 的 entries / withdrawals 是 billing 按行拼好的记录，原样透传。
type commissionResponse struct {
	Summary     *billing.CommissionSummary   `json:"summary"`
	Entries     []map[string]any             `json:"entries"`
	Withdrawals []map[string]any             `json:"withdrawals"`
	Transfers   []billing.CommissionTransfer `json:"transfers"`
}

func (h *handlers) myCommission(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
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
	httpx.OK(w, commissionResponse{Summary: sum, Entries: entries, Withdrawals: wds, Transfers: transfers})
}

type withdrawalResponse struct {
	ID string `json:"id"`
}

func (h *handlers) requestWithdrawal(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
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
	httpx.OK(w, withdrawalResponse{ID: id})
}
