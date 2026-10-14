package service

import (
	"context"
	"errors"
	"fmt"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// errSettleLost 是解冻改状态时没改到行（并发冲突）。
var errSettleLost = errors.New("commission: 解冻时条目状态已变")

// autoTransferPlan 是一轮解冻里「提现关闭时自动转余额」的参数：on 为 true 时站点币种 site 的条目解冻时挂上标记。
type autoTransferPlan struct {
	on   bool
	site money.Currency
}

// settleOutcome 是解冻一条的结果。
type settleOutcome int

const (
	settleSkipped     settleOutcome = iota // 锁不到或已不满足条件
	settleDone                             // 已解冻
	settleQuarantined                      // 确定性失败，已转人工复核
)

// SettleDue 把冻结期已满、不需复核的佣金逐条转为可提现（commission:settle 任务调用）。
// 先不加锁地找出到期条目，再逐条在各自的事务里 SKIP LOCKED 重新锁定并复核条件：两个 worker 撞上同一条也只会记一次账。
// 待结科目余额不够这类重试也不会好的失败，当场转人工复核（之后不再参与自动解冻），不每小时空转、不占批量名额。
// 提现关闭时站点币种的条目解冻时只挂「等自动转余额」标记；解冻都提交后再由 autoTransferDue 逐条在单独的事务里转入余额，
// 转失败的标记留着，下一轮重试（解冻事务里不同时锁佣金与余额科目，见 module-order 规则的锁序通则）。
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
		case out == settleDone:
			st.Settled++
		case out == settleQuarantined:
			st.Quarantined++
		default:
			st.Skipped++
		}
	}
	if err := ctx.Err(); err != nil {
		return st, err
	}
	n, failed, err := s.autoTransferDue(ctx, limit, plan)
	st.AutoTransferred, st.Failed = n, st.Failed+failed
	if firstErr == nil {
		firstErr = err
	}
	return st, firstErr
}

// settleOne 在自己的事务里解冻一条：锁条目（拿不到或已不满足条件就跳过）→ 记 commission_settled → 改状态；
// 提现关闭且为站点币种时同时挂上「等自动转余额」标记（转账在 autoTransferDue 的单独事务里做）。
// 锁序：条目行 → 推荐人的待结、可提现佣金科目（ledger 的 LockAccounts 一条语句按科目 id 升序加锁）；不拿用户咨询锁，不碰余额科目。
// 受益人已注销（identity 的无锁读）时不解冻、转人工复核。
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
		// 受益人已注销：注销提交那一刻才计提、没赶上注销作废的条目。不解冻，转人工复核由后台作废
		// （作废要记复核人，任务里没有；后台对已注销账号的条目只能作废、不能通过）
		gone, err := s.accountDeleted(ctx, e.ReferrerUserID)
		if err != nil {
			return err
		}
		if gone {
			changed, err := s.repo.MarkEntryReview(ctx, e.ID, reviewDeletedReason, now)
			if err != nil {
				return err
			}
			if !changed {
				return errSettleLost
			}
			plog.From(ctx).Warn("commission: 受益人已注销，条目转人工复核",
				zap.String("entry_id", e.ID.String()), zap.String("referrer_user_id", e.ReferrerUserID.String()))
			out = settleQuarantined
			return nil
		}
		transfer := plan.on && money.Currency(e.Currency) == plan.site
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
		changed, err := s.repo.MarkEntryAvailable(ctx, e.ID, txnID, now, transfer)
		if err != nil {
			return err
		}
		if !changed {
			return errSettleLost
		}
		out = settleDone
		return nil
	})
	if err != nil {
		return settleSkipped, err
	}
	return out, nil
}
