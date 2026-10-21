package service

import (
	"context"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// MyCommission 返回门户「我的佣金」：汇总、最近 100 条佣金、50 条提现、50 笔转余额。
// 横幅比例与计佣范围按该用户的单独设置（无则用全局）。第 2、3 级的条目不给上级看下线邮箱（From 为空）。
// 待复核条目标 UnderReview；只有 USDT 已打款的提现带交易哈希。
func (s *Service) MyCommission(ctx context.Context, userID uuid.UUID) (MyCommission, error) {
	refs, err := s.needRefs()
	if err != nil {
		return MyCommission{}, err
	}
	cfg, err := s.settings.Commission(ctx)
	if err != nil {
		return MyCommission{}, err
	}
	rate, scope, err := s.termsFor(ctx, userID, cfg)
	if err != nil {
		return MyCommission{}, err
	}
	dist, err := s.settings.CommissionDistribution(ctx)
	if err != nil {
		return MyCommission{}, err
	}
	wd, err := s.settings.CommissionWithdraw(ctx)
	if err != nil {
		return MyCommission{}, err
	}
	et, err := s.repo.EntryTotalsOf(ctx, userID)
	if err != nil {
		return MyCommission{}, err
	}
	wt, err := s.repo.WithdrawalTotalsOf(ctx, userID)
	if err != nil {
		return MyCommission{}, err
	}
	// 与提现、转余额同一口径；展示时不显示负数
	avail, err := s.availableFrom(ctx, userID, et, wt)
	if err != nil {
		return MyCommission{}, err
	}
	invitees, err := refs.InviteeCount(ctx, userID)
	if err != nil {
		return MyCommission{}, err
	}
	if avail.IsNegative() {
		avail = decimal.Zero
	}
	out := MyCommission{
		Summary: Summary{
			Pending:         et.Pending,
			Available:       avail,
			Withdrawing:     wt.InFlight,
			Settled:         wt.Paid,
			TotalEarned:     et.Total,
			Invitees:        invitees,
			Orders:          et.Orders,
			PaidInvitees:    et.PaidInvitees,
			RatePercent:     rate,
			MinWithdraw:     cfg.MinWithdraw,
			Scope:           scope,
			DirectRateBP:    DirectRateBP(rate, dist),
			WithdrawEnabled: wd.Enabled,
			WithdrawMethods: nonNilStrings(wd.Methods),
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
		if e.Level == 1 && !seen[e.RefereeUserID] {
			seen[e.RefereeUserID] = true
			referees = append(referees, e.RefereeUserID)
		}
	}
	emails, err := refs.Emails(ctx, referees)
	if err != nil {
		return MyCommission{}, err
	}
	for _, e := range entries {
		from := ""
		if e.Level == 1 {
			from = MaskEmail(emails[e.RefereeUserID])
		}
		out.Entries = append(out.Entries, EntryView{
			Amount:       e.CommissionAmount,
			Base:         e.BaseAmount,
			RateBP:       e.RateBP,
			Status:       e.Status,
			UnderReview:  e.Status == EntryPending && e.ReviewRequired,
			FrozenUntil:  e.FrozenUntil,
			CreatedAt:    e.CreatedAt,
			OrderNo:      e.OrderNo,
			From:         from,
			Level:        e.Level,
			SharePercent: e.SharePercent,
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
		txHash := ""
		if w.Status == WithdrawalPaid && IsUSDTMethod(w.Method) && w.PayoutReference != nil {
			txHash = *w.PayoutReference
		}
		out.Withdrawals = append(out.Withdrawals, WithdrawalView{
			ID:           w.ID,
			Amount:       w.Amount,
			Status:       w.Status,
			RejectReason: reason,
			RequestedAt:  w.RequestedAt,
			CompletedAt:  w.CompletedAt,
			Method:       w.Method,
			TxHash:       txHash,
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

// AdminOverview 返回后台分销概览：全站金额与笔数合计、当前参数；「本月」按站点时区的月初算。
func (s *Service) AdminOverview(ctx context.Context) (Overview, error) {
	refs, err := s.needRefs()
	if err != nil {
		return Overview{}, err
	}
	cfg, err := s.settings.Commission(ctx)
	if err != nil {
		return Overview{}, err
	}
	dist, err := s.settings.CommissionDistribution(ctx)
	if err != nil {
		return Overview{}, err
	}
	wd, err := s.settings.CommissionWithdraw(ctx)
	if err != nil {
		return Overview{}, err
	}
	site, err := s.settings.Site(ctx)
	if err != nil {
		return Overview{}, err
	}
	eo, err := s.repo.EntryOverview(ctx, monthStart(s.clock(), site.Timezone))
	if err != nil {
		return Overview{}, err
	}
	wo, err := s.repo.WithdrawalOverview(ctx)
	if err != nil {
		return Overview{}, err
	}
	invited, err := refs.ReferralCount(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		Pending:             eo.Pending,
		Available:           eo.Available,
		PaidOut:             wo.PaidOut,
		ThisMonth:           eo.ThisMonth,
		TotalEarned:         eo.Total,
		Entries:             eo.Entries,
		NeedReview:          eo.NeedReview,
		WaitingWithdrawals:  wo.Waiting,
		InvitedUsers:        invited,
		RatePercent:         cfg.RatePercent,
		FreezeDays:          cfg.FreezeDays,
		MinWithdraw:         cfg.MinWithdraw,
		Scope:               cfg.Scope,
		DistributionEnabled: dist.Enabled,
		DistributionLevels:  dist.Levels,
		WithdrawEnabled:     wd.Enabled,
		WithdrawMethods:     nonNilStrings(wd.Methods),
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

// AdminWithdrawals 按申请时间倒序列出最近 200 笔提现（statuses 为空不筛，多个状态取并集），收款信息在这里解封给管理员打款用。
// 后台「处理中」传 requested,approved，「已完成」传 paid,rejected。
func (s *Service) AdminWithdrawals(ctx context.Context, statuses []string) ([]AdminWithdrawal, error) {
	statuses, err := withdrawalStatusFilter(statuses)
	if err != nil {
		return nil, err
	}
	refs, err := s.needRefs()
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListWithdrawals(ctx, statuses, AdminWithdrawalsLimit)
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
			ID:              w.ID,
			UserID:          w.UserID,
			Email:           emails[w.UserID],
			Amount:          w.Amount,
			Status:          w.Status,
			Method:          w.Method,
			PayoutDetail:    string(payout),
			PayoutReference: deref(w.PayoutReference),
			PaidUSDTAmount:  w.PaidUSDTAmount,
			RejectReason:    reason,
			RequestedAt:     w.RequestedAt,
			CompletedAt:     w.CompletedAt,
			EarnedTotal:     earned[w.UserID],
		})
	}
	return out, nil
}

// SetConfig 改分销参数（后台）：比例、冻结天数、最低提现额、计佣范围一组，多级分销一组，提现开关与方式一组；
// 只写给出字段所在的组，三组在同一事务里，任一组校验失败整笔不写。校验与审计（commission.config_changed）在 settings 里做。
func (s *Service) SetConfig(ctx context.Context, u ConfigUpdate) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if u.RatePercent != nil || u.FreezeDays != nil || u.MinWithdraw != nil || u.Scope != nil {
			if err := s.settings.SetCommission(ctx, settingssvc.CommissionUpdate{
				RatePercent: u.RatePercent,
				FreezeDays:  u.FreezeDays,
				MinWithdraw: u.MinWithdraw,
				Scope:       u.Scope,
			}); err != nil {
				return err
			}
		}
		if u.DistributionEnabled != nil || u.DistributionL1 != nil || u.DistributionL2 != nil || u.DistributionL3 != nil {
			if err := s.settings.SetCommissionDistribution(ctx, settingssvc.CommissionDistributionUpdate{
				Enabled: u.DistributionEnabled,
				L1:      u.DistributionL1,
				L2:      u.DistributionL2,
				L3:      u.DistributionL3,
			}); err != nil {
				return err
			}
		}
		if u.WithdrawEnabled != nil || u.WithdrawMethods != nil {
			if err := s.settings.SetCommissionWithdraw(ctx, settingssvc.CommissionWithdrawUpdate{
				Enabled: u.WithdrawEnabled,
				Methods: u.WithdrawMethods,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// nonNilStrings 把 nil 切片换成空切片（JSON 输出 [] 而不是 null）。
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// withdrawalStatusFilter 校验并去重提现状态筛选；有一个不认识就整体 422。
func withdrawalStatusFilter(statuses []string) ([]string, error) {
	out := make([]string, 0, len(statuses))
	seen := make(map[string]bool, len(statuses))
	for _, st := range statuses {
		switch st {
		case WithdrawalRequested, WithdrawalApproved, WithdrawalRejected, WithdrawalPaid:
		default:
			return nil, errs.Invalid(map[string]string{"status": "状态只能是 requested、approved、rejected 或 paid"})
		}
		if !seen[st] {
			seen[st] = true
			out = append(out, st)
		}
	}
	return out, nil
}
