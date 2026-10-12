package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// MyCommission 返回门户「我的佣金」：站点币种下的汇总、最近 100 条佣金、50 条提现、50 笔转余额。
func (s *Service) MyCommission(ctx context.Context, userID uuid.UUID) (MyCommission, error) {
	refs, err := s.needRefs()
	if err != nil {
		return MyCommission{}, err
	}
	cfg, err := s.settings.Commission(ctx)
	if err != nil {
		return MyCommission{}, err
	}
	cur, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return MyCommission{}, err
	}
	et, err := s.repo.EntryTotalsOf(ctx, userID, string(cur))
	if err != nil {
		return MyCommission{}, err
	}
	wt, err := s.repo.WithdrawalTotalsOf(ctx, userID, string(cur))
	if err != nil {
		return MyCommission{}, err
	}
	tr, err := s.repo.TransferTotalOf(ctx, userID, string(cur))
	if err != nil {
		return MyCommission{}, err
	}
	invitees, err := refs.InviteeCount(ctx, userID)
	if err != nil {
		return MyCommission{}, err
	}
	// 与提现、转余额同一口径；展示时不显示负数
	avail := et.Available.Sub(wt.InFlight).Sub(wt.Paid).Sub(tr)
	if avail.IsNegative() {
		avail = decimal.Zero
	}
	out := MyCommission{
		Summary: Summary{
			Currency:     cur,
			Pending:      et.Pending,
			Available:    avail,
			Withdrawing:  wt.InFlight,
			Settled:      wt.Paid,
			TotalEarned:  et.Total,
			Invitees:     invitees,
			Orders:       et.Orders,
			PaidInvitees: et.PaidInvitees,
			RatePercent:  cfg.RatePercent,
			MinWithdraw:  cfg.MinWithdraw,
			Scope:        cfg.Scope,
		},
		Entries:     []EntryView{},
		Withdrawals: []WithdrawalView{},
		Transfers:   []TransferView{},
	}

	entries, err := s.repo.EntriesOf(ctx, userID, MyEntriesLimit)
	if err != nil {
		return MyCommission{}, err
	}
	referees := make([]uuid.UUID, 0, len(entries))
	seen := make(map[uuid.UUID]bool, len(entries))
	for _, e := range entries {
		if !seen[e.RefereeUserID] {
			seen[e.RefereeUserID] = true
			referees = append(referees, e.RefereeUserID)
		}
	}
	emails, err := refs.Emails(ctx, referees)
	if err != nil {
		return MyCommission{}, err
	}
	for _, e := range entries {
		out.Entries = append(out.Entries, EntryView{
			Amount:      e.CommissionAmount,
			Base:        e.BaseAmount,
			RateBP:      e.RateBP,
			Currency:    e.Currency,
			Status:      e.Status,
			FrozenUntil: e.FrozenUntil,
			CreatedAt:   e.CreatedAt,
			OrderNo:     e.OrderNo,
			From:        MaskEmail(emails[e.RefereeUserID]),
		})
	}

	wds, err := s.repo.WithdrawalsOf(ctx, userID, MyWithdrawalsLimit)
	if err != nil {
		return MyCommission{}, err
	}
	for _, w := range wds {
		reason := ""
		if w.RejectReason != nil {
			reason = *w.RejectReason
		}
		out.Withdrawals = append(out.Withdrawals, WithdrawalView{
			ID:           w.ID,
			Amount:       w.Amount,
			Currency:     w.Currency,
			Status:       w.Status,
			RejectReason: reason,
			RequestedAt:  w.RequestedAt,
			CompletedAt:  w.CompletedAt,
		})
	}

	trs, err := s.repo.TransfersOf(ctx, userID, MyTransfersLimit)
	if err != nil {
		return MyCommission{}, err
	}
	for _, t := range trs {
		out.Transfers = append(out.Transfers, TransferView{
			LedgerTxnID: t.LedgerTxnID,
			Amount:      t.Amount,
			Currency:    t.Currency,
			CreatedAt:   t.CreatedAt,
		})
	}
	return out, nil
}

// MaskEmail 给邮箱打码：保留 @ 前两个字符（不足三个字符全打码）与域名；不是邮箱时返回 "***"。
func MaskEmail(e string) string {
	for i := 0; i < len(e); i++ {
		if e[i] == '@' {
			if i <= 2 {
				return "***" + e[i:]
			}
			return e[:2] + "***" + e[i:]
		}
	}
	return "***"
}

// AdminOverview 返回后台分销概览：站点币种下的汇总与当前参数；「本月」按站点时区的月初算。
func (s *Service) AdminOverview(ctx context.Context) (Overview, error) {
	refs, err := s.needRefs()
	if err != nil {
		return Overview{}, err
	}
	cfg, err := s.settings.Commission(ctx)
	if err != nil {
		return Overview{}, err
	}
	site, err := s.settings.Site(ctx)
	if err != nil {
		return Overview{}, err
	}
	cur, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return Overview{}, err
	}
	eo, err := s.repo.EntryOverviewOf(ctx, string(cur), monthStart(s.clock(), site.Timezone))
	if err != nil {
		return Overview{}, err
	}
	wo, err := s.repo.WithdrawalOverviewOf(ctx, string(cur))
	if err != nil {
		return Overview{}, err
	}
	invited, err := refs.ReferralCount(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		Pending:            eo.Pending,
		Available:          eo.Available,
		PaidOut:            wo.PaidOut,
		ThisMonth:          eo.ThisMonth,
		TotalEarned:        eo.Total,
		Entries:            eo.Entries,
		NeedReview:         eo.NeedReview,
		WaitingWithdrawals: wo.Waiting,
		InvitedUsers:       invited,
		RatePercent:        cfg.RatePercent,
		FreezeDays:         cfg.FreezeDays,
		MinWithdraw:        cfg.MinWithdraw,
		Scope:              cfg.Scope,
	}, nil
}

// monthStart 返回 now 在站点时区下所在月份的月初（UTC）；时区无效按 UTC。
func monthStart(now time.Time, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	t := now.In(loc)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc).UTC()
}

// AdminWithdrawals 按申请时间倒序列出最近 200 笔提现（status 为空不筛），收款信息在这里解封给管理员打款用。
func (s *Service) AdminWithdrawals(ctx context.Context, status string) ([]AdminWithdrawal, error) {
	switch status {
	case "", WithdrawalRequested, WithdrawalApproved, WithdrawalRejected, WithdrawalPaid:
	default:
		return nil, errs.Invalid(map[string]string{"status": "状态只能是 requested、approved、rejected 或 paid"})
	}
	refs, err := s.needRefs()
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListWithdrawals(ctx, status, AdminWithdrawalsLimit)
	if err != nil {
		return nil, err
	}
	users := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]bool, len(rows))
	for _, w := range rows {
		if !seen[w.UserID] {
			seen[w.UserID] = true
			users = append(users, w.UserID)
		}
	}
	emails, err := refs.Emails(ctx, users)
	if err != nil {
		return nil, err
	}
	earned, err := s.repo.EarnedByUsers(ctx, users)
	if err != nil {
		return nil, err
	}
	out := make([]AdminWithdrawal, 0, len(rows))
	for _, w := range rows {
		payout, err := s.sealer.Open(w.PayoutSealed, payoutAAD(w.UserID))
		if err != nil {
			// 解不开（换了主密钥或数据被改）不挡整张列表，这一行收款信息留空并记日志
			plog.From(ctx).Warn("commission: 提现收款信息解封失败", zap.String("withdrawal_id", w.ID.String()))
			payout = nil
		}
		reason := ""
		if w.RejectReason != nil {
			reason = *w.RejectReason
		}
		out = append(out, AdminWithdrawal{
			ID:           w.ID,
			UserID:       w.UserID,
			Email:        emails[w.UserID],
			Amount:       w.Amount,
			Currency:     w.Currency,
			Status:       w.Status,
			PayoutDetail: string(payout),
			RejectReason: reason,
			RequestedAt:  w.RequestedAt,
			CompletedAt:  w.CompletedAt,
			EarnedTotal:  earned[repo.UserCurrency{UserID: w.UserID, Currency: w.Currency}],
		})
	}
	return out, nil
}

// SetConfig 改分销参数（后台）；校验与审计（commission.config_changed）在 settings 里做。
func (s *Service) SetConfig(ctx context.Context, u ConfigUpdate) error {
	return s.settings.SetCommission(ctx, settingssvc.CommissionUpdate{
		RatePercent: u.RatePercent,
		FreezeDays:  u.FreezeDays,
		MinWithdraw: u.MinWithdraw,
		Scope:       u.Scope,
	})
}
