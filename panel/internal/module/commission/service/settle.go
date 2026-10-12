package service

import (
	"context"
	"errors"
	"fmt"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// errSettleLost 是解冻改状态时没改到行（并发冲突）。
var errSettleLost = errors.New("commission: 解冻时条目状态已变")

// SettleDue 把冻结期已满、不需复核的佣金逐条转为可提现（commission:settle 任务调用）。
// 先不加锁地找出到期条目，再逐条在各自的事务里 SKIP LOCKED 重新锁定并复核条件：两个 worker 撞上同一条也只会记一次账。
// 单条失败只计数、不影响其他条目；有失败时返回第一条的错误（统计照常返回）。
func (s *Service) SettleDue(ctx context.Context, limit int) (SettleStats, error) {
	if limit <= 0 {
		limit = SettleBatchDefault
	}
	now := s.clock()
	ids, err := s.repo.DueEntryIDs(ctx, now, limit)
	if err != nil {
		return SettleStats{}, err
	}
	st := SettleStats{Scanned: len(ids)}
	var firstErr error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		ok, err := s.settleOne(ctx, id)
		switch {
		case err != nil:
			st.Failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("解冻佣金 %s: %w", id, err)
			}
		case ok:
			st.Settled++
		default:
			st.Skipped++
		}
	}
	return st, firstErr
}

// settleOne 在自己的事务里解冻一条：锁条目（拿不到或已不满足条件就跳过）→ 记 commission_settled → 改状态。
// 锁序：条目行 → 推荐人的可提现、待结佣金科目（ledger 按类型排序加锁）。
func (s *Service) settleOne(ctx context.Context, id uuid.UUID) (bool, error) {
	settled := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		now := s.clock()
		e, ok, err := s.repo.LockDueEntry(ctx, id, now)
		if err != nil || !ok {
			return err
		}
		if !CanEntryTransition(e.Status, EntryAvailable) {
			return nil
		}
		txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindCommissionSettled,
			Currency:   money.Currency(e.Currency),
			SourceType: SourceCommissionEntry,
			SourceID:   e.ID,
			Memo:       "佣金解冻",
			Legs: []ledgersvc.Leg{
				{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionPending, e.ReferrerUserID), Direction: ledgersvc.Debit, Amount: e.CommissionAmount},
				{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionAvailable, e.ReferrerUserID), Direction: ledgersvc.Credit, Amount: e.CommissionAmount},
			},
		})
		if err != nil {
			return err
		}
		changed, err := s.repo.MarkEntryAvailable(ctx, e.ID, txnID, now)
		if err != nil {
			return err
		}
		if !changed {
			return errSettleLost
		}
		settled = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return settled, nil
}
