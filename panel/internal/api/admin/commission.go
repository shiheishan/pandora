package admin

// 提现审批与打款。
//
// 三步：申请 → 审批 → 打款。只有最后一步动账本。
//
// 为什么审批不动账：审批只是一个人点了同意，钱还在平台账上。
// 如果审批时就记「钱出去了」，那么审批完到实际转账之间的这段时间里，
// 账本会显示一笔并不存在的支出 —— 而这段时间可能长达几天。

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// listWithdrawalsRow 是提现列表的一行（原为 listWithdrawals 内的局部类型，提到包级以便响应 DTO 引用）。
type listWithdrawalsRow struct {
	ID        string     `json:"id"`
	Email     string     `json:"email"`
	UserID    string     `json:"user_id"`
	Amount    int64      `json:"amount"`
	Currency  string     `json:"currency"`
	Status    string     `json:"status"`
	Payout    string     `json:"payout_detail"`
	Reject    string     `json:"reject_reason"`
	Requested *time.Time `json:"requested_at"`
	Completed *time.Time `json:"completed_at"`
	// Earned 是这个用户累计赚到的佣金，用来判断提现是否合理：
	// 提现额远大于历史佣金说明哪里不对
	Earned int64 `json:"earned_total"`
}

type listWithdrawalsResponse struct {
	Withdrawals []listWithdrawalsRow `json:"withdrawals"`
}

// listWithdrawals 返回提现申请列表。
//
// 收款信息在这里解密。管理员不解密就没法打款 —— 这是这份数据
// 存在的唯一理由，也是它必须加密存储的理由。
func (h *handlers) listWithdrawals(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	status := r.URL.Query().Get("status")

	rows, err := h.d.Billing.AdminListWithdrawals(r.Context(), tenantID, status)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out := make([]listWithdrawalsRow, 0, len(rows))
	for _, wd := range rows {
		out = append(out, listWithdrawalsRow{
			ID: wd.ID, Email: wd.Email, UserID: wd.UserID, Amount: wd.Amount,
			Currency: wd.Currency, Status: wd.Status,
			Payout: h.decryptWith(wd.PayoutEncrypted, "payout"),
			Reject: wd.Reject, Requested: wd.Requested, Completed: wd.Completed,
			Earned: wd.Earned,
		})
	}
	httpx.OK(w, listWithdrawalsResponse{Withdrawals: out})
}

type reviewWithdrawalResponse struct {
	Status string `json:"status"`
}

// reviewWithdrawal 批准或拒绝一笔提现。
func (h *handlers) reviewWithdrawal(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	var req struct {
		Action string `json:"action"` // approve / reject
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Reason = normalizeWithdrawalReason(req.Reason)
	if req.Action != "approve" && req.Action != "reject" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"操作只能是 approve 或 reject"))
		return
	}
	if req.Action == "reject" && req.Reason == "" {
		// 拒绝必须给理由：用户会来问，而「不知道为什么被拒」
		// 是最容易升级成工单和差评的一类回复
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"reason": "拒绝时必须填写理由"}))
		return
	}

	if req.Action == "approve" && req.Reason != "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"reason": "批准时不能填写拒绝理由"}))
		return
	}

	newStatus := "approved"
	if req.Action == "reject" {
		newStatus = "rejected"
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Billing.AdminReviewWithdrawal(r.Context(), tenantID, actorID, id, req.Action, newStatus, req.Reason)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, reviewWithdrawalResponse{Status: newStatus})
}

type markWithdrawalPaidResponse struct {
	Status string `json:"status"`
}

// markWithdrawalPaid 记录一笔提现已实际打款。
//
// 这是唯一动账本的一步：钱真的离开平台了。
func (h *handlers) markWithdrawalPaid(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	var req struct {
		Reference string `json:"payout_reference"` // 转账流水号
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Reference = normalizePayoutReference(req.Reference)
	if req.Reference == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"payout_reference": "请填写转账流水号，日后对账要用"}))
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Billing.AdminMarkWithdrawalPaid(r.Context(), tenantID, actorID, id, req.Reference)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, markWithdrawalPaidResponse{Status: "paid"})
}

// Withdrawal free-text is canonicalized before validation and before any
// transaction, audit digest or ledger-backed state transition sees it.
func normalizeWithdrawalReason(value string) string {
	return strings.TrimSpace(value)
}

func normalizePayoutReference(value string) string {
	return strings.TrimSpace(value)
}

type commissionOverviewResponse struct {
	Available          int64  `json:"available"`
	Entries            int    `json:"entries"`
	FreezeDays         int    `json:"freeze_days"`
	InvitedUsers       int    `json:"invited_users"`
	MinWithdraw        int64  `json:"min_withdraw"`
	NeedReview         int    `json:"need_review"`
	PaidOut            int64  `json:"paid_out"`
	Pending            int64  `json:"pending"`
	RatePercent        int    `json:"rate_percent"`
	Scope              string `json:"scope"`
	ThisMonth          int64  `json:"this_month"`
	TotalEarned        int64  `json:"total_earned"`
	WaitingWithdrawals int    `json:"waiting_withdrawals"`
}

// commissionOverview 是分销的整体情况，给管理员看的。
func (h *handlers) commissionOverview(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())

	st, err := h.d.Billing.AdminCommissionOverview(r.Context(), tenantID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out := commissionOverviewResponse{
		Pending: st.Pending, Available: st.Available, PaidOut: st.PaidOut,
		ThisMonth: st.ThisMonth, Entries: st.Entries,
		NeedReview: st.NeedReview, WaitingWithdrawals: st.WaitingWithdrawals,
		RatePercent: st.RatePercent, FreezeDays: st.FreezeDays, MinWithdraw: st.MinWithdraw,
		TotalEarned: st.TotalEarned, InvitedUsers: st.InvitedUsers, Scope: st.Scope,
	}
	httpx.OK(w, out)
}

type setCommissionConfigResponse struct {
	OK bool `json:"ok"`
}

// setCommissionConfig 改分销参数。
func (h *handlers) setCommissionConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req struct {
		RatePercent *int    `json:"rate_percent"`
		FreezeDays  *int    `json:"freeze_days"`
		MinWithdraw *int64  `json:"min_withdraw"`
		Scope       *string `json:"scope"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	fields := map[string]string{}
	if req.RatePercent != nil && (*req.RatePercent < 0 || *req.RatePercent > 50) {
		// 上限 50%：再高就不是分销而是送钱了。真要突破得改代码，
		// 这道坎的作用是拦住手滑多打一个零
		fields["rate_percent"] = "佣金比例需在 0 到 50 之间"
	}
	if req.FreezeDays != nil && (*req.FreezeDays < 0 || *req.FreezeDays > 90) {
		fields["freeze_days"] = "冻结天数需在 0 到 90 之间"
	}
	if req.MinWithdraw != nil && *req.MinWithdraw < 0 {
		fields["min_withdraw"] = "最低提现金额不能为负"
	}
	if req.Scope != nil && !billing.ValidCommissionScope(*req.Scope) {
		fields["scope"] = "计佣范围只能是 first_order 或 every_order"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(fields))
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Billing.AdminSetCommissionConfig(r.Context(), tenantID, actorID, billing.CommissionConfigInput{
		RatePercent: req.RatePercent, FreezeDays: req.FreezeDays,
		MinWithdraw: req.MinWithdraw, Scope: req.Scope,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, setCommissionConfigResponse{OK: true})
}

type adjustBalanceResponse struct {
	Balance int64 `json:"balance"`
}

// adjustBalance 由管理员直接增减用户余额。
//
// 这是整个后台最需要留痕的操作之一：它能凭空给账户加钱。
// 所以理由是必填的，且会连同调整前后的金额一起进审计。
func (h *handlers) adjustBalance(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	userID := chi.URLParam(r, "id")
	var req struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Reason   string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	var actorID string
	if a := httpx.PrincipalFrom(r.Context()); a != nil {
		actorID = a.UserID
	}

	after, err := h.d.Billing.AdjustBalance(r.Context(), tenantID, billing.AdjustBalanceInput{
		UserID:   userID,
		Amount:   req.Amount,
		Currency: req.Currency,
		Reason:   req.Reason,
		ActorID:  actorID,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, adjustBalanceResponse{Balance: after})
}
