package service

import (
	"context"
	"sort"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// MyCommission 返回门户「我的佣金」：一个币种的汇总（取法见 summaryCurrency）、最近 100 条佣金、50 条提现、50 笔转余额（明细含全部币种）。
// 横幅比例与计佣范围按该用户的单独设置（无则用全局）。提现关闭只关站点币种：汇总币种是站点币种时门户隐藏提现入口，
// 别的币种照常显示（门户按 SiteCurrency 判断）。第 2、3 级的条目不给上级看下线邮箱（From 为空）。
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
	cur, err := s.summaryCurrency(ctx, userID)
	if err != nil {
		return MyCommission{}, err
	}
	site, err := s.ledger.DefaultCurrency(ctx)
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
			Currency:        cur,
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
			SiteCurrency:    site,
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
			Currency:     e.Currency,
			Status:       e.Status,
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
		out.Withdrawals = append(out.Withdrawals, WithdrawalView{
			ID:           w.ID,
			Amount:       w.Amount,
			Currency:     w.Currency,
			Status:       w.Status,
			RejectReason: reason,
			RequestedAt:  w.RequestedAt,
			CompletedAt:  w.CompletedAt,
			Method:       w.Method,
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

// AdminOverview 返回后台分销概览：按币种分组的汇总与当前参数；「本月」按站点时区的月初算。
// 顶层金额是站点币种那一组（没有数据时为 0），顶层笔数是全部币种合计。
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
	siteCur, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return Overview{}, err
	}
	eos, err := s.repo.EntryOverviewByCurrency(ctx, monthStart(s.clock(), site.Timezone))
	if err != nil {
		return Overview{}, err
	}
	wos, err := s.repo.WithdrawalOverviewByCurrency(ctx)
	if err != nil {
		return Overview{}, err
	}
	invited, err := refs.ReferralCount(ctx)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{
		Currency:            siteCur,
		InvitedUsers:        invited,
		RatePercent:         cfg.RatePercent,
		FreezeDays:          cfg.FreezeDays,
		MinWithdraw:         cfg.MinWithdraw,
		Scope:               cfg.Scope,
		DistributionEnabled: dist.Enabled,
		DistributionLevels:  dist.Levels,
		WithdrawEnabled:     wd.Enabled,
		WithdrawMethods:     nonNilStrings(wd.Methods),
		ByCurrency:          mergeOverview(siteCur, eos, wos),
	}
	for _, c := range out.ByCurrency {
		out.Entries += c.Entries
		out.NeedReview += c.NeedReview
		out.WaitingWithdrawals += c.WaitingWithdrawals
		if c.Currency == siteCur {
			out.Pending, out.Available, out.PaidOut = c.Pending, c.Available, c.PaidOut
			out.ThisMonth, out.TotalEarned = c.ThisMonth, c.TotalEarned
		}
	}
	return out, nil
}

// mergeOverview 把条目与提现两组按币种合并成一张表（币种升序），站点币种没有数据时也补一行 0。
func mergeOverview(siteCur money.Currency, eos []repo.EntryOverview, wos []repo.WithdrawalOverview) []CurrencyOverview {
	byCur := map[money.Currency]*CurrencyOverview{}
	get := func(c money.Currency) *CurrencyOverview {
		if row, ok := byCur[c]; ok {
			return row
		}
		row := &CurrencyOverview{Currency: c}
		byCur[c] = row
		return row
	}
	get(siteCur)
	for _, e := range eos {
		row := get(money.Currency(e.Currency))
		row.Pending, row.Available, row.ThisMonth, row.TotalEarned = e.Pending, e.Available, e.ThisMonth, e.Total
		row.Entries, row.NeedReview = e.Entries, e.NeedReview
	}
	for _, w := range wos {
		row := get(money.Currency(w.Currency))
		row.PaidOut, row.WaitingWithdrawals = w.PaidOut, w.Waiting
	}
	out := make([]CurrencyOverview, 0, len(byCur))
	for _, row := range byCur {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
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
			ID:              w.ID,
			UserID:          w.UserID,
			Email:           emails[w.UserID],
			Amount:          w.Amount,
			Currency:        w.Currency,
			Status:          w.Status,
			Method:          w.Method,
			PayoutDetail:    string(payout),
			PayoutReference: deref(w.PayoutReference),
			PaidUSDTAmount:  w.PaidUSDTAmount,
			RejectReason:    reason,
			RequestedAt:     w.RequestedAt,
			CompletedAt:     w.CompletedAt,
			EarnedTotal:     earned[repo.UserCurrency{UserID: w.UserID, Currency: w.Currency}],
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
