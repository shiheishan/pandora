package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aegispanel/aegis/internal/module/commission/model"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// errSettleLost 是解冻改状态时没改到行（并发冲突）。
var errSettleLost = errors.New("commission: 解冻时条目状态已变")

// autoTransferPlan 是一轮解冻里「提现关闭时自动转余额」的参数：on 为 true 时只转站点币种 site 的条目。
type autoTransferPlan struct {
	on   bool
	site money.Currency
}

// settleOutcome 是解冻一条的结果。
type settleOutcome int

const (
	settleSkipped         settleOutcome = iota // 锁不到或已不满足条件
	settleDone                                 // 已解冻
	settleDoneTransferred                      // 已解冻并自动转入余额
	settleQuarantined                          // 确定性失败，已转人工复核
)

// SettleDue 把冻结期已满、不需复核的佣金逐条转为可提现（commission:settle 任务调用）。
// 先不加锁地找出到期条目，再逐条在各自的事务里 SKIP LOCKED 重新锁定并复核条件：两个 worker 撞上同一条也只会记一次账。
// 待结科目余额不够这类重试也不会好的失败，当场转人工复核（之后不再参与自动解冻），不每小时空转、不占批量名额。
// 提现关闭时站点币种的条目解冻后同一事务里自动转入余额。
// 其余单条失败只计数、不影响其他条目；有失败时返回第一条的错误（统计照常返回）。
func (s *Service) SettleDue(ctx context.Context, limit int) (SettleStats, error) {
	if limit <= 0 {
		limit = SettleBatchDefault
	}
	now := s.clock()
	wd, err := s.settings.CommissionWithdraw(ctx)
	if err != nil {
		return SettleStats{}, err
	}
	site, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return SettleStats{}, err
	}
	plan := autoTransferPlan{on: !wd.Enabled, site: site}
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
		out, err := s.settleOne(ctx, id, plan)
		switch {
		case err != nil:
			st.Failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("解冻佣金 %s: %w", id, err)
			}
		case out == settleDone || out == settleDoneTransferred:
			st.Settled++
			if out == settleDoneTransferred {
				st.AutoTransferred++
			}
		case out == settleQuarantined:
			st.Quarantined++
		default:
			st.Skipped++
		}
	}
	return st, firstErr
}

// settleOne 在自己的事务里解冻一条：锁条目（拿不到或已不满足条件就跳过）→ 记 commission_settled → 改状态；提现关闭且为站点币种时同一事务里自动转余额。
// 锁序：条目行 → 用户咨询锁（LockUser）→ 推荐人的可提现、待结佣金科目（ledger 按类型排序加锁）。
func (s *Service) settleOne(ctx context.Context, id uuid.UUID, plan autoTransferPlan) (settleOutcome, error) {
	out := settleSkipped
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		now := s.clock()
		e, ok, err := s.repo.LockDueEntry(ctx, id, now)
		if err != nil || !ok {
			return err
		}
		if !CanEntryTransition(e.Status, EntryAvailable) {
			return nil
		}
		transfer := plan.on && money.Currency(e.Currency) == plan.site
		if transfer {
			// 锁序 条目行 → 用户咨询锁 → 科目；与手动转余额、提现申请用同一把用户锁，避免与它们交叉锁可提现与余额科目。
			if err := s.repo.LockUser(ctx, e.ReferrerUserID); err != nil {
				return err
			}
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
		if errors.Is(err, ledgersvc.ErrBalanceConstraint) {
			// 库约束兜底：语句已失败、事务已作废，不能在同一事务里隔离，整笔回滚交给下一轮
			return err
		}
		if errors.Is(err, ledgersvc.ErrInsufficientBalance) {
			// ledger 在写库前判出余额不足（不是库约束兜底），事务还能用：同一事务里隔离这一条
			reason := fmt.Sprintf("%s，无法解冻 %s %s", reviewUnderfundedPrefix, e.CommissionAmount.StringFixed(2), e.Currency)
			changed, err := s.repo.MarkEntryReview(ctx, e.ID, reason, now)
			if err != nil {
				return err
			}
			if !changed {
				return errSettleLost
			}
			plog.From(ctx).Warn("commission: 待结佣金科目余额不足，条目转人工复核",
				zap.String("entry_id", e.ID.String()), zap.String("amount", e.CommissionAmount.StringFixed(2)),
				zap.String("currency", e.Currency))
			out = settleQuarantined
			return nil
		}
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
		if transfer {
			if err := s.autoTransferEntry(ctx, e); err != nil {
				return err
			}
			out = settleDoneTransferred
		} else {
			out = settleDone
		}
		return nil
	})
	if err != nil {
		return settleSkipped, err
	}
	return out, nil
}
