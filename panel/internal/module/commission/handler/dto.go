package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// listWithdrawalsQuery 是后台提现列表的查询参数。
type listWithdrawalsQuery struct {
	Status string `form:"status"`
}

// withdrawalRequest 是门户发起提现的请求体。
type withdrawalRequest struct {
	Amount       int64  `json:"amount"`
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

// reviewRequest 是后台审批提现的请求体。
type reviewRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// reviewResponse 是审批提现成功响应。
type reviewResponse struct {
	Status string `json:"status"`
}

// markPaidRequest 是标记已打款的请求体。
type markPaidRequest struct {
	PayoutReference string `json:"payout_reference"`
}

// markPaidResponse 是标记已打款成功响应。
type markPaidResponse struct {
	Status string `json:"status"`
}

// configRequest 是后台改分销参数的请求体。
type configRequest struct {
	RatePercent *int    `json:"rate_percent"`
	FreezeDays  *int    `json:"freeze_days"`
	MinWithdraw *int64  `json:"min_withdraw"`
	Scope       *string `json:"scope"`
}

// okResponse 是只回 ok 的写操作响应。
type okResponse struct {
	OK bool `json:"ok"`
}

// commissionSummaryResponse 是门户佣金汇总。
type commissionSummaryResponse struct {
	Currency     string `json:"currency"`
	Pending      int64  `json:"pending"`
	Available    int64  `json:"available"`
	Withdrawing  int64  `json:"withdrawing"`
	Settled      int64  `json:"settled"`
	Invitees     int64  `json:"invitees"`
	Orders       int64  `json:"orders"`
	PaidInvitees int64  `json:"paid_invitees"`
	TotalEarned  int64  `json:"total_earned"`
	RatePercent  int    `json:"rate_percent"`
	MinWithdraw  int64  `json:"min_withdraw"`
	Scope        string `json:"scope"`
}

// commissionEntryResponse 是门户佣金明细一行。
type commissionEntryResponse struct {
	Amount      int64     `json:"amount"`
	Base        int64     `json:"base"`
	RatePercent int64     `json:"rate_percent"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	FrozenUntil time.Time `json:"frozen_until"`
	CreatedAt   time.Time `json:"created_at"`
	OrderNo     string    `json:"order_no"`
	From        string    `json:"from"`
}

// myWithdrawalResponse 是门户提现记录一行。
type myWithdrawalResponse struct {
	ID           string     `json:"id"`
	Amount       int64      `json:"amount"`
	Currency     string     `json:"currency"`
	Status       string     `json:"status"`
	RejectReason string     `json:"reject_reason"`
	RequestedAt  time.Time  `json:"requested_at"`
	CompletedAt  *time.Time `json:"completed_at"`
}

// myTransferResponse 是门户转余额记录一行。
type myTransferResponse struct {
	LedgerTxnID string    `json:"ledger_txn_id"`
	Amount      int64     `json:"amount"`
	Currency    string    `json:"currency"`
	CreatedAt   time.Time `json:"created_at"`
}

// myCommissionResponse 是 GET /me/commission 响应。
type myCommissionResponse struct {
	Summary     commissionSummaryResponse `json:"summary"`
	Entries     []commissionEntryResponse `json:"entries"`
	Withdrawals []myWithdrawalResponse    `json:"withdrawals"`
	Transfers   []myTransferResponse      `json:"transfers"`
}

// currencyOverviewResponse 是后台分销概览里某一币种的一组（金额为分）。
type currencyOverviewResponse struct {
	Currency           string `json:"currency"`
	Pending            int64  `json:"pending"`
	Available          int64  `json:"available"`
	PaidOut            int64  `json:"paid_out"`
	ThisMonth          int64  `json:"this_month"`
	TotalEarned        int64  `json:"total_earned"`
	Entries            int64  `json:"entries"`
	NeedReview         int64  `json:"need_review"`
	WaitingWithdrawals int64  `json:"waiting_withdrawals"`
}

// overviewResponse 是后台分销概览：平铺的金额字段是站点币种（currency）那一组，笔数是全部币种合计；by_currency 按币种列出。
type overviewResponse struct {
	Currency           string                     `json:"currency"`
	ByCurrency         []currencyOverviewResponse `json:"by_currency"`
	Pending            int64                      `json:"pending"`
	Available          int64                      `json:"available"`
	PaidOut            int64                      `json:"paid_out"`
	ThisMonth          int64                      `json:"this_month"`
	TotalEarned        int64                      `json:"total_earned"`
	Entries            int64                      `json:"entries"`
	NeedReview         int64                      `json:"need_review"`
	WaitingWithdrawals int64                      `json:"waiting_withdrawals"`
	InvitedUsers       int64                      `json:"invited_users"`
	RatePercent        int                        `json:"rate_percent"`
	FreezeDays         int                        `json:"freeze_days"`
	MinWithdraw        int64                      `json:"min_withdraw"`
	Scope              string                     `json:"scope"`
}

// adminWithdrawalResponse 是后台提现列表一行。
type adminWithdrawalResponse struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	UserID       string     `json:"user_id"`
	Amount       int64      `json:"amount"`
	Currency     string     `json:"currency"`
	Status       string     `json:"status"`
	PayoutDetail string     `json:"payout_detail"`
	RejectReason string     `json:"reject_reason"`
	RequestedAt  time.Time  `json:"requested_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	EarnedTotal  int64      `json:"earned_total"`
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
	return commissionSummaryResponse{
		Currency:     string(s.Currency),
		Pending:      pending,
		Available:    avail,
		Withdrawing:  withdrawing,
		Settled:      settled,
		Invitees:     s.Invitees,
		Orders:       s.Orders,
		PaidInvitees: s.PaidInvitees,
		TotalEarned:  total,
		RatePercent:  s.RatePercent,
		MinWithdraw:  minW,
		Scope:        s.Scope,
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
		Amount:      amount,
		Base:        base,
		RatePercent: e.RateBP / 100,
		Currency:    e.Currency,
		Status:      e.Status,
		FrozenUntil: e.FrozenUntil,
		CreatedAt:   e.CreatedAt,
		OrderNo:     e.OrderNo,
		From:        e.From,
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
		Currency:     w.Currency,
		Status:       w.Status,
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
		Currency:    t.Currency,
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
	top, err := currencyOverviewFrom(service.CurrencyOverview{
		Currency: o.Currency, Pending: o.Pending, Available: o.Available, PaidOut: o.PaidOut,
		ThisMonth: o.ThisMonth, TotalEarned: o.TotalEarned,
	})
	if err != nil {
		return overviewResponse{}, err
	}
	minW, err := cents(o.MinWithdraw)
	if err != nil {
		return overviewResponse{}, err
	}
	rows := make([]currencyOverviewResponse, 0, len(o.ByCurrency))
	for _, c := range o.ByCurrency {
		row, err := currencyOverviewFrom(c)
		if err != nil {
			return overviewResponse{}, err
		}
		rows = append(rows, row)
	}
	return overviewResponse{
		Currency:           string(o.Currency),
		ByCurrency:         rows,
		Pending:            top.Pending,
		Available:          top.Available,
		PaidOut:            top.PaidOut,
		ThisMonth:          top.ThisMonth,
		TotalEarned:        top.TotalEarned,
		Entries:            o.Entries,
		NeedReview:         o.NeedReview,
		WaitingWithdrawals: o.WaitingWithdrawals,
		InvitedUsers:       o.InvitedUsers,
		RatePercent:        o.RatePercent,
		FreezeDays:         o.FreezeDays,
		MinWithdraw:        minW,
		Scope:              o.Scope,
	}, nil
}

// currencyOverviewFrom 把一组币种汇总转成分。
func currencyOverviewFrom(c service.CurrencyOverview) (currencyOverviewResponse, error) {
	vals := []decimal.Decimal{c.Pending, c.Available, c.PaidOut, c.ThisMonth, c.TotalEarned}
	out := make([]int64, len(vals))
	for i, v := range vals {
		n, err := cents(v)
		if err != nil {
			return currencyOverviewResponse{}, err
		}
		out[i] = n
	}
	return currencyOverviewResponse{
		Currency:           string(c.Currency),
		Pending:            out[0],
		Available:          out[1],
		PaidOut:            out[2],
		ThisMonth:          out[3],
		TotalEarned:        out[4],
		Entries:            c.Entries,
		NeedReview:         c.NeedReview,
		WaitingWithdrawals: c.WaitingWithdrawals,
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
	return adminWithdrawalResponse{
		ID:           w.ID.String(),
		Email:        w.Email,
		UserID:       w.UserID.String(),
		Amount:       amount,
		Currency:     w.Currency,
		Status:       w.Status,
		PayoutDetail: w.PayoutDetail,
		RejectReason: w.RejectReason,
		RequestedAt:  w.RequestedAt,
		CompletedAt:  w.CompletedAt,
		EarnedTotal:  earned,
	}, nil
}

// configToUpdate 把请求体转成 service.ConfigUpdate。
func (r configRequest) configToUpdate() service.ConfigUpdate {
	u := service.ConfigUpdate{
		RatePercent: r.RatePercent,
		FreezeDays:  r.FreezeDays,
		Scope:       r.Scope,
	}
	if r.MinWithdraw != nil {
		d := money.FromCents(*r.MinWithdraw)
		u.MinWithdraw = &d
	}
	return u
}
