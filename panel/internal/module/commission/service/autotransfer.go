package service

import (
	"context"
	"errors"
	"fmt"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/model"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// autoTransferMemo 是自动转余额的账本备注。
const autoTransferMemo = "佣金自动转入余额（提现已关闭）"

// errAutoTransferLost 是清标记时没改到行（并发冲突；持有用户锁与条目行锁时不应发生）。
var errAutoTransferLost = errors.New("commission: 自动转余额时条目标记已变")

// autoTransferDue 把挂着「等自动转余额」标记的条目逐条转入余额（提现关闭时由 settleOne 挂上），每条一个单独的事务。
// 提现已重新打开（plan.on 为 false）时只清标记、不转：用户自己提现或手动转余额。
// 单条失败只计数，标记留着，下一轮 commission:settle 重试；返回转成的条数、失败条数与第一条失败的错误。
func (s *Service) autoTransferDue(ctx context.Context, limit int, plan autoTransferPlan) (transferred, failed int, firstErr error) {
	list, err := s.repo.AutoTransferDueEntries(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range list {
		if err := ctx.Err(); err != nil {
			return transferred, failed, err
		}
		done, err := s.autoTransferOne(ctx, e, plan)
		switch {
		case err != nil:
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("佣金自动转余额 %s: %w", e.ID, err)
			}
		case done:
			transferred++
		}
	}
	return transferred, failed, firstErr
}

// autoTransferOne 在单独的事务里把一条佣金转入余额，转成时 done 为 true。
// 锁序：用户咨询锁（LockUser，与手动转余额、提现申请、注销作废同一把）→ 条目行（SKIP LOCKED，标记已清就跳过）→ 一笔分录同时锁可提现佣金与余额科目。
// 受益人已注销回 409 ErrAccountDeleted（注销时标记已清，正常走不到）。
// 转的金额是这条佣金与当前可用佣金里较小的那个：标记挂着期间用户可能已经手动转走一部分；可用为 0 时只清标记、不记账。
// 条目不是站点币种（站点币种被改过）或提现已重新打开时只清标记，钱留在可提现。
// 转余额记录带 entry_id，库里唯一，同一条佣金最多自动转一次；写审计 commission.auto_transferred_to_balance。
func (s *Service) autoTransferOne(ctx context.Context, e model.Entry, plan autoTransferPlan) (bool, error) {
	done := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockUser(ctx, e.ReferrerUserID); err != nil {
			return err
		}
		locked, ok, err := s.repo.LockAutoTransferEntry(ctx, e.ID)
		if err != nil || !ok {
			return err
		}
		// 受益人已注销：注销时已清掉他的标记、作废了可用佣金，走到这里说明数据不对，拒绝并留着标记待人工核对
		if err := s.rejectDeleted(ctx, locked.ReferrerUserID); err != nil {
			return err
		}
		cur := money.Currency(locked.Currency)
		amount := locked.CommissionAmount
		switch {
		case !plan.on:
			plog.From(ctx).Info("commission: 提现已重新打开，自动转余额只清标记",
				zap.String("entry_id", locked.ID.String()))
			amount = decimal.Zero
		case cur != plan.site:
			plog.From(ctx).Warn("commission: 条目已不是站点币种，不再自动转余额",
				zap.String("entry_id", locked.ID.String()), zap.String("currency", locked.Currency))
			amount = decimal.Zero // 只清标记
		default:
			avail, err := s.available(ctx, locked.ReferrerUserID, cur)
			if err != nil {
				return err
			}
			if avail.LessThan(amount) {
				amount = avail
			}
		}
		if amount.IsPositive() {
			id := locked.ID
			txnID, err := s.postTransfer(ctx, locked.ReferrerUserID, cur, amount, &id, autoTransferMemo)
			if err != nil {
				return err
			}
			if err := s.audit.Write(ctx, auditsvc.Event{
				Action:       ActionAutoTransferred,
				ResourceType: ResourceCommissionEntry,
				ResourceID:   &id,
				Data: map[string]any{
					"amount":       amount.StringFixed(2),
					"entry_amount": locked.CommissionAmount.StringFixed(2),
					"currency":     locked.Currency,
					"ledger_txn":   txnID.String(),
					"order_no":     locked.OrderNo,
				},
			}); err != nil {
				return err
			}
			done = true
		} else if plan.on && cur == plan.site {
			plog.From(ctx).Info("commission: 可用佣金已被用户转走，自动转余额只清标记",
				zap.String("entry_id", locked.ID.String()))
		}
		changed, err := s.repo.ClearAutoTransferDue(ctx, locked.ID, s.clock())
		if err != nil {
			return err
		}
		if !changed {
			return errAutoTransferLost
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return done, nil
}
