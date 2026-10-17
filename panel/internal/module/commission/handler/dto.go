package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// listWithdrawalsQuery 是后台提现列表的查询参数；status 逗号分隔，如 requested,approved。
type listWithdrawalsQuery struct {
	Status string `form:"status"`
}

// statuses 把逗号分隔的状态拆开（去空白、丢空段）；为空表示不筛。
func (q listWithdrawalsQuery) statuses() []string {
	var out []string
	for _, p := range strings.Split(q.Status, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// withdrawalRequest 是门户发起提现的请求体。
type withdrawalRequest struct {
	Amount       int64  `json:"amount"`
	Method       string `json:"method"`
	PayoutDetail string `json:"payout_detail"`
}

// transferRequest 是门户佣金转入余额的请求体。
type transferRequest struct {
	Amount int64 `json:"amount"`
}

// withdrawalIDResponse 是提现申请成功响应。
type withdrawalIDResponse struct {
	ID string `json:"id"`
}

// transferResponse 是佣金转入余额成功响应。
type transferResponse struct {
	LedgerTxnID string `json:"ledger_txn_id"`
	Amount      int64  `json:"amount"`
}

// listEntriesQuery 是后台佣金条目列表的查询参数（目前只有待复核这一种）。
type listEntriesQuery struct {
	Review string `form:"review"`
	Cursor string `form:"cursor"`
	Limit  int    `form:"limit"`
}

// reviewRequest 是后台审批提现（approve / reject）与复核佣金（approve / void）的请求体。
type reviewRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// reviewResponse 是审批提现、复核佣金成功响应：复核后的状态。
type reviewResponse struct {
	Status string `json:"status"`
}

// markPaidRequest 是标记已打款的请求体。
type markPaidRequest struct {
	PayoutReference string  `json:"payout_reference"`
	PaidUSDTAmount  *string `json:"paid_usdt_amount"`
}

// markPaidResponse 是标记已打款成功响应。
type markPaidResponse struct {
	Status string `json:"status"`
}

// configRequest 是后台改分销参数的请求体。
type configRequest struct {
	RatePercent         *int      `json:"rate_percent"`
	FreezeDays          *int      `json:"freeze_days"`
	MinWithdraw         *int64    `json:"min_withdraw"`
	Scope               *string   `json:"scope"`
	DistributionEnabled *bool     `json:"distribution_enabled"`
	DistributionL1      *int      `json:"distribution_l1"`
	DistributionL2      *int      `json:"distribution_l2"`
	DistributionL3      *int      `json:"distribution_l3"`
	WithdrawEnabled     *bool     `json:"withdraw_enabled"`
	WithdrawMethods     *[]string `json:"withdraw_methods"`
}

// okResponse 是只回 ok 的写操作响应。
type okResponse struct {
	OK bool `json:"ok"`
}

// commissionSummaryResponse 是门户佣金汇总。
type commissionSummaryResponse struct {
	Pending         int64    `json:"pending"`
	Available       int64    `json:"available"`
	Withdrawing     int64    `json:"withdrawing"`
	Settled         int64    `json:"settled"`
	Invitees        int64    `json:"invitees"`
	Orders          int64    `json:"orders"`
	PaidInvitees    int64    `json:"paid_invitees"`
	TotalEarned     int64    `json:"total_earned"`
	RatePercent     int      `json:"rate_percent"`
	MinWithdraw     int64    `json:"min_withdraw"`
	Scope           string   `json:"scope"`
	DirectRateBP    int64    `json:"direct_rate_bp"`
	WithdrawEnabled bool     `json:"withdraw_enabled"`
	WithdrawMethods []string `json:"withdraw_methods"`
}

// commissionEntryResponse 是门户佣金明细一行。
type commissionEntryResponse struct {
	Amount       int64     `json:"amount"`
	Base         int64     `json:"base"`
	RatePercent  int64     `json:"rate_percent"`
	RateBP       int64     `json:"rate_bp"`
	Level        int       `json:"level"`
	SharePercent int       `json:"share_percent"`
	Status       string    `json:"status"`
	FrozenUntil  time.Time `json:"frozen_until"`
	CreatedAt    time.Time `json:"created_at"`
	OrderNo      string    `json:"order_no"`
	From         string    `json:"from"`
}

// myWithdrawalResponse 是门户提现记录一行。
type myWithdrawalResponse struct {
	ID           string     `json:"id"`
	Amount       int64      `json:"amount"`
	Status       string     `json:"status"`
	Method       string     `json:"method"`
	RejectReason string     `json:"reject_reason"`
	RequestedAt  time.Time  `json:"requested_at"`
	CompletedAt  *time.Time `json:"completed_at"`
}

// myTransferResponse 是门户转余额记录一行。
type myTransferResponse struct {
	LedgerTxnID string    `json:"ledger_txn_id"`
	Amount      int64     `json:"amount"`
	CreatedAt   time.Time `json:"created_at"`
}

// myCommissionResponse 是 GET /me/commission 响应。
type myCommissionResponse struct {
	Summary     commissionSummaryResponse `json:"summary"`
	Entries     []commissionEntryResponse `json:"entries"`
	Withdrawals []myWithdrawalResponse    `json:"withdrawals"`
	Transfers   []myTransferResponse      `json:"transfers"`
}

// overviewResponse 是后台分销概览（金额为分）。
type overviewResponse struct {
	Pending             int64    `json:"pending"`
	Available           int64    `json:"available"`
	PaidOut             int64    `json:"paid_out"`
	ThisMonth           int64    `json:"this_month"`
	TotalEarned         int64    `json:"total_earned"`
	Entries             int64    `json:"entries"`
	NeedReview          int64    `json:"need_review"`
	WaitingWithdrawals  int64    `json:"waiting_withdrawals"`
	InvitedUsers        int64    `json:"invited_users"`
	RatePercent         int      `json:"rate_percent"`
	FreezeDays          int      `json:"freeze_days"`
	MinWithdraw         int64    `json:"min_withdraw"`
	Scope               string   `json:"scope"`
	DistributionEnabled bool     `json:"distribution_enabled"`
	DistributionLevels  []int    `json:"distribution_levels"`
	WithdrawEnabled     bool     `json:"withdraw_enabled"`
	WithdrawMethods     []string `json:"withdraw_methods"`
}

// adminWithdrawalResponse 是后台提现列表一行。
type adminWithdrawalResponse struct {
	ID              string     `json:"id"`
	Email           string     `json:"email"`
	UserID          string     `json:"user_id"`
	Amount          int64      `json:"amount"`
	Status          string     `json:"status"`
	Method          string     `json:"method"`
	PayoutDetail    string     `json:"payout_detail"`
	PayoutReference string     `json:"payout_reference"`
	PaidUSDTAmount  *string    `json:"paid_usdt_amount"`
	RejectReason    string     `json:"reject_reason"`
	RequestedAt     time.Time  `json:"requested_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	EarnedTotal     int64      `json:"earned_total"`
}

// reviewEntryResponse 是后台待复核佣金列表一行。
type reviewEntryResponse struct {
	ID            string    `json:"id"`
	OrderNo       string    `json:"order_no"`
	ReferrerEmail string    `json:"referrer_email"`
	RefereeEmail  string    `json:"referee_email"`
	Amount        int64     `json:"amount"`
	ReviewReason  string    `json:"review_reason"`
	ReviewSource  string    `json:"review_source"` // same_source / source_check / underfunded / account_deleted / other
	CreatedAt     time.Time `json:"created_at"`
	FrozenUntil   time.Time `json:"frozen_until"`
}

// reviewEntriesResponse 是后台待复核佣金列表响应；next_cursor 为空串表示没有下一页。
type reviewEntriesResponse struct {
	Entries    []reviewEntryResponse `json:"entries"`
	NextCursor string                `json:"next_cursor"`
}

// listWithdrawalsResponse 是后台提现列表响应。
type listWithdrawalsResponse struct {
	Withdrawals []adminWithdrawalResponse `json:"withdrawals"`
}

func cents(d decimal.Decimal) (int64, error) {
	return money.ToCents(d)
}

func summaryFrom(s service.Summary) (commissionSummaryResponse, error) {
	pending, err := cents(s.Pending)
	if err != nil {
		return commissionSummaryResponse{}, err
	}
	avail, err := cents(s.Available)
	if err != nil {
		return commissionSummaryResponse{}, err
	}
	withdrawing, err := cents(s.Withdrawing)
	if err != nil {
		return commissionSummaryResponse{}, err
	}
	settled, err := cents(s.Settled)
	if err != nil {
		return commissionSummaryResponse{}, err
	}
	total, err := cents(s.TotalEarned)
	if err != nil {
		return commissionSummaryResponse{}, err
	}
	minW, err := cents(s.MinWithdraw)
	if err != nil {
		return commissionSummaryResponse{}, err
	}
	withdrawMethods := s.WithdrawMethods
	if withdrawMethods == nil {
		withdrawMethods = []string{}
	}
	return commissionSummaryResponse{
		Pending:         pending,
		Available:       avail,
		Withdrawing:     withdrawing,
		Settled:         settled,
		Invitees:        s.Invitees,
		Orders:          s.Orders,
		PaidInvitees:    s.PaidInvitees,
		TotalEarned:     total,
		RatePercent:     s.RatePercent,
		MinWithdraw:     minW,
		Scope:           s.Scope,
		DirectRateBP:    s.DirectRateBP,
		WithdrawEnabled: s.WithdrawEnabled,
		WithdrawMethods: withdrawMethods,
	}, nil
}

func entryFrom(e service.EntryView) (commissionEntryResponse, error) {
	amount, err := cents(e.Amount)
	if err != nil {
		return commissionEntryResponse{}, err
	}
	base, err := cents(e.Base)
	if err != nil {
		return commissionEntryResponse{}, err
	}
	return commissionEntryResponse{
		Amount:       amount,
		Base:         base,
		RatePercent:  e.RateBP / 100,
		RateBP:       e.RateBP,
		Level:        e.Level,
		SharePercent: e.SharePercent,
		Status:       e.Status,
		FrozenUntil:  e.FrozenUntil,
		CreatedAt:    e.CreatedAt,
		OrderNo:      e.OrderNo,
		From:         e.From,
	}, nil
}

func myWithdrawalFrom(w service.WithdrawalView) (myWithdrawalResponse, error) {
	amount, err := cents(w.Amount)
	if err != nil {
		return myWithdrawalResponse{}, err
	}
	return myWithdrawalResponse{
		ID:           w.ID.String(),
		Amount:       amount,
		Status:       w.Status,
		Method:       w.Method,
		RejectReason: w.RejectReason,
		RequestedAt:  w.RequestedAt,
		CompletedAt:  w.CompletedAt,
	}, nil
}

func myTransferFrom(t service.TransferView) (myTransferResponse, error) {
	amount, err := cents(t.Amount)
	if err != nil {
		return myTransferResponse{}, err
	}
	return myTransferResponse{
		LedgerTxnID: t.LedgerTxnID.String(),
		Amount:      amount,
		CreatedAt:   t.CreatedAt,
	}, nil
}

// myCommissionFrom 把 service.MyCommission 转成响应 DTO。
func myCommissionFrom(mc service.MyCommission) (myCommissionResponse, error) {
	sum, err := summaryFrom(mc.Summary)
	if err != nil {
		return myCommissionResponse{}, err
	}
	entries := make([]commissionEntryResponse, 0, len(mc.Entries))
	for _, e := range mc.Entries {
		row, err := entryFrom(e)
		if err != nil {
			return myCommissionResponse{}, err
		}
		entries = append(entries, row)
	}
	withdrawals := make([]myWithdrawalResponse, 0, len(mc.Withdrawals))
	for _, w := range mc.Withdrawals {
		row, err := myWithdrawalFrom(w)
		if err != nil {
			return myCommissionResponse{}, err
		}
		withdrawals = append(withdrawals, row)
	}
	transfers := make([]myTransferResponse, 0, len(mc.Transfers))
	for _, t := range mc.Transfers {
		row, err := myTransferFrom(t)
		if err != nil {
			return myCommissionResponse{}, err
		}
		transfers = append(transfers, row)
	}
	return myCommissionResponse{
		Summary:     sum,
		Entries:     entries,
		Withdrawals: withdrawals,
		Transfers:   transfers,
	}, nil
}

// overviewFrom 把 service.Overview 转成响应 DTO。
func overviewFrom(o service.Overview) (overviewResponse, error) {
	pending, err := cents(o.Pending)
	if err != nil {
		return overviewResponse{}, err
	}
	avail, err := cents(o.Available)
	if err != nil {
		return overviewResponse{}, err
	}
	paidOut, err := cents(o.PaidOut)
	if err != nil {
		return overviewResponse{}, err
	}
	thisMonth, err := cents(o.ThisMonth)
	if err != nil {
		return overviewResponse{}, err
	}
	totalEarned, err := cents(o.TotalEarned)
	if err != nil {
		return overviewResponse{}, err
	}
	minW, err := cents(o.MinWithdraw)
	if err != nil {
		return overviewResponse{}, err
	}
	withdrawMethods := o.WithdrawMethods
	if withdrawMethods == nil {
		withdrawMethods = []string{}
	}
	return overviewResponse{
		Pending:             pending,
		Available:           avail,
		PaidOut:             paidOut,
		ThisMonth:           thisMonth,
		TotalEarned:         totalEarned,
		Entries:             o.Entries,
		NeedReview:          o.NeedReview,
		WaitingWithdrawals:  o.WaitingWithdrawals,
		InvitedUsers:        o.InvitedUsers,
		RatePercent:         o.RatePercent,
		FreezeDays:          o.FreezeDays,
		MinWithdraw:         minW,
		Scope:               o.Scope,
		DistributionEnabled: o.DistributionEnabled,
		DistributionLevels:  []int{o.DistributionLevels[0], o.DistributionLevels[1], o.DistributionLevels[2]},
		WithdrawEnabled:     o.WithdrawEnabled,
		WithdrawMethods:     withdrawMethods,
	}, nil
}

func adminWithdrawalFrom(w service.AdminWithdrawal) (adminWithdrawalResponse, error) {
	amount, err := cents(w.Amount)
	if err != nil {
		return adminWithdrawalResponse{}, err
	}
	earned, err := cents(w.EarnedTotal)
	if err != nil {
		return adminWithdrawalResponse{}, err
	}
	var paidUSDT *string
	if w.PaidUSDTAmount != nil {
		s := w.PaidUSDTAmount.String()
		paidUSDT = &s
	}
	return adminWithdrawalResponse{
		ID:              w.ID.String(),
		Email:           w.Email,
		UserID:          w.UserID.String(),
		Amount:          amount,
		Status:          w.Status,
		Method:          w.Method,
		PayoutDetail:    w.PayoutDetail,
		PayoutReference: w.PayoutReference,
		PaidUSDTAmount:  paidUSDT,
		RejectReason:    w.RejectReason,
		RequestedAt:     w.RequestedAt,
		CompletedAt:     w.CompletedAt,
		EarnedTotal:     earned,
	}, nil
}

func reviewEntriesFrom(p service.ReviewEntryPage) (reviewEntriesResponse, error) {
	out := reviewEntriesResponse{Entries: make([]reviewEntryResponse, 0, len(p.Entries)), NextCursor: p.NextCursor}
	for _, e := range p.Entries {
		amount, err := cents(e.Amount)
		if err != nil {
			return reviewEntriesResponse{}, err
		}
		out.Entries = append(out.Entries, reviewEntryResponse{
			ID:            e.ID.String(),
			OrderNo:       e.OrderNo,
			ReferrerEmail: e.ReferrerEmail,
			RefereeEmail:  e.RefereeEmail,
			Amount:        amount,
			ReviewReason:  e.ReviewReason,
			ReviewSource:  e.ReviewSource,
			CreatedAt:     e.CreatedAt,
			FrozenUntil:   e.FrozenUntil,
		})
	}
	return out, nil
}

// configToUpdate 把请求体转成 service.ConfigUpdate。
func (r configRequest) configToUpdate() service.ConfigUpdate {
	u := service.ConfigUpdate{
		RatePercent:         r.RatePercent,
		FreezeDays:          r.FreezeDays,
		Scope:               r.Scope,
		DistributionEnabled: r.DistributionEnabled,
		DistributionL1:      r.DistributionL1,
		DistributionL2:      r.DistributionL2,
		DistributionL3:      r.DistributionL3,
		WithdrawEnabled:     r.WithdrawEnabled,
		WithdrawMethods:     r.WithdrawMethods,
	}
	if r.MinWithdraw != nil {
		d := money.FromCents(*r.MinWithdraw)
		u.MinWithdraw = &d
	}
	return u
}
