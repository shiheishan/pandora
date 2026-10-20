package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// 数据巡检（report:integrity 每 30 分钟调一次，只读）。这两项都是库里约束拦不住的跨行一致性：
// 余额缓存由 Go 在记账时维护，冻结行与冻结科目由同一事务写，对不上就说明有 bug 或有人手改过库。

// CheckDrift 是「账本漂移」：科目余额缓存（ledger_accounts.balance）与它全部分录的合计对不上的科目。
// 样例是科目 id。后台概览的 ledger_drift_accounts 也取它的 Count（现算）。
func (s *Service) CheckDrift(ctx context.Context) (integrity.Result, error) {
	var debitNormal []string
	for _, t := range []AccountType{
		UserBalance, UserBalanceHold, UserCommissionPending, UserCommissionAvailable,
		ChannelCash, PlatformRevenue, PlatformFeeExpense, PlatformPromoExpense,
		Suspense, LatePaymentSuspense,
	} {
		if t.DebitNormal() {
			debitNormal = append(debitNormal, string(t))
		}
	}
	rows, err := s.repo.DriftRows(ctx, debitNormal)
	if err != nil {
		return integrity.Result{}, err
	}
	return integrity.FromRows(rows), nil
}

// CheckHolds 是「冻结对不上」：用户的冻结科目（user_balance_hold）余额不等于他 held 冻结行的金额合计。
// 冻结（HoldForOrder）、释放（ReleaseOrderHold）、扣款（postOrderPaid）都在同一笔交易里同时改冻结行与科目，
// 对不上说明冻结行状态与账本分录脱节（例如冻结行标了 released 却没记释放分录）。样例是用户 id。
func (s *Service) CheckHolds(ctx context.Context) (integrity.Result, error) {
	rows, err := s.repo.HoldMismatchRows(ctx)
	if err != nil {
		return integrity.Result{}, err
	}
	return integrity.FromRows(rows), nil
}
